package daemon

import (
	"fmt"
	"strings"
	"time"
)

// RenderStatus formats what the status command prints for one cluster. It is a
// pure function of the loaded state and the liveness verdict so it can be
// unit-tested directly. Liveness wins over the file: a state that says running
// but whose PID is dead (a stale PID file) renders as stopped, since the file is
// only as fresh as the daemon's last write.
//
// The rendered fields are exactly what this slice tracks — status, phase,
// started-at, restart count, the active tunnel's local port (when set), and
// last error (when set). Time-remaining lands in Slice E once the session
// deadline exists.
func RenderStatus(s State, running bool) string {
	if !running {
		return "status: stopped\n"
	}
	var b strings.Builder
	fmt.Fprint(&b, "status:   running\n")
	fmt.Fprintf(&b, "phase:    %s\n", s.Phase)
	fmt.Fprintf(&b, "started:  %s\n", s.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "restarts: %d\n", s.RestartCount)
	if s.LocalPort != 0 {
		fmt.Fprintf(&b, "local port: %d\n", s.LocalPort)
	}
	if s.LastError != "" {
		fmt.Fprintf(&b, "last error: %s\n", s.LastError)
	}
	return b.String()
}
