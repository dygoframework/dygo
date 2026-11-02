package redis_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/micro"
	dygoredis "github.com/dygoframework/dygo/micro/redis"
	goredis "github.com/redis/go-redis/v9"
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

type Add struct{ A, B int }

type math struct{ got chan string }

func (m *math) Patterns(r micro.Router) {
	r.Message("math.add", micro.Handle(func(ctx context.Context, in Add) (int, error) {
		if in.A < 0 {
			return 0, errs.BadRequest("negative")
		}
		return in.A + in.B, nil
	}))
	r.Message("math.whoami", micro.Handle(func(ctx context.Context, _ struct{}) (string, error) {
		msg, _ := micro.MessageFrom(ctx)
		return msg.Header["caller"], nil
	}))
	r.Event("audit.*", micro.HandleEvent(func(ctx context.Context, _ Add) error {
		msg, _ := micro.MessageFrom(ctx)
		m.got <- msg.Pattern
		return nil
	}))
}

func TestServiceOverRedis(t *testing.T) {
	addr := redisAddr(t)
	ctx := context.Background()
	m := &math{got: make(chan string, 2)}
	svc, err := dygo.CreateApplicationContext(ctx, &dygo.Module{Name: "math", Providers: []dygo.Provider{dygo.Value(m)}})
	if err != nil {
		t.Fatal(err)
	}
	svc.ConnectMicroservice(dygoredis.NewServer(dygoredis.Options{Addr: addr}))
	if err := svc.StartAllMicroservices(ctx); err != nil {
		t.Fatal(err)
	}
	defer svc.Close(ctx)

	gw, err := dygo.CreateApplicationContext(ctx, &dygo.Module{
		Name:    "gateway",
		Imports: []*dygo.Module{dygoredis.ClientModule(dygoredis.ClientOptions{Redis: goredis.Options{Addr: addr}})},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close(ctx)
	c := dygo.MustGet[*micro.Client](gw)

	if sum, err := micro.Send[int](ctx, c, "math.add", Add{A: 2, B: 3}); err != nil || sum != 5 {
		t.Fatalf("add = %d, %v", sum, err)
	}
	var he *errs.HTTPError
	if _, err := micro.Send[int](ctx, c, "math.add", Add{A: -1}); !errors.As(err, &he) || he.Status != 400 {
		t.Errorf("err = %v", err)
	}
	if who, err := micro.Send[string](micro.WithHeader(ctx, "caller", "gw"), c, "math.whoami", nil); err != nil || who != "gw" {
		t.Errorf("whoami = %q, %v", who, err)
	}
	if _, err := micro.Send[int](ctx, c, "math.none", nil); !errors.Is(err, micro.ErrNoHandler) {
		t.Errorf("none err = %v", err)
	}
	if err := c.Emit(ctx, "audit.add", Add{}); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-m.got:
		if p != "audit.add" {
			t.Errorf("pattern = %q", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event not delivered")
	}
}
