// Package grpc runs generated gRPC services inside a Dygo app. A provider
// with a GRPC method registers the services it implements:
//
//	func (s *UsersService) GRPC(r *grpc.Registrar) {
//		r.Register(&pb.Users_ServiceDesc, s,
//			grpc.Use(dygo.Guards(dygo.Ref[*TokenGuard]())),             // every method
//			grpc.Method("DeleteUser", roles.Set([]string{"admin"})),    // one method
//		)
//	}
//
// and the transport serves them:
//
//	app.ConnectMicroservice(grpc.NewServer(grpc.Options{Addr: ":50051"}))
//
// Every call runs through guards, interceptors, pipes (on the request
// message) and filters, with ExecutionContext.Kind() == dygo.KindRPC; the
// RPC host's Message is a *grpc.Call with the method and incoming metadata.
// Errors are mapped to real gRPC status codes: an *errs.HTTPError 404
// becomes codes.NotFound, 400 InvalidArgument, 401 Unauthenticated and so on
// ([Code]), and [ToHTTPError] maps a client's status error back.
package grpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
	ggrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Services is a provider that registers gRPC services.
type Services interface {
	GRPC(r *Registrar)
}

// Call describes the call being handled, the RPC host's Message.
type Call struct {
	// FullMethod is "/package.Service/Method".
	FullMethod string
	// Metadata is the incoming metadata.
	Metadata metadata.MD
	// Stream is the server stream for streaming methods, nil for unary ones.
	Stream ggrpc.ServerStream
}

// RegisterOption configures [Registrar.Register].
type RegisterOption func(*regConfig)

type regConfig struct {
	service []dygo.RouteOption
	methods map[string][]dygo.RouteOption
}

// Use applies enhancers and metadata to every method of the service.
func Use(opts ...dygo.RouteOption) RegisterOption {
	return func(c *regConfig) { c.service = append(c.service, opts...) }
}

// Method applies enhancers and metadata to one method, by its short name.
func Method(name string, opts ...dygo.RouteOption) RegisterOption {
	return func(c *regConfig) { c.methods[name] = append(c.methods[name], opts...) }
}

// Registrar is what a [Services] provider registers through.
type Registrar struct {
	s     *Server
	owner any
	errs  *[]error
}

// Register adds the service desc implemented by impl.
func (r *Registrar) Register(desc *ggrpc.ServiceDesc, impl any, opts ...RegisterOption) {
	cfg := &regConfig{methods: map[string][]dygo.RouteOption{}}
	for _, o := range opts {
		o(cfg)
	}
	known := map[string]bool{}
	add := func(name string) {
		known[name] = true
		full := "/" + desc.ServiceName + "/" + name
		pl, err := r.s.enh.Pipeline(dygo.Endpoint{
			Kind: dygo.KindRPC, Pattern: full, Owner: r.owner, Handler: name,
			ControllerOptions: cfg.service, Options: cfg.methods[name],
		})
		if err != nil {
			*r.errs = append(*r.errs, err)
			return
		}
		r.s.pipelines[full] = pl
	}
	for _, m := range desc.Methods {
		add(m.MethodName)
	}
	for _, m := range desc.Streams {
		add(m.StreamName)
	}
	for name := range cfg.methods {
		if !known[name] {
			*r.errs = append(*r.errs, fmt.Errorf("grpc: %s has no method %q", desc.ServiceName, name))
		}
	}
	r.s.grpc.RegisterService(desc, impl)
}

// Options configures [NewServer].
type Options struct {
	// Addr to listen on, such as ":50051". Ignored when Listener is set.
	Addr string
	// Listener to serve on instead of Addr.
	Listener net.Listener
	// GRPC are extra server options (credentials, keepalive, limits).
	GRPC []ggrpc.ServerOption
	// Global enhancers for every method.
	Global []dygo.RouteOption
	// InheritAppConfig also runs the app's global enhancers.
	InheritAppConfig bool
}

// Server is a dygo.Transport serving the app's gRPC services.
type Server struct {
	o         Options
	grpc      *ggrpc.Server
	enh       *dygo.Enhancers
	pipelines map[string]*dygo.Pipeline
	ln        net.Listener
	done      chan error
	mu        sync.Mutex
}

// NewServer returns the transport for app.ConnectMicroservice.
func NewServer(o Options) *Server { return &Server{o: o} }

// Addr returns the listening address once Listen has run.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// GRPC returns the underlying server, for registering services by hand
// (reflection, health) from OnApplicationBootstrap or later. It is nil
// before Listen.
func (s *Server) GRPC() *ggrpc.Server { return s.grpc }

// Listen implements dygo.Transport.
func (s *Server) Listen(_ context.Context, a *dygo.App) error {
	enh, err := dygo.NewEnhancers(a, s.o.Global, s.o.InheritAppConfig)
	if err != nil {
		return err
	}
	s.enh, s.pipelines = enh, map[string]*dygo.Pipeline{}
	opts := append([]ggrpc.ServerOption{
		ggrpc.ChainUnaryInterceptor(s.unary), ggrpc.ChainStreamInterceptor(s.stream),
	}, s.o.GRPC...)
	s.grpc = ggrpc.NewServer(opts...)
	var problems []error
	for _, p := range dygo.ProvidersImplementing[Services](a) {
		p.GRPC(&Registrar{s: s, owner: p, errs: &problems})
	}
	if err := errors.Join(problems...); err != nil {
		return err
	}
	ln := s.o.Listener
	if ln == nil {
		if ln, err = net.Listen("tcp", s.o.Addr); err != nil {
			return fmt.Errorf("grpc: %w", err)
		}
	}
	s.mu.Lock()
	s.ln, s.done = ln, make(chan error, 1)
	s.mu.Unlock()
	go func() { s.done <- s.grpc.Serve(ln) }()
	return nil
}

// Close implements dygo.Transport: a graceful stop, forced when ctx ends.
func (s *Server) Close(ctx context.Context) error {
	if s.grpc == nil {
		return nil
	}
	stopped := make(chan struct{})
	go func() { s.grpc.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
	case <-ctx.Done():
		s.grpc.Stop()
	}
	if err := <-s.done; err != nil && !errors.Is(err, ggrpc.ErrServerStopped) {
		return err
	}
	return nil
}

func (s *Server) unary(ctx context.Context, req any, info *ggrpc.UnaryServerInfo, h ggrpc.UnaryHandler) (any, error) {
	pl, ok := s.pipelines[info.FullMethod]
	if !ok {
		return h(ctx, req) // a service registered by hand
	}
	md, _ := metadata.FromIncomingContext(ctx)
	call := &Call{FullMethod: info.FullMethod, Metadata: md}
	out, err := pl.Run(ctx, call, req, func(ctx context.Context) (any, error) { return h(ctx, req) })
	return out, toStatus(err)
}

type wrappedStream struct {
	ggrpc.ServerStream
	ctx context.Context
}

func (w wrappedStream) Context() context.Context { return w.ctx }

func (s *Server) stream(srv any, ss ggrpc.ServerStream, info *ggrpc.StreamServerInfo, h ggrpc.StreamHandler) error {
	pl, ok := s.pipelines[info.FullMethod]
	if !ok {
		return h(srv, ss)
	}
	md, _ := metadata.FromIncomingContext(ss.Context())
	call := &Call{FullMethod: info.FullMethod, Metadata: md, Stream: ss}
	_, err := pl.Run(ss.Context(), call, nil, func(ctx context.Context) (any, error) {
		return nil, h(srv, wrappedStream{ServerStream: ss, ctx: ctx})
	})
	return toStatus(err)
}

// toStatus maps a handler error to a gRPC status error.
func toStatus(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, dygo.ErrFiltered) {
		return status.Error(codes.Unknown, "error handled by a filter")
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	var ge interface{ GRPCStatus() *status.Status }
	if errors.As(err, &ge) {
		return ge.GRPCStatus().Err()
	}
	b := errs.ToBody(err)
	return status.Error(Code(b.StatusCode), b.Message)
}

// Code maps an HTTP status to the gRPC code with the same meaning.
func Code(httpStatus int) codes.Code {
	switch httpStatus {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return codes.InvalidArgument
	case http.StatusUnauthorized:
		return codes.Unauthenticated
	case http.StatusForbidden:
		return codes.PermissionDenied
	case http.StatusNotFound, http.StatusGone:
		return codes.NotFound
	case http.StatusConflict:
		return codes.AlreadyExists
	case http.StatusPreconditionFailed:
		return codes.FailedPrecondition
	case http.StatusTooManyRequests, http.StatusRequestEntityTooLarge:
		return codes.ResourceExhausted
	case http.StatusNotImplemented, http.StatusMethodNotAllowed:
		return codes.Unimplemented
	case http.StatusServiceUnavailable:
		return codes.Unavailable
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return codes.DeadlineExceeded
	case errs.StatusClientClosedRequest:
		return codes.Canceled
	}
	if httpStatus >= 500 {
		return codes.Internal
	}
	return codes.Unknown
}

// HTTPStatus maps a gRPC code to an HTTP status, as grpc-gateway does.
func HTTPStatus(c codes.Code) int {
	switch c {
	case codes.OK:
		return http.StatusOK
	case codes.InvalidArgument, codes.OutOfRange:
		return http.StatusBadRequest
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists, codes.Aborted:
		return http.StatusConflict
	case codes.FailedPrecondition:
		return http.StatusPreconditionFailed
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Unimplemented:
		return http.StatusNotImplemented
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	case codes.Canceled:
		return errs.StatusClientClosedRequest
	}
	return http.StatusInternalServerError
}

// ToHTTPError maps an error from a gRPC client to an *errs.HTTPError, so an
// HTTP gateway passes a downstream NotFound on as a 404. Other errors are
// returned unchanged.
func ToHTTPError(err error) error {
	st, ok := status.FromError(err)
	if !ok || err == nil {
		return err
	}
	return errs.New(HTTPStatus(st.Code()), strings.TrimSpace(st.Message()), errs.Cause(err))
}
