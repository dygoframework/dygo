package cqrs_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/cqrs"
)

type CreateUser struct{ Email string }

type UserByEmail struct{ Email string }

type User struct{ Email string }

type userHandlers struct{ users map[string]*User }

func (h *userHandlers) CQRS(r *cqrs.Registry) {
	cqrs.HandleCommand(r, func(_ context.Context, c CreateUser) error {
		if c.Email == "" {
			return errors.New("email required")
		}
		h.users[c.Email] = &User{Email: c.Email}
		return nil
	})
	cqrs.HandleQuery(r, func(_ context.Context, q UserByEmail) (*User, error) {
		return h.users[q.Email], nil
	})
}

func TestModule(t *testing.T) {
	var log []string
	logMW := func(next cqrs.Handler) cqrs.Handler {
		return func(ctx context.Context, msg any) (any, error) {
			log = append(log, fmt.Sprintf("%T", msg))
			return next(ctx, msg)
		}
	}
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name:      "app",
		Imports:   []*dygo.Module{cqrs.Module(cqrs.Use(logMW))},
		Providers: []dygo.Provider{dygo.Value(&userHandlers{users: map[string]*User{}})},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	ctx := context.Background()
	cmds := dygo.MustGet[*cqrs.CommandBus](app)
	qs := dygo.MustGet[*cqrs.QueryBus](app)
	if err := cmds.Execute(ctx, CreateUser{Email: "a@b.c"}); err != nil {
		t.Fatal(err)
	}
	if err := cmds.Execute(ctx, CreateUser{}); err == nil {
		t.Error("want the handler's error")
	}
	u, err := cqrs.Ask[*User](ctx, qs, UserByEmail{Email: "a@b.c"})
	if err != nil || u == nil || u.Email != "a@b.c" {
		t.Fatalf("Ask = %v, %v", u, err)
	}
	if _, err := cqrs.Ask[string](ctx, qs, UserByEmail{}); err == nil || !strings.Contains(err.Error(), "not string") {
		t.Errorf("wrong type err = %v", err)
	}
	if err := cmds.Execute(ctx, UserByEmail{}); !errors.Is(err, cqrs.ErrNoHandler) {
		t.Errorf("no handler err = %v", err)
	}
	if got := strings.Join(log, " "); got != "cqrs_test.CreateUser cqrs_test.CreateUser cqrs_test.UserByEmail cqrs_test.UserByEmail" {
		t.Errorf("middleware saw %q", got)
	}
}

type dupHandlers struct{}

func (dupHandlers) CQRS(r *cqrs.Registry) {
	cqrs.HandleCommand(r, func(context.Context, CreateUser) error { return nil })
	cqrs.HandleCommand(r, func(context.Context, CreateUser) error { panic("x") })
}

func TestDuplicateHandlerFailsBoot(t *testing.T) {
	_, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name:      "app",
		Imports:   []*dygo.Module{cqrs.Module()},
		Providers: []dygo.Provider{dygo.Value(dupHandlers{})},
	})
	if err == nil || !strings.Contains(err.Error(), "command cqrs_test.CreateUser has two handlers") {
		t.Fatalf("err = %v", err)
	}
}

type panicky struct{}

func (panicky) CQRS(r *cqrs.Registry) {
	cqrs.HandleCommand(r, func(context.Context, CreateUser) error { panic("boom") })
}

func TestNewAndPanics(t *testing.T) {
	cmds, _, register := cqrs.New()
	if err := register(panicky{}); err != nil {
		t.Fatal(err)
	}
	err := cmds.Execute(context.Background(), CreateUser{})
	if err == nil || !strings.Contains(err.Error(), "panicked: boom") {
		t.Errorf("err = %v", err)
	}
	if err := cmds.Execute(context.Background(), nil); err == nil {
		t.Error("nil command should fail")
	}
}
