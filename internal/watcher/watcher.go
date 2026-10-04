// Package watcher implements a polling-based file watcher used by jobs
// scheduled as @watch in the agent config. It tracks per-path sha256
// checksums, dispatches when a file's content changes, and debounces
// bursts so a noisy editor save doesn't fire the job multiple times.
//
// Checksums are used (rather than mtime + size) so that the watcher
// ignores metadata-only changes (`touch`, `cp` of the same content,
// mtime skew from NFS) and reliably catches content-only changes
// inside the same second (mtime granularity on some filesystems is
// 1–2s). The cost is one read per watched file per tick; for briefs
// in /inbox/briefs that's a few KB — negligible.
//
// Polling (rather than fsnotify or another event-driven backend) keeps
// this package portable, dependency-free, and easy to reason about.
// Latency is bounded by Interval (default 1 second); for a busy
// system watching many directories, drop the Interval to taste.
package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Watch is one file watch. The callback fires after Debounce has
// elapsed with the unique paths that changed during the debounce
// window. Paths are matched against Pattern relative to Dir (a
// shell glob like "*.md").
type Watch struct {
	Dir      string
	Pattern  string
	Debounce time.Duration
	OnChange func(paths []string)

	mu      sync.Mutex
	states  map[string]fileState // path → last seen sha256 hex
	pending []string            // paths accumulated for the current debounce window
	timer   *time.Timer          // pending debounce timer; nil when idle
}

type fileState struct {
	sha string // hex-encoded sha256 of the file's content
}

// Watcher polls all registered watches at a fixed interval and
// dispatches changes via each watch's callback. Methods are not safe
// for concurrent use after Start.
type Watcher struct {
	Interval time.Duration

	mu      sync.Mutex
	watches []*Watch
	stopped chan struct{}
	done    chan struct{}
}

// New creates a watcher that polls every interval. If interval is zero,
// defaults to 1 second.
func New(interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = time.Second
	}
	return &Watcher{
		Interval: interval,
		stopped:  make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Add registers a watch. Add must be called before Start. If Debounce
// is zero, defaults to 200ms. If OnChange is nil, a no-op is used.
func (w *Watcher) Add(watch *Watch) {
	if watch.Debounce == 0 {
		watch.Debounce = 200 * time.Millisecond
	}
	if watch.OnChange == nil {
		watch.OnChange = func([]string) {}
	}
	if watch.states == nil {
		watch.states = make(map[string]fileState)
	}
	w.mu.Lock()
	w.watches = append(w.watches, watch)
	w.mu.Unlock()
}

// Start begins polling. It returns immediately; polling runs until
// Stop is called or ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	go w.loop(ctx)
}

func (w *Watcher) loop(ctx context.Context) {
	defer close(w.done)
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopped:
			return
		case <-t.C:
			w.tick()
		}
	}
}

// Stop cancels polling and waits for the loop to exit.
func (w *Watcher) Stop() {
	close(w.stopped)
	<-w.done
}

func (w *Watcher) tick() {
	w.mu.Lock()
	watches := append([]*Watch(nil), w.watches...)
	w.mu.Unlock()

	for _, watch := range watches {
		w.tickOne(watch)
	}
}

func (w *Watcher) tickOne(watch *Watch) {
	// Resolve the glob against the directory. An absolute glob (one that
	// starts with /) uses its directory as the watch root; a relative
	// glob is resolved relative to Dir.
	var watchDir, pattern string
	if filepath.IsAbs(watch.Pattern) {
		watchDir = filepath.Dir(watch.Pattern)
		pattern = filepath.Base(watch.Pattern)
	} else {
		watchDir = watch.Dir
		pattern = watch.Pattern
	}

	matches, err := filepath.Glob(filepath.Join(watchDir, pattern))
	if err != nil {
		return
	}

	watch.mu.Lock()
	defer watch.mu.Unlock()

	seen := make(map[string]bool, len(matches))
	var changed []string
	for _, p := range matches {
		seen[p] = true
		sha, err := hashFile(p)
		if err != nil {
			continue
		}
		cur := fileState{sha: sha}
		if prev, ok := watch.states[p]; !ok || prev != cur {
			changed = append(changed, p)
			watch.states[p] = cur
		}
	}

	// Drop state for files that no longer exist.
	for p := range watch.states {
		if !seen[p] {
			delete(watch.states, p)
		}
	}

	if len(changed) > 0 {
		sort.Strings(changed)
		watch.scheduleCallback(changed, watch.Debounce)
	}
}

func (w *Watch) scheduleCallback(paths []string, debounce time.Duration) {
	w.pending = append(w.pending, paths...)
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(debounce, func() {
		w.mu.Lock()
		batch := w.pending
		w.pending = nil
		w.timer = nil
		cb := w.OnChange
		w.mu.Unlock()
		if cb != nil && len(batch) > 0 {
			cb(batch)
		}
	})
}
// hashFile returns the lowercase hex-encoded sha256 of the file's content.
// On any error (read failure, etc.) it returns an empty string and the
// error; callers skip the file in that case.
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
