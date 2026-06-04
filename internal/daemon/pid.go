package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// WritePID records pid in the PID file at path, written atomically (temp +
// rename) and dir-created like the other control files. The parent writes this
// right after spawning the daemon so status/down can find the process.
func WritePID(path string, pid int) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating daemon pid dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".pid-*")
	if err != nil {
		return fmt.Errorf("creating daemon pid temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds
	if _, err := tmp.WriteString(strconv.Itoa(pid) + "\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing daemon pid temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting daemon pid permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing daemon pid temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing daemon pid %s: %w", path, err)
	}
	return nil
}

// ReadPID returns the pid recorded at path. A missing file is reported as an
// os.IsNotExist error so callers can map "no PID file" to not-running rather
// than a hard failure; a non-numeric file is a wrapped error, never a bogus pid
// that down might signal.
func ReadPID(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err // preserve os.ErrNotExist for os.IsNotExist callers
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, fmt.Errorf("daemon pid file %s is not a number: %w", path, err)
	}
	return pid, nil
}

// RemovePID deletes the PID file. A missing file is not an error — the daemon
// removes it on exit and a redundant cleanup must stay quiet.
func RemovePID(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing daemon pid %s: %w", path, err)
	}
	return nil
}

// Running reports whether the process pid is alive and signalable by us. On
// unix, sending signal 0 with kill(2) performs the existence/permission check
// without actually delivering a signal. The probe is injectable so the decision
// logic is unit-testable without real processes; pass nil to use the real
// syscall. Any probe error (no such process, or not signalable) reads as
// not-running — a daemon we spawned is always ours to signal, so the only error
// that matters in practice is "gone", which is exactly the stale-PID case.
// Note this deliberately departs from the conventional kill(2,0) idiom, which
// treats EPERM (process exists but owned by another user) as alive: a recycled
// PID owned by someone else is not our daemon, so reading it as not-running is
// the safer verdict for this same-uid control plane.
func Running(pid int, probe func(pid int) error) bool {
	if pid <= 0 {
		return false // 0/-1 address process groups, never a single daemon
	}
	if probe == nil {
		probe = func(p int) error { return syscall.Kill(p, 0) }
	}
	return probe(pid) == nil
}
