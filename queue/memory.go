package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"time"
)

// MemoryOptions configures [NewMemory].
type MemoryOptions struct {
	// Backoff is the wait before attempt n+1 after attempt n failed. The
	// default doubles from one second up to one minute.
	Backoff func(attempt int) time.Duration
	// KeepFailed bounds how many failed jobs Failed returns (100 by default).
	KeepFailed int
}

// FailedJob is a job that used all its attempts.
type FailedJob struct {
	Job *Job
	Err error
	At  time.Time
}

// Memory is a [Driver] that keeps jobs in the process. Jobs are lost when the
// process stops.
type Memory struct {
	opts MemoryOptions

	mu      sync.Mutex
	ready   map[string][]*Job
	ids     map[string]bool // queued, delayed or running
	timers  map[*Job]*time.Timer
	running int
	failed  []FailedJob
	changed chan struct{} // closed and replaced on every change
}

// NewMemory returns an in-memory driver.
func NewMemory(o MemoryOptions) *Memory {
	if o.Backoff == nil {
		o.Backoff = func(n int) time.Duration { return min(time.Second<<(n-1), time.Minute) }
	}
	if o.KeepFailed <= 0 {
		o.KeepFailed = 100
	}
	return &Memory{
		opts: o, ready: map[string][]*Job{}, ids: map[string]bool{},
		timers: map[*Job]*time.Timer{}, changed: make(chan struct{}),
	}
}

func (m *Memory) notify() {
	close(m.changed)
	m.changed = make(chan struct{})
}

// Enqueue implements Driver.
func (m *Memory) Enqueue(_ context.Context, j *Job) error {
	if j.Queue == "" {
		j.Queue = DefaultQueue
	}
	if j.MaxAttempts <= 0 {
		j.MaxAttempts = 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if j.ID == "" {
		j.ID = newID()
	} else if m.ids[j.ID] {
		return ErrDuplicate
	}
	m.ids[j.ID] = true
	cp := *j
	cp.Attempt = 0
	m.schedule(&cp, j.RunAt)
	return nil
}

// schedule makes j ready at t. m.mu is held.
func (m *Memory) schedule(j *Job, t time.Time) {
	if d := time.Until(t); d > 0 {
		m.timers[j] = time.AfterFunc(d, func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if _, ok := m.timers[j]; ok {
				delete(m.timers, j)
				m.push(j)
			}
		})
		return
	}
	m.push(j)
}

func (m *Memory) push(j *Job) {
	m.ready[j.Queue] = append(m.ready[j.Queue], j)
	m.notify()
}

// take removes the oldest ready job of the queues, waiting until there is
// one or ctx is done.
func (m *Memory) take(ctx context.Context, queues []string) *Job {
	for {
		m.mu.Lock()
		for _, q := range queues {
			if js := m.ready[q]; len(js) > 0 {
				j := js[0]
				m.ready[q] = js[1:]
				m.running++
				m.mu.Unlock()
				return j
			}
		}
		ch := m.changed
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil
		}
	}
}

// Work implements Driver. Running handlers get a context that is not
// cancelled with ctx, so they finish after Work is asked to stop.
func (m *Memory) Work(ctx context.Context, queues []string, concurrency int, handle func(context.Context, *Job) error) error {
	sem := make(chan struct{}, max(1, concurrency))
	var wg sync.WaitGroup
	defer wg.Wait()
	hctx := context.WithoutCancel(ctx)
	for {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil
		}
		j := m.take(ctx, queues)
		if j == nil {
			return nil
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			j.Attempt++
			m.finish(j, handle(hctx, j))
		}()
	}
}

func (m *Memory) finish(j *Job, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running--
	defer m.notify()
	if err == nil {
		delete(m.ids, j.ID)
		return
	}
	if j.Attempt < j.MaxAttempts && !errors.Is(err, ErrSkipRetry) {
		m.schedule(j, time.Now().Add(m.opts.Backoff(j.Attempt)))
		return
	}
	delete(m.ids, j.ID)
	m.failed = append(m.failed, FailedJob{Job: j, Err: err, At: time.Now()})
	if over := len(m.failed) - m.opts.KeepFailed; over > 0 {
		m.failed = slices.Delete(m.failed, 0, over)
	}
}

// Pending returns the number of jobs queued, waiting for a retry or delay, or
// running.
func (m *Memory) Pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.ids)
}

// Failed returns the latest jobs that used all their attempts, oldest first.
func (m *Memory) Failed() []FailedJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.failed)
}

// Wait blocks until no job is pending, or ctx is done. Tests use it after
// adding jobs.
func (m *Memory) Wait(ctx context.Context) error {
	for {
		m.mu.Lock()
		n, ch := len(m.ids), m.changed
		m.mu.Unlock()
		if n == 0 {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
