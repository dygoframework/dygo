package dygo_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/meta"
)

// fakeTransport is the smallest transport: handlers come from providers with
// a FakePatterns method, and Send runs one through its pipeline.
type fakeTransport struct {
	tr       *trace
	global   []dygo.RouteOption
	inherit  bool
	listenFn func() error
	pls      map[string]*dygo.Pipeline
	fns      map[string]func(ctx context.Context, in *fakeIn) (any, error)
}

type fakeIn struct{ Name string }

type fakeRouter struct {
	owner any
	ctrl  []dygo.RouteOption
	add   func(pattern string, fn func(ctx context.Context, in *fakeIn) (any, error), ctrl, opts []dygo.RouteOption)
}

func (r fakeRouter) Use(opts ...dygo.RouteOption) fakeRouter {
	r.ctrl = append(append([]dygo.RouteOption(nil), r.ctrl...), opts...)
	return r
}

func (r fakeRouter) Handle(pattern string, fn func(ctx context.Context, in *fakeIn) (any, error), opts ...dygo.RouteOption) {
	r.add(pattern, fn, r.ctrl, opts)
}

type fakePatterns interface{ FakePatterns(r fakeRouter) }

func (t *fakeTransport) Listen(_ context.Context, a *dygo.App) error {
	if t.listenFn != nil {
		if err := t.listenFn(); err != nil {
			return err
		}
	}
	enh, err := dygo.NewEnhancers(a, t.global, t.inherit)
	if err != nil {
		return err
	}
	t.pls = map[string]*dygo.Pipeline{}
	t.fns = map[string]func(ctx context.Context, in *fakeIn) (any, error){}
	var errList []error
	for _, p := range dygo.ProvidersImplementing[fakePatterns](a) {
		r := fakeRouter{owner: p}
		r.add = func(pattern string, fn func(ctx context.Context, in *fakeIn) (any, error), ctrl, opts []dygo.RouteOption) {
			pl, err := enh.Pipeline(dygo.Endpoint{
				Kind: dygo.KindRPC, Pattern: pattern, Owner: p, Handler: pattern,
				Input: reflect.TypeFor[fakeIn](), ControllerOptions: ctrl, Options: opts,
			})
			if err != nil {
				errList = append(errList, err)
				return
			}
			t.pls[pattern], t.fns[pattern] = pl, fn
		}
		p.FakePatterns(r)
	}
	if t.tr != nil {
		t.tr.add("listen")
	}
	return errors.Join(errList...)
}

func (t *fakeTransport) Close(context.Context) error {
	if t.tr != nil {
		t.tr.add("close")
	}
	return nil
}

func (t *fakeTransport) send(pattern, name string) (any, error) {
	in := &fakeIn{Name: name}
	pl := t.pls[pattern]
	return pl.Run(context.Background(), "msg", in, func(ctx context.Context) (any, error) {
		return t.fns[pattern](ctx, in)
	})
}

type rpcRoles = []string

var rpcRolesKey = meta.NewKey[rpcRoles]("roles")

type greeter struct{ tr *trace }

func (g *greeter) FakePatterns(r fakeRouter) {
	r = r.Use(rpcRolesKey.Set(rpcRoles{"user"}))
	r.Handle("greet", func(_ context.Context, in *fakeIn) (any, error) {
		g.tr.add("handler:" + in.Name)
		return "hello " + in.Name, nil
	}, rpcRolesKey.Set(rpcRoles{"admin"}))
	r.Handle("boom", func(context.Context, *fakeIn) (any, error) { panic("kaboom") })
	r.Handle("fail", func(context.Context, *fakeIn) (any, error) { return nil, errs.NotFound("no such user") })
}

func TestTransportPipeline(t *testing.T) {
	tr := &trace{}
	g := &greeter{tr: tr}
	var seen []string
	guard := dygo.GuardFunc(func(ec dygo.ExecutionContext) (bool, error) {
		roles, _ := rpcRolesKey.Get(ec)
		host := ec.RPC()
		seen = append(seen, string(ec.Kind())+" "+host.Pattern()+" "+host.Message().(string)+" "+strings.Join(roles, ","))
		if ec.HTTP() != nil || ec.WS() != nil {
			t.Error("an RPC context has an HTTP or WS host")
		}
		return true, nil
	})
	upper := dygo.PipeFunc(func(_ dygo.ExecutionContext, in any) error {
		in.(*fakeIn).Name = strings.ToUpper(in.(*fakeIn).Name)
		return nil
	})
	ft := &fakeTransport{global: []dygo.RouteOption{
		dygo.Guards(guard), dygo.Interceptors(tr.interceptor("global")), dygo.Pipes(upper),
	}}
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name:      "app",
		Providers: []dygo.Provider{dygo.Value(g)},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.ConnectMicroservice(ft)
	if err := app.StartAllMicroservices(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())

	out, err := ft.send("greet", "ada")
	if err != nil || out != "hello ADA" {
		t.Fatalf("greet = %v, %v", out, err)
	}
	if got := tr.take(); got != "i:global handler:ADA /i:global" {
		t.Errorf("order = %q", got)
	}
	if len(seen) != 1 || seen[0] != "rpc greet msg admin" {
		t.Errorf("guard saw %q", seen)
	}
	info := ft.pls["greet"].Info()
	if info.Module != "app" || info.Method != "rpc" || info.Controller != "*dygo_test.greeter" {
		t.Errorf("info = %+v", info)
	}

	_, err = ft.send("boom", "x")
	var pe *dygo.PanicError
	if !errors.As(err, &pe) || pe.Value != "kaboom" {
		t.Errorf("panic err = %v", err)
	}
	_, err = ft.send("fail", "x")
	var he *errs.HTTPError
	if !errors.As(err, &he) || he.Status != 404 {
		t.Errorf("fail err = %v", err)
	}
}

func TestTransportFiltersAndGuards(t *testing.T) {
	tr := &trace{}
	caught := ""
	ft := &fakeTransport{global: []dygo.RouteOption{
		dygo.Filters(dygo.FilterFunc(func(err error, host dygo.ArgumentsHost) bool {
			caught = string(host.Kind()) + " " + err.Error()
			return strings.Contains(err.Error(), "no such")
		})),
	}}
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name:      "app",
		Providers: []dygo.Provider{dygo.Value(&greeter{tr: tr})},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.ConnectMicroservice(ft)
	if err := app.StartAllMicroservices(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	if _, err := ft.send("fail", "x"); !errors.Is(err, dygo.ErrFiltered) {
		t.Errorf("filtered err = %v", err)
	}
	if caught != "rpc no such user" {
		t.Errorf("caught = %q", caught)
	}
	if err := ft.pls["greet"].Fail(context.Background(), nil, errors.New("bad payload")); err == nil || errors.Is(err, dygo.ErrFiltered) {
		t.Errorf("Fail = %v", err)
	}
}

func TestTransportInheritsAppGroupsOnlyWhenAsked(t *testing.T) {
	for _, inherit := range []bool{false, true} {
		tr := &trace{}
		ft := &fakeTransport{inherit: inherit}
		app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
			Name: "app",
			Providers: []dygo.Provider{
				dygo.Value(&greeter{tr: tr}),
				dygo.Value(tr.guard("app"), dygo.Group(dygo.AppGuards)),
			},
		}, dygo.WithGlobalGuards(tr.guard("option")))
		if err != nil {
			t.Fatal(err)
		}
		app.ConnectMicroservice(ft)
		if err := app.StartAllMicroservices(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := ft.send("greet", "x"); err != nil {
			t.Fatal(err)
		}
		var want string
		if inherit {
			want = "g:option g:app handler:x"
		} else {
			want = "handler:x"
		}
		if got := tr.take(); got != want {
			t.Errorf("inherit=%v: %q, want %q", inherit, got, want)
		}
		app.Close(context.Background())
	}
}

type hookRecorder struct{ tr *trace }

func (h *hookRecorder) BeforeApplicationShutdown(context.Context, string) error {
	h.tr.add("before")
	return nil
}

func (h *hookRecorder) OnApplicationShutdown(context.Context, string) error {
	h.tr.add("shutdown")
	return nil
}

func TestTransportLifecycle(t *testing.T) {
	tr := &trace{}
	listening := make(chan struct{})
	ft := &fakeTransport{tr: tr, listenFn: func() error { close(listening); return nil }}
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name:      "app",
		Providers: []dygo.Provider{dygo.Value(&hookRecorder{tr: tr})},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.ConnectMicroservice(ft)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- app.Run(ctx) }()
	<-listening
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := tr.take(); got != "listen before close shutdown" {
		t.Errorf("order = %q", got)
	}
}

func TestTransportStartFailureClosesStarted(t *testing.T) {
	tr := &trace{}
	ok := &fakeTransport{tr: tr}
	bad := &fakeTransport{listenFn: func() error { return errors.New("broker down") }}
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	app.ConnectMicroservice(ok)
	app.ConnectMicroservice(bad)
	err = app.StartAllMicroservices(context.Background())
	if err == nil || !strings.Contains(err.Error(), "broker down") {
		t.Fatalf("err = %v", err)
	}
	if got := tr.take(); got != "listen close" {
		t.Errorf("order = %q", got)
	}
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := tr.take(); got != "" {
		t.Errorf("closed again: %q", got)
	}
}

func TestTransportBadEnhancerIsAnError(t *testing.T) {
	ft := &fakeTransport{global: []dygo.RouteOption{dygo.Guards(dygo.Ref[*hookRecorder]())}}
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	app.ConnectMicroservice(ft)
	if err := app.StartAllMicroservices(context.Background()); err == nil {
		t.Fatal("want an error for a missing guard provider")
	}
}
