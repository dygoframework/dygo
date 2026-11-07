package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const aiUsage = `Usage: dygo ai <command> [flags]

  init [-provider openai|anthropic|ollama] [-model id] [-base-url URL] [-force] [-skip-tidy]
        add the ai module to this app: AI_* settings in .env and .env.example,
        ai.Module in the root module, a fake model in the app test, and the
        github.com/dygoframework/dygo/ai module in go.mod

The app reads AI_PROVIDER, AI_MODEL, AI_API_KEY and AI_BASE_URL from .env,
so switching vendors later is a change to .env. Put your key in .env (it is
git-ignored), never in a flag or in code. ollama needs no key: it is the
openai provider pointed at http://localhost:11434/v1.
`

// aiProviders are what dygo ai init writes for each -provider.
var aiProviders = map[string]struct {
	provider, model, baseURL string
	key                      bool
}{
	"openai":    {"openai", "gpt-5", "", true},
	"anthropic": {"anthropic", "claude-sonnet-5-5", "", true},
	"ollama":    {"openai", "llama3.2", "http://localhost:11434/v1", false},
}

// aiModule is the root module import dygo ai init adds: both providers, so
// AI_PROVIDER alone picks one.
const aiModule = "ai.Module(openai.Provider, anthropic.Provider)"

func cmdAI(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, aiUsage)
		return exitError(2)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, aiUsage)
		return nil
	case "init":
		return cmdAIInit(rest, stdout)
	}
	return fmt.Errorf("dygo ai: unknown command %q (run dygo ai help)", sub)
}

// aiInitOptions are the dygo ai init flags.
type aiInitOptions struct {
	Provider, Model, BaseURL string
	Force, SkipTidy          bool
	Prompt                   io.Reader // ask for the provider when it was not given
}

func cmdAIInit(args []string, stdout io.Writer) error {
	fs := newFlags("ai init")
	var o aiInitOptions
	fs.StringVar(&o.Provider, "provider", "", "openai, anthropic or ollama")
	fs.StringVar(&o.Model, "model", "", "the model id (default: one per provider)")
	fs.StringVar(&o.BaseURL, "base-url", "", "a compatible server instead of the vendor's (https, or http on localhost)")
	fs.BoolVar(&o.Force, "force", false, "replace AI settings already in .env")
	fs.BoolVar(&o.SkipTidy, "skip-tidy", false, "do not run go mod tidy")
	pos, _, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return fmt.Errorf("dygo ai init: unexpected argument %q", pos[0])
	}
	if o.Provider == "" && isTerminal(os.Stdin) {
		o.Prompt = os.Stdin
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	proj, err := findProject(cwd)
	if err != nil {
		return err
	}
	return aiInit(cwd, proj, o, stdout)
}

func aiInit(cwd string, proj project, o aiInitOptions, stdout io.Writer) error {
	if o.Prompt != nil {
		if err := promptAI(o.Prompt, stdout, &o); err != nil {
			return err
		}
	}
	name := strings.ToLower(o.Provider)
	p, ok := aiProviders[name]
	if !ok {
		if o.Provider == "" {
			return errors.New("dygo ai init: pass -provider openai, anthropic or ollama")
		}
		return fmt.Errorf("unknown AI provider %q: want openai, anthropic or ollama", o.Provider)
	}
	model, baseURL := cmp(o.Model, p.model), cmp(o.BaseURL, p.baseURL)
	if strings.ContainsAny(model+baseURL, "\n\r") {
		return errors.New("dygo ai init: -model and -base-url must be one line")
	}

	// Check everything before writing anything.
	envFile := filepath.Join(proj.Dir, ".env")
	if !o.Force {
		for key, want := range map[string]string{"AI_PROVIDER": p.provider, "AI_MODEL": model, "AI_BASE_URL": baseURL} {
			if v, ok := envValue(envFile, key); ok && v != "" && v != want {
				return fmt.Errorf("%s is already %q in %s; pass -force to replace it", key, v, rel(cwd, envFile))
			}
		}
	}

	// 1. .env and .env.example. The key is never written by dygo: the user
	// puts it in .env, which is git-ignored.
	example := filepath.Join(proj.Dir, ".env.example")
	for _, f := range []struct {
		path, header, key string
	}{{envFile, envHeader, ""}, {example, envExampleHeader, "change-me"}} {
		vals := [][2]string{{"AI_PROVIDER", p.provider}, {"AI_MODEL", model}}
		if baseURL != "" {
			vals = append(vals, [2]string{"AI_BASE_URL", baseURL})
		}
		if p.key {
			if v, ok := envValue(f.path, "AI_API_KEY"); !ok || (v == "" && f.key != "") {
				vals = append(vals, [2]string{"AI_API_KEY", f.key})
			}
		}
		for _, kv := range vals {
			if err := setEnv(f.path, kv[0], kv[1], f.header+"\n"); err != nil {
				return err
			}
		}
		fmt.Fprintln(stdout, "UPDATE", rel(cwd, f.path), "(AI_*)")
	}

	// 2. ai.Module in the root module.
	if err := wireAIModule(cwd, proj, stdout); err != nil {
		return err
	}

	// 3. A fake model in the app test, so go test never calls the vendor and
	// needs no key.
	if err := wireAITest(cwd, proj, stdout); err != nil {
		return err
	}

	// 4. go.mod: the ai module, from the same checkout as dygo when it is
	// replaced.
	if err := addSubModule(proj, "ai"); err != nil {
		return err
	}
	if !o.SkipTidy {
		cmd := exec.Command("go", "mod", "tidy")
		cmd.Dir = proj.Dir
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(stdout, "\ngo mod tidy failed; fix the error and run it again in %s:\n%s", proj.Dir, out)
			return nil
		}
	}

	fmt.Fprintln(stdout, "\nNext:")
	if p.key {
		fmt.Fprintf(stdout, "  put your %s key in .env:  AI_API_KEY=...   # .env is git-ignored; never commit a key\n", name)
	} else {
		fmt.Fprintf(stdout, "  ollama pull %s   # and keep ollama serve running\n", model)
	}
	fmt.Fprint(stdout, "  take an ai.Model in a service and call ai.Ask(ctx, model, \"Hello\")\n"+
		"  or stream it to the browser: return ai.SSE(model.Stream(ctx, req))\n")
	return nil
}

func promptAI(in io.Reader, out io.Writer, o *aiInitOptions) error {
	r := bufio.NewReader(in)
	for {
		fmt.Fprint(out, "AI provider (openai, anthropic or ollama) [openai]: ")
		line, err := r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		o.Provider = cmp(strings.ToLower(strings.TrimSpace(line)), "openai")
		if _, ok := aiProviders[o.Provider]; ok {
			return nil
		}
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("unknown AI provider %q: want openai, anthropic or ollama", o.Provider)
		}
		fmt.Fprintln(out, "Please answer openai, anthropic or ollama.")
	}
}

// wireAIModule imports ai.Module(openai.Provider, anthropic.Provider) in the
// root module, unless an ai.Module is there already.
func wireAIModule(cwd string, proj project, stdout io.Writer) error {
	root := filepath.Join(proj.Dir, filepath.FromSlash(cmp(proj.Config.RootModuleDir, cmp(proj.Config.Main, "."))))
	if packageContains(root, "ai.Module(") {
		return nil
	}
	imports := []string{frameworkPath + "/ai", frameworkPath + "/ai/anthropic", frameworkPath + "/ai/openai"}
	sites, err := findModules(root)
	if err != nil {
		return err
	}
	site, ok := pickModule(sites)
	if !ok {
		fmt.Fprintf(stdout, "\nNo root module found in %s: import %s in it yourself, with the imports %q.\n", rel(cwd, root), aiModule, imports)
		return nil
	}
	if _, err := editModule(site, moduleEdit{Field: "Imports", Expr: aiModule, ImportPath: imports[0]}); err != nil {
		return err
	}
	if err := addImports(site.File, imports[1:]); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "UPDATE %s (Imports += %s)\n", rel(cwd, site.File), aiModule)
	return nil
}

// appTestBoot is how the app test that dygo new writes builds the app.
var appTestBoot = regexp.MustCompile(`dygotest\.New\(AppModule\(([^()]*)\)\)`)

// wireAITest makes the app test use a fake model instead of the vendor.
func wireAITest(cwd string, proj project, stdout io.Writer) error {
	path := filepath.Join(proj.Dir, filepath.FromSlash(cmp(proj.Config.RootModuleDir, cmp(proj.Config.Main, "."))), "app_test.go")
	src, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || bytes.Contains(src, []byte("ai.Model]()")) {
		return nil
	}
	if err != nil {
		return err
	}
	override := ".OverrideValue(dygo.TypeOf[ai.Model](), fake.New())"
	if !appTestBoot.Match(src) {
		fmt.Fprintf(stdout, "\nIn tests, replace the model with a fake: dygotest.New(...)%s\n", override)
		return nil
	}
	out := appTestBoot.ReplaceAll(src, []byte("${0}"+override))
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return err
	}
	if err := addImports(path, []string{frameworkPath + "/ai", frameworkPath + "/ai/fake"}); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "UPDATE", rel(cwd, path), "(fake ai.Model)")
	return nil
}

// addImports adds the import paths to a Go file and formats it.
func addImports(path string, imports []string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, imp := range imports {
		if src, err = addImport(path, src, imp); err != nil {
			return err
		}
	}
	if src, err = format.Source(src); err != nil {
		return err
	}
	return os.WriteFile(path, src, 0o644)
}
