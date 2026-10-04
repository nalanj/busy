package scheduler

import (
	"errors"
	"testing"
	"time"
)

func TestParseSchedule(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		wantErr  bool
	}{
		// 5-field cron should get seconds prepended
		{"0 9 * * *", "0 0 9 * * *", false},
		// 6-field cron should pass through
		{"0 0 9 * * *", "0 0 9 * * *", false},
		// @every intervals
		{"@every 30s", "@every 30s", false},
		{"@every 5m", "@every 5m", false},
		// @once is handled separately
		{"@once", "", true},
		// Invalid
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := Parse(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error for '%s'", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error for '%s': %v", tt.input, err)
				return
			}
			if result != tt.expected {
				t.Errorf("Expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestSchedulerAdd(t *testing.T) {
	s := New()

	// Test adding a valid schedule
	id, err := s.Add("test", "@every 1s", func() {
	})
	if err != nil {
		t.Errorf("Failed to add schedule: %v", err)
	}
	if id == 0 {
		t.Error("Expected non-zero entry ID")
	}

	// Test adding @once (should error)
	_, err = s.Add("once", "@once", func() {})
	if err == nil {
		t.Error("Expected error for @once")
	}

	s.Stop()
}

func TestSchedulerStartStop(t *testing.T) {
	s := New()
	s.Start()
	s.Stop()
}

func TestSchedulerMultipleJobs(t *testing.T) {
	s := New()

	callCount := 0
	addAndVerify := func(name, schedule string) {
		_, err := s.Add(name, schedule, func() {
			callCount++
		})
		if err != nil {
			t.Errorf("Failed to add job %s: %v", name, err)
		}
	}

	addAndVerify("job1", "@every 1s")
	addAndVerify("job2", "0 * * * *")
	addAndVerify("job3", "@every 30s")

	s.Start()
	time.Sleep(100 * time.Millisecond)
	s.Stop()

	// Should have scheduled without errors
	if callCount > 0 {
		// Jobs may or may not have fired in this short window
	}
}

func TestRunOnce(t *testing.T) {
	called := false
	RunOnce(func() {
		called = true
	}, 50*time.Millisecond)

	time.Sleep(100 * time.Millisecond)
	if !called {
		t.Error("RunOnce did not call the function")
	}
}

// Verifies that @watch schedules surface through *WatchScheduleError
// so the caller can dispatch them to the watcher package.
func TestParseWatchSchedule(t *testing.T) {
	_, err := Parse("@watch /inbox/briefs/*.md")
	if err == nil {
		t.Fatal("expected error for @watch schedule")
	}
	if !errors.Is(err, ErrWatchSchedule) {
		t.Errorf("expected errors.Is(err, ErrWatchSchedule), got %v", err)
	}
	var wsErr *WatchScheduleError
	if !errors.As(err, &wsErr) {
		t.Errorf("expected errors.As to extract *WatchScheduleError")
	}
	if wsErr.Glob != "/inbox/briefs/*.md" {
		t.Errorf("expected glob /inbox/briefs/*.md, got %q", wsErr.Glob)
	}
}

// @watch without an argument should be an error, not silently misroute.
func TestParseWatchScheduleMissingArg(t *testing.T) {
	_, err := Parse("@watch ")
	if err == nil {
		t.Fatal("expected error for @watch with no glob")
	}
	if errors.Is(err, ErrWatchSchedule) {
		t.Errorf("expected a real error, not ErrWatchSchedule; got %v", err)
	}
}
