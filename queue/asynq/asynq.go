// Package asynq is a [queue.Driver] that keeps jobs in Redis with
// github.com/hibiken/asynq, so they survive restarts and can be handled by
// other processes:
//
//	queue.Module(queue.Options{Driver: asynq.New(asynq.RedisClientOpt{Addr: "localhost:6379"})})
//
// Each job becomes an asynq task whose type is the job name. A job's
// attempts map to asynq's MaxRetry (attempts - 1), and queue.SkipRetry to
// asynq.SkipRetry. Failed tasks are archived by asynq and can be inspected
// with its CLI or web UI.
package asynq

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dygoframework/dygo/queue"
	"github.com/hibiken/asynq"
)

// RedisClientOpt is asynq's Redis connection options.
type RedisClientOpt = asynq.RedisClientOpt

// Options tunes the driver.
type Options struct {
	// RetryDelay is the wait before the next attempt; asynq's exponential
	// default when nil.
	RetryDelay func(attempt int, err error) time.Duration
	// ShutdownTimeout is how long Work waits for running jobs when it stops
	// (asynq's default of 8 seconds when zero).
	ShutdownTimeout time.Duration
	// Retention keeps completed tasks for this long, for inspection.
	Retention time.Duration
}

// Driver is the asynq driver.
type Driver struct {
	redis  asynq.RedisConnOpt
	client *asynq.Client
	opts   Options
}

// New returns a driver that connects with redis.
func New(redis asynq.RedisConnOpt, opts ...Options) *Driver {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	return &Driver{redis: redis, client: asynq.NewClient(redis), opts: o}
}

// Close closes the producer's Redis connection.
func (d *Driver) Close() error { return d.client.Close() }

// OnApplicationShutdown closes the connection when the driver is a provider.
func (d *Driver) OnApplicationShutdown(context.Context, string) error { return d.Close() }

// Enqueue implements queue.Driver.
func (d *Driver) Enqueue(ctx context.Context, j *queue.Job) error {
	if j.Queue == "" {
		j.Queue = queue.DefaultQueue
	}
	opts := []asynq.Option{asynq.Queue(j.Queue), asynq.MaxRetry(max(0, j.MaxAttempts-1))}
	if j.ID != "" {
		opts = append(opts, asynq.TaskID(j.ID))
	}
	if !j.RunAt.IsZero() {
		opts = append(opts, asynq.ProcessAt(j.RunAt))
	}
	if d.opts.Retention > 0 {
		opts = append(opts, asynq.Retention(d.opts.Retention))
	}
	info, err := d.client.EnqueueContext(ctx, asynq.NewTask(j.Name, j.Payload), opts...)
	if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
		return queue.ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("asynq: enqueue %s: %w", j.Name, err)
	}
	j.ID = info.ID
	return nil
}

// Work implements queue.Driver with an asynq server.
func (d *Driver) Work(ctx context.Context, queues []string, concurrency int, handle func(context.Context, *queue.Job) error) error {
	qs := make(map[string]int, len(queues))
	for _, q := range queues {
		qs[q] = 1
	}
	cfg := asynq.Config{
		Concurrency:     concurrency,
		Queues:          qs,
		ShutdownTimeout: d.opts.ShutdownTimeout,
		Logger:          quietLogger{},
		LogLevel:        asynq.ErrorLevel,
	}
	if d.opts.RetryDelay != nil {
		cfg.RetryDelayFunc = func(n int, err error, _ *asynq.Task) time.Duration { return d.opts.RetryDelay(n+1, err) }
	}
	srv := asynq.NewServer(d.redis, cfg)
	h := asynq.HandlerFunc(func(ctx context.Context, t *asynq.Task) error {
		j := &queue.Job{Name: t.Type(), Payload: t.Payload()}
		j.ID, _ = asynq.GetTaskID(ctx)
		j.Queue, _ = asynq.GetQueueName(ctx)
		retried, _ := asynq.GetRetryCount(ctx)
		maxRetry, _ := asynq.GetMaxRetry(ctx)
		j.Attempt, j.MaxAttempts = retried+1, maxRetry+1
		err := handle(ctx, j)
		if errors.Is(err, queue.ErrSkipRetry) {
			return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
		}
		return err
	})
	if err := srv.Start(h); err != nil {
		return fmt.Errorf("asynq: starting worker: %w", err)
	}
	<-ctx.Done()
	srv.Shutdown()
	return nil
}

type quietLogger struct{}

func (quietLogger) Debug(...any) {}
func (quietLogger) Info(...any)  {}
func (quietLogger) Warn(...any)  {}
func (quietLogger) Error(...any) {}
func (quietLogger) Fatal(...any) {}
