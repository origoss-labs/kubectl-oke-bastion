package daemon

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The PID file must round-trip: status/down read back exactly the pid the
// parent wrote after spawning the daemon.
func TestPID_WriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.pid")
	if err := WritePID(path, 4242); err != nil {
		t.Fatalf("WritePID: %v", err)
	}
	got, err := ReadPID(path)
	if err != nil {
		t.Fatalf("ReadPID: %v", err)
	}
	if got != 4242 {
		t.Errorf("ReadPID = %d, want 4242", got)
	}
}

// A missing PID file must be distinguishable (os.ErrNotExist) so callers treat
// "no daemon" as not-running rather than a hard error.
func TestPID_ReadMissingIsNotExist(t *testing.T) {
	_, err := ReadPID(filepath.Join(t.TempDir(), "absent.pid"))
	if !os.IsNotExist(err) {
		t.Errorf("ReadPID of a missing file: err = %v, want an IsNotExist error", err)
	}
}

// A garbage PID file must error, not return a bogus pid that down might signal.
func TestPID_ReadGarbageErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.pid")
	if err := os.WriteFile(path, []byte("not-a-number"), 0o600); err != nil {
		t.Fatalf("seeding garbage: %v", err)
	}
	if _, err := ReadPID(path); err == nil {
		t.Fatal("expected an error reading a garbage PID file, got nil")
	}
}

// Running delegates to an injected signal probe so liveness is unit-testable
// without spawning processes: a nil error means the process exists and is
// signalable (alive); any error means not-running.
func TestRunning_InjectedProbe(t *testing.T) {
	cases := []struct {
		name      string
		probeErr  error
		wantAlive bool
	}{
		{"alive when probe succeeds", nil, true},
		{"dead when probe errors", syscall.ESRCH, false},
		{"not-running on permission/other error", syscall.EPERM, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := func(int) error { return tc.probeErr }
			if got := Running(1234, probe); got != tc.wantAlive {
				t.Errorf("Running = %v, want %v", got, tc.wantAlive)
			}
		})
	}
}

// The default probe (no injection) must read the current process as alive and a
// PID that cannot exist as not-running, proving the real syscall.Kill(pid,0)
// wiring works end to end.
func TestRunning_DefaultProbeAgainstRealProcesses(t *testing.T) {
	if !Running(os.Getpid(), nil) {
		t.Error("Running(self) = false, want true — the current process is alive")
	}
	// PID 0 means "this process group" to kill(2), never an arbitrary process,
	// and a huge PID will not be allocated; both must read not-running.
	if Running(0, nil) {
		t.Error("Running(0) = true, want false")
	}
	if Running(1<<30, nil) {
		t.Error("Running(huge pid) = true, want false")
	}
}
