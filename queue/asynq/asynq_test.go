package asynq_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/queue"
	dygoasynq "github.com/dygoframework/dygo/queue/asynq"
)

// redisAddr uses the server in DYGO_TEST_REDIS_ADDR, or starts a throwaway
// redis-server. The test is skipped when neither is available.
func redisAddr(t *testing.T) string {
	t.Helper()
	if addr := os.Getenv("DYGO_TEST_REDIS_ADDR"); addr != "" {
		dygotest.Integration(t)
		return addr
	}
	bin := dygotest.RequireCommand(t, "redis-server")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cmd := exec.Command(bin, "--port", fmt.Sprint(port), "--save", "", "--appendonly", "no")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for range 100 {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return addr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("redis did not start")
	return ""
}

type Signup struct{ Email string }

type worker struct {
	got   chan string
	tries atomic.Int32
}

func (w *worker) Process(r *queue.Registry) {
	queue.Handle(r, "signup", func(ctx context.Context, s Signup) error {
		j, _ := queue.JobFrom(ctx)
		if w.tries.Add(1) == 1 {
			return errors.New("first try fails")
		}
		w.got <- fmt.Sprintf("%s attempt %d/%d", s.Email, j.Attempt, j.MaxAttempts)
		return nil
	})
	queue.Handle(r, "never", func(ctx context.Context, s Signup) error {
		w.got <- "never"
		return queue.SkipRetry(errors.New("bad"))
	}, queue.FromQueue("other"))
}

func TestAsynqDriver(t *testing.T) {
	addr := redisAddr(t)
	d := dygoasynq.New(dygoasynq.RedisClientOpt{Addr: addr}, dygoasynq.Options{
		RetryDelay: func(int, error) time.Duration { return 10 * time.Millisecond },
	})
	defer d.Close()
	w := &worker{got: make(chan string, 4)}
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name:      "app",
		Imports:   []*dygo.Module{queue.Module(queue.Options{Driver: d})},
		Providers: []dygo.Provider{dygo.Value(w)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	q := dygo.MustGet[*queue.Queue](app)
	ctx := context.Background()
	if _, err := q.Add(ctx, "signup", Signup{Email: "a@b.c"}, queue.ID("s1")); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Add(ctx, "signup", Signup{Email: "a@b.c"}, queue.ID("s1")); !errors.Is(err, queue.ErrDuplicate) {
		t.Errorf("dup err = %v", err)
	}
	if _, err := q.Add(ctx, "never", Signup{}, queue.On("other")); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	timeout := time.After(15 * time.Second)
	for len(seen) < 2 {
		select {
		case s := <-w.got:
			seen[s] = true
		case <-timeout:
			t.Fatalf("got %v", seen)
		}
	}
	if !seen["a@b.c attempt 2/3"] || !seen["never"] {
		t.Errorf("got %v", seen)
	}
}
