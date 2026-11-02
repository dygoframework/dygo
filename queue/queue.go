// Package queue runs background jobs: producers add
// jobs to a named queue, and processors handle them in workers with retries.
// A [Driver] stores the jobs; [NewMemory] keeps them in the process (lost on
// restart, for development and tests), and the separate module
// github.com/dygoframework/dygo/queue/asynq keeps them in Redis.
//
// A provider with a Process method is a processor:
//
//	type WelcomeEmail struct{ UserID string }
//
//	func (m *Mailer) Process(r *queue.Registry) {
//		queue.Handle(r, "welcome-email", m.sendWelcome) // func(ctx, WelcomeEmail) error
//	}
//
// and producers inject *queue.Queue:
//
//	_, err := s.queue.Add(ctx, "welcome-email", WelcomeEmail{UserID: id}, queue.Delay(time.Minute))
//
// Payloads are JSON. A handler error retries the job, up to its attempts
// (3 by default), with the driver's backoff. dygo generate job writes a
// processor.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/dygoframework/dygo"
)

// DefaultQueue is the queue of jobs added without [On].
const DefaultQueue = "default"

// Job is one unit of work, as a driver stores it.
type Job struct {
	ID      string
	Queue   string
	Name    string
	Payload []byte
	// Attempt is 1 on the first run.
	Attempt     int
	MaxAttempts int
	// RunAt is when the job may run first; zero means now.
	RunAt time.Time
}

// Driver stores and delivers jobs.
type Driver interface {
	// Enqueue stores j. It sets j.ID when empty; a driver may reject an ID
	// that is already queued with [ErrDuplicate].
	Enqueue(ctx context.Context, j *Job) error
	// Work delivers jobs of the given queues to handle, at most concurrency
	// at a time, until ctx is cancelled; then it waits for running handlers
	// and returns. When handle returns an error the driver retries the job
	// until MaxAttempts, then keeps it as failed. An error wrapped with
	// [SkipRetry] fails the job at once.
	Work(ctx context.Context, queues []string, concurrency int, handle func(ctx context.Context, j *Job) error) error
}

// ErrDuplicate is returned when a job ID is already queued.
var ErrDuplicate = errors.New("queue: duplicate job id")

// ErrSkipRetry marks an error that must not be retried; see [SkipRetry].
var ErrSkipRetry = errors.New("queue: skip retry")

// SkipRetry wraps err so the job fails without more attempts.
func SkipRetry(err error) error { return fmt.Errorf("%w: %w", ErrSkipRetry, err) }

// AddOption configures one job.
type AddOption func(*Job)

// On puts the job on queue name instead of [DefaultQueue].
func On(name string) AddOption { return func(j *Job) { j.Queue = name } }

// Delay runs the job no earlier than d from now.
func Delay(d time.Duration) AddOption { return func(j *Job) { j.RunAt = time.Now().Add(d) } }

// At runs the job no earlier than t.
func At(t time.Time) AddOption { return func(j *Job) { j.RunAt = t } }

// Attempts sets how many times the job may run (at least 1).
func Attempts(n int) AddOption { return func(j *Job) { j.MaxAttempts = max(1, n) } }

// ID sets the job ID, to deduplicate jobs.
func ID(id string) AddOption { return func(j *Job) { j.ID = id } }

// Processor is a provider that handles jobs. Process is called once, during
// OnModuleInit of the queue.
type Processor interface {
	Process(r *Registry)
}

// Registry is what a [Processor] registers its handlers through.
type Registry struct {
	q    *Queue
	errs *[]error
}

// Handle registers fn for jobs named name, on [DefaultQueue] unless the
// handler options say otherwise. The payload is decoded into a T; a payload
// that does not decode fails without a retry.
func Handle[T any](r *Registry, name string, fn func(ctx context.Context, payload T) error, opts ...HandleOption) {
	h := &handler{queue: DefaultQueue, fn: func(ctx context.Context, j *Job) error {
		var v T
		if err := json.Unmarshal(j.Payload, &v); err != nil {
			return SkipRetry(fmt.Errorf("decoding payload of %s: %w", name, err))
		}
		return fn(WithJob(ctx, j), v)
	}}
	for _, o := range opts {
		o(h)
	}
	if err := r.q.register(name, h); err != nil {
		*r.errs = append(*r.errs, err)
	}
}

// HandleOption configures a handler.
type HandleOption func(*handler)

// FromQueue makes the handler consume queue name.
func FromQueue(name string) HandleOption { return func(h *handler) { h.queue = name } }

type handler struct {
	queue string
	fn    func(ctx context.Context, j *Job) error
}

type jobKey struct{}

// WithJob returns ctx carrying j. Handlers get it already set.
func WithJob(ctx context.Context, j *Job) context.Context { return context.WithValue(ctx, jobKey{}, j) }

// JobFrom returns the job being handled, for its ID and attempt.
func JobFrom(ctx context.Context) (*Job, bool) {
	j, ok := ctx.Value(jobKey{}).(*Job)
	return j, ok
}

// Options configures [Module].
type Options struct {
	// Driver stores the jobs. The default is NewMemory(MemoryOptions{}).
	Driver Driver
	// Concurrency is how many jobs run at once (10 by default).
	Concurrency int
	// Attempts is the default number of attempts (3 by default).
	Attempts int
	// NoWorker only produces jobs; another process handles them.
	NoWorker bool
	// OnError gets every failed attempt; the default ignores them.
	OnError func(ctx context.Context, j *Job, err error)
}

// Module provides *Queue. It is global; import it once in the root module.
func Module(o Options) *dygo.Module {
	return &dygo.Module{
		Name:   "queue",
		Global: true,
		Providers: []dygo.Provider{
			dygo.Provide(func(ref *dygo.ModuleRef) *Queue {
				q := New(o)
				q.ref = ref
				return q
			}),
		},
		Exports: []dygo.Token{dygo.TypeOf[*Queue]()},
	}
}

// Queue adds jobs and, unless Options.NoWorker is set, runs a worker for the
// registered handlers from OnApplicationBootstrap until
// BeforeApplicationShutdown, which waits for running jobs.
type Queue struct {
	ref  *dygo.ModuleRef
	opts Options

	mu       sync.Mutex
	handlers map[string]*handler
	cancel   context.CancelFunc
	done     chan error
}

// New returns a queue outside of a module. Use Register, Start and Stop.
func New(o Options) *Queue {
	if o.Driver == nil {
		o.Driver = NewMemory(MemoryOptions{})
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 10
	}
	if o.Attempts <= 0 {
		o.Attempts = 3
	}
	return &Queue{opts: o, handlers: map[string]*handler{}}
}

// Driver returns the queue's driver.
func (q *Queue) Driver() Driver { return q.opts.Driver }

// Add enqueues a job named name with payload encoded as JSON and returns its
// ID.
func (q *Queue) Add(ctx context.Context, name string, payload any, opts ...AddOption) (string, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("queue: encoding %s: %w", name, err)
	}
	j := &Job{Queue: DefaultQueue, Name: name, Payload: b, MaxAttempts: q.opts.Attempts}
	for _, o := range opts {
		o(j)
	}
	if err := q.opts.Driver.Enqueue(ctx, j); err != nil {
		return "", err
	}
	return j.ID, nil
}

// Register adds the handlers of p.
func (q *Queue) Register(p Processor) error {
	var errs []error
	p.Process(&Registry{q: q, errs: &errs})
	return errors.Join(errs...)
}

func (q *Queue) register(name string, h *handler) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, dup := q.handlers[name]; dup {
		return fmt.Errorf("queue: job %q has two handlers", name)
	}
	q.handlers[name] = h
	return nil
}

// OnModuleInit registers every [Processor].
func (q *Queue) OnModuleInit(context.Context) error {
	var errs []error
	for _, p := range dygo.Discover[Processor](q.ref) {
		errs = append(errs, q.Register(p))
	}
	return errors.Join(errs...)
}

// OnApplicationBootstrap starts the worker.
func (q *Queue) OnApplicationBootstrap(context.Context) error {
	if q.opts.NoWorker {
		return nil
	}
	return q.Start()
}

// BeforeApplicationShutdown stops the worker and waits for running jobs.
func (q *Queue) BeforeApplicationShutdown(ctx context.Context, _ string) error {
	return q.Stop(ctx)
}

// Start runs the worker in the background for the queues that have handlers.
func (q *Queue) Start() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cancel != nil || len(q.handlers) == 0 {
		return nil
	}
	var queues []string
	seen := map[string]bool{}
	for _, h := range q.handlers {
		if !seen[h.queue] {
			seen[h.queue] = true
			queues = append(queues, h.queue)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	q.cancel, q.done = cancel, make(chan error, 1)
	go func() { q.done <- q.opts.Driver.Work(ctx, queues, q.opts.Concurrency, q.handle) }()
	return nil
}

// Stop ends the worker and waits for running jobs until ctx is done.
func (q *Queue) Stop(ctx context.Context) error {
	q.mu.Lock()
	cancel, done := q.cancel, q.done
	q.cancel = nil
	q.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("queue: waiting for running jobs: %w", ctx.Err())
	}
}

func (q *Queue) handle(ctx context.Context, j *Job) (err error) {
	q.mu.Lock()
	h := q.handlers[j.Name]
	q.mu.Unlock()
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("queue: job %s %s panicked: %v\n%s", j.Name, j.ID, v, debug.Stack())
		}
		if err != nil && q.opts.OnError != nil {
			q.opts.OnError(ctx, j, err)
		}
	}()
	if h == nil {
		return SkipRetry(fmt.Errorf("queue: no handler for job %q", j.Name))
	}
	return h.fn(ctx, j)
}
