package ws_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/ws"
)

type quiet struct{}

func (quiet) Gateway(g ws.Router) {}

func benchBroadcast(b *testing.B, clients int, stalled int) {
	srv := ws.NewServer(ws.Options{PingInterval: -1, WriteTimeout: 200 * time.Millisecond})
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{Name: "a", Providers: []dygo.Provider{dygo.Value(quiet{})}})
	if err != nil {
		b.Fatal(err)
	}
	if err := srv.Register(app); err != nil {
		b.Fatal(err)
	}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	url := "ws" + strings.TrimPrefix(hs.URL, "http")
	ctx := context.Background()
	for i := range clients {
		c, _, err := websocket.Dial(ctx, url, nil)
		if err != nil {
			b.Fatal(err)
		}
		c.SetReadLimit(1 << 20)
		defer func() { _ = c.CloseNow() }()
		if i < stalled {
			continue // never reads: a slow client
		}
		go func() {
			for {
				if _, _, err := c.Read(ctx); err != nil {
					return
				}
			}
		}()
	}
	for len(srv.Clients()) < clients {
		time.Sleep(time.Millisecond)
	}
	payload := map[string]string{"text": strings.Repeat("x", 2000)}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = srv.Broadcast(ctx, "news", payload)
	}
}

func BenchmarkBroadcast100(b *testing.B)           { benchBroadcast(b, 100, 0) }
func BenchmarkBroadcast100OneStalled(b *testing.B) { benchBroadcast(b, 100, 1) }
