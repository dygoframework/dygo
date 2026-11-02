// Package schedule runs jobs on a cron schedule, at an interval or once after
// a delay. A provider with a Jobs method declares its
// jobs:
//
//	func (r *Reports) Jobs(s *schedule.Registry) {
//		s.Cron("nightly-report", "0 3 * * *", r.nightly)
//		s.Every("refresh-cache", 5*time.Minute, r.refresh)
//		s.After("warm-up", 10*time.Second, r.warmUp)
//	}
//
// Import [Module] once in the root module. The *Scheduler finds every [Jobs]
// provider in OnModuleInit, starts the jobs in OnApplicationBootstrap, and in
// BeforeApplicationShutdown stops them and waits for running ones. Inject
// *schedule.Scheduler to list, stop, start or add jobs at runtime. dygo
// generate cron writes a job provider.
package schedule

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dygoframework/dygo"
)

// Jobs is a provider that declares scheduled jobs.
type Jobs interface {
	Jobs(s *Registry)
}

// Func is the work of a job. ctx is cancelled when the application shuts
// down.
type Func func(ctx context.Context) error

// Kind tells how a job is scheduled.
type Kind string

// The kinds of job.
const (
	KindCron     Kind = "cron"
	KindInterval Kind = "interval"
	KindTimeout  Kind = "timeout"
)

// Option configures one job.
type Option func(*job)

// AllowOverlap lets a run start while the previous one is still running. By
// default a tick that finds the job running is skipped.
func AllowOverlap() Option { return func(j *job) { j.overlap = true } }

// Disabled registers the job without starting it; start it with
// [Scheduler.Start].
func Disabled() Option { return func(j *job) { j.disabled = true } }

// TimeZone sets the location a cron expression is read in (time.Local by
// default).
func TimeZone(loc *time.Location) Option { return func(j *job) { j.loc = loc } }

// Registry is what a [Jobs] provider declares its jobs through.
type Registry struct {
	s    *Scheduler
	errs *[]error
}

// Cron adds a job run at the times matching expr (see [ParseCron]).
func (r *Registry) Cron(name, expr string, fn Func, opts ...Option) {
	r.add(r.s.AddCron(name, expr, fn, opts...))
}

// Every adds a job run every d, the first time d after startup.
func (r *Registry) Every(name string, d time.Duration, fn Func, opts ...Option) {
	r.add(r.s.AddInterval(name, d, fn, opts...))
}

// After adds a job run once, d after startup.
func (r *Registry) After(name string, d time.Duration, fn Func, opts ...Option) {
	r.add(r.s.AddTimeout(name, d, fn, opts...))
}

func (r *Registry) add(err error) {
	if err != nil {
		*r.errs = append(*r.errs, err)
	}
}

// ModuleOption configures [Module].
type ModuleOption func(*Scheduler)

// OnError sets the handler for job errors and panics. The default logs them
// with slog.Default().
func OnError(fn func(ctx context.Context, job string, err error)) ModuleOption {
	return func(s *Scheduler) { s.onError = fn }
}

// Module provides the *Scheduler. It is global; import it once in the root
// module.
func Module(opts ...ModuleOption) *dygo.Module {
	return &dygo.Module{
		Name:   "schedule",
		Global: true,
		Providers: []dygo.Provider{
			dygo.Provide(func(ref *dygo.ModuleRef) *Scheduler {
				s := New(opts...)
				s.ref = ref
				return s
			}),
		},
		Exports: []dygo.Token{dygo.TypeOf[*Scheduler]()},
	}
}

// Scheduler runs jobs. Jobs added before OnApplicationBootstrap (or before
// [Scheduler.StartAll] outside a module) wait for it; jobs added later start
// at once.
type Scheduler struct {
	ref     *dygo.ModuleRef
	onError func(ctx context.Context, job string, err error)

	mu      sync.Mutex
	jobs    map[string]*job
	started bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// New returns a scheduler outside of a module, for tests and small programs.
// Call StartAll to run its jobs and StopAll to end them.
func New(opts ...ModuleOption) *Scheduler {
	s := &Scheduler{jobs: map[string]*job{}}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.onError = func(ctx context.Context, job string, err error) {
		slog.Default().ErrorContext(ctx, "schedule: job failed", "job", job, "err", err)
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

type job struct {
	name     string
	kind     Kind
	every    time.Duration
	cron     *Cron
	fn       Func
	overlap  bool
	disabled bool
	loc      *time.Location

	// guarded by Scheduler.mu
	timer   *time.Timer
	next    time.Time
	last    time.Time
	gen     int // bumped on every arm and disarm, so a stale timer does nothing
	running int
	done    bool // a timeout that ran
}

// JobInfo describes a job, for [Scheduler.Jobs].
type JobInfo struct {
	Name string
	Kind Kind
	// Spec is the cron expression or the duration.
	Spec string
	// Next is the next planned run, zero when the job is stopped or done.
	Next time.Time
	// Last is when the latest run started, zero if it never ran.
	Last    time.Time
	Running bool
	Active  bool
}

// OnModuleInit collects the jobs of every [Jobs] provider.
func (s *Scheduler) OnModuleInit(context.Context) error {
	if s.ref == nil {
		return nil
	}
	var errs []error
	for _, p := range dygo.Discover[Jobs](s.ref) {
		p.Jobs(&Registry{s: s, errs: &errs})
	}
	return errors.Join(errs...)
}

// OnApplicationBootstrap starts the jobs.
func (s *Scheduler) OnApplicationBootstrap(context.Context) error {
	s.StartAll()
	return nil
}

// BeforeApplicationShutdown stops every job, cancels the context of running
// ones and waits for them until ctx is done.
func (s *Scheduler) BeforeApplicationShutdown(ctx context.Context, _ string) error {
	return s.StopAll(ctx)
}

// StartAll starts every job that is not disabled.
func (s *Scheduler) StartAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.ctx.Err() != nil {
		return
	}
	s.started = true
	for _, j := range s.jobs {
		if !j.disabled {
			s.arm(j, time.Now())
		}
	}
}

// StopAll stops every job, cancels the context of running ones and waits for
// them to return until ctx is done. The scheduler cannot be started again.
func (s *Scheduler) StopAll(ctx context.Context) error {
	s.mu.Lock()
	for _, j := range s.jobs {
		s.disarm(j)
	}
	s.cancel()
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("schedule: waiting for running jobs: %w", ctx.Err())
	}
}

// AddCron adds a cron job (see [ParseCron]).
func (s *Scheduler) AddCron(name, expr string, fn Func, opts ...Option) error {
	j := newJob(name, KindCron, fn, opts)
	c, err := ParseCron(expr, j.loc)
	if err != nil {
		return fmt.Errorf("job %q: %w", name, err)
	}
	j.cron = c
	return s.add(j)
}

// AddInterval adds a job run every d.
func (s *Scheduler) AddInterval(name string, d time.Duration, fn Func, opts ...Option) error {
	if d <= 0 {
		return fmt.Errorf("schedule: job %q: interval must be positive, got %s", name, d)
	}
	j := newJob(name, KindInterval, fn, opts)
	j.every = d
	return s.add(j)
}

// AddTimeout adds a job run once, d after it starts.
func (s *Scheduler) AddTimeout(name string, d time.Duration, fn Func, opts ...Option) error {
	if d < 0 {
		return fmt.Errorf("schedule: job %q: delay must not be negative, got %s", name, d)
	}
	j := newJob(name, KindTimeout, fn, opts)
	j.every = d
	return s.add(j)
}

func newJob(name string, k Kind, fn Func, opts []Option) *job {
	j := &job{name: name, kind: k, fn: fn}
	for _, o := range opts {
		o(j)
	}
	return j
}

func (s *Scheduler) add(j *job) error {
	if j.name == "" {
		return errors.New("schedule: a job needs a name")
	}
	if j.fn == nil {
		return fmt.Errorf("schedule: job %q: func is nil", j.name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.jobs[j.name]; dup {
		return fmt.Errorf("schedule: job %q is already registered", j.name)
	}
	s.jobs[j.name] = j
	if s.started && !j.disabled {
		s.arm(j, time.Now())
	}
	return nil
}

// Remove stops a job and forgets it. A running run finishes.
func (s *Scheduler) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[name]
	if !ok {
		return fmt.Errorf("schedule: no job %q", name)
	}
	s.disarm(j)
	delete(s.jobs, name)
	return nil
}

// Stop pauses a job. A running run finishes.
func (s *Scheduler) Stop(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[name]
	if !ok {
		return fmt.Errorf("schedule: no job %q", name)
	}
	s.disarm(j)
	return nil
}

// Start resumes a stopped or disabled job. An interval or timeout counts its
// delay again from now.
func (s *Scheduler) Start(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[name]
	if !ok {
		return fmt.Errorf("schedule: no job %q", name)
	}
	if s.ctx.Err() != nil {
		return errors.New("schedule: scheduler is stopped")
	}
	j.disabled = false
	if s.started && j.timer == nil {
		s.arm(j, time.Now())
	}
	return nil
}

// Jobs lists the jobs sorted by name.
func (s *Scheduler) Jobs() []JobInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]JobInfo, 0, len(s.jobs))
	for _, j := range s.jobs {
		info := JobInfo{
			Name: j.name, Kind: j.kind, Last: j.last, Running: j.running > 0, Active: j.timer != nil,
		}
		if j.timer != nil {
			info.Next = j.next
		}
		if j.cron != nil {
			info.Spec = j.cron.String()
		} else {
			info.Spec = j.every.String()
		}
		out = append(out, info)
	}
	slices.SortFunc(out, func(a, b JobInfo) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// arm plans the next run of j after now. s.mu is held.
func (s *Scheduler) arm(j *job, now time.Time) {
	if j.done {
		return
	}
	var next time.Time
	switch j.kind {
	case KindCron:
		next = j.cron.Next(now)
		if next.IsZero() {
			return
		}
	default:
		next = now.Add(j.every)
	}
	j.gen++
	gen := j.gen
	j.next = next
	j.timer = time.AfterFunc(time.Until(next), func() { s.fire(j, gen) })
}

// disarm stops j's timer. s.mu is held.
func (s *Scheduler) disarm(j *job) {
	if j.timer != nil {
		j.timer.Stop()
		j.timer = nil
	}
	j.gen++
}

func (s *Scheduler) fire(j *job, gen int) {
	s.mu.Lock()
	if j.gen != gen || s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	j.timer = nil
	now := time.Now()
	run := j.overlap || j.running == 0
	if run {
		j.running++
		j.last = now
		s.wg.Add(1)
	}
	if j.kind == KindTimeout {
		j.done = true
	} else {
		s.arm(j, now)
	}
	ctx := s.ctx
	s.mu.Unlock()
	if !run {
		return
	}
	go func() {
		defer s.wg.Done()
		err := runJob(ctx, j)
		s.mu.Lock()
		j.running--
		s.mu.Unlock()
		if err != nil {
			s.onError(ctx, j.name, err)
		}
	}()
}

func runJob(ctx context.Context, j *job) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("schedule: job %q panicked: %v\n%s", j.name, v, debug.Stack())
		}
	}()
	return j.fn(ctx)
}
