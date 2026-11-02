package events_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/events"
)

type OrderCreated struct{ ID string }

type OrderPaid struct{ ID string }

type auditable interface{ AuditID() string }

func (o OrderPaid) AuditID() string { return "paid:" + o.ID }

type mailer struct {
	mu   sync.Mutex
	got  []string
	done chan string
}

func (m *mailer) add(s string) {
	m.mu.Lock()
	m.got = append(m.got, s)
	m.mu.Unlock()
}

func (m *mailer) Events(r *events.Registry) {
	events.On(r, func(_ context.Context, e OrderCreated) error {
		m.add("created:" + e.ID)
		return nil
	})
	events.On(r, func(_ context.Context, e auditable) error {
		m.add("audit:" + e.AuditID())
		return nil
	})
	events.On(r, func(_ context.Context, e OrderCreated) error {
		time.Sleep(20 * time.Millisecond)
		m.done <- "async:" + e.ID
		return nil
	}, events.Async())
}

type orders struct{ em *events.Emitter }

func newOrders(em *events.Emitter) *orders { return &orders{em: em} }

func TestModuleDiscoversListeners(t *testing.T) {
	m := &mailer{done: make(chan string, 1)}
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name:    "app",
		Imports: []*dygo.Module{events.Module()},
		Providers: []dygo.Provider{
			dygo.Value(m),
			dygo.Provide(newOrders),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	o := dygo.MustGet[*orders](app)
	if err := o.em.Emit(context.Background(), OrderCreated{ID: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := o.em.Emit(context.Background(), OrderPaid{ID: "2"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(context.Background()); err != nil { // waits for the async listener
		t.Fatal(err)
	}
	select {
	case got := <-m.done:
		if got != "async:1" {
			t.Errorf("async got %q", got)
		}
	default:
		t.Error("shutdown did not wait for the async listener")
	}
	if got := strings.Join(m.got, " "); got != "created:1 audit:paid:2" {
		t.Errorf("got %q", got)
	}
	// After shutdown async listeners no longer start.
	if err := o.em.Emit(context.Background(), OrderCreated{ID: "3"}); err != nil {
		t.Fatal(err)
	}
	if len(m.done) != 0 {
		t.Error("async listener ran after shutdown")
	}
}

func TestErrorsAndPanics(t *testing.T) {
	em := events.New()
	boom := errors.New("boom")
	events.Subscribe(em, func(context.Context, OrderCreated) error { return boom })
	events.Subscribe(em, func(context.Context, OrderCreated) error { panic("bad") })
	ran := false
	unsub := events.Subscribe(em, func(context.Context, OrderCreated) error { ran = true; return nil })
	err := em.Emit(context.Background(), OrderCreated{})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "panicked: bad") || !ran {
		t.Fatalf("err = %v, ran = %v", err, ran)
	}
	unsub()
	ran = false
	_ = em.Emit(context.Background(), OrderCreated{})
	if ran {
		t.Error("unsubscribed listener ran")
	}
	if em.HasListeners(OrderPaid{}) {
		t.Error("OrderPaid has no listeners")
	}
	if err := em.Emit(context.Background(), nil); err == nil {
		t.Error("Emit(nil) should fail")
	}
}

func TestAsyncErrorsGoToHandler(t *testing.T) {
	got := make(chan error, 1)
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name: "app",
		Imports: []*dygo.Module{events.Module(events.OnError(func(_ context.Context, _ any, err error) {
			got <- err
		}))},
	})
	if err != nil {
		t.Fatal(err)
	}
	em := dygo.MustGet[*events.Emitter](app)
	events.Subscribe(em, func(context.Context, OrderCreated) error { return errors.New("late") }, events.Async())
	if err := em.Emit(context.Background(), OrderCreated{}); err != nil {
		t.Fatalf("Emit returned the async error: %v", err)
	}
	if err := (<-got); err.Error() != "late" {
		t.Errorf("err = %v", err)
	}
	app.Close(context.Background())
}

func TestConcurrentEmitAndShutdown(t *testing.T) {
	em := events.New()
	events.Subscribe(em, func(context.Context, OrderCreated) error { return nil }, events.Async())
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_ = em.Emit(context.Background(), OrderCreated{})
			}
		}()
	}
	if err := em.BeforeApplicationShutdown(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}
