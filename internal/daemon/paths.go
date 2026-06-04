// Package daemon is the control plane for the per-cluster background tunnel
// (ADR-0009): it spawns a detached daemon, tracks it through a PID file and a
// state file, and inspects it for status — all file-and-signal based, no socket.
//
// The testable logic — state I/O, path layout, PID liveness, status rendering —
// is split into separate files from the OS-level re-exec/detach (run.go), which
// is integration-only and not unit-tested. The daemon body itself (resolving
// config, running the supervisor) lives in the cli package's __daemon command,
// which reuses the same runTunnel assembly as `up --foreground`.
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
)

// Paths resolves the per-cluster control files under a base directory. The base
// is injectable so tests point it at t.TempDir() and never touch the operator's
// real config dir; production uses DefaultBase (the config-dir convention).
type Paths struct {
	dir string
}

// NewPaths returns the Paths for clusterKey under base. The cluster key is
// sanitized to a single path element so a key containing separators (a kube
// context like "ctx/sub") cannot escape the daemons directory.
func NewPaths(base, clusterKey string) Paths {
	return Paths{dir: filepath.Join(base, "daemons", sanitizeKey(clusterKey))}
}

// Dir is the per-cluster directory holding this cluster's control files.
func (p Paths) Dir() string { return p.dir }

// State is the path to the cluster's daemon state file (phase, started-at, …).
func (p Paths) State() string { return filepath.Join(p.dir, "state.json") }

// PID is the path to the cluster's PID file, written by the parent after spawn.
func (p Paths) PID() string { return filepath.Join(p.dir, "daemon.pid") }

// Log is the path the detached daemon's stdout/stderr are redirected to, since
// nothing is on a terminal once it is detached (ADR-0009).
func (p Paths) Log() string { return filepath.Join(p.dir, "daemon.log") }

// DefaultBase mirrors the config-dir convention (store/config DefaultPath) so
// the daemon files sit beside config.yaml under the user config dir
// (e.g. ~/.config/kubectl-oke-bastion on Linux).
func DefaultBase() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating user config dir: %w", err)
	}
	return filepath.Join(dir, "kubectl-oke-bastion"), nil
}

// sanitizeKey flattens a cluster key into one filesystem-safe path element:
// path separators become underscores so the key can never escape its parent
// dir, and an empty key falls back to a fixed name so the path stays valid.
func sanitizeKey(key string) string {
	const repl = '_'
	out := make([]rune, 0, len(key))
	for _, r := range key {
		switch r {
		case '/', '\\', ':':
			out = append(out, repl)
		default:
			out = append(out, r)
		}
	}
	s := string(out)
	if s == "" || s == "." || s == ".." {
		return "default"
	}
	return s
}
