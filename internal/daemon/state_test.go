package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A saved State must round-trip through the file unchanged: status reads back
// exactly what the daemon wrote (phase, start time, restart count, last error).
func TestState_SaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := State{
		Phase:        PhaseRunning,
		StartedAt:    time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC),
		RestartCount: 3,
		LastError:    "boom",
	}

	if err := SaveState(path, want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.Phase != want.Phase {
		t.Errorf("Phase = %q, want %q", got.Phase, want.Phase)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, want.StartedAt)
	}
	if got.RestartCount != want.RestartCount {
		t.Errorf("RestartCount = %d, want %d", got.RestartCount, want.RestartCount)
	}
	if got.LastError != want.LastError {
		t.Errorf("LastError = %q, want %q", got.LastError, want.LastError)
	}
}

// A corrupt state file must be reported as a wrapped error, never panic — a
// torn write or hand-edit cannot be allowed to crash status.
func TestState_LoadCorruptFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seeding corrupt file: %v", err)
	}
	if _, err := LoadState(path); err == nil {
		t.Fatal("expected an error loading a corrupt state file, got nil")
	}
}

// A missing state file is reported as an error (the daemon writes it on start),
// so status can distinguish "never started" from "running". The error must
// satisfy errors.Is(os.ErrNotExist) — the status command relies on that to map
// a never-started daemon to "stopped" rather than crashing.
func TestState_LoadMissingFileIsNotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.json")
	_, err := LoadState(path)
	if err == nil {
		t.Fatal("expected an error loading a missing state file, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("LoadState of a missing file: err = %v, want errors.Is(os.ErrNotExist)", err)
	}
}
