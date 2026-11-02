package micro_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/meta"
	"github.com/dygoframework/dygo/micro"
)

type GetUser struct {
	ID string `json:"id"`
}

type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type OrderCreated struct {
	OrderID string `json:"orderId"`
}

var public = meta.NewKey[bool]("public")

type usersHandlers struct {
	mu     sync.Mutex
	events []string
}

func (h *usersHandlers) Patterns(r micro.Router) {
	r.Message("users.get", micro.Handle(h.get))
	r.Message("users.ping", micro.Handle(func(context.Context, struct{}) (string, error) { return "pong", nil }), public.Set(true))
	r.Message("users.panic", micro.Handle(func(context.Context, struct{}) (string, error) { panic("boom") }))
	r.Event("orders.created", micro.HandleEvent(func(_ context.Context, e OrderCreated) error {
		h.add("a:" + e.OrderID)
		return nil
	}))
	r.Event("orders.created", micro.HandleEvent(func(_ context.Context, e OrderCreated) error {
		h.add("b:" + e.OrderID)
		return errors.New("b failed")
	}))
}

func (h *usersHandlers) add(s string) {
	h.mu.Lock()
	h.events = append(h.events, s)
	h.mu.Unlock()
}

func (h *usersHandlers) get(ctx context.Context, in GetUser) (*User, error) {
	m, _ := micro.MessageFrom(ctx)
	if in.ID == "" {
		return nil, errs.BadRequest("id is required", errs.Code("ID_REQUIRED"))
	}
	if in.ID != "1" {
		return nil, errs.NotFound("user " + in.ID + " not found")
	}
	return &User{ID: "1", Name: "Ada via " + m.Header["x-caller"]}, nil
}

// tokenGuard checks a header, as a service-to-service auth guard would.
func tokenGuard(ec dygo.ExecutionContext) (bool, error) {
	if p, _ := public.Get(ec); p {
		return true, nil
	}
	m := ec.RPC().Message().(*micro.Message)
	if m.Header["authorization"] != "secret" {
		return false, errs.Unauthorized("bad service token")
	}
	return true, nil
}

func setup(t *testing.T) (*usersHandlers, *micro.Local, *micro.Client) {
	t.Helper()
	h := &usersHandlers{}
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name:      "users",
		Providers: []dygo.Provider{dygo.Value(h)},
	})
	if err != nil {
		t.Fatal(err)
	}
	local := micro.NewLocal(micro.ServerOptions{Global: []dygo.RouteOption{dygo.Guards(dygo.GuardFunc(tokenGuard))}})
	app.ConnectMicroservice(local)
	if err := app.StartAllMicroservices(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close(context.Background()) })
	client := micro.NewClient(local, micro.Before(func(_ context.Context, m *micro.Message) error {
		if m.Header == nil {
			m.Header = map[string]string{}
		}
		m.Header["authorization"] = "secret"
		return nil
	}))
	return h, local, client
}

func TestSendAndErrors(t *testing.T) {
	_, local, client := setup(t)
	ctx := micro.WithHeader(context.Background(), "x-caller", "orders")

	u, err := micro.Send[*User](ctx, client, "users.get", GetUser{ID: "1"})
	if err != nil || u.Name != "Ada via orders" {
		t.Fatalf("Send = %+v, %v", u, err)
	}
	var he *errs.HTTPError
	_, err = micro.Send[*User](ctx, client, "users.get", GetUser{ID: "2"})
	if !errors.As(err, &he) || he.Status != 404 || he.Message != "user 2 not found" {
		t.Errorf("not found err = %v", err)
	}
	_, err = micro.Send[*User](ctx, client, "users.get", GetUser{})
	if !errors.As(err, &he) || he.Status != 400 || he.Code != "ID_REQUIRED" {
		t.Errorf("bad request err = %#v", err)
	}
	_, err = micro.Send[*User](ctx, client, "users.get", "not an object")
	if !errors.As(err, &he) || he.Status != 400 || he.Message != "invalid payload" {
		t.Errorf("bad payload err = %v", err)
	}
	_, err = micro.Send[string](ctx, client, "users.panic", struct{}{})
	if !errors.As(err, &he) || he.Status != 500 || strings.Contains(he.Message, "boom") {
		t.Errorf("panic err = %v", err)
	}
	if _, err := micro.Send[string](ctx, client, "nope", nil); !errors.Is(err, micro.ErrNoHandler) {
		t.Errorf("no handler err = %v", err)
	}

	// Without the token the guard refuses; the public pattern still works.
	bare := micro.NewClient(local)
	_, err = micro.Send[*User](ctx, bare, "users.get", GetUser{ID: "1"})
	if !errors.As(err, &he) || he.Status != 401 {
		t.Errorf("guard err = %v", err)
	}
	if s, err := micro.Send[string](ctx, bare, "users.ping", nil); err != nil || s != "pong" {
		t.Errorf("ping = %q, %v", s, err)
	}
}

func TestEmitRunsEveryEventHandler(t *testing.T) {
	h, local, client := setup(t)
	var reported []error
	local.OnError = func(_ context.Context, _ *micro.Message, err error) { reported = append(reported, err) }
	if err := client.Emit(context.Background(), "orders.created", OrderCreated{OrderID: "o1"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.events, " "); got != "a:o1 b:o1" {
		t.Errorf("events = %q", got)
	}
	if len(reported) != 1 || reported[0].Error() != "b failed" {
		t.Errorf("reported = %v", reported)
	}
}

type dupHandlers struct{}

func (dupHandlers) Patterns(r micro.Router) {
	r.Message("x", micro.Handle(func(context.Context, struct{}) (int, error) { return 1, nil }))
	r.Message("x", micro.Handle(func(context.Context, struct{}) (int, error) { return 2, nil }))
	r.Message("", nil)
}

func TestCollectReportsProblems(t *testing.T) {
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name:      "app",
		Providers: []dygo.Provider{dygo.Value(dupHandlers{})},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	_, err = micro.Collect(app, micro.ServerOptions{})
	if err == nil || !strings.Contains(err.Error(), `message pattern "x" is handled by both`) || !strings.Contains(err.Error(), "empty pattern") {
		t.Fatalf("err = %v", err)
	}
}

func TestHandlersListing(t *testing.T) {
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name:      "users",
		Providers: []dygo.Provider{dygo.Value(&usersHandlers{})},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	hs, err := micro.Collect(app, micro.ServerOptions{Filter: func(p string) bool { return !strings.HasSuffix(p, "panic") }})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(hs.MessagePatterns(), ","); got != "users.get,users.ping" {
		t.Errorf("messages = %s", got)
	}
	if got := strings.Join(hs.EventPatterns(), ","); got != "orders.created" {
		t.Errorf("events = %s", got)
	}
	e, _ := hs.Message("users.get")
	info := e.Info()
	if info.Method != "rpc" || info.Module != "users" || !strings.Contains(info.Handler, "get") || info.Input.Name() != "GetUser" {
		t.Errorf("info = %+v", info)
	}
	if len(hs.All()) != 4 {
		t.Errorf("all = %d", len(hs.All()))
	}
}
