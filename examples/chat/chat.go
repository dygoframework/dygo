package main

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/ai"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/validate"
)

// ChatModule is the chat feature: the page, the streamed chat and tickets.
var ChatModule = &dygo.Module{
	Name:        "chat",
	Controllers: []dygo.Provider{dygo.Provide(NewController)},
}

// Limits on what a browser may send, checked before anything reaches the
// vendor.
const (
	maxTurns     = 40
	maxTextBytes = 4000
)

const systemPrompt = "You are a helpful assistant in a demo chat app. Keep answers short."

//go:embed index.html
var page []byte

// Controller holds the routes. The model is whatever ai.Module built from
// .env, or the fake one in tests.
type Controller struct{ model ai.Model }

// NewController is the constructor.
func NewController(m ai.Model) *Controller { return &Controller{model: m} }

// Routes registers the routes.
func (c *Controller) Routes(r dygo.Router) {
	r.Get("/", dygo.Handle(c.page))
	r.Post("/chat", dygo.Handle(c.chat))
	r.Post("/tickets", dygo.Handle(c.ticket))
}

func (c *Controller) page(context.Context, struct{}) (dygo.Responder, error) {
	return dygo.Stream(bytes.NewReader(page), "text/html; charset=utf-8"), nil
}

// Turn is one message of the conversation, as the page sends it.
type Turn struct {
	Role string `json:"role"` // "user" or "assistant"
	Text string `json:"text"`
}

// ChatIn is the body of POST /chat: the whole conversation so far, ending
// with the user's new message. The server keeps no state.
type ChatIn struct {
	Body struct {
		Messages []Turn `json:"messages"`
	} `body:"json"`
}

// Validate is run by the validation pipe; a bad body answers 400.
func (in *ChatIn) Validate(context.Context) error {
	var v validate.Errors
	msgs := in.Body.Messages
	switch {
	case len(msgs) == 0:
		v.Add("body", "messages", "is required")
	case len(msgs) > maxTurns:
		v.Add("body", "messages", fmt.Sprintf("has more than %d turns", maxTurns))
	case msgs[len(msgs)-1].Role != "user":
		v.Add("body", "messages", "must end with a user message")
	}
	for i, m := range msgs {
		field := fmt.Sprintf("messages[%d]", i)
		if m.Role != "user" && m.Role != "assistant" {
			v.Add("body", field+".role", "must be user or assistant")
		}
		if m.Text == "" || len(m.Text) > maxTextBytes {
			v.Add("body", field+".text", fmt.Sprintf("must be 1 to %d bytes", maxTextBytes))
		}
	}
	return v.Err()
}

// chat streams the answer as server-sent events: one event per chunk, then
// "end", or "error" with a message safe to show. If the browser goes away,
// the call to the vendor is cancelled.
func (c *Controller) chat(ctx context.Context, in ChatIn) (dygo.Responder, error) {
	req := ai.Request{System: systemPrompt}
	for _, m := range in.Body.Messages {
		if m.Role == "user" {
			req.Messages = append(req.Messages, ai.User(m.Text))
		} else {
			req.Messages = append(req.Messages, ai.Assistant(m.Text))
		}
	}
	return ai.SSE(c.model.Stream(ctx, req)), nil
}

// Ticket is the typed answer of POST /tickets. Its JSON Schema is sent to the
// model (the tags shape it), and Validate checks the decoded answer; a bad
// one goes back to the model to fix.
type Ticket struct {
	Title    string `json:"title" description:"A short title, at most 80 characters"`
	Priority string `json:"priority" validate:"oneof=low medium high"`
	Summary  string `json:"summary" description:"Two sentences about the problem"`
}

// Validate is what ai.Generate runs on the answer.
func (t *Ticket) Validate(context.Context) error {
	var v validate.Errors
	if t.Title == "" || len(t.Title) > 80 {
		v.Add("body", "title", "must be 1 to 80 characters")
	}
	if t.Priority != "low" && t.Priority != "medium" && t.Priority != "high" {
		v.Add("body", "priority", "must be low, medium or high")
	}
	return v.Err()
}

// TicketIn is the body of POST /tickets.
type TicketIn struct {
	Body struct {
		Email string `json:"email"`
	} `body:"json"`
}

// Validate checks the email text.
func (in *TicketIn) Validate(context.Context) error {
	var v validate.Errors
	if in.Body.Email == "" || len(in.Body.Email) > maxTextBytes {
		v.Add("body", "email", fmt.Sprintf("must be 1 to %d bytes", maxTextBytes))
	}
	return v.Err()
}

func (c *Controller) ticket(ctx context.Context, in TicketIn) (Ticket, error) {
	t, err := ai.Generate[Ticket](ctx, c.model, ai.Request{
		System:   "Turn the customer's email into a support ticket.",
		Messages: []ai.Message{ai.User(in.Body.Email)},
	})
	if err != nil {
		// The vendor's message stays in the logs; the client gets a safe one.
		return Ticket{}, errs.ServiceUnavailable(ai.PublicMessage(err), errs.Cause(err))
	}
	return t, nil
}
