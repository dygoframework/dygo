package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/micro"
)

func TestOrdersFlow(t *testing.T) {
	ctx := context.Background()
	app, err := dygo.Create(ctx, AppModule())
	if err != nil {
		t.Fatal(err)
	}
	local := micro.NewLocal(micro.ServerOptions{})
	app.ConnectMicroservice(local)
	if err := app.StartAllMicroservices(ctx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	res, err := http.Post(srv.URL+"/orders", "application/json", strings.NewReader(`{"item":"pen","qty":2}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 201 || !strings.Contains(string(b), `"id":"1"`) {
		t.Fatalf("POST = %d %s", res.StatusCode, b)
	}
	res, _ = http.Post(srv.URL+"/orders", "application/json", strings.NewReader(`{"item":""}`))
	if res.StatusCode != 400 {
		t.Errorf("bad POST = %d", res.StatusCode)
	}
	res.Body.Close()
	res, _ = http.Get(srv.URL + "/orders/1")
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), `"item":"pen"`) {
		t.Errorf("GET = %s", b)
	}
	o, err := micro.Send[Order](ctx, micro.NewClient(local), "orders.get", OrderByID{ID: "1"})
	if err != nil || o.Qty != 2 {
		t.Errorf("orders.get = %+v, %v", o, err)
	}

	m := dygo.MustGet[*Mailer](app)
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.Lock()
		n := len(m.Sent)
		m.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("confirmation not sent")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := app.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
