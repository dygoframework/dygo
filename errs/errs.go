// Package errs defines HTTP errors for Dygo handlers and the default writer
// that turns any error into a JSON response:
//
//	{"statusCode": 404, "error": "Not Found", "message": "user not found", "code": "USER_NOT_FOUND"}
//
// Return an *HTTPError from a handler to choose the status:
//
//	return nil, errs.NotFound("user not found", errs.Code("USER_NOT_FOUND"))
package errs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/dygoframework/dygo/internal/binder"
)

// HTTPError is an error with an HTTP status.
type HTTPError struct {
	// Status is the HTTP status code.
	Status int
	// Code is a machine readable error code.
	Code string
	// Message is shown to the client. Defaults to the status text.
	Message string
	// Details is any extra JSON-encodable data for the client.
	Details any
	// Cause is logged but never sent to the client.
	Cause error
}

func (e *HTTPError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Cause != nil {
		return msg + ": " + e.Cause.Error()
	}
	return msg
}

// Unwrap returns the cause.
func (e *HTTPError) Unwrap() error { return e.Cause }

// Option configures an HTTPError.
type Option func(*HTTPError)

// Code sets the machine readable error code.
func Code(code string) Option { return func(e *HTTPError) { e.Code = code } }

// Details attaches extra data sent to the client.
func Details(d any) Option { return func(e *HTTPError) { e.Details = d } }

// Cause attaches an underlying error that is logged, not sent.
func Cause(err error) Option { return func(e *HTTPError) { e.Cause = err } }

// New creates an HTTPError with any status.
func New(status int, msg string, opts ...Option) *HTTPError {
	e := &HTTPError{Status: status, Message: msg}
	for _, o := range opts {
		o(e)
	}
	return e
}

// BadRequest returns a 400 error.
func BadRequest(msg string, opts ...Option) *HTTPError {
	return New(http.StatusBadRequest, msg, opts...)
}

// Unauthorized returns a 401 error.
func Unauthorized(msg string, opts ...Option) *HTTPError {
	return New(http.StatusUnauthorized, msg, opts...)
}

// Forbidden returns a 403 error.
func Forbidden(msg string, opts ...Option) *HTTPError {
	return New(http.StatusForbidden, msg, opts...)
}

// NotFound returns a 404 error.
func NotFound(msg string, opts ...Option) *HTTPError {
	return New(http.StatusNotFound, msg, opts...)
}

// MethodNotAllowed returns a 405 error.
func MethodNotAllowed(msg string, opts ...Option) *HTTPError {
	return New(http.StatusMethodNotAllowed, msg, opts...)
}

// Conflict returns a 409 error.
func Conflict(msg string, opts ...Option) *HTTPError {
	return New(http.StatusConflict, msg, opts...)
}

// Gone returns a 410 error.
func Gone(msg string, opts ...Option) *HTTPError {
	return New(http.StatusGone, msg, opts...)
}

// PayloadTooLarge returns a 413 error.
func PayloadTooLarge(msg string, opts ...Option) *HTTPError {
	return New(http.StatusRequestEntityTooLarge, msg, opts...)
}

// UnprocessableEntity returns a 422 error.
func UnprocessableEntity(msg string, opts ...Option) *HTTPError {
	return New(http.StatusUnprocessableEntity, msg, opts...)
}

// TooManyRequests returns a 429 error.
func TooManyRequests(msg string, opts ...Option) *HTTPError {
	return New(http.StatusTooManyRequests, msg, opts...)
}

// Internal returns a 500 error.
func Internal(msg string, opts ...Option) *HTTPError {
	return New(http.StatusInternalServerError, msg, opts...)
}

// NotImplemented returns a 501 error.
func NotImplemented(msg string, opts ...Option) *HTTPError {
	return New(http.StatusNotImplemented, msg, opts...)
}

// ServiceUnavailable returns a 503 error.
func ServiceUnavailable(msg string, opts ...Option) *HTTPError {
	return New(http.StatusServiceUnavailable, msg, opts...)
}

// Converter is an error that chooses its own response, such as
// *validate.ValidationError. DefaultWriter uses HTTPError's result.
type Converter interface {
	error
	HTTPError() *HTTPError
}

// Body is the JSON error body.
type Body struct {
	StatusCode int    `json:"statusCode"`
	Error      string `json:"error"`
	Message    string `json:"message"`
	Code       string `json:"code,omitempty"`
	Details    any    `json:"details,omitempty"`
}

// Writer writes err as the response.
type Writer func(w http.ResponseWriter, r *http.Request, err error)

// StatusClientClosedRequest is written (without a body) when the client went
// away before the handler finished.
const StatusClientClosedRequest = 499

// DefaultWriter maps errors to responses:
//
//   - *HTTPError: its status, message, code and details
//   - a [Converter]: the *HTTPError it returns
//   - binding errors: 400 with the failing fields in details
//   - a body over the size limit: 413
//   - context.Canceled: 499, no body
//   - context.DeadlineExceeded: 503
//   - anything else: 500 with a generic message; the real error is logged
func DefaultWriter(log *slog.Logger) Writer {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return func(w http.ResponseWriter, r *http.Request, err error) {
		body := ToBody(err)
		switch {
		case body.StatusCode == StatusClientClosedRequest:
			w.WriteHeader(StatusClientClosedRequest)
			return
		case body.StatusCode >= 500:
			log.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path,
				"status", body.StatusCode, "err", err)
		}
		WriteJSON(w, body)
	}
}

// ToBody converts any error to the body DefaultWriter sends.
func ToBody(err error) Body {
	var he *HTTPError
	var conv Converter
	var be *binder.Error
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &he):
		b := Body{StatusCode: he.Status, Message: he.Message, Code: he.Code, Details: he.Details}
		if b.Message == "" {
			b.Message = http.StatusText(he.Status)
		}
		return withError(b)
	case errors.As(err, &conv):
		if he := conv.HTTPError(); he != nil {
			return ToBody(he)
		}
		return withError(Body{StatusCode: http.StatusInternalServerError, Message: "Internal Server Error"})
	case errors.As(err, &be):
		return withError(Body{StatusCode: http.StatusBadRequest, Message: "invalid request", Details: be.Fields})
	case errors.As(err, &mbe):
		return withError(Body{StatusCode: http.StatusRequestEntityTooLarge, Message: "request body too large"})
	case errors.Is(err, context.Canceled):
		return Body{StatusCode: StatusClientClosedRequest}
	case errors.Is(err, context.DeadlineExceeded):
		return withError(Body{StatusCode: http.StatusServiceUnavailable, Message: "request timed out"})
	default:
		return withError(Body{StatusCode: http.StatusInternalServerError, Message: "Internal Server Error"})
	}
}

func withError(b Body) Body {
	b.Error = http.StatusText(b.StatusCode)
	return b
}

// WriteJSON writes b with its status code.
func WriteJSON(w http.ResponseWriter, b Body) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(b.StatusCode)
	_ = json.NewEncoder(w).Encode(b)
}
