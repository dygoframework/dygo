// Package storage keeps files for a Dygo app: uploads, avatars, exports.
//
// Apps code against one small interface, [Store]. Drivers live in their own
// packages: storage/local keeps files in a folder on disk, and storage/s3
// talks to AWS S3 and to RustFS.
// Which one runs is a setting in .env (STORAGE_DRIVER), not code:
//
//	type AvatarService struct{ files storage.Store }
//
//	func NewAvatarService(s storage.Store) *AvatarService { return &AvatarService{files: s} }
//
//	func (s *AvatarService) Save(ctx context.Context, userID string, fh *multipart.FileHeader) (storage.Object, error) {
//		return storage.SaveUpload(ctx, s.files, fh, "avatars/"+userID, storage.Limits{
//			MaxBytes: 2 << 20,
//			Types:    []string{"image/png", "image/jpeg"},
//		})
//	}
//
// [Module] reads the settings and provides the app's [Store]. Tests use a
// local store in a temporary folder; storage/storagetest holds the checks
// every driver must pass.
//
// The package uses only the standard library and Dygo core.
package storage

import (
	"context"
	"errors"
	"io"
	"iter"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dygoframework/dygo/errs"
)

// Store keeps files by key, such as "avatars/42.png". Implementations must be
// safe for concurrent use.
type Store interface {
	// Put writes r under key, replacing what was there.
	Put(ctx context.Context, key string, r io.Reader, opts ...PutOption) (Object, error)
	// Get opens the file at key. The caller closes the reader. A missing key
	// returns an error that matches [ErrNotFound].
	Get(ctx context.Context, key string) (io.ReadCloser, Object, error)
	// Stat describes the file at key without reading it.
	Stat(ctx context.Context, key string) (Object, error)
	// Delete removes the file at key. Deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
	// List yields the files whose keys start with prefix, sorted by key. An
	// empty prefix lists everything.
	List(ctx context.Context, prefix string) iter.Seq2[Object, error]
	// URL returns a link a browser can open for ttl, or [ErrNoURL] when the
	// store cannot make one.
	URL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// Object describes a stored file.
type Object struct {
	Key         string
	Size        int64
	ContentType string
	// ETag changes when the content changes. Drivers that can't tell leave
	// it empty.
	ETag     string
	Modified time.Time
}

// The errors drivers return, matched with errors.Is.
var (
	ErrNotFound   = errors.New("storage: file not found")
	ErrInvalidKey = errors.New("storage: invalid key")
	ErrNoURL      = errors.New("storage: this store cannot make URLs")
)

// KeyError is the error a store returns for one key. It turns into a 404
// (not found) or 400 (invalid key) response on its own.
type KeyError struct {
	Key string
	Err error
}

func (e *KeyError) Error() string { return e.Err.Error() + ": " + e.Key }

// Unwrap returns the reason, such as [ErrNotFound].
func (e *KeyError) Unwrap() error { return e.Err }

// HTTPError picks the response for the error.
func (e *KeyError) HTTPError() *errs.HTTPError {
	switch {
	case errors.Is(e.Err, ErrNotFound):
		return errs.NotFound("file not found", errs.Cause(e))
	case errors.Is(e.Err, ErrInvalidKey):
		return errs.BadRequest("invalid file key", errs.Cause(e))
	}
	return errs.Internal(http.StatusText(http.StatusInternalServerError), errs.Cause(e))
}

// NotFound returns the error for a missing key, for drivers.
func NotFound(key string) error { return &KeyError{Key: key, Err: ErrNotFound} }

// MaxKeyLen is the longest key, in bytes. It is the S3 limit.
const MaxKeyLen = 1024

// CheckKey reports whether key is safe to use with any driver: not empty, at
// most [MaxKeyLen] bytes of UTF-8, segments separated by single slashes, no
// leading or trailing slash, no "." or ".." segment, no backslash and no
// control characters. Every driver calls it, so a key can never point outside
// the store.
func CheckKey(key string) error {
	if !validKey(key) {
		return &KeyError{Key: printable(key), Err: ErrInvalidKey}
	}
	return nil
}

// CheckPrefix is [CheckKey] for a List prefix: it may be empty or end with a
// slash.
func CheckPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	return CheckKey(strings.TrimSuffix(prefix, "/"))
}

func validKey(key string) bool {
	if key == "" || len(key) > MaxKeyLen || !utf8.ValidString(key) {
		return false
	}
	for _, r := range key {
		if r == '\\' || unicode.IsControl(r) {
			return false
		}
	}
	for seg := range strings.SplitSeq(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// printable shortens a bad key for an error message.
func printable(key string) string {
	key = strings.ToValidUTF8(key, "?")
	key = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, key)
	if len(key) > 64 {
		key = key[:64] + "..."
	}
	return key
}

// PutOptions are the settings of one Put. Drivers read them with
// [ApplyPut].
type PutOptions struct {
	// ContentType is stored with the file and sent back on download. Empty
	// means the driver guesses it from the key's extension.
	ContentType string
}

// PutOption changes a Put.
type PutOption func(*PutOptions)

// ContentType sets the file's media type, such as "image/png".
func ContentType(t string) PutOption { return func(o *PutOptions) { o.ContentType = t } }

// ApplyPut returns the settings opts make, for drivers.
func ApplyPut(opts ...PutOption) PutOptions {
	var o PutOptions
	for _, fn := range opts {
		fn(&o)
	}
	return o
}
