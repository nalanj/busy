package briefwatcher

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckEmptyInbox(t *testing.T) {
	got, err := Check(t.TempDir(), filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected no pending briefs, got %d", len(got))
	}
}

func TestCheckMissingInbox(t *testing.T) {
	got, err := Check("/no/such/dir", filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Errorf("missing inbox should not be an error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no pending briefs, got %d", len(got))
	}
}

func TestCheckMissingState(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.md"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := Check(dir, filepath.Join(t.TempDir(), "no-such-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 pending brief (no state), got %d", len(got))
	}
	if got[0].Name != "alpha" {
		t.Errorf("expected name 'alpha', got %q", got[0].Name)
	}
	if got[0].Sha == "" {
		t.Error("expected non-empty sha")
	}
}

func TestCheckUnchangedBriefs(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(filepath.Join(dir, "alpha.md"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	// First check: pending.
	got, err := Check(dir, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("first check: expected 1 pending, got %d", len(got))
	}
	// Write a state file that matches and re-check: nothing pending.
	info, _ := os.Stat(filepath.Join(dir, "alpha.md"))
	state := `{"alpha":{"mtime":` + itoa(info.ModTime().Unix()) + `,"sha":"` + got[0].Sha + `"}}` + "\n"
	if err := os.WriteFile(statePath, []byte(state), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = Check(dir, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("unchanged brief: expected 0 pending, got %d", len(got))
	}
}

func TestCheckChangedBriefs(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(filepath.Join(dir, "alpha.md"), []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := Check(dir, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("first check: expected 1 pending, got %d", len(got))
	}
	// Save state, then modify the brief.
	info, _ := os.Stat(filepath.Join(dir, "alpha.md"))
	state := `{"alpha":{"mtime":` + itoa(info.ModTime().Unix()) + `,"sha":"` + got[0].Sha + `"}}` + "\n"
	if err := os.WriteFile(statePath, []byte(state), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha.md"), []byte("v2 — different content"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = Check(dir, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("changed brief: expected 1 pending, got %d", len(got))
	}
}

func TestCheckIgnoresDotfilesAndNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".hidden.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := Check(dir, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "real" {
		t.Errorf("expected only real.md, got %+v", got)
	}
}

// itoa is a tiny strconv replacement to avoid the import in tests.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}