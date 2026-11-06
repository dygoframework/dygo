// Package openai is the [ai.Provider] for OpenAI's chat completions API and
// the servers that speak it, such as Ollama, vLLM or LM Studio (set
// AI_BASE_URL, for example http://localhost:11434/v1).
//
//	ai.Module(openai.Provider)
//
// It uses only the standard library.
package openai

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
const DefaultBaseURL = "https://api.openai.com/v1"

const name = "openai"

// Provider builds the model; pass it to [ai.Module].
var Provider = ai.Provider{Name: name, New: New}

// Model calls the chat completions API. It is safe for concurrent use.
type Model struct {
	url    string
	model  string
	key    ai.Secret
	client *http.Client
}

// New returns the model for cfg. An API key is required unless AI_BASE_URL
// points at another server.
func New(cfg ai.Config) (ai.Model, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		if cfg.APIKey == "" {
			return nil, errors.New("openai: AI_API_KEY is required")
		}
		base = DefaultBaseURL
	}
	return &Model{url: base + "/chat/completions", model: cfg.Model, key: cfg.APIKey, client: wire.Client()}, nil
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model         string         `json:"model"`
	Messages      []message      `json:"messages"`
	MaxTokens     int            `json:"max_completion_tokens,omitempty"`
	Temperature   *float64       `json:"temperature,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func (u *usage) ai() *ai.Usage {
	if u == nil {
		return nil
	}
	return &ai.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
}

type response struct {
	Choices []struct {
		Message      message `json:"message"`
		Delta        message `json:"delta"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage *usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (m *Model) body(req ai.Request, stream bool) request {
	r := request{Model: req.Model, MaxTokens: req.MaxTokens, Temperature: req.Temperature}
	if r.Model == "" {
		r.Model = m.model
	}
	r.Messages = make([]message, 0, len(req.Messages)+1)
	if req.System != "" {
		r.Messages = append(r.Messages, message{Role: "system", Content: req.System})
	}
	for _, msg := range req.Messages {
		r.Messages = append(r.Messages, message{Role: string(msg.Role), Content: msg.Text()})
	}
	if stream {
		r.Stream, r.StreamOptions = true, &streamOptions{IncludeUsage: true}
	}
	return r
}

func (m *Model) post(ctx context.Context, req ai.Request, stream bool) (*http.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	h := http.Header{}
	if m.key != "" {
		h.Set("Authorization", "Bearer "+m.key.Reveal())
	}
	if stream {
		h.Set("Accept", "text/event-stream")
	}
	return wire.Post(ctx, m.client, name, m.url, h, m.body(req, stream), toError)
}

func toError(res *http.Response, body []byte) error {
	msg, typ, code := wire.Message(body)
	var kind error
	switch code {
	case "context_length_exceeded":
		kind = ai.ErrTooLong
	case "content_filter", "content_policy_violation":
		kind = ai.ErrRefused
	}
	e := wire.StatusError(name, res, msg, kind)
	// Out of credit is a 429 too, but waiting will not help.
	if code == "insufficient_quota" || typ == "insufficient_quota" {
		e.Retryable = false
	}
	return e
}

// stopReason gives the finish reason the names other providers share.
func stopReason(s string) string {
	switch s {
	case "stop":
		return "end"
	case "length":
		return "max_tokens"
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
	if len(r.Choices) == 0 {
		return ai.Response{}, &ai.Error{Provider: name, Kind: ai.ErrUnavailable, Message: "response has no choices"}
	}
	c := r.Choices[0]
	out := ai.Response{Message: ai.Assistant(c.Message.Content), StopReason: stopReason(c.FinishReason)}
	if u := r.Usage.ai(); u != nil {
		out.Usage = *u
	}
	return out, nil
}

// Stream sends the request and yields the answer as it arrives. The last
// chunk carries the token usage.
func (m *Model) Stream(ctx context.Context, req ai.Request) iter.Seq2[ai.Chunk, error] {
	return func(yield func(ai.Chunk, error) bool) {
		res, err := m.post(ctx, req, true)
		if err != nil {
			yield(ai.Chunk{}, err)
			return
		}
		defer res.Body.Close()
		var stop string
		final, done := false, false
		for ev, err := range wire.Events(name, res.Body) {
			if err != nil {
				if ctx.Err() != nil {
					err = ctx.Err()
				}
				yield(ai.Chunk{}, err)
				return
			}
			if string(ev.Data) == "[DONE]" {
				done = true
				break
			}
			var r response
			if err := json.Unmarshal(ev.Data, &r); err != nil {
				yield(ai.Chunk{}, &ai.Error{Provider: name, Kind: ai.ErrUnavailable, Message: "bad stream event: " + err.Error()})
				return
			}
			if r.Error != nil {
				yield(ai.Chunk{}, &ai.Error{Provider: name, Kind: ai.ErrUnavailable, Message: r.Error.Message})
				return
			}
			var c ai.Chunk
			if len(r.Choices) > 0 {
				c.Text = r.Choices[0].Delta.Content
				if f := r.Choices[0].FinishReason; f != "" {
					stop = stopReason(f)
				}
			}
			// The usage comes alone in the last event, after the finish reason.
			if c.Usage = r.Usage.ai(); c.Usage != nil {
				c.StopReason, final = stop, true
			}
			if (c.Text != "" || c.Usage != nil) && !yield(c, nil) {
				return
			}
		}
		switch {
		case ctx.Err() != nil:
			yield(ai.Chunk{}, ctx.Err())
		case !done && stop == "":
			yield(ai.Chunk{}, &ai.Error{Provider: name, Kind: ai.ErrUnavailable, Retryable: true, Message: "stream ended early"})
		case !final && stop != "":
			yield(ai.Chunk{StopReason: stop}, nil)
		}
	}
}
