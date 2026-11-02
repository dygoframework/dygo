package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenGolden(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.27.0\n\nrequire github.com/dygoframework/dygo v0.0.0\n\nreplace github.com/dygoframework/dygo => "+repo+"\n")
	src, err := os.ReadFile("testdata/gen/notes/notes.go")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "notes", "notes.go"), string(src))

	var out bytes.Buffer
	if err := cmdGen([]string{dir}, &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "notes", genFile))
	if err != nil {
		t.Fatal(err)
	}
	golden := "testdata/gen/notes/dygo_gen.go.golden"
	if *update {
		write(t, golden, string(got))
	}
	want, _ := os.ReadFile(golden)
	if string(got) != string(want) {
		t.Fatalf("generated file differs (go test -update to accept):\n%s", got)
	}

	// Up to date: -check passes. A new route makes it stale, and a second
	// run writes it.
	out.Reset()
	if err := cmdGen([]string{"-check", dir}, &out); err != nil {
		t.Fatalf("check: %v\n%s", err, out.String())
	}
	f, _ := os.OpenFile(filepath.Join(dir, "notes", "notes.go"), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("\n//dygo:delete /{id}\nfunc (c *Controller) Delete(_ context.Context, in IDIn) (dygo.NoContent, error) { return dygo.NoContent{}, nil }\n")
	_ = f.Close()
	if err := cmdGen([]string{"-check", dir}, &out); err != exitError(1) || !strings.Contains(out.String(), "STALE") {
		t.Fatalf("stale check: %v\n%s", err, out.String())
	}
	if err := cmdGen([]string{dir}, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "notes", genFile)); !strings.Contains(string(got), `r.Delete("/{id}", dygo.Handle(c.Delete))`) {
		t.Fatalf("after adding a route:\n%s", got)
	}
	if !testing.Short() {
		cmd := exec.Command("go", "vet", "./...")
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("generated code does not build: %v\n%s", err, b)
		}
	}
}

func TestGenErrors(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{
			"no controller", "package p\n\ntype C struct{}\n\n//dygo:get /x\nfunc (c *C) X(ctx any) (int, error) { return 0, nil }\n",
			"type C has no //dygo:controller",
		},
		{
			"unknown directive", "package p\n\n//dygo:controller /c\ntype C struct{}\n\n//dygo:fetch /x\nfunc (c *C) X(ctx any) (int, error) { return 0, nil }\n",
			"unknown directive //dygo:fetch",
		},
		{
			"handwritten Routes", "package p\n\n//dygo:controller /c\ntype C struct{}\n\nfunc (c *C) Routes(r any) {}\n",
			"has //dygo: directives and a Routes method",
		},
		{
			"options on the path line", "package p\n\n//dygo:controller /c\ntype C struct{}\n\n//dygo:get /x code=201\nfunc (c *C) X(ctx any) (int, error) { return 0, nil }\n",
			"takes one path",
		},
		{
			"missing import", "package p\n\n//dygo:controller /c\n//dygo:use auth.Public()\ntype C struct{}\n",
			"unknown package auth",
		},
		{
			"bad signature", "package p\n\n//dygo:controller /c\ntype C struct{}\n\n//dygo:get /x\nfunc (c *C) X() (int, error) { return 0, nil }\n",
			"a handler takes",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "p.go"), tc.src)
			_, err := genPackage(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}
