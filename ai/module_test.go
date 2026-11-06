package ai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/ai"
	"github.com/dygoframework/dygo/ai/fake"
	"github.com/dygoframework/dygo/dygotest"
)

const key = "sk-test-secret-value"

func TestSecretHidden(t *testing.T) {
	s := ai.Secret(key)
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "key", s)
	j, _ := json.Marshal(struct{ K ai.Secret }{s})
	for _, out := range []string{fmt.Sprint(s), fmt.Sprintf("%v %s %#v", s, s, s), fmt.Sprintf("%+v", ai.Config{APIKey: s}), buf.String(), string(j)} {
		if strings.Contains(out, key) {
			t.Fatalf("key leaked: %s", out)
		}
	}
	if s.Reveal() != key {
		t.Fatal("Reveal")
	}
	var got ai.Secret
	if err := got.UnmarshalText([]byte(key)); err != nil || got.Reveal() != key {
		t.Fatalf("UnmarshalText: %v", err)
	}
}

func valid() ai.Config {
	return ai.Config{Provider: "test", Model: "m", Timeout: time.Second, MaxRetries: 2}
}

func TestConfigValidate(t *testing.T) {
	ok := []string{"", "https://api.example.com/v1", "http://localhost:11434/v1", "http://127.0.0.1:8000", "http://[::1]:8000"}
	for _, u := range ok {
		c := valid()
		c.BaseURL = u
		if err := c.Validate(); err != nil {
			t.Errorf("%q: %v", u, err)
		}
	}
	bad := map[string]func(*ai.Config){
		"no provider":       func(c *ai.Config) { c.Provider = "" },
		"no model":          func(c *ai.Config) { c.Model = "" },
		"zero timeout":      func(c *ai.Config) { c.Timeout = 0 },
		"negative retries":  func(c *ai.Config) { c.MaxRetries = -1 },
		"too many retries":  func(c *ai.Config) { c.MaxRetries = 99 },
		"http remote":       func(c *ai.Config) { c.BaseURL = "http://api.example.com/v1" },
		"other scheme":      func(c *ai.Config) { c.BaseURL = "ftp://api.example.com" },
		"not a url":         func(c *ai.Config) { c.BaseURL = "api.example.com" },
		"bad url":           func(c *ai.Config) { c.BaseURL = "https://%zz" },
		"user and password": func(c *ai.Config) { c.BaseURL = "https://u:pw@api.example.com" },
	}
	for name, edit := range bad {
		c := valid()
		edit(&c)
		err := c.Validate()
		if err == nil {
			t.Errorf("%s: no error", name)
		} else if strings.Contains(err.Error(), "pw") {
			t.Errorf("%s: password in error: %v", name, err)
		}
	}
}

// provider returns a Provider backed by m that records the config it got.
func provider(m ai.Model, got *ai.Config) ai.Provider {
	return ai.Provider{Name: "test", New: func(c ai.Config) (ai.Model, error) {
		if got != nil {
			*got = c
		}
		return m, nil
	}}
}

func TestNew(t *testing.T) {
	m := fake.New()
	got, err := ai.New(valid(), ai.Provider{Name: "other"}, provider(m, nil))
	if err != nil || got != m {
		t.Fatalf("New: %v %v", got, err)
	}
	c := valid()
	c.Provider = "nope"
	if _, err := ai.New(c, ai.Provider{Name: "a"}, ai.Provider{Name: "b"}); err == nil || !strings.Contains(err.Error(), "(a, b)") {
		t.Fatalf("unknown provider: %v", err)
	}
	if _, err := ai.New(ai.Config{}); err == nil {
		t.Fatal("empty config passed")
	}
}

func app(t *testing.T, mod *dygo.Module, log *slog.Logger) (*dygo.App, error) {
	t.Helper()
	root := &dygo.Module{Name: "app", Imports: []*dygo.Module{mod}}
	if log != nil {
		root.Imports = append(root.Imports, &dygo.Module{Name: "logger", Global: true, Providers: []dygo.Provider{dygo.Value(log)}, Exports: []dygo.Token{dygo.TypeOf[*slog.Logger]()}})
	}
	return dygotest.New(root).Build(context.Background())
}

func TestModuleFromEnv(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(filepath.Join(".", ".env"), []byte("AI_PROVIDER=test\nAI_MODEL=from-file\nAI_TIMEOUT=5s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_MODEL", "from-env") // real variables win
	t.Setenv("AI_API_KEY", key)
	var cfg ai.Config
	var buf bytes.Buffer
	a, err := app(t, ai.Module(provider(fake.New(fake.Reply("hello")), &cfg)), slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	m := dygo.MustGet[ai.Model](a)
	text, err := ai.Ask(context.Background(), m, "hi")
	if err != nil || text != "hello" {
		t.Fatalf("Ask: %q %v", text, err)
	}
	if cfg.Model != "from-env" || cfg.Timeout != 5*time.Second || cfg.MaxRetries != 2 || cfg.APIKey.Reveal() != key {
		t.Fatalf("config: %+v", cfg)
	}
	if !strings.Contains(buf.String(), "ai: provider ready") || strings.Contains(buf.String(), key) {
		t.Fatalf("log: %s", buf.String())
	}
}

func TestModuleErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("AI_PROVIDER", "")
	t.Setenv("AI_MODEL", "")
	if _, err := app(t, ai.Module(provider(fake.New(), nil)), nil); err == nil {
		t.Fatal("missing AI_PROVIDER passed")
	}
	c := valid()
	c.Provider = "other"
	if _, err := app(t, ai.ModuleWith(c, provider(fake.New(), nil)), nil); err == nil {
		t.Fatal("unknown provider passed")
	}
}

func TestModuleWith(t *testing.T) {
	a, err := app(t, ai.ModuleWith(valid(), provider(fake.New(fake.Fail(errors.New("boom"))), nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ai.Ask(context.Background(), dygo.MustGet[ai.Model](a), "hi"); err == nil {
		t.Fatal("want the fake's error")
	}
}
