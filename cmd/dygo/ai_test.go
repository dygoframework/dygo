package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dygoframework/dygo/dygotest"
)

func TestAICommandErrors(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"ai"}, &out, &out); err != exitError(2) || !strings.Contains(out.String(), "dygo ai <command>") {
		t.Fatalf("no args: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := run([]string{"ai", "help"}, &out, &out); err != nil || !strings.Contains(out.String(), "AI_API_KEY") {
		t.Fatalf("help: %v\n%s", err, out.String())
	}
	if err := run([]string{"help", "ai"}, &out, &out); err != nil {
		t.Fatalf("dygo help ai: %v", err)
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.27.0\n")
	t.Chdir(dir)
	for args, want := range map[string]string{
		"ai nope":                         `unknown command "nope"`,
		"ai init extra":                   `unexpected argument "extra"`,
		"ai init -nope":                   "flag provided but not defined",
		"ai init -provider gemini":        `unknown AI provider "gemini"`,
		"ai init -provider openai -force": "", // no root module: prints what to add
	} {
		err := run(strings.Fields(args), &out, &out)
		if want == "" {
			if err != nil {
				t.Errorf("%s: %v", args, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", args, err)
		}
	}
	proj, err := findProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := aiInit(dir, proj, aiInitOptions{}, &out); err == nil || !strings.Contains(err.Error(), "-provider") {
		t.Fatalf("init without provider: %v", err)
	}
	if err := aiInit(dir, proj, aiInitOptions{Provider: "openai", Model: "a\nAI_BASE_URL=http://evil", Force: true}, &out); err == nil {
		t.Fatal("a model with a newline was written to .env")
	}
}

func TestPromptAI(t *testing.T) {
	for in, want := range map[string]string{"": "openai", "\n": "openai", "nope\nAnthropic\n": "anthropic", "ollama": "ollama"} {
		var o aiInitOptions
		var out bytes.Buffer
		if err := promptAI(strings.NewReader(in), &out, &o); err != nil || o.Provider != want {
			t.Errorf("%q: %q %v", in, o.Provider, err)
		}
	}
	var o aiInitOptions
	if err := promptAI(strings.NewReader("nope"), &bytes.Buffer{}, &o); err == nil {
		t.Fatal("unknown provider at EOF passed")
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.27.0\n")
	proj, err := findProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := aiInit(dir, proj, aiInitOptions{Prompt: strings.NewReader("anthropic\n"), SkipTidy: true}, &out); err != nil {
		t.Fatal(err)
	}
	if v, _ := envValue(filepath.Join(dir, ".env"), "AI_PROVIDER"); v != "anthropic" {
		t.Fatalf("AI_PROVIDER=%q", v)
	}
}

// newTestProject runs dygo new without go mod tidy, against this checkout.
func newTestProject(t *testing.T, tidy bool) string {
	t.Helper()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "shop")
	args := []string{"new", dir, "-replace", repo}
	if !tidy {
		args = append(args, "-skip-tidy")
	}
	var out bytes.Buffer
	if err := run(args, &out, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	t.Chdir(dir)
	return dir
}

func TestAIInit(t *testing.T) {
	newTestProject(t, false)
	var out bytes.Buffer
	if err := run([]string{"ai", "init", "-provider", "anthropic", "-skip-tidy"}, &out, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, want := range []string{"UPDATE .env (AI_*)", "UPDATE app.module.go (Imports += ai.Module(openai.Provider, anthropic.Provider))", "UPDATE app_test.go (fake ai.Model)", "AI_API_KEY=..."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	env := readFile(t, ".env")
	for _, want := range []string{"AI_PROVIDER=anthropic\n", "AI_MODEL=claude-sonnet-5-5\n", "AI_API_KEY=\n", "# AI model, written by dygo ai init"} {
		if !strings.Contains(env, want) {
			t.Errorf(".env misses %q:\n%s", want, env)
		}
	}
	if got := readFile(t, ".env.example"); !strings.Contains(got, "AI_API_KEY=change-me\n") {
		t.Errorf(".env.example:\n%s", got)
	}
	mod := readFile(t, "app.module.go")
	for _, want := range []string{`"github.com/dygoframework/dygo/ai"`, `"github.com/dygoframework/dygo/ai/anthropic"`, `"github.com/dygoframework/dygo/ai/openai"`} {
		if !strings.Contains(mod, want) {
			t.Errorf("app.module.go misses %s:\n%s", want, mod)
		}
	}
	if got := readFile(t, "app_test.go"); !strings.Contains(got, ".OverrideValue(dygo.TypeOf[ai.Model](), fake.New())") || !strings.Contains(got, `"github.com/dygoframework/dygo/ai/fake"`) {
		t.Errorf("app_test.go:\n%s", got)
	}
	if got := readFile(t, "go.mod"); !strings.Contains(got, "replace github.com/dygoframework/dygo/ai => ") {
		t.Errorf("go.mod:\n%s", got)
	}

	// Again: nothing is added twice, and a key the user wrote stays.
	if err := setEnv(".env", "AI_API_KEY", "sk-mine", ""); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"ai", "init", "-provider", "anthropic", "-skip-tidy"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Count(readFile(t, "app.module.go"), "ai.Module(") != 1 || strings.Count(readFile(t, "app_test.go"), "OverrideValue") != 1 ||
		strings.Count(readFile(t, "go.mod"), "dygo/ai =>") != 1 || strings.Count(readFile(t, ".env"), "AI_API_KEY=") != 1 {
		t.Fatal("a second init wired twice")
	}
	if v, _ := envValue(".env", "AI_API_KEY"); v != "sk-mine" {
		t.Fatalf("key replaced: %q", v)
	}

	// Another provider needs -force; ollama adds the local URL and no key.
	if err := run([]string{"ai", "init", "-provider", "ollama", "-skip-tidy"}, &out, &out); err == nil || !strings.Contains(err.Error(), "-force") {
		t.Fatalf("switch without -force: %v", err)
	}
	out.Reset()
	if err := run([]string{"ai", "init", "-provider", "ollama", "-model", "qwen3", "-force", "-skip-tidy"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	env = readFile(t, ".env")
	for _, want := range []string{"AI_PROVIDER=openai\n", "AI_MODEL=qwen3\n", "AI_BASE_URL=http://localhost:11434/v1\n"} {
		if !strings.Contains(env, want) {
			t.Errorf(".env misses %q:\n%s", want, env)
		}
	}
	if !strings.Contains(out.String(), "ollama pull qwen3") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestAIInitHandEditedTest(t *testing.T) {
	newTestProject(t, false)
	write(t, "app_test.go", "package main\n")
	var out bytes.Buffer
	if err := run([]string{"ai", "init", "-provider", "openai", "-skip-tidy"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "replace the model with a fake") || readFile(t, "app_test.go") != "package main\n" {
		t.Fatalf("output:\n%s", out.String())
	}
}

// TestNewAIProject runs dygo ai init on a new project with go mod tidy: the
// app must vet, pass its own tests without a key, and pass dygo check.
func TestNewAIProject(t *testing.T) {
	dygotest.Integration(t) // builds real programs
	newTestProject(t, true)
	var out bytes.Buffer
	if err := run([]string{"ai", "init", "-provider", "anthropic"}, &out, &out); err != nil || strings.Contains(out.String(), "tidy failed") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, args := range [][]string{{"vet", "./..."}, {"test", "./..."}} {
		if b, err := exec.Command("go", args...).CombinedOutput(); err != nil {
			t.Fatalf("go %s: %v\n%s", args[0], err, b)
		}
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"check"}, &stdout, &stderr); err != nil {
		t.Fatalf("check: %v\n%s%s", err, stdout.String(), stderr.String())
	}
}
