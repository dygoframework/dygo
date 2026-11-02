package schedule_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/schedule"
)

type reports struct {
	ticks   atomic.Int32
	warmed  chan struct{}
	stopped chan struct{}
}

func (r *reports) Jobs(s *schedule.Registry) {
	s.Every("tick", 5*time.Millisecond, func(context.Context) error { r.ticks.Add(1); return nil })
	s.After("warm-up", time.Millisecond, func(context.Context) error { close(r.warmed); return nil })
	s.Cron("nightly", "0 3 * * *", func(context.Context) error { return nil }, schedule.TimeZone(time.UTC))
	s.Every("long", time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		select {
		case <-r.stopped:
		default:
			close(r.stopped)
		}
		return nil
	})
}

func TestModuleRunsJobs(t *testing.T) {
	r := &reports{warmed: make(chan struct{}), stopped: make(chan struct{})}
	app, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name:      "app",
		Imports:   []*dygo.Module{schedule.Module()},
		Providers: []dygo.Provider{dygo.Value(r)},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := dygo.MustGet[*schedule.Scheduler](app)
	<-r.warmed
	for r.ticks.Load() < 3 {
		time.Sleep(time.Millisecond)
	}
	var names []string
	for _, j := range s.Jobs() {
		names = append(names, j.Name+":"+string(j.Kind))
		if j.Name == "nightly" && (j.Spec != "0 3 * * *" || j.Next.UTC().Hour() != 3 || !j.Active) {
			t.Errorf("nightly = %+v", j)
		}
		if j.Name == "warm-up" && (j.Active || j.Last.IsZero()) {
			t.Errorf("warm-up = %+v", j)
		}
	}
	if got := strings.Join(names, " "); got != "long:interval nightly:cron tick:interval warm-up:timeout" {
		t.Errorf("jobs = %s", got)
	}
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.stopped:
	default:
		t.Error("shutdown did not cancel and wait for the running job")
	}
	n := r.ticks.Load()
	time.Sleep(20 * time.Millisecond)
	if r.ticks.Load() != n {
		t.Error("jobs ran after shutdown")
	}
}

type badJobs struct{}

func (badJobs) Jobs(s *schedule.Registry) {
	s.Cron("bad", "61 * * * *", func(context.Context) error { return nil })
	s.Every("dup", time.Second, func(context.Context) error { return nil })
	s.Every("dup", time.Second, func(context.Context) error { return nil })
}

func TestBadJobsFailBoot(t *testing.T) {
	_, err := dygo.CreateApplicationContext(context.Background(), &dygo.Module{
		Name:      "app",
		Imports:   []*dygo.Module{schedule.Module()},
		Providers: []dygo.Provider{dygo.Value(badJobs{})},
	})
	if err == nil || !strings.Contains(err.Error(), "61 is outside") || !strings.Contains(err.Error(), `"dup" is already registered`) {
		t.Fatalf("err = %v", err)
	}
}

func TestStopStartAndOverlap(t *testing.T) {
	errs := make(chan error, 10)
	s := schedule.New(schedule.OnError(func(_ context.Context, job string, err error) { errs <- err }))
	var runs, running, maxRunning atomic.Int32
	release := make(chan struct{})
	if err := s.AddInterval("slow", time.Millisecond, func(context.Context) error {
		runs.Add(1)
		n := running.Add(1)
		for {
			m := maxRunning.Load()
			if n <= m || maxRunning.CompareAndSwap(m, n) {
				break
			}
		}
		<-release
		running.Add(-1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTimeout("fails", 0, func(context.Context) error { panic("oops") }, schedule.Disabled()); err != nil {
		t.Fatal(err)
	}
	s.StartAll()
	time.Sleep(20 * time.Millisecond)
	if maxRunning.Load() != 1 {
		t.Errorf("overlapping runs: %d", maxRunning.Load())
	}
	if err := s.Stop("slow"); err != nil {
		t.Fatal(err)
	}
	close(release)
	time.Sleep(5 * time.Millisecond)
	n := runs.Load()
	time.Sleep(10 * time.Millisecond)
	if runs.Load() != n {
		t.Error("stopped job ran")
	}
	if err := s.Start("fails"); err != nil {
		t.Fatal(err)
	}
	if err := <-errs; !strings.Contains(err.Error(), "panicked: oops") {
		t.Errorf("err = %v", err)
	}
	if err := s.Start("missing"); err == nil {
		t.Error("want an error for an unknown job")
	}
	if err := s.Remove("slow"); err != nil {
		t.Fatal(err)
	}
	if err := s.StopAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start("fails"); err == nil {
		t.Error("start after StopAll should fail")
	}
}

func TestAddValidation(t *testing.T) {
	s := schedule.New()
	fn := func(context.Context) error { return nil }
	for _, err := range []error{
		s.AddInterval("a", 0, fn),
		s.AddTimeout("b", -time.Second, fn),
		s.AddCron("c", "bad", fn),
		s.AddInterval("", time.Second, fn),
		s.AddInterval("d", time.Second, nil),
	} {
		if err == nil {
			t.Error("want an error")
		}
	}
}
