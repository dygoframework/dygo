package errs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
)

func TestHTTPErrorMessageAndUnwrap(t *testing.T) {
	cause := errors.New("db down")
	e := errs.NotFound("", errs.Code("USER_NOT_FOUND"), errs.Details(map[string]int{"id": 1}), errs.Cause(cause))
	if e.Status != http.StatusNotFound || e.Code != "USER_NOT_FOUND" || e.Details == nil {
		t.Fatalf("%+v", e)
	}
	if got := e.Error(); got != "Not Found: db down" {
		t.Fatalf("Error() = %q", got)
	}
	if !errors.Is(e, cause) {
		t.Fatal("Unwrap does not reach the cause")
	}
	if got := errs.New(http.StatusTeapot, "short").Error(); got != "short" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestHelperStatuses(t *testing.T) {
	cases := map[int]func(string, ...errs.Option) *errs.HTTPError{
		400: errs.BadRequest, 401: errs.Unauthorized, 403: errs.Forbidden, 404: errs.NotFound,
		405: errs.MethodNotAllowed, 409: errs.Conflict, 410: errs.Gone, 413: errs.PayloadTooLarge,
		422: errs.UnprocessableEntity, 429: errs.TooManyRequests, 500: errs.Internal,
		501: errs.NotImplemented, 503: errs.ServiceUnavailable,
	}
	for want, fn := range cases {
		if got := fn("m").Status; got != want {
			t.Errorf("status %d, want %d", got, want)
		}
	}
}

type converted struct{ he *errs.HTTPError }

func (c converted) Error() string              { return "converted" }
func (c converted) HTTPError() *errs.HTTPError { return c.he }

func TestToBody(t *testing.T) {
	bindErr := &dygo.BindError{Fields: []dygo.FieldError{{Source: "query", Name: "n", Reason: "must be an integer"}}}
	cases := []struct {
		name    string
		err     error
		status  int
		message string
	}{
		{"http error", errs.Conflict("taken", errs.Code("TAKEN")), 409, "taken"},
		{"empty message", errs.New(http.StatusTeapot, ""), 418, "I'm a teapot"},
		{"wrapped", fmt.Errorf("svc: %w", errs.Forbidden("no")), 403, "no"},
		{"converter", converted{errs.UnprocessableEntity("bad")}, 422, "bad"},
		{"nil converter", converted{}, 500, "Internal Server Error"},
		{"binding", bindErr, 400, "invalid request"},
		{"body limit", &http.MaxBytesError{Limit: 1}, 413, "request body too large"},
		{"canceled", fmt.Errorf("x: %w", context.Canceled), errs.StatusClientClosedRequest, ""},
		{"deadline", context.DeadlineExceeded, 503, "request timed out"},
		{"other", errors.New("secret detail"), 500, "Internal Server Error"},
	}
	for _, c := range cases {
		b := errs.ToBody(c.err)
		if b.StatusCode != c.status || b.Message != c.message {
			t.Errorf("%s: got %d %q, want %d %q", c.name, b.StatusCode, b.Message, c.status, c.message)
		}
		if c.status != errs.StatusClientClosedRequest && b.Error != http.StatusText(c.status) {
			t.Errorf("%s: error %q", c.name, b.Error)
		}
	}
	if b := errs.ToBody(bindErr); b.Details == nil {
		t.Error("binding error lost its fields")
	}
}

func TestDefaultWriter(t *testing.T) {
	var logs bytes.Buffer
	w := errs.DefaultWriter(slog.New(slog.NewTextHandler(&logs, nil)))
	r := httptest.NewRequest(http.MethodGet, "/x", nil)

	rec := httptest.NewRecorder()
	w(rec, r, errs.NotFound("gone", errs.Code("NF")))
	var b errs.Body
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 404 || b.Code != "NF" || rec.Header().Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("%d %+v %v", rec.Code, b, rec.Header())
	}
	if logs.Len() != 0 {
		t.Fatalf("4xx was logged: %s", logs.String())
	}

	rec = httptest.NewRecorder()
	w(rec, r, errors.New("secret detail"))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("500 leaked the error: %s", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "secret detail") {
		t.Fatalf("500 not logged: %s", logs.String())
	}

	rec = httptest.NewRecorder()
	w(rec, r, context.Canceled)
	if rec.Code != errs.StatusClientClosedRequest || rec.Body.Len() != 0 {
		t.Fatalf("canceled: %d %q", rec.Code, rec.Body.String())
	}

	// A nil logger must not panic.
	errs.DefaultWriter(nil)(httptest.NewRecorder(), r, errors.New("x"))
}
