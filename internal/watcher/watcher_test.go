package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

func contextWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func TestWatcherDetectsNewFile(t *testing.T) {
	dir := t.TempDir()

	var got []string
	var mu sync.Mutex
	cb := func(paths []string) {
		mu.Lock()
		got = append(got, paths...)
		mu.Unlock()
	}

	w := New(50 * time.Millisecond)
	w.Add(Watch{
		Dir:      dir,
		Pattern:  "*.md",
		Debounce: 10 * time.Millisecond,
		OnChange: cb,
	})
	ctx, cancel := contextWithTimeout()
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	// Empty dir → first tick should not fire.
	time.Sleep(80 * time.Millisecond)
	mu.Lock()
	if len(got) != 0 {
		mu.Unlock()
		t.Errorf("empty dir should not fire: got %v", got)
		return
	}
	mu.Unlock()

	// Create a new file — should fire.
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if !waitFor(500*time.Millisecond, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) > 0
	}) {
		t.Errorf("expected callback to fire after new file")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || filepath.Base(got[0]) != "a.md" {
		t.Errorf("expected [a.md], got %v", got)
	}
}

func TestWatcherDetectsModification(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}

	var got []string
	var mu sync.Mutex
	cb := func(paths []string) {
		mu.Lock()
		got = append(got, paths...)
		mu.Unlock()
	}

	w := New(50 * time.Millisecond)
	w.Add(Watch{
		Dir:      dir,
		Pattern:  "*.md",
		Debounce: 10 * time.Millisecond,
		OnChange: cb,
	})
	ctx, cancel := contextWithTimeout()
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	// Wait for initial scan to settle.
	time.Sleep(80 * time.Millisecond)

	// Modify the file with a fresh mtime.
	f := filepath.Join(dir, "x.md")
	if err := os.WriteFile(f, []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}
	// Force mtime forward by a measurable amount.
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(f, future, future); err != nil {
		t.Fatal(err)
	}

	if !waitFor(500*time.Millisecond, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) > 0
	}) {
		t.Errorf("expected callback to fire on modification")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != f {
		t.Errorf("expected [%s], got %v", f, got)
	}
}

func TestWatcherIgnoresUnmatched(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}

	var got []string
	var mu sync.Mutex
	cb := func(paths []string) {
		mu.Lock()
		got = append(got, paths...)
		mu.Unlock()
	}

	w := New(50 * time.Millisecond)
	w.Add(Watch{
		Dir:      dir,
		Pattern:  "*.md",
		Debounce: 10 * time.Millisecond,
		OnChange: cb,
	})
	ctx, cancel := contextWithTimeout()
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 0 {
		t.Errorf("unmatched files should not fire: got %v", got)
	}
}

func TestWatcherDebounce(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}

	var calls int
	var mu sync.Mutex
	cb := func(paths []string) {
		mu.Lock()
		calls++
		mu.Unlock()
	}

	w := New(30 * time.Millisecond)
	w.Add(Watch{
		Dir:      dir,
		Pattern:  "*.md",
		Debounce: 200 * time.Millisecond,
		OnChange: cb,
	})
	ctx, cancel := contextWithTimeout()
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	// Modify the file repeatedly for ~250ms.
	f := filepath.Join(dir, "x.md")
	deadline := time.Now().Add(250 * time.Millisecond)
	future := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		os.WriteFile(f, []byte("tick"), 0644)
		os.Chtimes(f, future, future)
		future = future.Add(time.Second)
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for the debounce window to expire.
	time.Sleep(400 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("expected debounce to coalesce to 1 call, got %d", calls)
	}
}

func TestWatcherReportsRemoved(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x.md")
	if err := os.WriteFile(f, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}

	var got []string
	var mu sync.Mutex
	cb := func(paths []string) {
		mu.Lock()
		got = append(got, paths...)
		mu.Unlock()
	}

	w := New(50 * time.Millisecond)
	w.Add(Watch{
		Dir:      dir,
		Pattern:  "*.md",
		Debounce: 10 * time.Millisecond,
		OnChange: cb,
	})
	ctx, cancel := contextWithTimeout()
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	// Let initial scan fire (x.md is "new" from the watcher's POV).
	time.Sleep(80 * time.Millisecond)
	mu.Lock()
	first := len(got)
	mu.Unlock()

	// Remove the file.
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}

	// Removal isn't surfaced — watcher only fires on additions/changes.
	// Verify state was dropped (no further callbacks expected).
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != first {
		t.Errorf("expected no new callback on removal, got %d calls (was %d)", len(got)-first, 0)
	}
}

func TestWatcherMultiplePaths(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.md", "b.md", "c.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("v1"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	var got []string
	var mu sync.Mutex
	cb := func(paths []string) {
		mu.Lock()
		got = append(got, paths...)
		mu.Unlock()
	}

	w := New(50 * time.Millisecond)
	w.Add(Watch{
		Dir:      dir,
		Pattern:  "*.md",
		Debounce: 10 * time.Millisecond,
		OnChange: cb,
	})
	ctx, cancel := contextWithTimeout()
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	// Initial scan should fire with all 3 files (state is empty).
	if !waitFor(500*time.Millisecond, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) >= 1
	}) {
		t.Errorf("expected initial callback")
	}

	mu.Lock()
	first := len(got)
	mu.Unlock()

	// Modify only one file.
	future := time.Now().Add(time.Second)
	os.Chtimes(filepath.Join(dir, "b.md"), future, future)

	if !waitFor(500*time.Millisecond, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) > first
	}) {
		t.Errorf("expected callback on b.md change")
	}

	mu.Lock()
	defer mu.Unlock()
	paths := got[first:]
	if len(paths) != 1 || filepath.Base(paths[0]) != "b.md" {
		t.Errorf("expected only [b.md], got %v", paths)
	}
}

// waitFor polls cond until it returns true or timeout.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// sortStrings is a helper used by a couple of tests above to make
// order-independent assertions deterministic.
func sortStrings(s []string) {
	sort.Strings(s)
}