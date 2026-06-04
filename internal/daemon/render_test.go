package daemon

import (
	"strings"
	"testing"
	"time"
)

func TestRenderStatus(t *testing.T) {
	started := time.Date(2026, 6, 4, 9, 30, 0, 0, time.UTC)

	cases := []struct {
		name    string
		state   State
		running bool
		// substrings the rendering must contain; the renderer is a pure
		// function so we assert observable content, not exact layout.
		want []string
		// substrings the rendering must NOT contain.
		absent []string
	}{
		{
			name:    "running daemon reports running and its facts",
			state:   State{Phase: PhaseRunning, StartedAt: started, RestartCount: 2},
			running: true,
			want:    []string{"running", string(PhaseRunning), "2", "2026-06-04"},
		},
		{
			// An active tunnel surfaces its local loopback port so the operator
			// can point kubectl at it without re-deriving the -bastion context.
			name:    "active tunnel reports its local port",
			state:   State{Phase: PhaseActive, StartedAt: started, LocalPort: 49231},
			running: true,
			want:    []string{"running", string(PhaseActive), "49231"},
		},
		{
			name:    "live but errored daemon shows the last error",
			state:   State{Phase: PhaseRunning, StartedAt: started, LastError: "redial failed"},
			running: true,
			want:    []string{"running", "redial failed"},
		},
		{
			name:    "no last error omits the error line",
			state:   State{Phase: PhaseRunning, StartedAt: started},
			running: true,
			absent:  []string{"last error", "error:"},
		},
		{
			// A state file says running but the PID is dead → stale: status
			// must report stopped/not-running, trusting liveness over the file.
			name:    "stale PID reads as not running despite a running-phase state",
			state:   State{Phase: PhaseRunning, StartedAt: started},
			running: false,
			want:    []string{"stopped"},
			absent:  []string{"running"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderStatus(tc.state, tc.running)
			low := strings.ToLower(got)
			for _, w := range tc.want {
				if !strings.Contains(low, strings.ToLower(w)) {
					t.Errorf("rendering %q missing %q", got, w)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(low, strings.ToLower(a)) {
					t.Errorf("rendering %q should not contain %q", got, a)
				}
			}
		})
	}
}
