// Package supervisor runs the reactive self-healing loop that owns a bastion
// tunnel for the lifetime of one `kubectl oke bastion` invocation (ADR-0003,
// ADR-0009). It composes a Builder (sessions + tunnels) and Wiring (the
// -bastion kubeconfig context), watches the live tunnel, and rebuilds it on
// either condition (ADR-0010, supersedes ADR-0006):
//
//   - a break — the forward dropped, signalled event-driven by tun.Wait, or
//   - near-expiry — a periodic deadline check fires when the remaining session
//     TTL drops below a margin before the 3h cap.
//
// Both conditions drive ONE unified rebuild: a new session + a new tunnel on the
// same local port, with the -bastion context left in place (wired once, the
// local port is stable). A failing rebuild retries with a capped backoff so a
// hidden daemon can never hot-loop against a down bastion; the failure is
// recorded as last-error and the restart count is incremented, surfaced via the
// observer so `status` shows them alongside the time-remaining.
package supervisor

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Session is the bastion session a tunnel rides on.
type Session interface {
	// Alive reports whether the session is still usable. Retained from ADR-0006;
	// the unified rebuild loop (ADR-0010) no longer branches on it, but it stays
	// on the interface for diagnostics and to avoid a breaking change.
	Alive(ctx context.Context) bool
	// Deadline is when the session hits OCI's TTL cap. The loop rebuilds before
	// it (proactive rebuild near expiry) and reports it so status can show
	// time-remaining. This is the sanctioned ADR-0010 extension to the role.
	Deadline() time.Time
	// Close deletes the session.
	Close(ctx context.Context) error
}

// Tunnel is the live SSH local port-forward.
type Tunnel interface {
	// Wait blocks until the forward breaks (returns nil) or ctx is cancelled
	// (returns ctx.Err()).
	Wait(ctx context.Context) error
	// Close stops the forward.
	Close() error
	// LocalPort is the loopback port the forward listens on.
	LocalPort() int
}

// Builder creates the pieces the supervisor composes. The production Builder
// wraps the ephemeral key, session.Open, and tunnel.Open; tests fake it.
type Builder interface {
	// NewSession creates and activates a bastion session.
	NewSession(ctx context.Context) (Session, error)
	// OpenTunnel opens a forward over s. localPort is 0 on the first open (the
	// OS assigns a port) and the previously assigned port on every rebuild, so
	// the local endpoint — and the kubeconfig context wired to it — stays stable
	// across rebuilds.
	OpenTunnel(ctx context.Context, s Session, localPort int) (Tunnel, error)
}

// Wiring adds and removes the -bastion kubeconfig context pointing at the
// supervisor's stable local port.
type Wiring interface {
	Wire(localPort int) error
	Unwire() error
}

// Clock is the supervisor's view of time, injected so the deadline arithmetic
// and the periodic tick are deterministic in tests rather than wall-clock-bound.
// The production clock is the real time; tests supply a fake that controls Now
// and hands back a channel they fire to simulate the tick AND the backoff wait.
type Clock interface {
	// Now is the current time, used to compute remaining = deadline - now.
	Now() time.Time
	// After returns a channel that delivers after d, used for both the proactive
	// tick and the rebuild backoff wait.
	After(d time.Duration) <-chan time.Time
}

// realClock is the production Clock backed by the standard library.
type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Phase is the supervisor's coarse stage, carried in a Report so an observer can
// map it without string-matching a literal: a rename of a value is then a
// compile-time concern, not a silent mis-map to active.
type Phase string

const (
	// PhaseActive means the tunnel is up and the -bastion context points at it.
	PhaseActive Phase = "active"
	// PhaseRebuilding means a rebuild is in flight (recovering, not yet active),
	// whether triggered by a break or a near-expiry deadline.
	PhaseRebuilding Phase = "rebuilding"
)

// Report is the supervisor's progress snapshot handed to a WithObserver hook on
// each meaningful transition (active, rebuilding, rebuild failure). It carries
// exactly what the daemon needs to map into its state file — phase, the active
// local port, the session deadline (for time-remaining), the restart count, and
// the last rebuild error — keeping the supervisor decoupled from the daemon
// package (it knows nothing about daemon.State).
type Report struct {
	// Phase is the coarse stage (PhaseActive / PhaseRebuilding) for status.
	Phase Phase
	// LocalPort is the stable loopback port the forward listens on, once known.
	LocalPort int
	// Deadline is the current session's TTL boundary, for time-remaining.
	Deadline time.Time
	// RestartCount is the number of rebuilds attempted, incremented per rebuild.
	RestartCount int
	// LastErr is the most recent rebuild failure, empty once a rebuild succeeds.
	LastErr string
}

// Backoff bounds how long Run waits before retrying a failed rebuild. It rises
// from base, doubling each consecutive failure up to max, so a bastion that
// rejects every rebuild (down NSG, refused 6443, OCI API error) can't hot-spin
// the loop and hammer CreateSession.
const (
	defaultBaseBackoff = 1 * time.Second
	defaultMaxBackoff  = 30 * time.Second
)

// defaultRebuildMargin is how long before the TTL deadline the proactive rebuild
// fires, and defaultTickInterval is how often the loop re-checks the deadline.
// Both are ADR-0010's ~5min margin / ~30s tick. They are package consts (not
// options) to avoid speculative knobs; tests drive the tick via the fake clock's
// fired channel and set deadlines relative to the margin, so neither needs to be
// overridable to be tested without real waits.
const (
	defaultRebuildMargin = 5 * time.Minute
	defaultTickInterval  = 30 * time.Second
)

type options struct {
	baseBackoff time.Duration
	maxBackoff  time.Duration
	clock       Clock
	observe     func(Report)
}

// Option tunes Run. The zero-backoff option exists mainly so tests run fast.
type Option func(*options)

// WithBackoff sets the rebuild backoff floor and ceiling. base <= 0 disables the
// inter-rebuild wait entirely, which removes the only thing throttling the
// failed-rebuild retry: with a zero floor a rebuild that keeps failing
// busy-spins (After(0) fires immediately → retry → fail → spin), hammering
// CreateSession exactly as Run's contract says it must not. Production never
// passes 0 (the cli uses the 1s/30s defaults); reserve a zero floor for tests
// whose rebuilds SUCCEED (so the failed-rebuild branch is never entered).
func WithBackoff(base, max time.Duration) Option {
	return func(o *options) { o.baseBackoff, o.maxBackoff = base, max }
}

// WithClock injects the time source for the deadline check, the proactive tick,
// and the backoff wait. It is variadic-Option (non-breaking): production omits
// it and gets the real clock; tests pass a fake so time-based behaviour is
// instant and deterministic.
func WithClock(c Clock) Option { return func(o *options) { o.clock = c } }

// WithObserver registers a hook the supervisor calls with a Report on each
// transition. It is variadic-Option (non-breaking) and keeps the supervisor
// decoupled from the daemon: the cli path passes an observer that maps the
// Report into daemon.State and SaveState's it, so status sees restart-count,
// last-error, and time-remaining without the supervisor importing daemon.
func WithObserver(fn func(Report)) Option { return func(o *options) { o.observe = fn } }

// Run brings up the tunnel and holds it, self-healing on breaks and expiry,
// until ctx is cancelled or the initial bring-up fails. On return it tears down
// exactly once: removes the -bastion context, closes the tunnel, deletes the
// session.
func Run(ctx context.Context, b Builder, w Wiring, out io.Writer, opts ...Option) (err error) {
	o := options{baseBackoff: defaultBaseBackoff, maxBackoff: defaultMaxBackoff, clock: realClock{}}
	for _, fn := range opts {
		fn(&o)
	}
	var (
		sess         Session
		tun          Tunnel
		localPort    int
		wired        bool
		restartCount int
		lastErr      string
	)
	report := func(phase Phase) {
		if o.observe == nil {
			return
		}
		var dl time.Time
		if sess != nil {
			dl = sess.Deadline()
		}
		o.observe(Report{
			Phase:        phase,
			LocalPort:    localPort,
			Deadline:     dl,
			RestartCount: restartCount,
			LastErr:      lastErr,
		})
	}
	defer func() {
		if wired {
			if uerr := w.Unwire(); uerr != nil && err == nil {
				err = fmt.Errorf("removing -bastion context: %w", uerr)
			}
		}
		if tun != nil {
			_ = tun.Close()
		}
		if sess != nil {
			delCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = sess.Close(delCtx)
		}
	}()

	// Initial bring-up. A failure here (before the context is wired) stops the
	// loop with the error rather than spinning — the tunnel's own dial-retry has
	// already had its chance, and there is nothing for the operator to use yet.
	status(out, "connecting", "establishing bastion tunnel")
	sess, err = b.NewSession(ctx)
	if err != nil {
		return err
	}
	tun, err = b.OpenTunnel(ctx, sess, localPort)
	if err != nil {
		return err
	}
	localPort = tun.LocalPort()
	if err = w.Wire(localPort); err != nil {
		return err
	}
	wired = true
	status(out, "active", fmt.Sprintf("tunnel up on 127.0.0.1:%d", localPort))
	report(PhaseActive)

	// One unified select drives everything: while holding a tunnel it watches the
	// break channel, and after a failed rebuild it watches a backoff timer
	// instead — but ctx.Done and the proactive tick are always live, so a cancel
	// or a near-expiry deadline preempts both a steady tunnel and a backoff wait.
	// Routing the backoff through the same select (rather than a nested loop)
	// means there is never more than one outstanding clock timer of each kind,
	// which keeps the loop simple and the fake-clock tests unambiguous.
	tick := o.clock.After(defaultTickInterval)
	backoff := o.baseBackoff
	var (
		breakCh    chan struct{}      // non-nil while holding a live tunnel
		backoffCh  <-chan time.Time   // non-nil while waiting out a rebuild backoff
		waitCancel context.CancelFunc // cancels the current tun.Wait goroutine
		waitDone   chan struct{}      // closed when that goroutine has exited
	)

	// watch starts the goroutine that turns a tunnel break into a breakCh signal.
	// It captures the tunnel by value (not the shared tun variable) and closes
	// waitDone on exit, so stopWatch can join it before any rebuild mutates tun —
	// no concurrent read of tun while rebuild reassigns it.
	watch := func(t Tunnel) {
		ch := make(chan struct{}, 1)
		done := make(chan struct{})
		breakCh, waitDone = ch, done
		var waitCtx context.Context
		waitCtx, waitCancel = context.WithCancel(ctx)
		go func() {
			defer close(done)
			_ = t.Wait(waitCtx)
			if waitCtx.Err() == nil {
				ch <- struct{}{} // a real break, not our own rebuild cancel
			}
		}()
	}

	// stopWatch cancels the current watch goroutine and waits for it to exit, so
	// the loop never touches tun while the goroutine still reads it.
	stopWatch := func() {
		if waitCancel != nil {
			waitCancel()
		}
		if waitDone != nil {
			<-waitDone
			waitDone = nil
		}
		breakCh = nil
	}

	// rebuild does one rebuild attempt: drop the old tunnel+session, make a new
	// session and a new tunnel on the same local port (the -bastion context is
	// left wired). On success it resumes watching; on failure it records the
	// error and arms the capped backoff so the next attempt waits, never spins.
	rebuild := func() {
		restartCount++
		if tun != nil {
			_ = tun.Close()
			tun = nil
		}
		if sess != nil {
			delCtx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = sess.Close(delCtx)
			dcancel()
			sess = nil
		}
		var rerr error
		sess, rerr = b.NewSession(ctx)
		if rerr == nil {
			tun, rerr = b.OpenTunnel(ctx, sess, localPort)
		}
		if rerr != nil {
			lastErr = rerr.Error()
			report(PhaseRebuilding)
			status(out, "reconnecting", fmt.Sprintf("rebuild failed: %v; retrying", rerr))
			backoffCh = o.clock.After(backoff)
			if backoff *= 2; backoff > o.maxBackoff {
				backoff = o.maxBackoff
			}
			return
		}
		lastErr = ""
		backoff = o.baseBackoff
		status(out, "active", fmt.Sprintf("tunnel up on 127.0.0.1:%d", localPort))
		report(PhaseActive)
		watch(tun)
	}

	watch(tun) // begin watching the initial tunnel

	for {
		select {
		case <-ctx.Done():
			stopWatch()
			return ctx.Err()
		case <-breakCh:
			// The goroutine has returned (it sent on breakCh then exited); join it
			// so waitDone is drained before rebuild reassigns tun.
			stopWatch()
			status(out, "reconnecting", "tunnel broke; rebuilding")
			report(PhaseRebuilding)
			rebuild()
		case <-backoffCh:
			backoffCh = nil
			report(PhaseRebuilding)
			rebuild()
		case <-tick:
			tick = o.clock.After(defaultTickInterval) // re-arm for the next check
			if sess == nil {
				continue // mid-rebuild; nothing to check yet
			}
			remaining := sess.Deadline().Sub(o.clock.Now())
			if remaining >= defaultRebuildMargin {
				continue // deadline far out: keep holding
			}
			stopWatch()
			status(out, "reconnecting", fmt.Sprintf("session expiring in %s; rebuilding", remaining))
			report(PhaseRebuilding)
			rebuild()
		}
	}
}

// status emits a state-transition line to out. Errors writing status are
// ignored: they must not interrupt the supervision loop.
func status(out io.Writer, state, detail string) {
	_, _ = fmt.Fprintf(out, "[%s] %s\n", state, detail)
}
