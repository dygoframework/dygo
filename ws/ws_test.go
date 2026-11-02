package ws_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/ws"
)

type Say struct {
	Room string `json:"room"`
	Text string `json:"text"`
}

type Ack struct {
	OK bool `json:"ok"`
}

type userKey struct{}

type chat struct {
	server *ws.Server
	mu     sync.Mutex
	gone   []ws.DisconnectReason
	left   chan struct{}
}

func newChat(s *ws.Server) *chat { return &chat{server: s, left: make(chan struct{}, 4)} }

func (c *chat) Gateway(g ws.Router) {
	g = g.Use(dygo.Guards(dygo.GuardFunc(func(ec dygo.ExecutionContext) (bool, error) {
		cl := ec.WS().Client().(*ws.Client)
		if _, ok := cl.Get(userKey{}); !ok {
			return false, errs.Unauthorized("login first")
		}
		return true, nil
	})))
	g.On("join", ws.Handle(func(_ context.Context, cl *ws.Client, in Say) (Ack, error) {
		cl.Join(in.Room)
		return Ack{OK: true}, nil
	}))
	g.On("noop", ws.HandleNoReply(func(context.Context, *ws.Client, struct{}) error { return nil }))
	g.On("say", ws.Handle(func(ctx context.Context, cl *ws.Client, in Say) (Ack, error) {
		if in.Text == "" {
			return Ack{}, errs.BadRequest("text is required")
		}
		user, _ := cl.Get(userKey{})
		return Ack{OK: true}, c.server.To(in.Room).Except(ctx, cl, "message", Say{Room: in.Room, Text: user.(string) + ": " + in.Text})
	}))
}

func (c *chat) OnConnection(_ context.Context, cl *ws.Client) error {
	name := cl.Request().URL.Query().Get("user")
	if name == "mallory" {
		return errs.Forbidden("banned")
	}
	if name != "" {
		cl.Set(userKey{}, name)
	}
	return nil
}

func (c *chat) OnDisconnect(_ *ws.Client, r ws.DisconnectReason) {
	c.mu.Lock()
	c.gone = append(c.gone, r)
	c.mu.Unlock()
	c.left <- struct{}{}
}

type conn struct {
	t *testing.T
	c *websocket.Conn
}

func dial(t *testing.T, url string) (*conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	return &conn{t: t, c: c}, nil
}

func (c *conn) send(event string, data any, id int) {
	b, _ := json.Marshal(map[string]any{"event": event, "data": data, "id": id})
	if err := c.c.Write(context.Background(), websocket.MessageText, b); err != nil {
		c.t.Fatal(err)
	}
}

func (c *conn) read() map[string]any {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.c.Read(ctx)
	if err != nil {
		c.t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func TestGateway(t *testing.T) {
	var c *chat
	app, err := dygo.Create(context.Background(), &dygo.Module{
		Name:      "app",
		Imports:   []*dygo.Module{ws.Module(ws.Options{})},
		Providers: []dygo.Provider{dygo.Provide(func(s *ws.Server) *chat { c = newChat(s); return c })},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	ada, err := dial(t, url+"?user=ada")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := dial(t, url+"?user=bob")
	if err != nil {
		t.Fatal(err)
	}
	ada.send("noop", nil, 0)
	ada.send("join", Say{Room: "go"}, 1)
	bob.send("join", Say{Room: "go"}, 1)
	if m := ada.read(); m["event"] != "join" { // noop sent nothing back
		t.Errorf("ada got %v", m)
	}
	bob.read()
	ada.send("say", Say{Room: "go", Text: "hi"}, 2)
	if m := ada.read(); m["event"] != "say" || m["id"] != 2.0 || m["data"].(map[string]any)["ok"] != true {
		t.Errorf("ack = %v", m)
	}
	if m := bob.read(); m["event"] != "message" || m["data"].(map[string]any)["text"] != "ada: hi" {
		t.Errorf("bob got %v", m)
	}
	ada.send("say", Say{Room: "go"}, 3)
	if m := ada.read(); m["event"] != "exception" || m["id"] != 3.0 || m["data"].(map[string]any)["statusCode"] != 400.0 {
		t.Errorf("exception = %v", m)
	}
	ada.send("nope", nil, 4)
	if m := ada.read(); m["data"].(map[string]any)["statusCode"] != 404.0 {
		t.Errorf("unknown event = %v", m)
	}

	// Anonymous clients connect but the guard refuses their events.
	anon, err := dial(t, url)
	if err != nil {
		t.Fatal(err)
	}
	anon.send("join", Say{Room: "go"}, 1)
	if m := anon.read(); m["data"].(map[string]any)["statusCode"] != 401.0 {
		t.Errorf("guard = %v", m)
	}

	// A refused connection is closed with 1008 and the error message.
	mal, err := dial(t, url+"?user=mallory")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = mal.c.Read(context.Background())
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.StatusPolicyViolation || ce.Reason != "banned" {
		t.Errorf("refused close = %v", err)
	}

	srvWS := dygo.MustGet[*ws.Server](app)
	if n := len(srvWS.To("go").Clients()); n != 2 {
		t.Errorf("room size = %d", n)
	}
	bob.c.Close(websocket.StatusNormalClosure, "bye")
	<-c.left
	if n := len(srvWS.To("go").Clients()); n != 1 {
		t.Errorf("room size after leave = %d", n)
	}

	// Shutdown closes the remaining clients with 1001. Clients answer the
	// close handshake while they read.
	readErr := make(chan error, 2)
	for _, cl := range []*conn{ada, anon} {
		go func() { _, _, err := cl.c.Read(context.Background()); readErr <- err }()
	}
	start := time.Now()
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("shutdown took %s", d)
	}
	err = <-readErr
	if !errors.As(err, &ce) || ce.Code != websocket.StatusGoingAway {
		t.Errorf("shutdown close = %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.gone) < 1 || c.gone[0].Code != 1000 || c.gone[0].Text != "bye" {
		t.Errorf("disconnect reasons = %+v", c.gone)
	}
}

type dup struct{}

func (dup) Gateway(g ws.Router) {
	h := ws.HandleNoReply(func(context.Context, *ws.Client, struct{}) error { return nil })
	g.On("a", h)
	g.On("a", h)
	g.On("exception", h)
}

func TestDuplicateEventsFailBoot(t *testing.T) {
	_, err := dygo.Create(context.Background(), &dygo.Module{
		Name:      "app",
		Imports:   []*dygo.Module{ws.Module(ws.Options{})},
		Providers: []dygo.Provider{dygo.Value(dup{})},
	})
	if err == nil || !strings.Contains(err.Error(), `ws event "a" is handled by both`) || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("err = %v", err)
	}
}
