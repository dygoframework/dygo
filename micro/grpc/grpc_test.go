package grpc_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/meta"
	dygogrpc "github.com/dygoframework/dygo/micro/grpc"
	ggrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// healthService stands in for any generated service.
type healthService struct {
	grpc_health_v1.UnimplementedHealthServer
}

var open = meta.NewKey[bool]("open")

func (h *healthService) GRPC(r *dygogrpc.Registrar) {
	r.Register(&grpc_health_v1.Health_ServiceDesc, h, dygogrpc.Method("List", open.Set(true)))
}

func (h *healthService) Check(_ context.Context, req *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	switch req.Service {
	case "missing":
		return nil, errs.NotFound("no such service")
	case "boom":
		panic("boom")
	}
	return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING}, nil
}

func (h *healthService) List(context.Context, *grpc_health_v1.HealthListRequest) (*grpc_health_v1.HealthListResponse, error) {
	return &grpc_health_v1.HealthListResponse{}, nil
}

func tokenGuard(ec dygo.ExecutionContext) (bool, error) {
	if o, _ := open.Get(ec); o {
		return true, nil
	}
	call := ec.RPC().Message().(*dygogrpc.Call)
	if v := call.Metadata.Get("authorization"); len(v) == 0 || v[0] != "secret" {
		return false, errs.Unauthorized("missing token")
	}
	return true, nil
}

func TestGRPC(t *testing.T) {
	ctx := context.Background()
	app, err := dygo.CreateApplicationContext(ctx, &dygo.Module{Name: "app", Providers: []dygo.Provider{dygo.Value(&healthService{})}})
	if err != nil {
		t.Fatal(err)
	}
	srv := dygogrpc.NewServer(dygogrpc.Options{Addr: "127.0.0.1:0", Global: []dygo.RouteOption{dygo.Guards(dygo.GuardFunc(tokenGuard))}})
	app.ConnectMicroservice(srv)
	if err := app.StartAllMicroservices(ctx); err != nil {
		t.Fatal(err)
	}
	defer app.Close(ctx)

	cc, err := ggrpc.NewClient(srv.Addr().String(), ggrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	client := grpc_health_v1.NewHealthClient(cc)
	authed := metadata.AppendToOutgoingContext(ctx, "authorization", "secret")

	res, err := client.Check(authed, &grpc_health_v1.HealthCheckRequest{})
	if err != nil || res.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("Check = %v, %v", res, err)
	}
	cases := []struct {
		ctx     context.Context
		service string
		code    codes.Code
	}{
		{ctx, "", codes.Unauthenticated},
		{authed, "missing", codes.NotFound},
		{authed, "boom", codes.Internal},
	}
	for _, c := range cases {
		_, err := client.Check(c.ctx, &grpc_health_v1.HealthCheckRequest{Service: c.service})
		if status.Code(err) != c.code {
			t.Errorf("%q: code = %v (%v)", c.service, status.Code(err), err)
		}
	}
	if _, err := client.List(ctx, &grpc_health_v1.HealthListRequest{}); err != nil {
		t.Errorf("open method: %v", err)
	}
	_, err = client.Check(authed, &grpc_health_v1.HealthCheckRequest{Service: "missing"})
	var he *errs.HTTPError
	if !errors.As(dygogrpc.ToHTTPError(err), &he) || he.Status != http.StatusNotFound || he.Message != "no such service" {
		t.Errorf("ToHTTPError = %v", dygogrpc.ToHTTPError(err))
	}
}

type badService struct{ healthService }

func (b *badService) GRPC(r *dygogrpc.Registrar) {
	r.Register(&grpc_health_v1.Health_ServiceDesc, b, dygogrpc.Method("Nope"))
}

func TestUnknownMethodOption(t *testing.T) {
	ctx := context.Background()
	app, err := dygo.CreateApplicationContext(ctx, &dygo.Module{Name: "app", Providers: []dygo.Provider{dygo.Value(&badService{})}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(ctx)
	app.ConnectMicroservice(dygogrpc.NewServer(dygogrpc.Options{Addr: "127.0.0.1:0"}))
	if err := app.StartAllMicroservices(ctx); err == nil {
		t.Fatal("want an error for an unknown method")
	}
}
