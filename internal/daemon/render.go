package daemon

import (
	"fmt"
	"strings"
	"time"
)

// RenderStatus formats what the status command prints for one cluster. It is a
// pure function of the loaded state, the liveness verdict, and the current time
// so it can be unit-tested directly (now is passed in, not read from the wall
// clock, so the rendered time-remaining is deterministic). Liveness wins over
// the file: a state that says running but whose PID is dead (a stale PID file)
// renders as stopped, since the file is only as fresh as the daemon's last
// write.
//
// The rendered fields are status, phase, started-at, restart count, the active
// tunnel's local port (when set), the time remaining before the session's TTL
// deadline (when a future expiry is known), and last error (when set).
func RenderStatus(s State, running bool, now time.Time) string {
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
	// Time-remaining = expiry − now, clamped to ≥0 and omitted when unknown or
	// already past (a non-positive remaining is not meaningful to show).
	if !s.SessionExpiry.IsZero() {
		if remaining := s.SessionExpiry.Sub(now); remaining > 0 {
			fmt.Fprintf(&b, "time remaining: %s\n", remaining.Round(time.Second))
		}
	}
	if s.LastError != "" {
		fmt.Fprintf(&b, "last error: %s\n", s.LastError)
	}
	return b.String()
}
