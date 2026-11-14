// Package local keeps a Dygo app's files in a folder on disk.
//
// Select it with STORAGE_DRIVER=local (the default) and STORAGE_LOCAL_DIR:
//
//	storage.Module(local.Driver)
//
// Every file operation goes through an [os.Root], so no key can reach outside
// the folder, not even through a symbolic link. Writes go to a temporary file
// first and are renamed into place, so a reader never sees half a file.
//
// The folder keeps no metadata: Get and Stat take the content type from the
// key's extension ("avatars/42.png" is image/png). Give keys an extension.
package local

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"mime"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/dygoframework/dygo/storage"
)

// Driver builds a [Store] from STORAGE_LOCAL_DIR and STORAGE_PUBLIC_URL.
var Driver = storage.Driver{
	Name: "local",
	New: func(c storage.Config) (storage.Store, error) {
		return New(c.LocalDir, Options{PublicURL: c.PublicURL})
	},
}

// Options are the optional settings of a [Store].
type Options struct {
	// PublicURL is where the folder is served from. When set, URL returns
	// PublicURL + key; otherwise URL returns storage.ErrNoURL.
	PublicURL string
}

// Store is a [storage.Store] on a folder.
type Store struct {
	root      *os.Root
	dir       string
	publicURL string
}

// tmpPrefix marks files that are still being written. List skips them.
const tmpPrefix = ".dygo-tmp-"

// New opens dir, creating it if needed.
func New(dir string, o Options) (*Store, error) {
	if dir == "" {
		return nil, errors.New("storage/local: STORAGE_LOCAL_DIR is empty")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("storage/local: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("storage/local: %w", err)
	}
	pub := o.PublicURL
	if pub != "" && !strings.HasSuffix(pub, "/") {
		pub += "/"
	}
	return &Store{root: root, dir: dir, publicURL: pub}, nil
}

// Close releases the folder. The app calls it at shutdown.
func (s *Store) Close() error { return s.root.Close() }

// OnApplicationShutdown closes the folder when the app stops.
func (s *Store) OnApplicationShutdown(context.Context) error { return s.Close() }

// Put writes r to key through a temporary file and a rename.
func (s *Store) Put(ctx context.Context, key string, r io.Reader, opts ...storage.PutOption) (storage.Object, error) {
	if err := start(ctx, key); err != nil {
		return storage.Object{}, err
	}
	o := storage.ApplyPut(opts...)
	name := key // a checked key is a slash path os.Root accepts on every system
	if dir := path.Dir(name); dir != "." {
		if err := s.root.MkdirAll(dir, 0o750); err != nil {
			return storage.Object{}, s.fail("put", key, err)
		}
	}
	tmp := path.Join(path.Dir(name), tmpPrefix+randomName())
	f, err := s.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return storage.Object{}, s.fail("put", key, err)
	}
	_, err = io.Copy(f, ctxReader{ctx, r})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = s.root.Rename(tmp, name)
	}
	if err != nil {
		_ = s.root.Remove(tmp)
		return storage.Object{}, s.fail("put", key, err)
	}
	obj, err := s.Stat(ctx, key)
	if err == nil && o.ContentType != "" {
		obj.ContentType = o.ContentType
	}
	return obj, err
}

// Get opens the file at key.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, storage.Object, error) {
	if err := start(ctx, key); err != nil {
		return nil, storage.Object{}, err
	}
	f, err := s.root.Open(key)
	if err != nil {
		return nil, storage.Object{}, s.fail("get", key, err)
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fs.ErrNotExist
	}
	if err != nil {
		f.Close()
		return nil, storage.Object{}, s.fail("get", key, err)
	}
	return f, object(key, info), nil
}

// Stat describes the file at key.
func (s *Store) Stat(ctx context.Context, key string) (storage.Object, error) {
	if err := start(ctx, key); err != nil {
		return storage.Object{}, err
	}
	info, err := s.root.Stat(key)
	if err == nil && !info.Mode().IsRegular() {
		err = fs.ErrNotExist
	}
	if err != nil {
		return storage.Object{}, s.fail("stat", key, err)
	}
	return object(key, info), nil
}

// Delete removes the file at key. A missing file is not an error.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := start(ctx, key); err != nil {
		return err
	}
	if info, err := s.root.Stat(key); err == nil && info.IsDir() {
		return nil // a folder is not a file; there is nothing to delete
	}
	err := s.root.Remove(key)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return s.fail("delete", key, err)
	}
	return nil
}

// List yields the files under prefix, sorted by key.
func (s *Store) List(ctx context.Context, prefix string) iter.Seq2[storage.Object, error] {
	return func(yield func(storage.Object, error) bool) {
		if err := storage.CheckPrefix(prefix); err != nil {
			yield(storage.Object{}, err)
			return
		}
		objs, err := s.list(ctx, prefix)
		if err != nil {
			yield(storage.Object{}, err)
			return
		}
		for _, o := range objs {
			if !yield(o, nil) {
				return
			}
		}
	}
}

func (s *Store) list(ctx context.Context, prefix string) ([]storage.Object, error) {
	// Walk only the folder the prefix is in.
	base := "."
	if i := strings.LastIndex(prefix, "/"); i >= 0 {
		base = prefix[:i]
	}
	var objs []storage.Object
	err := fs.WalkDir(s.root.FS(), base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), tmpPrefix) {
			return nil
		}
		key := p // fs.FS paths already use slashes
		if !strings.HasPrefix(key, prefix) || storage.CheckKey(key) != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // removed while walking
		}
		objs = append(objs, object(key, info))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("storage/local: list %q: %w", prefix, err)
	}
	slices.SortFunc(objs, func(a, b storage.Object) int { return strings.Compare(a.Key, b.Key) })
	return objs, nil
}

// URL returns PublicURL + key, or storage.ErrNoURL when no PublicURL is set.
// The link does not expire; ttl is ignored.
func (s *Store) URL(ctx context.Context, key string, _ time.Duration) (string, error) {
	if err := start(ctx, key); err != nil {
		return "", err
	}
	if s.publicURL == "" {
		return "", storage.ErrNoURL
	}
	segs := strings.Split(key, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}
	return s.publicURL + strings.Join(segs, "/"), nil
}

// Check reports whether the folder can be written, for the health check.
func (s *Store) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := tmpPrefix + "check-" + randomName()
	f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return fmt.Errorf("storage/local: %s is not writable: %w", s.dir, err)
	}
	f.Close()
	return s.root.Remove(name)
}

func start(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return storage.CheckKey(key)
}

func (s *Store) fail(op, key string, err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
		return storage.NotFound(key)
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err // the path is ours; the key says enough
	}
	return fmt.Errorf("storage/local: %s %s: %w", op, key, err)
}

func object(key string, info fs.FileInfo) storage.Object {
	ct := mime.TypeByExtension(path.Ext(key))
	if ct == "" {
		ct = "application/octet-stream"
	}
	return storage.Object{
		Key:         key,
		Size:        info.Size(),
		ContentType: ct,
		ETag:        fmt.Sprintf("%x-%x", info.ModTime().UnixNano(), info.Size()),
		Modified:    info.ModTime(),
	}
}

func randomName() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ctxReader stops a copy when ctx ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
