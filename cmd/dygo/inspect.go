package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/dygoframework/dygo"
)

// inspect runs the main package with DYGO_INSPECT_OUT set and reads the
// report dygo.Create writes. With routes, providers are built so that
// controllers can register their routes; otherwise nothing is built.
// inspectReport is the file dygo.Create writes when DYGO_INSPECT_OUT is set.
// It mirrors the core's unexported type of the same name; the JSON is the
// contract between them.
type inspectReport struct {
	Graph  *dygo.GraphInfo `json:"graph,omitempty"`
	Routes []routeSummary  `json:"routes,omitempty"`
	Errors []string        `json:"errors,omitempty"`
}

// routeSummary is one route of the report.
type routeSummary struct {
	Method     string `json:"method"`
	Host       string `json:"host,omitempty"`
	Path       string `json:"path"`
	Version    string `json:"version,omitempty"`
	Controller string `json:"controller"`
	Module     string `json:"module"`
	Handler    string `json:"handler"`
	Input      string `json:"input,omitempty"`
	Output     string `json:"output,omitempty"`
	Status     int    `json:"status,omitempty"`
}

func inspect(pkg string, routes bool) (*inspectReport, error) {
	dir, err := os.MkdirTemp("", "dygo-inspect-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "report.json")
	mode := "graph"
	if routes {
		mode = "routes"
	}
	cmd := exec.Command("go", "run", pkg)
	cmd.Env = append(os.Environ(), "DYGO_INSPECT_OUT="+out, "DYGO_INSPECT="+mode)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	runErr := cmd.Run()
	data, err := os.ReadFile(out)
	if errors.Is(err, os.ErrNotExist) {
		msg := strings.TrimSpace(output.String())
		if runErr == nil {
			return nil, fmt.Errorf("%s exited without calling dygo.Create or dygo.CreateApplicationContext\n%s", pkg, msg)
		}
		return nil, fmt.Errorf("%s failed before dygo.Create (%v)\n%s", pkg, runErr, msg)
	}
	if err != nil {
		return nil, err
	}
	var rep inspectReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("reading the report: %w", err)
	}
	return &rep, nil
}

// pkgArg returns the package argument. By default it is the "main" of
// dygo.json when the command runs at the project root, else ".".
func pkgArg(pos []string) (string, error) {
	switch len(pos) {
	case 0:
		if cwd, err := os.Getwd(); err == nil {
			if p, err := findProject(cwd); err == nil && p.Config.Main != "" && p.Dir == cwd {
				return p.Config.Main, nil
			}
		}
		return ".", nil
	case 1:
		return pos[0], nil
	}
	return "", fmt.Errorf("unexpected argument %q: give at most one package (default: .)", pos[1])
}

// printErrors prints a boot error report and returns exit status 1.
func printErrors(w io.Writer, rep *inspectReport) error {
	fmt.Fprintf(w, "%d wiring problem(s):\n", len(rep.Errors))
	for _, e := range rep.Errors {
		fmt.Fprintln(w, "  -", strings.ReplaceAll(e, "\n", "\n    "))
	}
	return exitError(1)
}

func cmdRoutes(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("routes")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, _, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	pkg, err := pkgArg(pos)
	if err != nil {
		return err
	}
	rep, err := inspect(pkg, true)
	if err != nil {
		return err
	}
	if len(rep.Errors) > 0 {
		return printErrors(stderr, rep)
	}
	if *asJSON {
		return writeJSON(stdout, rep.Routes)
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "METHOD\tPATH\tHANDLER\tINPUT\tOUTPUT")
	for _, r := range rep.Routes {
		method := r.Method
		if method == "" {
			method = "ALL"
		}
		path := strings.TrimSuffix(r.Path, "{$}") // ServeMux's "exactly this path"
		if r.Host != "" {
			path = r.Host + path
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", method, path, r.Handler, dash(shortType(r.Input)), dash(shortType(r.Output)))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "\n%d routes\n", len(rep.Routes))
	return nil
}

// importPathRE matches the import path part of a qualified type name inside
// type arguments: Page[example.com/app/users.User] reads Page[users.User].
var importPathRE = regexp.MustCompile(`[\w.~-]+(?:/[\w.~-]+)+/`)

func shortType(s string) string { return importPathRE.ReplaceAllString(s, "") }

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func cmdCheck(args []string, stdout, stderr io.Writer) error {
	pos, _, err := parseFlags(newFlags("check"), args)
	if err != nil {
		return err
	}
	pkg, err := pkgArg(pos)
	if err != nil {
		return err
	}
	rep, err := inspect(pkg, false)
	if err != nil {
		return err
	}
	if len(rep.Errors) > 0 {
		return printErrors(stderr, rep)
	}
	p, c := counts(rep.Graph)
	fmt.Fprintf(stdout, "ok: %d modules, %d providers, %d controllers\n", len(rep.Graph.Modules), p, c)
	return nil
}

func counts(g *dygo.GraphInfo) (providers, controllers int) {
	for _, p := range g.Providers {
		if p.Controller {
			controllers++
		} else {
			providers++
		}
	}
	return providers, controllers
}

func cmdInfo(args []string, stdout, _ io.Writer) error {
	pos, _, err := parseFlags(newFlags("info"), args)
	if err != nil {
		return err
	}
	pkg, err := pkgArg(pos)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	goVersion, _ := exec.Command("go", "env", "GOVERSION").Output()
	fmt.Fprintf(tw, "Go\t%s\n", strings.TrimSpace(string(goVersion)))
	fmt.Fprintf(tw, "OS/arch\t%s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(tw, "dygo CLI\t%s\n", version())
	fmt.Fprintf(tw, "dygo framework\t%s\n", frameworkVersion())
	if rep, err := inspect(pkg, false); err != nil {
		fmt.Fprintf(tw, "App\tnot inspected: %s\n", firstLine(err.Error()))
	} else if len(rep.Errors) > 0 {
		fmt.Fprintf(tw, "App\t%d wiring problem(s), run dygo check\n", len(rep.Errors))
	} else {
		p, c := counts(rep.Graph)
		fmt.Fprintf(tw, "Modules\t%d\nProviders\t%d\nControllers\t%d\n", len(rep.Graph.Modules), p, c)
	}
	return tw.Flush()
}

// frameworkVersion is the dygo version the current module requires.
func frameworkVersion() string {
	out, err := exec.Command("go", "list", "-m", "-json", frameworkPath).Output()
	if err != nil {
		return "not required by this module"
	}
	var m struct {
		Version string
		Dir     string
		Replace *struct{ Path, Version, Dir string }
	}
	if json.Unmarshal(out, &m) != nil {
		return "unknown"
	}
	switch {
	case m.Replace != nil && m.Replace.Dir != "":
		return fmt.Sprintf("%s => %s", m.Version, m.Replace.Dir)
	case m.Version == "":
		return "(this module) " + m.Dir
	}
	return m.Version
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
