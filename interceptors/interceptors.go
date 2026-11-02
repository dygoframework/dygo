// Package interceptors holds ready made interceptors.
package interceptors

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
)

// Timeout gives the handler a context deadline of d. If the handler returns
// context.DeadlineExceeded because of it, the client gets 408 Request Timeout.
// Handlers must honour ctx; Go cannot stop
// a handler that ignores it. The deadline covers the handler, not writing the
// response, so streaming results are cut off when it passes.
func Timeout(d time.Duration) dygo.Interceptor {
	return dygo.InterceptorFunc(func(ec dygo.ExecutionContext, next dygo.CallHandler) (any, error) {
		ctx, cancel := context.WithTimeout(ec.Context(), d)
		defer cancel()
		ec.SetContext(ctx)
		out, err := next.Handle()
		if err != nil && errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errs.New(http.StatusRequestTimeout, "", errs.Cause(err))
		}
		return out, err
	})
}

// Logging logs every handled request with its route, duration and error. A
// nil logger means slog.Default().
func Logging(log *slog.Logger) dygo.Interceptor {
	if log == nil {
		log = slog.Default()
	}
	return dygo.InterceptorFunc(func(ec dygo.ExecutionContext, next dygo.CallHandler) (any, error) {
		ctx := ec.Context()
		if !log.Enabled(ctx, slog.LevelInfo) {
			return next.Handle() // costs nothing when Info is off
		}
		start := time.Now()
		out, err := next.Handle()
		ri := ec.Route()
		if err != nil {
			log.LogAttrs(ctx, slog.LevelInfo, "request failed", slog.String("method", ri.Method), slog.String("route", ri.Path),
				slog.String("handler", ri.Handler), slog.Duration("duration", time.Since(start)), slog.Any("err", err))
		} else {
			log.LogAttrs(ctx, slog.LevelInfo, "request handled", slog.String("method", ri.Method), slog.String("route", ri.Path),
				slog.String("handler", ri.Handler), slog.Duration("duration", time.Since(start)))
		}
		return out, err
	})
}
