// Package health serves liveness and readiness checks:
//
//	var AppModule = &dygo.Module{
//		Imports: []*dygo.Module{health.Module(health.Options{})},
//		Providers: []dygo.Provider{
//			dygo.Provide(func(db *sql.DB) health.Indicator { return health.Ping("db", db) },
//				dygo.Group(health.Indicators)),
//		},
//	}
//
// GET /health/live answers 200 while the process runs. GET /health/ready runs
// every indicator and answers 200 when all are up, 503 otherwise, and 503 as
// soon as shutdown starts so load balancers stop sending traffic. The body has
// the terminus shape:
//
//	{"status":"ok","info":{"db":{"status":"up"}},"error":{},"details":{"db":{"status":"up"}}}
package health

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/openapi"
)

// Indicators is the group name for indicators: provide them with
// dygo.Group(health.Indicators).
const Indicators = "dygo.health.indicators"

// check runs an indicator, turning a panic into an error: indicators run on
// their own goroutines, where a panic would otherwise end the process.
func check(ctx context.Context, ind Indicator) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return ind.Check(ctx)
}

// Indicator checks one dependency.
type Indicator interface {
	Name() string
	Check(ctx context.Context) error
}

type indicator struct {
	name string
	fn   func(context.Context) error
}

func (i indicator) Name() string                    { return i.name }
func (i indicator) Check(ctx context.Context) error { return i.fn(ctx) }

// Func makes an indicator from a function.
func Func(name string, fn func(ctx context.Context) error) Indicator { return indicator{name, fn} }

// Pinger is implemented by *sql.DB, *sql.Conn and most database clients.
type Pinger interface {
	PingContext(ctx context.Context) error
}

// Ping checks a database (or anything with PingContext).
func Ping(name string, p Pinger) Indicator { return Func(name, p.PingContext) }

// HTTP checks that url answers GET with a status below 400.
func HTTP(name, url string, client *http.Client) Indicator {
	if client == nil {
		client = http.DefaultClient
	}
	return Func(name, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	})
}

// Options configure Module.
type Options struct {
	// Path defaults to "/health"; the checks are Path+"/live" and Path+"/ready".
	Path string
	// Timeout bounds each readiness run. Defaults to 3 seconds.
	Timeout time.Duration
	// Use adds route options to the checks, for example your auth guard's
	// "public" marker or ratelimit.Skip().
	Use []dygo.RouteOption
}

// Module returns a module with the health controller. Its routes are version
// neutral; exclude Path from the global prefix if you use one:
// dygo.WithGlobalPrefix("/api", "/health").
func Module(o Options) *dygo.Module {
	if o.Path == "" {
		o.Path = "/health"
	}
	if o.Timeout == 0 {
		o.Timeout = 3 * time.Second
	}
	return &dygo.Module{
		Name: "health",
		Controllers: []dygo.Provider{dygo.Provide(func(p struct {
			dygo.In
			Indicators []Indicator `group:"dygo.health.indicators"`
		},
		) *Controller {
			return &Controller{opts: o, indicators: p.Indicators}
		})},
	}
}

// Controller serves the checks.
type Controller struct {
	opts       Options
	indicators []Indicator
}

// Status is one indicator's result.
type Status struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// Result is the readiness body.
type Result struct {
	Status  string            `json:"status"`
	Info    map[string]Status `json:"info"`
	Error   map[string]Status `json:"error"`
	Details map[string]Status `json:"details"`
}

// Routes registers the checks.
func (c *Controller) Routes(r dygo.Router) {
	r = r.Prefix(c.opts.Path).Use(dygo.Version(dygo.VersionNeutral), openapi.Exclude()).Use(c.opts.Use...)
	r.Get("/live", dygo.HandleNoInput(func(context.Context) (Result, error) {
		return Result{Status: "ok", Info: map[string]Status{}, Error: map[string]Status{}, Details: map[string]Status{}}, nil
	}))
	r.Get("/ready", dygo.HandleNoInput(c.Ready))
}

// Ready runs every indicator concurrently.
func (c *Controller) Ready(ctx context.Context) (Result, error) {
	res := Result{Status: "ok", Info: map[string]Status{}, Error: map[string]Status{}, Details: map[string]Status{}}
	select {
	case <-dygo.Stopping(ctx):
		res.Status = "shutting_down"
		dygo.SetStatus(ctx, http.StatusServiceUnavailable)
		return res, nil
	default:
	}
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, ind := range c.indicators {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := check(ctx, ind)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				s := Status{Status: "down", Message: err.Error()}
				res.Error[ind.Name()], res.Details[ind.Name()] = s, s
				return
			}
			res.Info[ind.Name()], res.Details[ind.Name()] = Status{Status: "up"}, Status{Status: "up"}
		}()
	}
	wg.Wait()
	if len(res.Error) > 0 {
		res.Status = "error"
		dygo.SetStatus(ctx, http.StatusServiceUnavailable)
	}
	return res, nil
}
