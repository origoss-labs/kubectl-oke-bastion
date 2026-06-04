package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/daemon"
)

// fakeWiring records the calls the stateWiring delegates to it, with no
// kubeconfig I/O, so the state-updating wrapper is testable in isolation.
type fakeWiring struct {
	wiredPort int
	unwired   bool
	wireErr   error
}

func (f *fakeWiring) Wire(localPort int) error {
	f.wiredPort = localPort
	return f.wireErr
}

func (f *fakeWiring) Unwire() error {
	f.unwired = true
	return nil
}

// stateWiring.Wire must delegate to the inner wiring AND write active+port to
// the state file, so the real -bastion context is wired while status reflects
// the live tunnel.
func TestStateWiring_WireWritesActiveAndDelegates(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	inner := &fakeWiring{}
	started := time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC)
	w := &stateWiring{inner: inner, statePath: statePath, startedAt: started, out: io.Discard}

	if err := w.Wire(51234); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	if inner.wiredPort != 51234 {
		t.Errorf("inner wiring got port %d, want 51234 (not delegated)", inner.wiredPort)
	}

	got, err := daemon.LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.Phase != daemon.PhaseActive {
		t.Errorf("Phase = %q, want %q", got.Phase, daemon.PhaseActive)
	}
	if got.LocalPort != 51234 {
		t.Errorf("LocalPort = %d, want 51234", got.LocalPort)
	}
	if !got.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, started)
	}
}

// stateWiring.Unwire must mark the state stopped AND delegate to the inner
// wiring, so teardown unwires the real -bastion context and status then reads
// stopped.
func TestStateWiring_UnwireMarksStoppedAndDelegates(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	inner := &fakeWiring{}
	w := &stateWiring{inner: inner, statePath: statePath, startedAt: time.Now(), out: io.Discard}

	if err := w.Wire(40000); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	if err := w.Unwire(); err != nil {
		t.Fatalf("Unwire: %v", err)
	}
	if !inner.unwired {
		t.Error("inner wiring Unwire was not delegated to")
	}
	got, err := daemon.LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.Phase != daemon.PhaseStopped {
		t.Errorf("Phase = %q, want %q", got.Phase, daemon.PhaseStopped)
	}
}

// A state-file write failure (owner-only status plumbing) must never veto the
// load-bearing wire: Wire still delegates to inner, returns no error, and logs
// the failure to out. State is allowed to be stale, never to strand a healthy
// tunnel.
func TestStateWiring_WireSurvivesStateWriteFailure(t *testing.T) {
	// Make SaveState fail deterministically: point the state path's parent dir at
	// an existing regular file, so SaveState's MkdirAll of the dir errors.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seeding blocker file: %v", err)
	}
	statePath := filepath.Join(blocker, "state.json") // parent is a file → MkdirAll fails

	inner := &fakeWiring{}
	var logged bytes.Buffer
	w := &stateWiring{inner: inner, statePath: statePath, startedAt: time.Now(), out: &logged}

	if err := w.Wire(7777); err != nil {
		t.Fatalf("Wire must not error on a state-write failure, got: %v", err)
	}
	if inner.wiredPort != 7777 {
		t.Errorf("inner wiring got port %d, want 7777: the wire was skipped", inner.wiredPort)
	}
	if logged.Len() == 0 {
		t.Error("expected the state-write failure to be logged to out")
	}
}
