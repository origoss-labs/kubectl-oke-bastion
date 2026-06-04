package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Phase is the daemon's coarse lifecycle stage, surfaced by status. It is a
// small closed set of strings so the on-disk value stays human-readable and the
// renderer can print it verbatim.
type Phase string

const (
	// PhaseStarting is written before the tunnel work begins, while the daemon
	// is establishing its session and forward.
	PhaseStarting Phase = "starting"
	// PhaseRunning is the recovering-but-not-yet-active phase. The Slice D daemon
	// goes straight starting→active→stopped and never writes it; it is retained
	// for Slice E, which reports a tunnel rebuilding after a break/expiry as
	// running-not-yet-active. Tests also use it as a sample non-active phase.
	PhaseRunning Phase = "running"
	// PhaseActive means the tunnel is up: a session is open, the forward is
	// listening on LocalPort, and the -bastion context is wired to it.
	PhaseActive Phase = "active"
	// PhaseStopped means the daemon exited cleanly on a signal.
	PhaseStopped Phase = "stopped"
)

// State is the daemon's self-reported status, written by the daemon and read by
// the status command. It is owner-only plumbing, not a secret. StartedAt is a
// time.Time so callers can inject a deterministic value in tests and the
// renderer can format it consistently.
type State struct {
	Phase        Phase     `json:"phase"`
	StartedAt    time.Time `json:"started_at"`
	RestartCount int       `json:"restart_count"`
	LastError    string    `json:"last_error,omitempty"`
	// LocalPort is the loopback port the active tunnel listens on, captured when
	// the supervisor brings the forward up. Zero until the tunnel is active;
	// surfaced by status so the operator sees where the -bastion context points.
	LocalPort int `json:"local_port,omitempty"`
}

// LoadState reads the state file at path. Unlike config.Load, a missing file is
// an error here: the daemon writes the state file on start, so its absence means
// "no daemon ever ran for this cluster" — a distinct condition status reports,
// not a zero State. A file that exists but cannot be decoded is wrapped, never
// a panic, so a torn write or hand-edit can't crash status.
func LoadState(path string) (State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return State{}, fmt.Errorf("reading daemon state %s: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}, fmt.Errorf("daemon state %s is corrupt: %w", path, err)
	}
	return s, nil
}

// SaveState writes s to path atomically (temp file in the same dir + rename),
// matching the config convention so a crash mid-write can never leave a
// truncated file that LoadState would reject. The dir is created 0700 and the
// file 0600.
func SaveState(path string, s State) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding daemon state: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating daemon state dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return fmt.Errorf("creating daemon state temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing daemon state temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting daemon state permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing daemon state temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing daemon state %s: %w", path, err)
	}
	return nil
}
