// Package ai calls large language models from a Dygo app.
//
// Apps code against one small interface, [Model]. Providers for each vendor
// live in their own sub-modules, and tests use the scripted model in ai/fake:
//
//	type ChatService struct{ model ai.Model }
//
//	func NewChatService(m ai.Model) *ChatService { return &ChatService{model: m} }
//
//	func (s *ChatService) Summary(ctx context.Context, text string) (string, error) {
//		return ai.Ask(ctx, s.model, "Summarize: "+text)
//	}
//
// [Generate] asks for a typed answer and decodes it into a Go struct.
//
// [Provide] puts a model in the container, wrapped in [Middleware] such as
// [Retry], [Timeout] and [Log]. A streamed answer goes to the browser with
// [SSE], or over a WebSocket with [Emit].
//
// The package uses only the standard library and Dygo core.
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
)

// Model is a chat model. Implementations must be safe for concurrent use.
type Model interface {
	// Chat sends the request and waits for the whole answer.
	Chat(ctx context.Context, req Request) (Response, error)
	// Stream sends the request and yields the answer in chunks as they
	// arrive. An error ends the stream. Stopping the loop early cancels the
	// call.
	Stream(ctx context.Context, req Request) iter.Seq2[Chunk, error]
}

// Role says who wrote a message.
type Role string

// The roles a message can have.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Part is one piece of a message. [TextPart] is the only part for now; images,
// audio and files come later.
type Part interface{ isPart() }

// TextPart is plain text.
type TextPart struct{ Text string }

func (TextPart) isPart() {}

// Message is one turn of a conversation.
type Message struct {
	Role  Role
	Parts []Part
}

// User returns a user message holding text.
func User(text string) Message { return Message{Role: RoleUser, Parts: []Part{TextPart{text}}} }

// Assistant returns an assistant message holding text, for earlier turns of a
// conversation.
func Assistant(text string) Message {
	return Message{Role: RoleAssistant, Parts: []Part{TextPart{text}}}
}

// Text joins the message's text parts.
func (m Message) Text() string {
	if len(m.Parts) == 1 {
		if t, ok := m.Parts[0].(TextPart); ok {
			return t.Text
		}
	}
	var b strings.Builder
	for _, p := range m.Parts {
		if t, ok := p.(TextPart); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// Request is what is sent to a model.
type Request struct {
	// Model is the vendor's model id. Empty means the provider's default.
	Model string
	// System is the system prompt.
	System   string
	Messages []Message
	// MaxTokens caps the answer's length. 0 means the provider's default.
	MaxTokens int
	// Temperature, from 0 to 2, when set.
	Temperature *float64
	// Schema, when set, asks for a JSON answer of that shape. [Generate]
	// sets it; providers use the vendor's JSON mode.
	Schema *Schema
}

// Schema is the JSON Schema of an answer. Build one with [SchemaFor].
type Schema struct {
	// Name names the shape for the vendor, such as "Ticket".
	Name string
	// JSON is the schema itself.
	JSON json.RawMessage
}

// Prompt returns a request with one user message.
func Prompt(text string) Request { return Request{Messages: []Message{User(text)}} }

// Validate reports a malformed request before anything is sent. Providers
// call it first; the error wraps [ErrInvalidRequest].
func (r Request) Validate() error {
	if len(r.Messages) == 0 {
		return invalid("no messages")
	}
	for i, m := range r.Messages {
		switch m.Role {
		case RoleUser, RoleAssistant:
		case RoleSystem:
			return invalid("message %d: put the system prompt in Request.System", i)
		default:
			return invalid("message %d: unknown role %q", i, m.Role)
		}
		if len(m.Parts) == 0 {
			return invalid("message %d: no parts", i)
		}
		for j, p := range m.Parts {
			if p == nil {
				return invalid("message %d: part %d is nil", i, j)
			}
		}
	}
	if r.Schema != nil && (r.Schema.Name == "" || !json.Valid(r.Schema.JSON)) {
		return invalid("Schema needs a name and valid JSON")
	}
	if r.MaxTokens < 0 {
		return invalid("MaxTokens is negative")
	}
	if t := r.Temperature; t != nil && (*t < 0 || *t > 2) {
		return invalid("Temperature %v is outside 0..2", *t)
	}
	return nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}

// Usage counts the tokens of one call.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Total is input plus output tokens.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// Response is a model's whole answer.
type Response struct {
	Message Message
	// StopReason is why the model stopped, such as "end" or "max_tokens".
	StopReason string
	Usage      Usage
}

// Text is the answer's text.
func (r Response) Text() string { return r.Message.Text() }

// Chunk is one piece of a streamed answer.
type Chunk struct {
	Text string
	// Usage and StopReason are set on the last chunk, when the provider
	// reports them.
	Usage      *Usage
	StopReason string
}

// Ask sends one user message and returns the answer's text.
func Ask(ctx context.Context, m Model, prompt string) (string, error) {
	res, err := m.Chat(ctx, Prompt(prompt))
	if err != nil {
		return "", err
	}
	return res.Text(), nil
}
