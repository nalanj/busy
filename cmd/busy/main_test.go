package main

import (
	"strings"
	"testing"

	"github.com/nalanj/busy/internal/config"
)

func TestCheckPreconditions(t *testing.T) {
	tests := []struct {
		name      string
		conds     []string
		wantSkip  bool
		wantInMsg string // substring expected in the reason when skipped
	}{
		{
			name:     "no preconditions means pass",
			conds:    nil,
			wantSkip: false,
		},
		{
			name:     "all preconditions pass",
			conds:    []string{"true", "true"},
			wantSkip: false,
		},
		{
			name:     "one precondition fails",
			conds:    []string{"true", "false", "true"},
			wantSkip: true,
		},
		{
			name:      "stdout is captured in reason",
			conds:     []string{`echo "nothing to do" && false`},
			wantSkip:  true,
			wantInMsg: "nothing to do",
		},
		{
			name:     "shell error propagates",
			conds:    []string{`exit 7`},
			wantSkip: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skip, reason := checkPreconditions(tt.conds)
			if skip != tt.wantSkip {
				t.Errorf("skip: got %v, want %v (reason=%q)", skip, tt.wantSkip, reason)
			}
			if tt.wantInMsg != "" && !strings.Contains(reason, tt.wantInMsg) {
				t.Errorf("reason %q does not contain %q", reason, tt.wantInMsg)
			}
		})
	}
}

func TestFindJob(t *testing.T) {
	jobs := []config.JobConfig{
		{Name: "a"},
		{Name: "b"},
	}
	if _, ok := findJob(jobs, "a"); !ok {
		t.Error("expected to find job 'a'")
	}
	if _, ok := findJob(jobs, "missing"); ok {
		t.Error("expected not to find 'missing'")
	}
}
