package local_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dygoframework/dygo/storage"
	"github.com/dygoframework/dygo/storage/local"
	"github.com/dygoframework/dygo/storage/storagetest"
)

func TestConformance(t *testing.T) {
	storagetest.Run(t, storagetest.Local(t))
}

func TestConformanceWithPublicURL(t *testing.T) {
	s, err := local.New(t.TempDir(), local.Options{PublicURL: "https://cdn.example.com/files"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	storagetest.Run(t, s)
}

func TestURL(t *testing.T) {
	ctx := context.Background()
	s := storagetest.Local(t)
	if _, err := s.URL(ctx, "a.png", time.Minute); !errors.Is(err, storage.ErrNoURL) {
		t.Fatalf("no public URL: %v", err)
	}
	s2, err := local.New(t.TempDir(), local.Options{PublicURL: "https://cdn.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	u, err := s2.URL(ctx, "my photos/a b?.png", time.Minute)
	if err != nil || u != "https://cdn.example.com/my%20photos/a%20b%3F.png" {
		t.Fatalf("URL: %q %v", u, err)
	}
}

func TestContentTypeFromExtension(t *testing.T) {
	ctx := context.Background()
	s := storagetest.Local(t)
	obj, err := s.Put(ctx, "a.png", strings.NewReader("x"), storage.ContentType("image/x-mine"))
	if err != nil || obj.ContentType != "image/x-mine" {
		t.Fatalf("Put: %+v %v", obj, err)
	}
	if obj, _ := s.Stat(ctx, "a.png"); obj.ContentType != "image/png" {
		t.Fatalf("Stat: %+v", obj)
	}
	_, _ = s.Put(ctx, "noext", strings.NewReader("x"))
	if obj, _ := s.Stat(ctx, "noext"); obj.ContentType != "application/octet-stream" {
		t.Fatalf("Stat noext: %+v", obj)
	}
}

func TestSymlinkCannotEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	s, err := local.New(dir, local.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, _, err := s.Get(ctx, "link/secret"); err == nil {
		t.Fatal("read through a symlink outside the folder")
	}
	if _, err := s.Put(ctx, "link/new", strings.NewReader("x")); err == nil {
		t.Fatal("wrote through a symlink outside the folder")
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); err == nil {
		t.Fatal("file written outside the folder")
	}
	for o, err := range s.List(ctx, "") {
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("listed %q through a symlink", o.Key)
	}
}

func TestListSkipsTempFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".dygo-tmp-123"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := local.New(dir, local.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for o, err := range s.List(context.Background(), "") {
		t.Fatalf("listed %q (%v)", o.Key, err)
	}
}

func TestListCanceled(t *testing.T) {
	s := storagetest.Local(t)
	_, _ = s.Put(context.Background(), "a/b", strings.NewReader("x"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, err := range s.List(ctx, "") {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("List: %v", err)
		}
	}
}

func TestCanceledCalls(t *testing.T) {
	s := storagetest.Local(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.Get(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Errorf("Get: %v", err)
	}
	if _, err := s.Stat(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Errorf("Stat: %v", err)
	}
	if err := s.Delete(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Errorf("Delete: %v", err)
	}
	if _, err := s.URL(ctx, "a", 0); !errors.Is(err, context.Canceled) {
		t.Errorf("URL: %v", err)
	}
	if err := s.Check(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Check: %v", err)
	}
}

// failReader fails halfway, so Put must leave no file behind.
type failReader struct{ n int }

func (f *failReader) Read(p []byte) (int, error) {
	if f.n > 0 {
		return 0, errors.New("disk on fire")
	}
	f.n++
	return copy(p, "half"), nil
}

func TestPutFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	s, err := local.New(dir, local.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.Put(ctx, "a/b.txt", &failReader{}); err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("Put: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "a"))
	if len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
}

func TestDeleteFolderIsNoop(t *testing.T) {
	s := storagetest.Local(t)
	ctx := context.Background()
	_, _ = s.Put(ctx, "dir/a", strings.NewReader("x"))
	if err := s.Delete(ctx, "dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stat(ctx, "dir/a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get(ctx, "dir"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get folder: %v", err)
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	s, err := local.New(dir, local.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("Check left %v", entries)
	}
	if err := s.OnApplicationShutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(context.Background()); err == nil {
		t.Fatal("Check after close passed")
	}
	if _, err := s.Put(context.Background(), "a", strings.NewReader("x")); err == nil || errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Put after close: %v", err)
	}
}

func TestNew(t *testing.T) {
	if _, err := local.New("", local.Options{}); err == nil {
		t.Fatal("empty dir passed")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := local.New(file, local.Options{}); err == nil {
		t.Fatal("a file passed as the folder")
	}
	if _, err := local.New(filepath.Join(file, "sub"), local.Options{}); err == nil {
		t.Fatal("a folder under a file passed")
	}
	dir := filepath.Join(t.TempDir(), "new", "dir")
	s, err := local.Driver.New(storage.Config{Driver: "local", LocalDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("Driver did not create the folder")
	}
	_ = s.(*local.Store).Close()
}
