// Package supervisor runs the reactive self-healing loop that owns a bastion
// tunnel for the lifetime of one `kubectl oke bastion` invocation (ADR-0003,
// ADR-0006). It composes a Builder (sessions + tunnels) and Wiring (the
// -bastion kubeconfig context), watches the live tunnel, and rebuilds it on
// failure: a break (session still valid) is recovered by redialing the tunnel
// alone; an expired or dead session is recovered by creating a new session and
// then a new tunnel. A build failure that the tunnel's own dial-retry could not
// absorb stops the loop with the error rather than spinning. Proactive
// make-before-break rotation across the TTL boundary is deferred (ADR-0006).
package supervisor

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Session is the bastion session a tunnel rides on.
type Session interface {
	// Alive reports whether the session is still usable. False means it expired
	// or was deleted, so recovery needs a new session, not just a redial.
	Alive(ctx context.Context) bool
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
	// OS assigns a port) and the previously assigned port on every redial, so
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

// Run brings up the tunnel and holds it, self-healing on breaks and expiry,
// until ctx is cancelled or a rebuild fails. On return it tears down exactly
// once: removes the -bastion context, closes the tunnel, deletes the session.
func Run(ctx context.Context, b Builder, w Wiring, out io.Writer) (err error) {
	var (
		sess      Session
		tun       Tunnel
		localPort int
		wired     bool
	)
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

	first := true
	for {
		if sess == nil {
			if first {
				status(out, "connecting", "establishing bastion tunnel")
			} else {
				status(out, "recreating", "session ended; creating a new one")
			}
			sess, err = b.NewSession(ctx)
			if err != nil {
				return err
			}
		}

		tun, err = b.OpenTunnel(ctx, sess, localPort)
		if err != nil {
			return err
		}
		localPort = tun.LocalPort()

		if !wired {
			if err = w.Wire(localPort); err != nil {
				return err
			}
			wired = true
		}

		status(out, "active", fmt.Sprintf("tunnel up on 127.0.0.1:%d", localPort))
		first = false

		waitErr := tun.Wait(ctx)
		_ = tun.Close()
		tun = nil
		if ctx.Err() != nil {
			return waitErr
		}

		status(out, "reconnecting", "tunnel broke; recovering")
		if !sess.Alive(ctx) {
			delCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = sess.Close(delCtx)
			cancel()
			sess = nil
		}
	}
}

// status emits a state-transition line to out. Errors writing status are
// ignored: they must not interrupt the supervision loop.
func status(out io.Writer, state, detail string) {
	_, _ = fmt.Fprintf(out, "[%s] %s\n", state, detail)
}
