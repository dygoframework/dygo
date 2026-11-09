package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dygoframework/dygo/ai"
	"github.com/dygoframework/dygo/ai/fake"
	"github.com/dygoframework/dygo/validate"
)

type Ticket struct {
	Title    string `json:"title" validate:"required,max=80"`
	Priority string `json:"priority" validate:"oneof=low medium high"`
	Count    int    `json:"count,omitempty" default:"1" example:"2"`
	Default  string `json:"default,omitempty"` // a field named like a keyword
	Tags     []Tag  `json:"tags,omitempty"`
}

type Tag struct {
	Name string `json:"name" format:"x"`
}

// Validate is what validate.Methods calls.
func (t *Ticket) Validate(context.Context) error {
	var v validate.Errors
	if t.Title == "" {
		v.Add("body", "title", "is required")
	}
	return v.Err()
}

func TestGenerate(t *testing.T) {
	m := fake.New(fake.Reply("```json\n{\"title\":\"Login broken\",\"priority\":\"high\"}\n```"))
	got, err := ai.Generate[Ticket](context.Background(), m, ai.Prompt("make a ticket"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Login broken" || got.Priority != "high" {
		t.Fatalf("got %+v", got)
	}
	req := m.Requests()[0]
	if req.Schema == nil || req.Schema.Name != "Ticket" {
		t.Fatalf("schema %+v", req.Schema)
	}
}

func TestGenerateFixesOnce(t *testing.T) {
	m := fake.New(fake.Reply(`not json`), fake.Reply(`{"title":"","priority":"low"}`))
	caller := ai.Prompt("make a ticket")
	_, err := ai.Generate[Ticket](context.Background(), m, caller)
	if !errors.Is(err, ai.ErrBadOutput) || !strings.Contains(err.Error(), "title") {
		t.Fatalf("got %v", err)
	}
	reqs := m.Requests()
	if len(reqs) != 2 || len(reqs[1].Messages) != 3 || !strings.Contains(reqs[1].Messages[2].Text(), "not JSON") {
		t.Fatalf("fix request %+v", reqs)
	}
	if reqs[1].Messages[1].Role != ai.RoleAssistant || reqs[1].Messages[1].Text() != "not json" {
		t.Fatalf("bad answer not sent back: %+v", reqs[1].Messages)
	}
	if len(caller.Messages) != 1 {
		t.Fatal("caller's messages changed")
	}

	m = fake.New(fake.Reply(`{}`), fake.Reply(`{"title":"ok","priority":"low"}`))
	if got, err := ai.Generate[*Ticket](context.Background(), m, ai.Prompt("x")); err != nil || got.Title != "ok" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestGenerateOptions(t *testing.T) {
	m := fake.New(fake.Reply(`{}`))
	if _, err := ai.Generate[Ticket](context.Background(), m, ai.Prompt("x"), ai.Fixes(0)); !errors.Is(err, ai.ErrBadOutput) {
		t.Fatalf("got %v", err)
	}
	if len(m.Requests()) != 1 {
		t.Fatal("Fixes(0) should not retry")
	}

	m = fake.New(fake.Reply(`{}`))
	if _, err := ai.Generate[Ticket](context.Background(), m, ai.Prompt("x"), ai.CheckWith(nil)); err != nil {
		t.Fatalf("no check: %v", err)
	}

	boom := errors.New("boom")
	check := validate.SchemaFunc(func(context.Context, any) error { return boom })
	m = fake.New(fake.Reply(`{}`))
	if _, err := ai.Generate[Ticket](context.Background(), m, ai.Prompt("x"), ai.CheckWith(check)); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}

	m = fake.New(fake.Reply(`{}`), fake.Reply(`{}`), fake.Reply(`{}`), fake.Reply(`{}`), fake.Reply(`{}`))
	_, _ = ai.Generate[Ticket](context.Background(), m, ai.Prompt("x"), ai.Fixes(10))
	if n := len(m.Requests()); n != 1+ai.MaxFixes {
		t.Fatalf("%d calls", n)
	}
}

// stopModel answers with a stop reason.
type stopModel struct {
	fake.Model
	stop string
}

func (s *stopModel) Chat(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{Message: ai.Assistant(`{"ti`), StopReason: s.stop}, nil
}

func TestGenerateStops(t *testing.T) {
	ctx := context.Background()
	if _, err := ai.Generate[Ticket](ctx, &stopModel{stop: "refused"}, ai.Prompt("x")); !errors.Is(err, ai.ErrRefused) {
		t.Fatalf("got %v", err)
	}
	if _, err := ai.Generate[Ticket](ctx, &stopModel{stop: "max_tokens"}, ai.Prompt("x")); !errors.Is(err, ai.ErrBadOutput) {
		t.Fatalf("got %v", err)
	}
	if _, err := ai.Generate[Ticket](ctx, fake.New(fake.Fail(ai.ErrRateLimited)), ai.Prompt("x")); !errors.Is(err, ai.ErrRateLimited) {
		t.Fatalf("got %v", err)
	}
	if _, err := ai.Generate[string](ctx, fake.New(), ai.Prompt("x")); !errors.Is(err, ai.ErrInvalidRequest) {
		t.Fatalf("got %v", err)
	}
}

func TestSchemaFor(t *testing.T) {
	s, err := ai.SchemaFor[Ticket]()
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := ai.SchemaFor[Ticket](); again != s {
		t.Fatal("schema not cached")
	}
	var tree map[string]any
	if err := json.Unmarshal(s.JSON, &tree); err != nil {
		t.Fatal(err)
	}
	props := tree["properties"].(map[string]any)
	if tree["type"] != "object" || tree["additionalProperties"] != false || props["default"] == nil {
		t.Fatalf("schema %s", s.JSON)
	}
	for _, kw := range []string{"maxLength", `"default":1`, "examples", "int64"} {
		if strings.Contains(string(s.JSON), kw) {
			t.Errorf("schema keeps %s: %s", kw, s.JSON)
		}
	}
	if !strings.Contains(string(s.JSON), `"enum":["low","medium","high"]`) {
		t.Errorf("enum lost: %s", s.JSON)
	}
	tag := tree["$defs"].(map[string]any)["Tag"].(map[string]any)
	if tag["additionalProperties"] != false {
		t.Errorf("$defs not closed: %v", tag)
	}

	type Weird struct {
		When string `json:"when" validate:"datetime"`
	}
	if s, _ := ai.SchemaFor[Weird](); s.Name != "Weird" || !strings.Contains(string(s.JSON), "date-time") {
		t.Fatalf("got %+v", s)
	}
	if s, _ := ai.SchemaFor[struct{ A int }](); s.Name != "output" {
		t.Fatalf("anonymous name %q", s.Name)
	}
}

func TestEmit(t *testing.T) {
	type sent struct {
		event string
		data  any
	}
	var got []sent
	emit := func(_ context.Context, event string, data any) error {
		got = append(got, sent{event, data})
		return nil
	}
	ctx := context.Background()
	m := fake.New(fake.Chunks("Hel", "", "lo"), fake.Broken(ai.ErrRateLimited, "a"), fake.Broken(context.Canceled, "b"))
	if err := ai.Emit(ctx, m.Stream(ctx, ai.Prompt("x")), emit, "answer"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != (sent{"answer", "Hel"}) || got[1] != (sent{"answer", "lo"}) || got[2].event != "answer.end" {
		t.Fatalf("got %+v", got)
	}

	got = nil
	if err := ai.Emit(ctx, m.Stream(ctx, ai.Prompt("x")), emit, "answer"); !errors.Is(err, ai.ErrRateLimited) {
		t.Fatalf("got %v", err)
	}
	if last := got[len(got)-1]; last.event != "answer.error" || last.data != ai.PublicMessage(ai.ErrRateLimited) {
		t.Fatalf("got %+v", got)
	}

	got = nil
	if err := ai.Emit(ctx, m.Stream(ctx, ai.Prompt("x")), emit, "answer"); !errors.Is(err, context.Canceled) || len(got) != 1 {
		t.Fatalf("cancel: %v, %+v", err, got)
	}

	boom := errors.New("closed")
	m = fake.New(fake.Chunks("a", "b"))
	calls := 0
	failing := func(context.Context, string, any) error { calls++; return boom }
	if err := ai.Emit(ctx, m.Stream(ctx, ai.Prompt("x")), failing, "answer"); !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("got %v after %d calls", err, calls)
	}
}
