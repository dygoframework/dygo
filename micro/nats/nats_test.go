package nats_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/micro"
	dygonats "github.com/dygoframework/dygo/micro/nats"
	"github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
)

func runServer(t *testing.T) string {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = server.RANDOM_PORT
	s := natstest.RunServer(&opts)
	t.Cleanup(s.Shutdown)
	return s.ClientURL()
}

type Add struct{ A, B int }

type math struct {
	audits atomic.Int32
	got    chan string
}

func (m *math) Patterns(r micro.Router) {
	r.Message("math.add", micro.Handle(func(ctx context.Context, in Add) (int, error) {
		if in.A < 0 {
			return 0, errs.BadRequest("negative")
		}
		return in.A + in.B, nil
	}))
	r.Message("math.whoami", micro.Handle(func(ctx context.Context, _ struct{}) (string, error) {
		msg, _ := micro.MessageFrom(ctx)
		return msg.Header["X-Caller"], nil
	}))
	r.Event("audit.>", micro.HandleEvent(func(ctx context.Context, in Add) error {
		msg, _ := micro.MessageFrom(ctx)
		m.audits.Add(1)
		m.got <- msg.Pattern
		return nil
	}))
}

// The service and the caller are two apps, as in production: the math
// service serves NATS, the gateway calls it through ClientModule.
func TestServiceOverNATS(t *testing.T) {
	url := runServer(t)
	ctx := context.Background()
	m := &math{got: make(chan string, 4)}
	svc, err := dygo.CreateApplicationContext(ctx, &dygo.Module{Name: "math", Providers: []dygo.Provider{dygo.Value(m)}})
	if err != nil {
		t.Fatal(err)
	}
	svc.ConnectMicroservice(dygonats.NewServer(dygonats.Options{URL: url, Queue: "math"}))
	if err := svc.StartAllMicroservices(ctx); err != nil {
		t.Fatal(err)
	}
	defer svc.Close(ctx)

	gw, err := dygo.CreateApplicationContext(ctx, &dygo.Module{
		Name:    "gateway",
		Imports: []*dygo.Module{dygonats.ClientModule(dygonats.ClientOptions{URL: url})},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close(ctx)
	c := dygo.MustGet[*micro.Client](gw)

	sum, err := micro.Send[int](ctx, c, "math.add", Add{A: 2, B: 3})
	if err != nil || sum != 5 {
		t.Fatalf("add = %d, %v", sum, err)
	}
	var he *errs.HTTPError
	if _, err := micro.Send[int](ctx, c, "math.add", Add{A: -1}); !errors.As(err, &he) || he.Status != 400 {
		t.Errorf("err = %v", err)
	}
	who, err := micro.Send[string](micro.WithHeader(ctx, "X-Caller", "gateway"), c, "math.whoami", nil)
	if err != nil || who != "gateway" {
		t.Errorf("whoami = %q, %v", who, err)
	}
	if _, err := micro.Send[int](ctx, c, "math.missing", nil); !errors.Is(err, micro.ErrNoHandler) {
		t.Errorf("missing err = %v", err)
	}
	if err := c.Emit(ctx, "audit.math.add", Add{}); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-m.got:
		if p != "audit.math.add" {
			t.Errorf("event pattern = %q", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event not delivered")
	}
}

func TestListenFailsWithoutBroker(t *testing.T) {
	ctx := context.Background()
	app, err := dygo.CreateApplicationContext(ctx, &dygo.Module{Name: "app", Providers: []dygo.Provider{dygo.Value(&math{})}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(ctx)
	app.ConnectMicroservice(dygonats.NewServer(dygonats.Options{URL: "nats://127.0.0.1:1"}))
	if err := app.StartAllMicroservices(ctx); err == nil {
		t.Fatal("want a connection error")
	}
}
