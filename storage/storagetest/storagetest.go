// Package storagetest helps test code that uses storage.
//
// [Local] gives a test its own store in a temporary folder:
//
//	files := storagetest.Local(t)
//	app := dygotest.New(AppModule).OverrideValue(dygo.TypeOf[storage.Store](), files).Compile(t)
//
// [Run] is the set of checks every driver must pass; driver packages call it
// from their tests.
package storagetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dygoframework/dygo/storage"
	"github.com/dygoframework/dygo/storage/local"
)

// Local returns a local store in a folder the test removes when it ends.
func Local(t testing.TB) *local.Store {
	t.Helper()
	s, err := local.New(t.TempDir(), local.Options{})
	if err != nil {
		t.Fatalf("storagetest: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// Run checks that s behaves like a storage.Store. s should be empty, or hold
// only keys outside the "storagetest/" prefix.
func Run(t *testing.T, s storage.Store) {
	t.Helper()
	ctx := context.Background()
	const p = "storagetest/"

	t.Run("PutGet", func(t *testing.T) {
		obj, err := s.Put(ctx, p+"a.txt", strings.NewReader("hello"), storage.ContentType("text/plain"))
		must(t, err)
		if obj.Key != p+"a.txt" || obj.Size != 5 {
			t.Fatalf("Put: %+v", obj)
		}
		if got := read(t, s, p+"a.txt"); got != "hello" {
			t.Fatalf("Get: %q", got)
		}
		// Replace.
		_, err = s.Put(ctx, p+"a.txt", strings.NewReader("bye"))
		must(t, err)
		if got := read(t, s, p+"a.txt"); got != "bye" {
			t.Fatalf("Get after replace: %q", got)
		}
	})

	t.Run("Stat", func(t *testing.T) {
		data := bytes.Repeat([]byte("x"), 70_000)
		_, err := s.Put(ctx, p+"deep/dir/big.bin", bytes.NewReader(data))
		must(t, err)
		obj, err := s.Stat(ctx, p+"deep/dir/big.bin")
		must(t, err)
		if obj.Size != int64(len(data)) || obj.Modified.IsZero() || obj.ContentType == "" {
			t.Fatalf("Stat: %+v", obj)
		}
	})

	t.Run("NotFound", func(t *testing.T) {
		if _, err := s.Stat(ctx, p+"missing"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Stat: %v", err)
		}
		if _, _, err := s.Get(ctx, p+"missing"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Get: %v", err)
		}
		// A folder is not a file.
		if _, err := s.Stat(ctx, p+"deep"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Stat folder: %v", err)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		_, err := s.Put(ctx, p+"gone.txt", strings.NewReader("x"))
		must(t, err)
		must(t, s.Delete(ctx, p+"gone.txt"))
		if _, err := s.Stat(ctx, p+"gone.txt"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("after Delete: %v", err)
		}
		must(t, s.Delete(ctx, p+"gone.txt")) // again: no error
	})

	t.Run("List", func(t *testing.T) {
		for _, k := range []string{"list/b", "list/a", "list/sub/c", "list-other"} {
			_, err := s.Put(ctx, p+k, strings.NewReader(k))
			must(t, err)
		}
		if got := keys(t, s, p+"list/"); got != "a b sub/c" {
			t.Fatalf("List list/: %q", got)
		}
		// Sorted by key in byte order: "list-other" comes before "list/a".
		if got := keys(t, s, p+"list"); got != "-other /a /b /sub/c" {
			t.Fatalf("List list: %q", got)
		}
		if got := keys(t, s, p+"nothing/"); got != "" {
			t.Fatalf("List nothing/: %q", got)
		}
		// Stopping early is fine.
		for _, err := range s.List(ctx, p) {
			must(t, err)
			break
		}
	})

	t.Run("BadKeys", func(t *testing.T) {
		for _, k := range []string{"", "/abs", "a/../../etc/passwd", "..", "a//b", "a/", `a\b`, "a\x00b", strings.Repeat("k", storage.MaxKeyLen+1)} {
			if _, err := s.Put(ctx, k, strings.NewReader("x")); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("Put %q: %v", k, err)
			}
			if _, _, err := s.Get(ctx, k); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("Get %q: %v", k, err)
			}
			if _, err := s.Stat(ctx, k); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("Stat %q: %v", k, err)
			}
			if err := s.Delete(ctx, k); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("Delete %q: %v", k, err)
			}
			if _, err := s.URL(ctx, k, time.Minute); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("URL %q: %v", k, err)
			}
		}
		for _, err := range s.List(ctx, "../") {
			if !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("List ../: %v", err)
			}
		}
	})

	t.Run("Canceled", func(t *testing.T) {
		c, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.Put(c, p+"c.txt", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
			t.Fatalf("Put: %v", err)
		}
		if _, err := s.Stat(ctx, p+"c.txt"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("canceled Put left a file: %v", err)
		}
	})

	t.Run("URL", func(t *testing.T) {
		_, err := s.Put(ctx, p+"u.txt", strings.NewReader("x"))
		must(t, err)
		u, err := s.URL(ctx, p+"u.txt", time.Minute)
		if err != nil && !errors.Is(err, storage.ErrNoURL) {
			t.Fatalf("URL: %v", err)
		}
		if err == nil && !strings.HasPrefix(u, "http") {
			t.Fatalf("URL: %q", u)
		}
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, s storage.Store, key string) string {
	t.Helper()
	r, _, err := s.Get(context.Background(), key)
	must(t, err)
	defer r.Close()
	b, err := io.ReadAll(r)
	must(t, err)
	return string(b)
}

// keys lists prefix and returns the keys without it, space separated.
func keys(t *testing.T, s storage.Store, prefix string) string {
	t.Helper()
	var out []string
	for o, err := range s.List(context.Background(), prefix) {
		must(t, err)
		out = append(out, strings.TrimPrefix(o.Key, prefix))
	}
	return strings.Join(out, " ")
}
