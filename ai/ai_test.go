package ai_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dygoframework/dygo/ai"
	"github.com/dygoframework/dygo/ai/fake"
)

func TestMessageText(t *testing.T) {
	if got := ai.User("hi").Text(); got != "hi" {
		t.Fatalf("got %q", got)
	}
	m := ai.Message{Role: ai.RoleUser, Parts: []ai.Part{ai.TextPart{Text: "a"}, ai.TextPart{Text: "b"}}}
	if got := m.Text(); got != "ab" {
		t.Fatalf("got %q", got)
	}
	if got := (ai.Message{}).Text(); got != "" {
		t.Fatalf("got %q", got)
	}
	a := ai.Assistant("yes")
	if a.Role != ai.RoleAssistant || a.Text() != "yes" {
		t.Fatalf("got %+v", a)
	}
	r := ai.Response{Message: a, Usage: ai.Usage{InputTokens: 3, OutputTokens: 4}}
	if r.Text() != "yes" || r.Usage.Total() != 7 {
		t.Fatalf("got %+v", r)
	}
}

func TestValidate(t *testing.T) {
	temp := func(v float64) *float64 { return &v }
	ok := ai.Prompt("hi")
	ok.Messages = append(ok.Messages, ai.Assistant("hello"))
	ok.Temperature = temp(2)
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]ai.Request{
		"no messages":   {},
		"system role":   {Messages: []ai.Message{{Role: ai.RoleSystem, Parts: []ai.Part{ai.TextPart{Text: "x"}}}}},
		"unknown role":  {Messages: []ai.Message{{Role: "tool", Parts: []ai.Part{ai.TextPart{Text: "x"}}}}},
		"no parts":      {Messages: []ai.Message{{Role: ai.RoleUser}}},
		"nil part":      {Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{nil}}}},
		"negative max":  {Messages: []ai.Message{ai.User("x")}, MaxTokens: -1},
		"temp too high": {Messages: []ai.Message{ai.User("x")}, Temperature: temp(2.1)},
		"temp negative": {Messages: []ai.Message{ai.User("x")}, Temperature: temp(-0.1)},
	}
	for name, r := range bad {
		if err := r.Validate(); !errors.Is(err, ai.ErrInvalidRequest) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestAsk(t *testing.T) {
	m := fake.New(fake.Reply("Paris"), fake.Fail(ai.ErrRefused))
	got, err := ai.Ask(context.Background(), m, "Capital of France?")
	if err != nil || got != "Paris" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := ai.Ask(context.Background(), m, "again"); !errors.Is(err, ai.ErrRefused) {
		t.Fatalf("got %v", err)
	}
	reqs := m.Requests()
	if len(reqs) != 2 || reqs[0].Messages[0].Text() != "Capital of France?" {
		t.Fatalf("got %+v", reqs)
	}
}
