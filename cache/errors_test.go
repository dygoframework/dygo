package cache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dygoframework/dygo/cache"
)

// failStore fails every call with err.
type failStore struct{ err error }

func (f failStore) Get(context.Context, string) ([]byte, bool, error) { return nil, false, f.err }
func (f failStore) Set(context.Context, string, []byte, time.Duration) error {
	return f.err
}
func (f failStore) Delete(context.Context, string) error { return f.err }

func TestTTL(t *testing.T) {
	if got := cache.New(cache.Options{TTL: time.Minute}).TTL(); got != time.Minute {
		t.Fatalf("TTL = %v", got)
	}
}

func TestEncodingErrors(t *testing.T) {
	ctx := context.Background()
	c := cache.New(cache.Options{})
	if err := cache.Set(ctx, c, "ch", make(chan int), 0); err == nil {
		t.Fatal("Set: no error for a channel")
	}
	if err := c.SetRaw(ctx, "bad", []byte("{"), 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cache.Get[int](ctx, c, "bad"); err == nil {
		t.Fatal("Get: no error for broken JSON")
	}
	_, err := cache.GetOrSet(ctx, c, "ch2", 0, func(context.Context) (chan int, error) { return make(chan int), nil })
	if err == nil {
		t.Fatal("GetOrSet: no error for a channel")
	}
}

func TestGetOrSetStoreError(t *testing.T) {
	boom := errors.New("boom")
	c := cache.New(cache.Options{Store: failStore{boom}})
	_, err := cache.GetOrSet(context.Background(), c, "k", 0, func(context.Context) (int, error) { return 1, nil })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestGetOrSetWaiterCancelled(t *testing.T) {
	c := cache.New(cache.Options{})
	started, release := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = cache.GetOrSet(context.Background(), c, "slow", 0, func(context.Context) (int, error) {
			close(started)
			<-release
			return 1, nil
		})
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cache.GetOrSet(ctx, c, "slow", 0, func(context.Context) (int, error) { return 2, nil })
	close(release)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestMemoryOverwrite(t *testing.T) {
	ctx := context.Background()
	m := cache.NewMemory(2)
	_ = m.Set(ctx, "a", []byte("1"), 0)
	_ = m.Set(ctx, "a", []byte("2"), 0)
	if b, ok, _ := m.Get(ctx, "a"); !ok || string(b) != "2" {
		t.Fatalf("Get = %q %v", b, ok)
	}
}
