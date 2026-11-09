// Package anthropic is the [ai.Provider] for Anthropic's Messages API.
//
//	ai.Module(anthropic.Provider)
//
// It uses only the standard library.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"strings"

	"github.com/dygoframework/dygo/ai"
	"github.com/dygoframework/dygo/ai/internal/wire"
)

// DefaultBaseURL is used when AI_BASE_URL is empty.
const DefaultBaseURL = "https://api.anthropic.com/v1"

// DefaultMaxTokens is sent when a request leaves MaxTokens at 0: the API
// requires a value.
const DefaultMaxTokens = 4096

// apiVersion is the API version header the request and response shapes
// below follow.
const apiVersion = "2023-06-01"

const name = "anthropic"

// Provider builds the model; pass it to [ai.Module].
var Provider = ai.Provider{Name: name, New: New}

// Model calls the Messages API. It is safe for concurrent use.
type Model struct {
	url    string
	model  string
	key    ai.Secret
	client *http.Client
}

// New returns the model for cfg. AI_API_KEY is required.
func New(cfg ai.Config) (ai.Model, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("anthropic: AI_API_KEY is required")
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Model{url: base + "/messages", model: cfg.Model, key: cfg.APIKey, client: wire.Client()}, nil
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model       string    `json:"model"`
	System      string    `json:"system,omitempty"`
	Messages    []message `json:"messages"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature *float64  `json:"temperature,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
	Output      *output   `json:"output_config,omitempty"`
}

// output asks for JSON of a schema (structured outputs).
type output struct {
	Format struct {
		Type   string          `json:"type"`
		Schema json.RawMessage `json:"schema"`
	} `json:"format"`
}

type usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type response struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      usage  `json:"usage"`
}

// event is one streamed event; only the fields used are listed.
type event struct {
	Type    string    `json:"type"`
	Message *response `json:"message"`
	Delta   struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage *usage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (m *Model) body(req ai.Request, stream bool) request {
	r := request{Model: req.Model, System: req.System, MaxTokens: req.MaxTokens, Temperature: req.Temperature, Stream: stream}
	if r.Model == "" {
		r.Model = m.model
	}
	if r.MaxTokens == 0 {
		r.MaxTokens = DefaultMaxTokens
	}
	r.Messages = make([]message, len(req.Messages))
	for i, msg := range req.Messages {
		r.Messages[i] = message{Role: string(msg.Role), Content: msg.Text()}
	}
	if req.Schema != nil {
		r.Output = &output{}
		r.Output.Format.Type, r.Output.Format.Schema = "json_schema", req.Schema.JSON
	}
	return r
}

func (m *Model) post(ctx context.Context, req ai.Request, stream bool) (*http.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set("X-Api-Key", m.key.Reveal())
	h.Set("Anthropic-Version", apiVersion)
	if stream {
		h.Set("Accept", "text/event-stream")
	}
	return wire.Post(ctx, m.client, name, m.url, h, m.body(req, stream), toError)
}

// kindOf maps an Anthropic error type to an ai error kind; nil leaves the
// status to decide.
func kindOf(typ, msg string) error {
	switch {
	case typ == "overloaded_error" || typ == "api_error":
		return ai.ErrUnavailable
	case typ == "invalid_request_error" && strings.Contains(strings.ToLower(msg), "too long"):
		return ai.ErrTooLong
	}
	return nil
}

func toError(res *http.Response, body []byte) error {
	msg, typ, _ := wire.Message(body)
	return wire.StatusError(name, res, msg, kindOf(typ, msg))
}

// stopReason gives the stop reason the names other providers share.
func stopReason(s string) string {
	switch s {
	case "end_turn", "stop_sequence":
		return "end"
	case "refusal":
		return "refused"
	}
	return s
}

// Chat sends the request and waits for the whole answer.
func (m *Model) Chat(ctx context.Context, req ai.Request) (ai.Response, error) {
	res, err := m.post(ctx, req, false)
	if err != nil {
		return ai.Response{}, err
	}
	defer res.Body.Close()
	var r response
	if err := wire.DecodeJSON(name, res.Body, &r); err != nil {
		return ai.Response{}, err
	}
	var text strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	return ai.Response{
		Message:    ai.Assistant(text.String()),
		StopReason: stopReason(r.StopReason),
		Usage:      ai.Usage{InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens},
	}, nil
}

// Stream sends the request and yields the answer as it arrives. The last
// chunk carries the token usage and stop reason.
func (m *Model) Stream(ctx context.Context, req ai.Request) iter.Seq2[ai.Chunk, error] {
	return func(yield func(ai.Chunk, error) bool) {
		res, err := m.post(ctx, req, true)
		if err != nil {
			yield(ai.Chunk{}, err)
			return
		}
		defer res.Body.Close()
		var u ai.Usage
		var stop string
		for ev, err := range wire.Events(name, res.Body) {
			if err != nil {
				if ctx.Err() != nil {
					err = ctx.Err()
				}
				yield(ai.Chunk{}, err)
				return
			}
			var e event
			if err := json.Unmarshal(ev.Data, &e); err != nil {
				yield(ai.Chunk{}, &ai.Error{Provider: name, Kind: ai.ErrUnavailable, Message: "bad stream event: " + err.Error()})
				return
			}
			switch e.Type {
			case "message_start":
				if e.Message != nil {
					u.InputTokens = e.Message.Usage.InputTokens
				}
			case "content_block_delta":
				if e.Delta.Type == "text_delta" && e.Delta.Text != "" && !yield(ai.Chunk{Text: e.Delta.Text}, nil) {
					return
				}
			case "message_delta":
				stop = stopReason(e.Delta.StopReason)
				if e.Usage != nil {
					u.OutputTokens = e.Usage.OutputTokens
				}
			case "message_stop":
				yield(ai.Chunk{Usage: &u, StopReason: stop}, nil)
				return
			case "error":
				e2 := &ai.Error{Provider: name, Kind: ai.ErrUnavailable}
				if e.Error != nil {
					e2.Message = e.Error.Message
					if k := kindOf(e.Error.Type, e.Error.Message); k != nil {
						e2.Kind = k
					}
					e2.Retryable = e.Error.Type == "overloaded_error"
				}
				yield(ai.Chunk{}, e2)
				return
			}
		}
		if ctx.Err() != nil {
			yield(ai.Chunk{}, ctx.Err())
			return
		}
		yield(ai.Chunk{}, &ai.Error{Provider: name, Kind: ai.ErrUnavailable, Retryable: true, Message: "stream ended early"})
	}
}
