//go:build unix

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// daemonArg is the hidden argv the parent re-execs itself with to enter the
// daemon run loop. Kept here beside Spawn/Run so the contract between the two
// halves lives in one place.
const daemonArg = "__daemon"

// Spawn re-execs this binary into its hidden __daemon entrypoint as a detached
// background process and records its PID, then returns immediately (ADR-0009).
// It is the integration boundary — OS process creation, setsid, and stdio
// redirection — and is deliberately thin and not unit-tested; the testable
// state/pid/render logic lives in the sibling files.
//
// Detachment is unix-only: Setsid puts the child in a new session with no
// controlling terminal so it survives the parent shell, and its stdout/stderr
// are redirected to the per-cluster log file since nothing is on a terminal.
// extraArgs are appended after the __daemon arg (e.g. the cluster key) so the
// child knows which cluster it serves.
func Spawn(p Paths, extraArgs ...string) error {
	if err := os.MkdirAll(p.Dir(), 0o700); err != nil {
		return fmt.Errorf("creating daemon dir: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating own executable to re-exec: %w", err)
	}
	logFile, err := os.OpenFile(p.Log(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening daemon log %s: %w", p.Log(), err)
	}
	defer func() { _ = logFile.Close() }() // the child dups the fd; the parent's copy is not needed

	cmd := exec.Command(self, append([]string{daemonArg}, extraArgs...)...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // new session: outlives the parent shell

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawning daemon: %w", err)
	}
	if err := WritePID(p.PID(), cmd.Process.Pid); err != nil {
		// The child is already running detached; surface the failure but don't
		// orphan it silently — the caller can re-run down by PID from the log.
		return fmt.Errorf("recording daemon pid (daemon started as %d): %w", cmd.Process.Pid, err)
	}
	// Release the child so it is reparented to init rather than becoming a
	// zombie awaiting a Wait this short-lived parent will never make.
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("releasing daemon process: %w", err)
	}
	return nil
}
