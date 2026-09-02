package scheduler

import (
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
	parsed, err := parseSchedule(schedule)
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

func parseSchedule(schedule string) (string, error) {
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
