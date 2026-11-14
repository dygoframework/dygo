package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/dygoframework/dygo/errs"
)

// Limits are the rules [SaveUpload] checks before a file is stored.
type Limits struct {
	// MaxBytes is the largest file accepted. Zero means no limit; set one.
	MaxBytes int64
	// Types are the accepted media types, such as "image/png" or "image/*".
	// The type is read from the file's first bytes, not from the name or
	// the browser's header. Empty means any type.
	Types []string
}

// sniffLen is how many bytes http.DetectContentType looks at.
const sniffLen = 512

// SaveUpload stores one uploaded file under key. It refuses a file larger
// than lim.MaxBytes with 413 and a file whose content is not one of
// lim.Types with 415, and stores the content type it detected. Use it with a
// *multipart.FileHeader field bound from a form:
//
//	type AvatarForm struct {
//		File *multipart.FileHeader `form:"file" required:"true"`
//	}
func SaveUpload(ctx context.Context, s Store, fh *multipart.FileHeader, key string, lim Limits) (Object, error) {
	if fh == nil {
		return Object{}, errs.BadRequest("no file uploaded")
	}
	if err := CheckKey(key); err != nil {
		return Object{}, err
	}
	if lim.MaxBytes > 0 && fh.Size > lim.MaxBytes {
		return Object{}, tooLarge(lim.MaxBytes)
	}
	f, err := fh.Open()
	if err != nil {
		return Object{}, fmt.Errorf("storage: open upload: %w", err)
	}
	defer f.Close()

	head := make([]byte, sniffLen)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return Object{}, fmt.Errorf("storage: read upload: %w", err)
	}
	head = head[:n]
	ct := http.DetectContentType(head)
	if !allowedType(ct, lim.Types) {
		return Object{}, errs.New(http.StatusUnsupportedMediaType,
			"file type "+mediaType(ct)+" is not allowed", errs.Code("unsupported_file_type"))
	}

	body := io.MultiReader(bytes.NewReader(head), f)
	var lr *limitReader
	if lim.MaxBytes > 0 {
		// The header's size comes from the client; count the real bytes too.
		lr = &limitReader{r: body, left: lim.MaxBytes + 1}
		body = lr
	}
	obj, err := s.Put(ctx, key, body, ContentType(ct))
	if lr != nil && lr.left == 0 {
		_ = s.Delete(ctx, key)
		return Object{}, tooLarge(lim.MaxBytes)
	}
	return obj, err
}

func tooLarge(max int64) error {
	return errs.PayloadTooLarge(fmt.Sprintf("file is larger than %d bytes", max), errs.Code("file_too_large"))
}

// limitReader reads at most left bytes, then reports EOF.
type limitReader struct {
	r    io.Reader
	left int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}

func mediaType(ct string) string {
	t, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return ct
	}
	return t
}

func allowedType(ct string, types []string) bool {
	if len(types) == 0 {
		return true
	}
	t := mediaType(ct)
	for _, want := range types {
		want = strings.ToLower(strings.TrimSpace(want))
		if want == t || (strings.HasSuffix(want, "/*") && strings.HasPrefix(t, strings.TrimSuffix(want, "*"))) {
			return true
		}
	}
	return false
}
