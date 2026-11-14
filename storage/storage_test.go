package storage_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/health"
	"github.com/dygoframework/dygo/storage"
	"github.com/dygoframework/dygo/storage/local"
	"github.com/dygoframework/dygo/storage/storagetest"
)

func TestCheckKey(t *testing.T) {
	for _, k := range []string{"a", "a.png", "avatars/42.png", "a b/c-d_e.f", "émoji/😀", strings.Repeat("k", storage.MaxKeyLen), "..a", "a.."} {
		if err := storage.CheckKey(k); err != nil {
			t.Errorf("%q: %v", k, err)
		}
	}
	for _, k := range []string{"", "/a", "a/", "a//b", ".", "..", "a/./b", "a/../b", `a\b`, "a\nb", "a\x7fb", "\xff", strings.Repeat("k", storage.MaxKeyLen+1)} {
		err := storage.CheckKey(k)
		if !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("%q: %v", k, err)
			continue
		}
		if msg := err.Error(); strings.ContainsAny(msg, "\n\x7f") || len(msg) > 120 {
			t.Errorf("%q: unsafe message %q", k, msg)
		}
	}
	for _, p := range []string{"", "a/", "a", "a/b/"} {
		if err := storage.CheckPrefix(p); err != nil {
			t.Errorf("prefix %q: %v", p, err)
		}
	}
	for _, p := range []string{"/", "../", "a//"} {
		if err := storage.CheckPrefix(p); err == nil {
			t.Errorf("prefix %q passed", p)
		}
	}
}

func TestKeyErrorResponse(t *testing.T) {
	for err, want := range map[error]int{
		storage.NotFound("a"):                                 http.StatusNotFound,
		storage.CheckKey("../a"):                              http.StatusBadRequest,
		&storage.KeyError{Key: "a", Err: errors.New("other")}: http.StatusInternalServerError,
	} {
		var c errs.Converter
		if !errors.As(err, &c) || c.HTTPError().Status != want {
			t.Errorf("%v: want %d", err, want)
		}
	}
	if err := storage.NotFound("a"); !errors.Is(err, storage.ErrNotFound) || !strings.Contains(err.Error(), ": a") {
		t.Fatalf("NotFound: %v", err)
	}
}

func TestApplyPut(t *testing.T) {
	if o := storage.ApplyPut(storage.ContentType("image/png")); o.ContentType != "image/png" {
		t.Fatalf("%+v", o)
	}
	if o := storage.ApplyPut(); o.ContentType != "" {
		t.Fatalf("%+v", o)
	}
}

const secret = "very-secret-key"

func TestSecretHidden(t *testing.T) {
	s := storage.Secret(secret)
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "key", s)
	j, _ := json.Marshal(struct{ K storage.Secret }{s})
	for _, out := range []string{fmt.Sprint(s), fmt.Sprintf("%v %s %#v", s, s, s), fmt.Sprintf("%+v", storage.Config{SecretKey: s}), buf.String(), string(j)} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret leaked: %s", out)
		}
	}
	var got storage.Secret
	if err := got.UnmarshalText([]byte(secret)); err != nil || got.Reveal() != secret {
		t.Fatalf("UnmarshalText: %v", err)
	}
}

func TestConfigValidate(t *testing.T) {
	ok := []storage.Config{
		{Driver: "local"},
		{Driver: "s3", Endpoint: "https://s3.example.com"},
		{Driver: "rustfs", Endpoint: "http://localhost:9000"},
		{Driver: "rustfs", Endpoint: "http://127.0.0.1:9000"},
		{Driver: "local", PublicURL: "http://files.example.com/"},
	}
	for _, c := range ok {
		if err := c.Validate(); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	bad := []storage.Config{
		{},
		{Driver: "s3", Endpoint: "http://s3.example.com"},
		{Driver: "s3", Endpoint: "ftp://s3.example.com"},
		{Driver: "s3", Endpoint: "s3.example.com"},
		{Driver: "s3", Endpoint: "https://u:pw@s3.example.com"},
		{Driver: "local", PublicURL: "/files"},
	}
	for _, c := range bad {
		err := c.Validate()
		if err == nil {
			t.Errorf("%+v passed", c)
		} else if strings.Contains(err.Error(), "pw") {
			t.Errorf("password in error: %v", err)
		}
	}
}

func TestNew(t *testing.T) {
	dir := t.TempDir()
	s, err := storage.New(storage.Config{Driver: "local", LocalDir: dir}, storage.Driver{Name: "other"}, local.Driver)
	if err != nil {
		t.Fatal(err)
	}
	s.(*local.Store).Close()
	if _, err := storage.New(storage.Config{Driver: "nope"}, local.Driver, storage.Driver{Name: "s3"}); err == nil || !strings.Contains(err.Error(), "(local, s3)") {
		t.Fatalf("unknown driver: %v", err)
	}
	if _, err := storage.New(storage.Config{}); err == nil {
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
	if err := os.WriteFile(".env", []byte("STORAGE_DRIVER=local\nSTORAGE_LOCAL_DIR=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STORAGE_LOCAL_DIR", "from-env") // real variables win
	t.Setenv("STORAGE_SECRET_KEY", secret)
	var buf bytes.Buffer
	a, err := app(t, storage.Module(local.Driver), slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s := dygo.MustGet[storage.Store](a)
	if _, err := s.Put(context.Background(), "a.txt", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("from-env", "a.txt")); err != nil {
		t.Fatal("file not in STORAGE_LOCAL_DIR from the environment")
	}
	if !strings.Contains(buf.String(), "storage: ready") || !strings.Contains(buf.String(), "driver=local") || strings.Contains(buf.String(), secret) {
		t.Fatalf("log: %s", buf.String())
	}
}

func TestModuleDefaults(t *testing.T) {
	t.Chdir(t.TempDir())
	a, err := app(t, storage.Module(local.Driver), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dygo.MustGet[storage.Store](a).Put(context.Background(), "a", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("storage", "a")); err != nil {
		t.Fatal("default folder is not ./storage")
	}
}

func TestModuleErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("STORAGE_DRIVER", "s3")
	if _, err := app(t, storage.Module(local.Driver), nil); err == nil {
		t.Fatal("driver not passed to Module was accepted")
	}
	t.Setenv("STORAGE_PATH_STYLE", "maybe")
	if _, err := app(t, storage.Module(local.Driver), nil); err == nil {
		t.Fatal("bad bool passed")
	}
}

// fakeStore is a store with a health check that fails.
type fakeStore struct {
	storage.Store
	err error
}

func (f fakeStore) Check(context.Context) error { return f.err }

func ready(t *testing.T, s storage.Store) int {
	t.Helper()
	d := storage.Driver{Name: "fake", New: func(storage.Config) (storage.Store, error) { return s, nil }}
	root := &dygo.Module{Name: "app", Imports: []*dygo.Module{
		storage.ModuleWith(storage.Config{Driver: "fake"}, d),
		health.Module(health.Options{}),
	}}
	srv := dygotest.New(root).CompileHTTP(t)
	return srv.GET("/health/ready").Do(t).StatusCode
}

func TestHealthCheck(t *testing.T) {
	if code := ready(t, fakeStore{Store: storagetest.Local(t)}); code != http.StatusOK {
		t.Fatalf("healthy store: %d", code)
	}
	if code := ready(t, fakeStore{Store: storagetest.Local(t), err: errors.New("bucket gone")}); code != http.StatusServiceUnavailable {
		t.Fatalf("failing store: %d", code)
	}
	// A store without Check is always up.
	if code := ready(t, struct{ storage.Store }{storagetest.Local(t)}); code != http.StatusOK {
		t.Fatalf("store without Check: %d", code)
	}
}

// upload makes a *multipart.FileHeader holding data, as a bound form gives.
func upload(t *testing.T, name string, data []byte) *multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(data)
	_ = w.Close()
	form, err := multipart.NewReader(&body, w.Boundary()).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = form.RemoveAll() })
	return form.File["file"][0]
}

var png = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 100)...)

func status(err error) int {
	var c errs.Converter
	if errors.As(err, &c) {
		return c.HTTPError().Status
	}
	var h *errs.HTTPError
	if errors.As(err, &h) {
		return h.Status
	}
	return 0
}

func TestSaveUpload(t *testing.T) {
	ctx := context.Background()
	s := storagetest.Local(t)
	lim := storage.Limits{MaxBytes: 1000, Types: []string{"image/png", "image/jpeg"}}

	obj, err := storage.SaveUpload(ctx, s, upload(t, "me.png", png), "avatars/1.png", lim)
	if err != nil || obj.ContentType != "image/png" || obj.Size != int64(len(png)) {
		t.Fatalf("png: %+v %v", obj, err)
	}
	r, _, err := s.Get(ctx, "avatars/1.png")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if !bytes.Equal(got, png) {
		t.Fatal("stored bytes differ")
	}

	// The name says png, the content is HTML: refused.
	_, err = storage.SaveUpload(ctx, s, upload(t, "evil.png", []byte("<html><script>x</script>")), "avatars/2.png", lim)
	if status(err) != http.StatusUnsupportedMediaType || !strings.Contains(err.Error(), "text/html") {
		t.Fatalf("html: %v", err)
	}
	// Wildcards.
	if _, err := storage.SaveUpload(ctx, s, upload(t, "a.png", png), "a.png", storage.Limits{Types: []string{" Image/* "}}); err != nil {
		t.Fatalf("image/*: %v", err)
	}
	// No type list: anything goes, even an empty file.
	if obj, err := storage.SaveUpload(ctx, s, upload(t, "e", nil), "empty", storage.Limits{}); err != nil || obj.Size != 0 {
		t.Fatalf("empty: %+v %v", obj, err)
	}
	// Too large by the header.
	big := append(append([]byte{}, png...), bytes.Repeat([]byte{1}, 2000)...)
	if _, err := storage.SaveUpload(ctx, s, upload(t, "big.png", big), "big.png", lim); status(err) != http.StatusRequestEntityTooLarge {
		t.Fatalf("big: %v", err)
	}
	// Exactly the limit is fine.
	if _, err := storage.SaveUpload(ctx, s, upload(t, "x.png", png), "x.png", storage.Limits{MaxBytes: int64(len(png))}); err != nil {
		t.Fatalf("at limit: %v", err)
	}
	// Bad input.
	if _, err := storage.SaveUpload(ctx, s, nil, "a", lim); status(err) != http.StatusBadRequest {
		t.Fatalf("nil: %v", err)
	}
	if _, err := storage.SaveUpload(ctx, s, upload(t, "a.png", png), "../a", lim); status(err) != http.StatusBadRequest {
		t.Fatalf("bad key: %v", err)
	}
}

func TestSaveUploadLyingSize(t *testing.T) {
	ctx := context.Background()
	s := storagetest.Local(t)
	big := append(append([]byte{}, png...), bytes.Repeat([]byte{1}, 2000)...)
	fh := upload(t, "big.png", big)
	fh.Size = 10 // the header claims less than the content
	if _, err := storage.SaveUpload(ctx, s, fh, "big.png", storage.Limits{MaxBytes: 1000}); status(err) != http.StatusRequestEntityTooLarge {
		t.Fatalf("lying size: %v", err)
	}
	if _, err := s.Stat(ctx, "big.png"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("partial file kept: %v", err)
	}
}

func TestSaveUploadOpenFails(t *testing.T) {
	// A header with no content behind it, as after the form was removed.
	broken := &multipart.FileHeader{Filename: "a.png", Size: 3}
	if _, err := storage.SaveUpload(context.Background(), storagetest.Local(t), broken, "a.png", storage.Limits{}); err == nil {
		t.Fatal("open of a broken header passed")
	}
}
