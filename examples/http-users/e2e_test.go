package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/dygoframework/dygo/config"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/examples/http-users/users"
)

func server(t *testing.T, env ...string) *dygotest.Server {
	vars := map[string]string{}
	for i := 0; i+1 < len(env); i += 2 {
		vars[env[i]] = env[i+1]
	}
	cfg, err := config.Load[Config](config.Env(vars))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	return dygotest.New(AppModule(cfg, log)).With(Options(cfg, log)...).CompileHTTP(t)
}

type errBody struct {
	StatusCode int
	Message    string
	Code       string
	Details    []map[string]string
}

const (
	admin = "Bearer admin-token"
	user  = "Bearer user-token"
)

func TestUsersAuth(t *testing.T) {
	srv := server(t)
	srv.GET("/health/live").Do(t).Status(t, 200) // public

	var e errBody
	srv.GET("/api/v1/users").Do(t).Status(t, 401).JSON(t, &e)
	if e.Message != "missing bearer token" {
		t.Fatal(e)
	}
	srv.GET("/api/v1/users").Header("Authorization", "Bearer nope").Do(t).Status(t, 401)
	srv.GET("/api/v1/users").Header("Authorization", user).Do(t).Status(t, 200)
	srv.POST("/api/v1/users").Header("Authorization", user).
		JSON(map[string]string{"email": "x@y.z", "name": "X"}).Do(t).Status(t, 403)
	srv.DELETE("/api/v1/users/0123456789abcdef").Header("Authorization", user).Do(t).Status(t, 403)
	srv.DELETE("/api/v1/users/0123456789abcdef").Header("Authorization", admin).Do(t).Status(t, 404)
}

func TestUsersValidation(t *testing.T) {
	srv := server(t)
	var e errBody
	srv.POST("/api/v1/users").Header("Authorization", admin).
		JSON(map[string]string{"email": "nope", "name": " "}).Do(t).Status(t, 400).JSON(t, &e)
	if e.Message != "validation failed" || len(e.Details) != 2 || e.Details[0]["name"] != "email" || e.Details[1]["name"] != "name" {
		t.Fatal(e)
	}
	srv.GET("/api/v1/users?limit=0").Header("Authorization", user).Do(t).Status(t, 400).JSON(t, &e)
	if len(e.Details) != 1 || e.Details[0]["source"] != "query" || e.Details[0]["name"] != "limit" {
		t.Fatal(e)
	}
}

func TestUsersE2E(t *testing.T) {
	srv := server(t)
	as := func(r *dygotest.Req) *dygotest.Req { return r.Header("Authorization", admin) }

	var u users.User
	res := as(srv.POST("/api/v1/users")).Header("X-Request-ID", "req-1").
		JSON(map[string]string{"email": "ada@example.com", "name": "Ada Lovelace"}).Do(t).Status(t, 201).JSON(t, &u)
	if u.ID == "" || res.Header.Get("X-Request-ID") != "req-1" {
		t.Fatalf("%+v %v", u, res.Header)
	}

	var e errBody
	as(srv.POST("/api/v1/users")).JSON(map[string]string{"email": "ada@example.com", "name": "Again"}).
		Do(t).Status(t, 409).JSON(t, &e)
	if e.Code != "EMAIL_TAKEN" {
		t.Fatal(e)
	}
	as(srv.POST("/api/v1/users")).JSON(map[string]any{"email": "x@y.z", "name": "X", "admin": true}).
		Do(t).Status(t, 400) // StrictJSON rejects unknown fields

	var got users.User
	as(srv.GET("/api/v1/users/"+string(u.ID))).Do(t).Status(t, 200).JSON(t, &got)
	if got.Email != u.Email {
		t.Fatal(got)
	}

	var v2 users.UserV2
	as(srv.GET("/api/v2/users/"+string(u.ID))).Do(t).Status(t, 200).JSON(t, &v2)
	if v2.FirstName != "Ada" || v2.LastName != "Lovelace" {
		t.Fatal(v2)
	}

	as(srv.GET("/api/v1/users/not-an-id")).Do(t).Status(t, 400).JSON(t, &e)
	if len(e.Details) != 1 || e.Details[0]["name"] != "id" {
		t.Fatal(e)
	}
	as(srv.GET("/api/v1/users/0123456789abcdef")).Do(t).Status(t, 404).JSON(t, &e)
	if e.Code != "USER_NOT_FOUND" {
		t.Fatal(e)
	}

	var page users.Page[users.User]
	as(srv.GET("/api/v1/users?limit=10")).Do(t).Status(t, 200).JSON(t, &page)
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatal(page)
	}
	as(srv.GET("/api/v1/users?limit=0")).Do(t).Status(t, 400)
	as(srv.GET("/api/v1/users?limit=lots")).Do(t).Status(t, 400)

	as(srv.PATCH("/api/v1/users/"+string(u.ID))).JSON(map[string]string{"name": "Ada King"}).Do(t).Status(t, 200)
	as(srv.DELETE("/api/v1/users/"+string(u.ID))).Do(t).Status(t, 204)
	as(srv.DELETE("/api/v1/users/"+string(u.ID))).Do(t).Status(t, 404)
	as(srv.POST("/api/v1/users")).JSON(map[string]string{"name": strings.Repeat("x", 2<<20)}).Do(t).Status(t, 413)
}

func TestUsersSSE(t *testing.T) {
	srv := server(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/users/events?max=1", nil)
	req.Header.Set("Authorization", user)
	resp, err := srv.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(resp.Header)
	}
	// The stream is open once headers arrive; now cause an event.
	srv.POST("/api/v1/users").Header("Authorization", admin).
		JSON(map[string]string{"email": "e@x.io", "name": "Eve"}).Do(t).Status(t, 201)

	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var ev users.Event
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(line), "data: ")), &ev); err != nil {
		t.Fatalf("%q: %v", line, err)
	}
	if ev.Type != "created" || ev.User.Name != "Eve" {
		t.Fatal(ev)
	}
	_, _ = io.Copy(io.Discard, resp.Body) // max=1 ends the stream
}

func TestProductionFeatures(t *testing.T) {
	srv := server(t, "RATE_LIMIT", "3", "CORS_ORIGINS", "https://app.example.com")

	// Health checks and docs are public.
	srv.GET("/health/ready").Do(t).Status(t, 200)
	if html := srv.GET("/docs").Do(t).Status(t, 200).Text(); !strings.Contains(html, "/openapi.json") {
		t.Fatal(html)
	}
	var doc struct {
		Info  struct{ Title string }
		Paths map[string]map[string]struct {
			Summary  string
			Tags     []string
			Security *[]map[string][]string
		}
	}
	srv.GET("/openapi.json").Do(t).Status(t, 200).JSON(t, &doc)
	create := doc.Paths["/api/v1/users"]["post"]
	if doc.Info.Title != "Users API" || create.Summary != "Create a user" || create.Tags[0] != "users" || create.Security != nil {
		t.Fatalf("%+v", doc)
	}
	if _, ok := doc.Paths["/api/v2/users/{id}"]["get"]; !ok || doc.Paths["/health/ready"] != nil {
		t.Fatalf("paths %v", doc.Paths)
	}

	// Request IDs, security headers and CORS.
	res := srv.GET("/api/v1/users").Header("Authorization", user).Header("X-Request-ID", "r-1").
		Header("Origin", "https://app.example.com").Do(t).Status(t, 200)
	h := res.Header
	if h.Get("X-Request-ID") != "r-1" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Access-Control-Allow-Origin") != "https://app.example.com" || h.Get("X-RateLimit-Limit") != "3" {
		t.Fatal(h)
	}

	// The rate limit (3 per minute per route and client) applies; health does not count.
	srv.GET("/api/v1/users").Header("Authorization", user).Do(t).Status(t, 200)
	srv.GET("/api/v1/users").Header("Authorization", user).Do(t).Status(t, 200)
	srv.GET("/api/v1/users").Header("Authorization", user).Do(t).Status(t, 429)
	for range 5 {
		srv.GET("/health/live").Do(t).Status(t, 200)
	}
}
