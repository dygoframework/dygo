package openai_test

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
	"github.com/dygoframework/dygo/ai/openai"
)

// server answers every call with handle, after checking what all requests
// must carry.
func server(t *testing.T, key string, handle func(w http.ResponseWriter, body map[string]any)) ai.Model {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		want := ""
		if key != "" {
			want = "Bearer " + key
		}
		if got := r.Header.Get("Authorization"); got != want {
			t.Errorf("authorization %q", got)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		handle(w, body)
	}))
	t.Cleanup(srv.Close)
	m, err := openai.New(ai.Config{Provider: "openai", Model: "default-model", APIKey: ai.Secret(key), BaseURL: srv.URL + "/v1/"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestChat(t *testing.T) {
	var sent map[string]any
	m := server(t, "sk-test", func(w http.ResponseWriter, body map[string]any) {
		sent = body
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"Paris"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":1}}`)
	})
	temp := 0.2
	req := ai.Request{System: "Be brief.", Messages: []ai.Message{ai.User("Capital of France?")}, MaxTokens: 50, Temperature: &temp}
	res, err := m.Chat(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text() != "Paris" || res.StopReason != "end" || res.Usage != (ai.Usage{InputTokens: 9, OutputTokens: 1}) {
		t.Fatalf("got %+v", res)
	}
	msgs := sent["messages"].([]any)
	if sent["model"] != "default-model" || sent["max_completion_tokens"] != 50.0 || sent["temperature"] != 0.2 || sent["stream"] != nil || len(msgs) != 2 {
		t.Fatalf("sent %v", sent)
	}
	if first := msgs[0].(map[string]any); first["role"] != "system" || first["content"] != "Be brief." {
		t.Fatalf("sent %v", msgs)
	}
}

func TestChatModelAndLength(t *testing.T) {
	m := server(t, "", func(w http.ResponseWriter, body map[string]any) {
		if body["model"] != "other" {
			t.Errorf("model %v", body["model"])
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"cut"},"finish_reason":"length"}]}`)
	})
	req := ai.Prompt("x")
	req.Model = "other"
	res, err := m.Chat(context.Background(), req)
	if err != nil || res.StopReason != "max_tokens" || res.Usage.Total() != 0 {
		t.Fatalf("got %+v %v", res, err)
	}
}

func TestChatBadResponses(t *testing.T) {
	for body, kind := range map[string]error{`{"choices":[]}`: ai.ErrUnavailable, `not json`: ai.ErrUnavailable} {
		m := server(t, "", func(w http.ResponseWriter, _ map[string]any) { _, _ = io.WriteString(w, body) })
		if _, err := m.Chat(context.Background(), ai.Prompt("x")); !errors.Is(err, kind) {
			t.Errorf("%s: got %v", body, err)
		}
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status    int
		body      string
		kind      error
		retryable bool
	}{
		{400, `{"error":{"message":"too many tokens","code":"context_length_exceeded"}}`, ai.ErrTooLong, false},
		{400, `{"error":{"message":"blocked","code":"content_filter"}}`, ai.ErrRefused, false},
		{429, `{"error":{"message":"slow down","type":"requests"}}`, ai.ErrRateLimited, true},
		{429, `{"error":{"message":"no credit","type":"insufficient_quota","code":"insufficient_quota"}}`, ai.ErrRateLimited, false},
		{401, `{"error":{"message":"Incorrect API key provided: sk-te***"}}`, ai.ErrAuth, false},
		{503, `upstream down`, ai.ErrUnavailable, true},
	}
	for _, c := range cases {
		m := server(t, "sk-test", func(w http.ResponseWriter, _ map[string]any) {
			w.WriteHeader(c.status)
			_, _ = io.WriteString(w, c.body)
		})
		_, err := m.Chat(context.Background(), ai.Prompt("x"))
		var e *ai.Error
		if !errors.As(err, &e) || !errors.Is(err, c.kind) || e.Retryable != c.retryable || e.Provider != "openai" || e.Status != c.status {
			t.Errorf("%d %s: got %v (retryable %v)", c.status, c.body, err, ai.IsRetryable(err))
		}
		// The same mapping applies before a stream starts.
		for _, err := range m.Stream(context.Background(), ai.Prompt("x")) {
			if !errors.Is(err, c.kind) {
				t.Errorf("stream %d: got %v", c.status, err)
			}
		}
	}
}

func sse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", e)
	}
}

func TestStream(t *testing.T) {
	m := server(t, "sk-test", func(w http.ResponseWriter, body map[string]any) {
		if body["stream"] != true || body["stream_options"].(map[string]any)["include_usage"] != true {
			t.Errorf("sent %v", body)
		}
		sse(w,
			`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
			`{"choices":[{"delta":{"content":"Hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
			`[DONE]`)
	})
	var chunks []ai.Chunk
	for c, err := range m.Stream(context.Background(), ai.Prompt("hi")) {
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, c)
	}
	if len(chunks) != 3 || chunks[0].Text != "Hel" || chunks[2].Usage.Total() != 5 || chunks[2].StopReason != "end" {
		t.Fatalf("got %+v", chunks)
	}
}

func TestStreamWithoutUsage(t *testing.T) {
	// Some compatible servers send no usage event.
	m := server(t, "", func(w http.ResponseWriter, _ map[string]any) {
		sse(w, `{"choices":[{"delta":{"content":"a"},"finish_reason":"length"}]}`, `[DONE]`)
	})
	res, err := ai.Collect(m.Stream(context.Background(), ai.Prompt("hi")))
	if err != nil || res.Text() != "a" || res.StopReason != "max_tokens" {
		t.Fatalf("got %+v %v", res, err)
	}
}

func TestStreamFailures(t *testing.T) {
	cases := map[string][]string{
		"ended early": {`{"choices":[{"delta":{"content":"a"}}]}`},
		"bad event":   {`{nope`},
		"error event": {`{"error":{"message":"server had an error"}}`},
	}
	for name, events := range cases {
		m := server(t, "", func(w http.ResponseWriter, _ map[string]any) { sse(w, events...) })
		if _, err := ai.Collect(m.Stream(context.Background(), ai.Prompt("hi"))); !errors.Is(err, ai.ErrUnavailable) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestStreamCancel(t *testing.T) {
	release := make(chan struct{})
	m := server(t, "", func(w http.ResponseWriter, _ map[string]any) {
		sse(w, `{"choices":[{"delta":{"content":"a"}}]}`)
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
	// Stopping early closes the stream without an error.
	for _, err := range m.Stream(context.Background(), ai.Prompt("hi")) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
}

func TestInvalidRequestIsNotSent(t *testing.T) {
	m := server(t, "", func(http.ResponseWriter, map[string]any) { t.Error("request was sent") })
	if _, err := m.Chat(context.Background(), ai.Request{}); !errors.Is(err, ai.ErrInvalidRequest) {
		t.Fatalf("got %v", err)
	}
}

func TestNew(t *testing.T) {
	if _, err := openai.New(ai.Config{Model: "m"}); err == nil || !strings.Contains(err.Error(), "AI_API_KEY") {
		t.Fatalf("got %v", err)
	}
	if openai.Provider.Name != "openai" {
		t.Fatal(openai.Provider.Name)
	}
	if _, err := openai.New(ai.Config{Model: "m", APIKey: "k"}); err != nil {
		t.Fatal(err)
	}
}

func TestChatSchema(t *testing.T) {
	var sent map[string]any
	m := server(t, "sk-test", func(w http.ResponseWriter, body map[string]any) {
		sent = body
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}]}`)
	})
	req := ai.Prompt("x")
	req.Schema = &ai.Schema{Name: "Ticket", JSON: json.RawMessage(`{"type":"object"}`)}
	if _, err := m.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	f := sent["response_format"].(map[string]any)
	js := f["json_schema"].(map[string]any)
	if f["type"] != "json_schema" || js["name"] != "Ticket" || js["strict"] != false || js["schema"].(map[string]any)["type"] != "object" {
		t.Fatalf("sent %v", f)
	}
}
