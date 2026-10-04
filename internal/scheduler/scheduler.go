package scheduler

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cron *cron.Cron
}

func New() *Scheduler {
	return &Scheduler{
		cron: cron.New(cron.WithSeconds()),
	}
}

func (s *Scheduler) Add(name, schedule string, fn func()) (cron.EntryID, error) {
	parsed, err := Parse(schedule)
	if err != nil {
		return 0, err
	}
	return s.cron.AddFunc(parsed, fn)
}

func (s *Scheduler) Start() {
	s.cron.Start()
}

func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
}

// ErrWatchSchedule is returned by Parse when the schedule is
// "@watch <glob>". The caller should type-assert to *WatchScheduleError
// to extract the glob, then set up a watcher instead of a cron job.
var ErrWatchSchedule = errors.New("@watch schedule")

// WatchScheduleError carries the glob from an @watch schedule.
type WatchScheduleError struct {
	Glob string
}

func (e *WatchScheduleError) Error() string { return "@watch " + e.Glob }

// Is implements the errors.Is contract so callers can use
// errors.Is(err, ErrWatchSchedule).
func (e *WatchScheduleError) Is(target error) bool {
	return target == ErrWatchSchedule
}

func Parse(schedule string) (string, error) {
	schedule = strings.TrimSpace(schedule)

	// Handle @once and @session-start - one-shot execution
	if schedule == "@once" || schedule == "@session-start" || schedule == "@web-message" {
		return "", fmt.Errorf("%s is handled separately, not by scheduler", schedule)
	}

	// Handle @every intervals
	if strings.HasPrefix(schedule, "@every ") {
		duration := strings.TrimPrefix(schedule, "@every ")
		return "@every " + duration, nil
	}

	// Handle @watch <glob>. Parsing is delegated to the watcher
	// package; we surface the glob through *WatchScheduleError so the
	// caller can set up the file watcher.
	if strings.HasPrefix(schedule, "@watch ") {
		glob := strings.TrimPrefix(schedule, "@watch ")
		glob = strings.TrimSpace(glob)
		if glob == "" {
			return "", fmt.Errorf("@watch requires a glob argument")
		}
		return "", &WatchScheduleError{Glob: glob}
	}

	// Assume standard cron with 5 fields (min hour day month dow)
	// Convert to 6 fields by prepending seconds (0)
	parts := strings.Fields(schedule)
	if len(parts) == 5 {
		return "0 " + schedule, nil
	}
	if len(parts) == 6 {
		return schedule, nil
	}

	return "", fmt.Errorf("invalid schedule format: %s", schedule)
}

// RunOnce executes a function once after a delay
func RunOnce(fn func(), delay time.Duration) {
	time.AfterFunc(delay, fn)
}
