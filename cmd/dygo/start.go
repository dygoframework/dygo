package main

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func cmdStart(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("start")
	watch := fs.Bool("watch", false, "rebuild and restart when a Go file, go.mod or .env file changes")
	poll := fs.Duration("poll", 500*time.Millisecond, "how often -watch looks for changes")
	pos, appArgs, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	pkg, err := pkgArg(pos)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tmp, err := os.MkdirTemp("", "dygo-start-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	r := &runner{pkg: pkg, args: appArgs, stdout: stdout, stderr: stderr, bin: filepath.Join(tmp, "app")}
	if runtime.GOOS == "windows" {
		r.bin += ".exe"
	}
	if !*watch {
		if err := r.build(ctx); err != nil {
			return exitError(1)
		}
		r.start()
		select {
		case <-ctx.Done():
			r.stop()
		case <-r.exited:
		}
		if code := r.cmd.ProcessState.ExitCode(); code > 0 {
			return exitError(code)
		}
		return nil
	}

	proj, err := findProject(".")
	if err != nil {
		return err
	}
	return r.watch(ctx, proj.Dir, *poll)
}

// runner builds the app and runs it as a child process.
type runner struct {
	pkg            string
	args           []string
	stdout, stderr io.Writer
	bin            string
	cmd            *exec.Cmd
	exited         chan struct{}
}

func (r *runner) build(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-o", r.bin, r.pkg)
	cmd.Stdout, cmd.Stderr = r.stderr, r.stderr
	return cmd.Run()
}

func (r *runner) start() {
	r.cmd = exec.Command(r.bin, r.args...)
	r.cmd.Stdin, r.cmd.Stdout, r.cmd.Stderr = os.Stdin, r.stdout, r.stderr
	r.exited = make(chan struct{})
	if err := r.cmd.Start(); err != nil {
		fmt.Fprintln(r.stderr, "dygo:", err)
		close(r.exited)
		return
	}
	go func() {
		_ = r.cmd.Wait()
		close(r.exited)
	}()
}

// stop asks the app to shut down with SIGTERM, so its shutdown hooks run, and
// kills it after 10 seconds.
func (r *runner) stop() {
	if r.cmd == nil || r.cmd.Process == nil {
		return
	}
	select {
	case <-r.exited:
		return
	default:
	}
	if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = r.cmd.Process.Kill() // Windows has no SIGTERM
	}
	select {
	case <-r.exited:
	case <-time.After(10 * time.Second):
		fmt.Fprintln(r.stderr, "dygo: the app did not stop within 10s; killing it")
		_ = r.cmd.Process.Kill()
		<-r.exited
	}
}

// watch rebuilds on change. The running app is only replaced once the new
// build succeeds, so a compile error leaves the last good version serving.
func (r *runner) watch(ctx context.Context, root string, poll time.Duration) error {
	running := false
	rebuild := func() {
		if err := r.build(ctx); err != nil {
			if ctx.Err() == nil {
				fmt.Fprintln(r.stderr, "dygo: build failed; waiting for changes")
			}
			return
		}
		if running {
			r.stop()
		}
		r.start()
		running = true
	}
	last, err := snapshot(root)
	if err != nil {
		return err
	}
	rebuild()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	var exited <-chan struct{}
	if running {
		exited = r.exited
	}
	for {
		select {
		case <-ctx.Done():
			r.stop()
			return nil
		case <-exited:
			fmt.Fprintf(r.stderr, "dygo: app exited (%v); waiting for changes\n", r.cmd.ProcessState)
			running, exited = false, nil
		case <-ticker.C:
			cur, err := snapshot(root)
			if err != nil || cur == last {
				continue
			}
			// Let an editor or formatter finish writing before building.
			time.Sleep(100 * time.Millisecond)
			if again, err := snapshot(root); err == nil {
				cur = again
			}
			last = cur
			fmt.Fprintln(r.stderr, "dygo: change detected, rebuilding")
			rebuild()
			if running {
				exited = r.exited
			}
		}
	}
}

// snapshot hashes the names, sizes and modification times of the files that
// affect the build. Tests, hidden directories, vendor, node_modules and
// testdata are skipped.
func snapshot(root string) (uint64, error) {
	h := fnv.New64a()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !watched(name) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		fmt.Fprintf(h, "%s|%d|%d\n", path, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	return h.Sum64(), err
}

func watched(name string) bool {
	switch {
	case strings.HasSuffix(name, "_test.go"):
		return false
	case strings.HasSuffix(name, ".go"), name == "go.mod", name == "go.sum", name == ".env", strings.HasPrefix(name, ".env."):
		return !strings.HasSuffix(name, ".example")
	}
	return false
}
