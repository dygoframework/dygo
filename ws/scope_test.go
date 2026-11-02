package ws_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/ws"
)

// msgState is request scoped: one per message with PerMessageScope, one per
// connection without it.
type msgState struct {
	n     int64
	user  string
	ended *sync.Map
}

func (m *msgState) OnRequestEnd(context.Context) { m.ended.Store(m.n, true) }

type counter struct {
	gateway dygo.Scoped[*msgState]
}

func (g *counter) Gateway(r ws.Router) {
	r.On("plain", ws.Handle(g.handle))
	r.On("scoped", ws.Handle(g.handle), ws.PerMessageScope())
}

func (g *counter) handle(ctx context.Context, _ *ws.Client, _ struct{}) (string, error) {
	st, err := g.gateway.Get(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d %s", st.n, st.user), nil
}

func TestPerMessageScope(t *testing.T) {
	var built atomic.Int64
	ended := &sync.Map{}
	app, err := dygo.Create(context.Background(), &dygo.Module{
		Name:    "app",
		Imports: []*dygo.Module{ws.Module(ws.Options{PingInterval: -1})},
		Providers: []dygo.Provider{
			dygo.Provide(func(r *http.Request) *msgState {
				return &msgState{n: built.Add(1), user: r.URL.Query().Get("user"), ended: ended}
			}, dygo.WithScope(dygo.RequestScoped)),
			dygo.Provide(func(s dygo.Scoped[*msgState]) *counter { return &counter{gateway: s} }),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	c, err := dial(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws?user=ada")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.c.CloseNow() }()

	ask := func(event string) string {
		t.Helper()
		c.send(event, struct{}{}, 1)
		m := c.read()
		if m["event"] != event {
			t.Fatalf("%s: got %v", event, m)
		}
		return m["data"].(string)
	}
	// Each scoped message builds its own instance, which sees the handshake
	// request and is ended after the message.
	a, b := ask("scoped"), ask("scoped")
	if a != "1 ada" || b != "2 ada" {
		t.Errorf("scoped = %q, %q", a, b)
	}
	if _, ok := ended.Load(int64(1)); !ok {
		t.Error("instance 1 was not ended")
	}
	// Without the option the instance belongs to the connection.
	p1, p2 := ask("plain"), ask("plain")
	if p1 != p2 || p1 != "3 ada" {
		t.Errorf("plain = %q, %q", p1, p2)
	}
	if _, ok := ended.Load(int64(3)); ok {
		t.Error("the connection's instance ended while connected")
	}
}
