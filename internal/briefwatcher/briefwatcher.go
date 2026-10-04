// Package briefwatcher detects new or changed briefs in a directory,
// comparing against a persisted state file. It is used to skip the
// per-poll LLM call when nothing has changed since the last run.
package briefwatcher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Brief is one brief that the caller has not yet processed.
type Brief struct {
	Name  string `json:"name"`  // filename without .md
	Path  string `json:"path"`  // full path to the .md file
	Mtime int64  `json:"mtime"` // unix seconds
	Sha   string `json:"sha"`   // sha256 hex
}

// processedState is the on-disk shape of the state file: each project
// name maps to the mtime and content hash at the time of last
// successful processing.
type processedState map[string]struct {
	Mtime int64  `json:"mtime"`
	Sha   string `json:"sha"`
}

// Check lists the .md files in inboxDir (skipping dotfiles and
// non-markdown files) and returns those that are new or have changed
// since the last successful processing, as recorded in statePath.
//
// A missing state file is treated as empty (every brief is pending).
// The state file is not modified by this function; the caller is
// responsible for writing the updated state after it has successfully
// processed the returned briefs.
func Check(inboxDir, statePath string) ([]Brief, error) {
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading inbox dir %s: %w", inboxDir, err)
	}

	var state processedState
	if data, err := os.ReadFile(statePath); err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, fmt.Errorf("parsing state file %s: %w", statePath, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading state file %s: %w", statePath, err)
	}

	var pending []Brief
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.HasSuffix(name, ".md") {
			continue
		}

		path := filepath.Join(inboxDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			// Unreadable brief — skip rather than fail the whole check.
			// The LLM-driven path will produce a clearer error if needed.
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}

		sum := sha256.Sum256(data)
		sha := hex.EncodeToString(sum[:])
		mtime := info.ModTime().Unix()
		briefName := strings.TrimSuffix(name, ".md")

		prev, seen := state[briefName]
		if seen && prev.Mtime == mtime && prev.Sha == sha {
			continue
		}

		pending = append(pending, Brief{
			Name:  briefName,
			Path:  path,
			Mtime: mtime,
			Sha:   sha,
		})
	}

	sort.Slice(pending, func(i, j int) bool {
		return pending[i].Name < pending[j].Name
	})

	// Return only one brief per call so each fire handles a single
	// brief. With three or more pending, processing them all in one
	// run lets the model accumulate a large tool-call history
	// (read_file, bash, etc.) and the next API call times out. One
	// brief per cycle keeps the session compact and recovers cleanly
	// from any individual failure.
	if len(pending) > 1 {
		pending = pending[:1]
	}

	return pending, nil
}

// MarkDone updates the state file to record that the brief with the
// given name has been processed. The mtime and sha should be the
// values Check returned for the brief; on the next Check call the
// same values will match and the brief will be skipped.
//
// This is intended to be called by the host process (main.go) AFTER
// the LLM has produced its output, NOT by the model itself. Letting
// the model write the state is unreliable: it wraps entries in
// arbitrary outer keys, uses the wrong field names, or simply
// forgets — every error mode the brief-watcher's earlier state-file
// runs exhibited.
//
// The state is written atomically via a temp file + rename so a
// crash mid-write can't corrupt the state file. A missing or
// malformed existing state file is treated as empty; only the entry
// for `name` is written (other entries are preserved).
func MarkDone(statePath, name string, mtime int64, sha string) error {
	state := processedState{}
	if data, err := os.ReadFile(statePath); err == nil {
		// Tolerate malformed state — start fresh rather than refuse to
		// update. The next Check will re-process anything that was in
		// the corrupted state.
		_ = json.Unmarshal(data, &state)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading state file %s: %w", statePath, err)
	}

	state[name] = struct {
		Mtime int64  `json:"mtime"`
		Sha   string `json:"sha"`
	}{Mtime: mtime, Sha: sha}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling state: %w", err)
	}
	tmpPath := statePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	return os.Rename(tmpPath, statePath)
}