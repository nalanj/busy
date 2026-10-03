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

	return pending, nil
}