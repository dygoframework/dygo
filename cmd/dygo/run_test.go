package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunHelpAndErrors(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"run", "-h"}, &out, &out); err != nil || !strings.Contains(out.String(), "Usage: dygo run [command]") {
		t.Fatalf("dygo run -h: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := run([]string{"help", "run"}, &out, &out); err != nil || !strings.Contains(out.String(), "dygo run seed --users 10") {
		t.Fatalf("dygo help run: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := run([]string{"help"}, &out, &out); err != nil || !strings.Contains(out.String(), "run        run one of the app's console commands") {
		t.Fatalf("dygo help lists run: %v\n%s", err, out.String())
	}

	t.Chdir(t.TempDir())
	if err := run([]string{"run", "seed"}, &out, &out); err == nil {
		t.Fatal("dygo run outside a project must fail")
	}

	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.27.0\n")
	write(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	t.Chdir(dir)
	if err := run([]string{"run", "seed"}, &out, &out); err == nil || !strings.Contains(err.Error(), "does not run console commands yet: run dygo g command <name> first") {
		t.Fatalf("dygo run without console.Main: %v", err)
	}
}

func TestRunPassesTheExitStatus(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.27.0\n")
	// A main package that mentions console.Main( and exits with its argument count.
	write(t, filepath.Join(dir, "main.go"), `package main

import (
	"fmt"
	"os"
)

// console.Main( is what dygo run looks for.
func main() {
	fmt.Println("args", os.Args[1:])
	os.Exit(len(os.Args) - 1)
}
`)
	t.Chdir(dir)
	var out bytes.Buffer
	if err := run([]string{"run", "a", "b"}, &out, &out); err != exitError(2) || !strings.Contains(out.String(), "args [a b]") {
		t.Fatalf("dygo run a b: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := run([]string{"run"}, &out, &out); err != exitError(1) || !strings.Contains(out.String(), "args [help]") {
		t.Fatalf("dygo run with no command runs help: %v\n%s", err, out.String())
	}
	write(t, filepath.Join(dir, "main.go"), "package main\n\n// console.Main(\nfunc main() { nope }\n")
	if err := run([]string{"run", "x"}, &out, &out); err == nil || !strings.Contains(err.Error(), "building") {
		t.Fatalf("a main package that doesn't build: %v", err)
	}
}
