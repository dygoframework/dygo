// Package report runs a background loop over every reporter in the
// application, collected through a provider group. It starts in
// OnApplicationBootstrap and stops in OnModuleDestroy.
package report

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/examples/worker/config"
)

// Group is the provider group reporters join with dygo.Group(report.Group).
const Group = "reporters"

// Reporter is implemented by anything that wants to be reported.
type Reporter interface {
	Name() string
	Report(ctx context.Context) string
}

// Module provides the runner.
var Module = &dygo.Module{
	Name:      "report",
	Providers: []dygo.Provider{dygo.Provide(NewRunner)},
	Exports:   []dygo.Token{dygo.TypeOf[*Runner]()},
}

// Params shows a parameter struct: several dependencies, one of them a group.
type Params struct {
	dygo.In
	Config    *config.Config
	Log       *slog.Logger
	Reporters []Reporter `group:"reporters"`
}

// Runner reports on an interval.
type Runner struct {
	p      Params
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	runs   int
}

// NewRunner is the constructor.
func NewRunner(p Params) *Runner { return &Runner{p: p} }

// OnApplicationBootstrap starts the loop once every module is initialized.
func (r *Runner) OnApplicationBootstrap(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTicker(r.p.Config.Interval)
		defer t.Stop()
		for {
			r.RunOnce(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return nil
}

// OnModuleDestroy stops the loop and waits for it.
func (r *Runner) OnModuleDestroy(context.Context) error {
	if r.cancel != nil {
		r.cancel()
		r.wg.Wait()
	}
	return nil
}

// RunOnce asks every reporter for a report.
func (r *Runner) RunOnce(ctx context.Context) {
	r.mu.Lock()
	r.runs++
	r.mu.Unlock()
	for _, rep := range r.p.Reporters {
		r.p.Log.Info("report", "reporter", rep.Name(), "result", rep.Report(ctx))
	}
}

// Runs returns how many times the loop ran.
func (r *Runner) Runs() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs
}
