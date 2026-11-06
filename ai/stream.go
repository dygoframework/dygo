package ai

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/dygoframework/dygo"
)

// MaxAnswerBytes caps the text [Collect] keeps, so a runaway stream cannot
// use up memory. Collect fails with [ErrTooLarge] past it.
const MaxAnswerBytes = 8 << 20

// Collect reads a whole stream into a Response.
func Collect(stream iter.Seq2[Chunk, error]) (Response, error) {
	var b strings.Builder
	res := Response{Message: Message{Role: RoleAssistant}}
	for c, err := range stream {
		if err != nil {
			return Response{}, err
		}
		if b.Len()+len(c.Text) > MaxAnswerBytes {
			return Response{}, fmt.Errorf("%w (%d bytes)", ErrTooLarge, MaxAnswerBytes)
		}
		b.WriteString(c.Text)
		if c.Usage != nil {
			res.Usage = *c.Usage
		}
		if c.StopReason != "" {
			res.StopReason = c.StopReason
		}
	}
	res.Message.Parts = []Part{TextPart{b.String()}}
	return res, nil
}

// SSE sends a streamed answer to the browser as server-sent events: one event
// per chunk with its text, then an "end" event. If the stream fails, the last
// event is "error" with [PublicMessage] as data; the real error never reaches
// the client. When the client goes away, the stream stops and the call is
// cancelled.
//
//	func (c *ChatController) Ask(ctx context.Context, in AskInput) (dygo.Responder, error) {
//		return ai.SSE(c.model.Stream(ctx, ai.Prompt(in.Question))), nil
//	}
func SSE(stream iter.Seq2[Chunk, error]) dygo.Responder {
	return dygo.SSE(events(stream))
}

func events(stream iter.Seq2[Chunk, error]) iter.Seq[any] {
	return func(yield func(any) bool) {
		for c, err := range stream {
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					yield(dygo.SSEEvent{Event: "error", Data: PublicMessage(err)})
				}
				return
			}
			if c.Text != "" && !yield(c.Text) {
				return
			}
		}
		yield(dygo.SSEEvent{Event: "end", Data: ""})
	}
}

// Provide puts m in the container as the app's [Model], wrapped in mw (the
// first one is the outermost). The module is global: import it once, in the
// root module.
func Provide(m Model, mw ...Middleware) *dygo.Module {
	return &dygo.Module{
		Name:      "ai",
		Global:    true,
		Providers: []dygo.Provider{dygo.Value(Chain(m, mw...), dygo.For(dygo.TypeOf[Model]()))},
		Exports:   []dygo.Token{dygo.TypeOf[Model]()},
	}
}
