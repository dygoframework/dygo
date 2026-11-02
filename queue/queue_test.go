package queue_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/queue"
)

type WelcomeEmail struct{ UserID string }

type mailer struct {
	mu    sync.Mutex
	sent  []string
	flaky atomic.Int32
}

func (m *mailer) Process(r *queue.Registry) {
	queue.Handle(r, "welcome", func(ctx context.Context, e WelcomeEmail) error {
		j, _ := queue.JobFrom(ctx)
		m.mu.Lock()
		m.sent = append(m.sent, e.UserID+"@"+j.Queue)
		m.mu.Unlock()
		return nil
	})
	queue.Handle(r, "flaky", func(ctx context.Context, e WelcomeEmail) error {
		if m.flaky.Add(1) < 3 {
			return errors.New("smtp down")
		}
		return nil
	})
	queue.Handle(r, "report", func(ctx context.Context, e WelcomeEmail) error {
		return errors.New("always")
	}, queue.FromQueue("reports"))
}

func TestModuleRunsJobs(t *testing.T) {
	mem := queue.NewMemory(queue.MemoryOptions{Backoff: func(int) time.Duration { return time.Millisecond }})
	var attemptErrs atomic.Int32
	m := &mailer{}
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name: "app",
		Imports: []*dygo.Module{queue.Module(queue.Options{
			Driver:  mem,
			OnError: func(context.Context, *queue.Job, error) { attemptErrs.Add(1) },
		})},
		Providers: []dygo.Provider{dygo.Value(m)},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	q := dygo.MustGet[*queue.Queue](app)
	if _, err := q.Add(ctx, "welcome", WelcomeEmail{UserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Add(ctx, "welcome", WelcomeEmail{UserID: "u2"}, queue.Delay(10*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Add(ctx, "flaky", WelcomeEmail{}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Add(ctx, "report", WelcomeEmail{}, queue.On("reports"), queue.Attempts(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Add(ctx, "nobody", WelcomeEmail{}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Add(ctx, "welcome", WelcomeEmail{UserID: "dup"}, queue.ID("x"), queue.Delay(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Add(ctx, "welcome", WelcomeEmail{UserID: "dup"}, queue.ID("x")); !errors.Is(err, queue.ErrDuplicate) {
		t.Errorf("dup err = %v", err)
	}
	wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for mem.Pending() > 1 { // the hour-delayed job stays
		if wctx.Err() != nil {
			t.Fatal("jobs did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if err := app.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.sent, " "); got != "u1@default u2@default" {
		t.Errorf("sent = %q", got)
	}
	if m.flaky.Load() != 3 {
		t.Errorf("flaky ran %d times", m.flaky.Load())
	}
	// The report job (2 attempts) fails; "nobody" sits in the default queue
	// without a handler and fails at once.
	failed := mem.Failed()
	var names []string
	for _, f := range failed {
		names = append(names, f.Job.Name)
	}
	if strings.Join(names, " ") != "nobody report" && strings.Join(names, " ") != "report nobody" {
		t.Errorf("failed = %v", names)
	}
	if n := attemptErrs.Load(); n != 2+2+1 {
		t.Errorf("OnError calls = %d", n)
	}
}

func TestBadPayloadAndPanicDoNotRetryForever(t *testing.T) {
	mem := queue.NewMemory(queue.MemoryOptions{Backoff: func(int) time.Duration { return 0 }})
	q := queue.New(queue.Options{Driver: mem})
	var panics atomic.Int32
	err := q.Register(processorFunc(func(r *queue.Registry) {
		queue.Handle(r, "num", func(context.Context, int) error { return nil })
		queue.Handle(r, "panic", func(context.Context, int) error { panics.Add(1); panic("x") })
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, _ = q.Add(ctx, "num", "not a number")
	_, _ = q.Add(ctx, "panic", 1)
	if err := q.Start(); err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := mem.Wait(wctx); err != nil {
		t.Fatal(err)
	}
	if err := q.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if panics.Load() != 3 {
		t.Errorf("panic job ran %d times, want 3 attempts", panics.Load())
	}
	for _, f := range mem.Failed() {
		if f.Job.Name == "num" && (f.Job.Attempt != 1 || !strings.Contains(f.Err.Error(), "decoding payload")) {
			t.Errorf("num failed with %v after %d attempts", f.Err, f.Job.Attempt)
		}
	}
	if len(mem.Failed()) != 2 {
		t.Errorf("failed = %d", len(mem.Failed()))
	}
}

func TestDuplicateHandler(t *testing.T) {
	q := queue.New(queue.Options{})
	err := q.Register(processorFunc(func(r *queue.Registry) {
		queue.Handle(r, "a", func(context.Context, int) error { return nil })
		queue.Handle(r, "a", func(context.Context, int) error { return nil })
	}))
	if err == nil || !strings.Contains(err.Error(), `"a" has two handlers`) {
		t.Errorf("err = %v", err)
	}
}

func TestStopWaitsForRunningJobs(t *testing.T) {
	mem := queue.NewMemory(queue.MemoryOptions{})
	q := queue.New(queue.Options{Driver: mem, Concurrency: 2})
	started := make(chan struct{}, 2)
	var finished atomic.Int32
	_ = q.Register(processorFunc(func(r *queue.Registry) {
		queue.Handle(r, "slow", func(context.Context, int) error {
			started <- struct{}{}
			time.Sleep(30 * time.Millisecond)
			finished.Add(1)
			return nil
		})
	}))
	for i := range 4 {
		_, _ = q.Add(context.Background(), "slow", i)
	}
	_ = q.Start()
	<-started
	<-started
	if err := q.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if finished.Load() != 2 {
		t.Errorf("finished = %d, want the 2 running jobs", finished.Load())
	}
	if mem.Pending() != 2 {
		t.Errorf("pending = %d", mem.Pending())
	}
}

type processorFunc func(r *queue.Registry)

func (f processorFunc) Process(r *queue.Registry) { f(r) }
