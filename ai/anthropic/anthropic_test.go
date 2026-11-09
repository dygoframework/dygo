package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dygoframework/dygo/ai"
	"github.com/dygoframework/dygo/ai/anthropic"
)

func server(t *testing.T, handle func(w http.ResponseWriter, body map[string]any)) ai.Model {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("X-Api-Key") != "key-test" || r.Header.Get("Anthropic-Version") != "2023-06-01" {
			t.Errorf("got %s %v", r.URL.Path, r.Header)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		handle(w, body)
	}))
	t.Cleanup(srv.Close)
	m, err := anthropic.New(ai.Config{Provider: "anthropic", Model: "default-model", APIKey: "key-test", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestChat(t *testing.T) {
	var sent map[string]any
	m := server(t, func(w http.ResponseWriter, body map[string]any) {
		sent = body
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"Pa"},{"type":"thinking","text":"x"},{"type":"text","text":"ris"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":2}}`)
	})
	req := ai.Request{System: "Be brief.", Messages: []ai.Message{ai.User("Capital?"), ai.Assistant("Of what?"), ai.User("France")}}
	res, err := m.Chat(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text() != "Paris" || res.StopReason != "end" || res.Usage != (ai.Usage{InputTokens: 12, OutputTokens: 2}) {
		t.Fatalf("got %+v", res)
	}
	if sent["model"] != "default-model" || sent["system"] != "Be brief." || sent["max_tokens"] != 4096.0 || len(sent["messages"].([]any)) != 3 || sent["stream"] != nil {
		t.Fatalf("sent %v", sent)
	}
}

func TestChatOptions(t *testing.T) {
	m := server(t, func(w http.ResponseWriter, body map[string]any) {
		if body["model"] != "other" || body["max_tokens"] != 10.0 || body["temperature"] != 1.0 || body["system"] != nil {
			t.Errorf("sent %v", body)
		}
		_, _ = io.WriteString(w, `{"content":[],"stop_reason":"refusal"}`)
	})
	temp := 1.0
	req := ai.Prompt("x")
	req.Model, req.MaxTokens, req.Temperature = "other", 10, &temp
	res, err := m.Chat(context.Background(), req)
	if err != nil || res.StopReason != "refused" {
		t.Fatalf("got %+v %v", res, err)
	}
	m = server(t, func(w http.ResponseWriter, _ map[string]any) { _, _ = io.WriteString(w, `nope`) })
	if _, err := m.Chat(context.Background(), ai.Prompt("x")); !errors.Is(err, ai.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status    int
		body      string
		kind      error
		retryable bool
	}{
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 250000 tokens"}}`, ai.ErrTooLong, false},
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"bad role"}}`, ai.ErrInvalidRequest, false},
		{401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, ai.ErrAuth, false},
		{429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}`, ai.ErrRateLimited, true},
		{529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, ai.ErrUnavailable, true},
	}
	for _, c := range cases {
		m := server(t, func(w http.ResponseWriter, _ map[string]any) {
			w.WriteHeader(c.status)
			_, _ = io.WriteString(w, c.body)
		})
		_, err := m.Chat(context.Background(), ai.Prompt("x"))
		var e *ai.Error
		if !errors.As(err, &e) || !errors.Is(err, c.kind) || e.Retryable != c.retryable || e.Provider != "anthropic" {
			t.Errorf("%d: got %v", c.status, err)
		}
	}
}

func sse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		var typ struct{ Type string }
		_ = json.Unmarshal([]byte(e), &typ)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ.Type, e)
	}
}

func TestStream(t *testing.T) {
	m := server(t, func(w http.ResponseWriter, body map[string]any) {
		if body["stream"] != true {
			t.Errorf("sent %v", body)
		}
		sse(w,
			`{"type":"message_start","message":{"content":[],"usage":{"input_tokens":7,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"ping"}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`)
	})
	res, err := ai.Collect(m.Stream(context.Background(), ai.Prompt("hi")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text() != "Hello" || res.StopReason != "max_tokens" || res.Usage != (ai.Usage{InputTokens: 7, OutputTokens: 2}) {
		t.Fatalf("got %+v", res)
	}
}

func TestStreamFailures(t *testing.T) {
	cases := []struct {
		name      string
		events    []string
		kind      error
		retryable bool
	}{
		{"ended early", []string{`{"type":"content_block_delta","delta":{"type":"text_delta","text":"a"}}`}, ai.ErrUnavailable, true},
		{"bad event", []string{`{nope`}, ai.ErrUnavailable, false},
		{"overloaded", []string{`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`}, ai.ErrUnavailable, true},
		{"too long", []string{`{"type":"error","error":{"type":"invalid_request_error","message":"Prompt is too long"}}`}, ai.ErrTooLong, false},
		{"bare error", []string{`{"type":"error"}`}, ai.ErrUnavailable, false},
	}
	for _, c := range cases {
		m := server(t, func(w http.ResponseWriter, _ map[string]any) { sse(w, c.events...) })
		var last error
		for _, err := range m.Stream(context.Background(), ai.Prompt("hi")) {
			last = err
		}
		if !errors.Is(last, c.kind) || ai.IsRetryable(last) != c.retryable {
			t.Errorf("%s: got %v", c.name, last)
		}
	}
	m := server(t, func(w http.ResponseWriter, _ map[string]any) {
		w.WriteHeader(429)
		_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"slow"}}`)
	})
	for _, err := range m.Stream(context.Background(), ai.Prompt("hi")) {
		if !errors.Is(err, ai.ErrRateLimited) {
			t.Fatalf("got %v", err)
		}
	}
}

func TestStreamCancel(t *testing.T) {
	release := make(chan struct{})
	m := server(t, func(w http.ResponseWriter, _ map[string]any) {
		sse(w, `{"type":"content_block_delta","delta":{"type":"text_delta","text":"a"}}`)
		w.(http.Flusher).Flush()
		<-release
	})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var last error
	for c, err := range m.Stream(ctx, ai.Prompt("hi")) {
		if c.Text == "a" {
			cancel()
		}
		last = err
	}
	if !errors.Is(last, context.Canceled) {
		t.Fatalf("got %v", last)
	}
	for _, err := range m.Stream(context.Background(), ai.Prompt("hi")) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
}

func TestNew(t *testing.T) {
	if _, err := anthropic.New(ai.Config{Model: "m"}); err == nil || !strings.Contains(err.Error(), "AI_API_KEY") {
		t.Fatalf("got %v", err)
	}
	if _, err := anthropic.New(ai.Config{Model: "m", APIKey: "k"}); err != nil || anthropic.Provider.Name != "anthropic" {
		t.Fatal(err)
	}
	m := server(t, func(http.ResponseWriter, map[string]any) { t.Error("request was sent") })
	if _, err := m.Chat(context.Background(), ai.Request{}); !errors.Is(err, ai.ErrInvalidRequest) {
		t.Fatalf("got %v", err)
	}
}

func TestChatSchema(t *testing.T) {
	var sent map[string]any
	m := server(t, func(w http.ResponseWriter, body map[string]any) {
		sent = body
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{}"}],"stop_reason":"end_turn"}`)
	})
	req := ai.Prompt("x")
	req.Schema = &ai.Schema{Name: "Ticket", JSON: json.RawMessage(`{"type":"object"}`)}
	if _, err := m.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	f := sent["output_config"].(map[string]any)["format"].(map[string]any)
	if f["type"] != "json_schema" || f["schema"].(map[string]any)["type"] != "object" {
		t.Fatalf("sent %v", sent["output_config"])
	}
	if _, ok := sent["response_format"]; ok {
		t.Fatal("unexpected response_format")
	}
}
