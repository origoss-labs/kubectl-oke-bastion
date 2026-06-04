package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/bastion"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/config"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/daemon"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/kubeconfig"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/ociauth"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/session"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/sshkey"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/supervisor"
)

// stateWiring wraps the real -bastion kubeconfig wiring to also drive the daemon
// state file: the supervisor calls Wire exactly when the tunnel comes up with
// the assigned local port, so that is the moment to record phase=active+port,
// and Unwire fires once on teardown, so that is the moment to mark stopped. It
// always delegates to inner, so the real -bastion context is still wired/unwired
// — this wrapper only adds the state side-effect, keeping the supervisor itself
// untouched (Slice E owns the supervisor).
//
// The state file is owner-only status plumbing and must never veto the
// load-bearing wire: both methods delegate to inner first and treat the state
// write as best-effort, logging a failure to out (→ the daemon log) and
// proceeding — matching supervisor.status's "writing status must not interrupt"
// philosophy. A status racing a failed state write reads stale, never a stranded
// tunnel.
type stateWiring struct {
	inner     supervisor.Wiring
	statePath string
	startedAt time.Time
	out       io.Writer
}

func (w *stateWiring) Wire(localPort int) error {
	if err := w.inner.Wire(localPort); err != nil {
		return err
	}
	if serr := daemon.SaveState(w.statePath, daemon.State{
		Phase:     daemon.PhaseActive,
		StartedAt: w.startedAt,
		LocalPort: localPort,
	}); serr != nil {
		_, _ = fmt.Fprintf(w.out, "recording active state: %v\n", serr)
	}
	return nil
}

func (w *stateWiring) Unwire() error {
	uerr := w.inner.Unwire()
	if serr := daemon.SaveState(w.statePath, daemon.State{
		Phase:     daemon.PhaseStopped,
		StartedAt: w.startedAt,
	}); serr != nil {
		_, _ = fmt.Fprintf(w.out, "recording stopped state: %v\n", serr)
	}
	return uerr
}

// runTunnel is the single supervisor-assembly path both __daemon (background)
// and `up --foreground` use, satisfying "reusing the same code path". Given a
// resolved cluster, its daemon Paths, an auth override, and an output sink, it
// resolves the OCI provider and cluster facts, builds the live session/tunnel
// builder and the state-driving -bastion wiring, and runs the supervisor until
// ctx is cancelled. The supervisor's deferred teardown then deletes the session
// and unwires the context; the in-process ephemeral key is dropped when the
// process exits. It returns nil on a clean ctx-cancel so a SIGTERM shutdown is
// not reported as a failure.
//
// The live OCI client construction here is integration-only (HITL); the
// separable, testable pieces are kubeconfig.InfoForContext and stateWiring.
func runTunnel(ctx context.Context, cluster config.Cluster, p daemon.Paths, auth ociauth.Spec, out io.Writer) error {
	provider, err := ociauth.Provider(auth)
	if err != nil {
		return err
	}

	// The private endpoint is not in config.yaml; it lives in the merged base
	// context Slice B wrote, keyed by the configured kube context.
	info, err := kubeconfig.InfoForContext(cluster.KubeContext)
	if err != nil {
		return err
	}

	key, err := sshkey.Generate()
	if err != nil {
		return err
	}
	client, err := bastion.NewClient(provider, cluster.Region)
	if err != nil {
		return err
	}

	builder := &liveBuilder{
		client:    client,
		key:       key,
		bastionID: cluster.BastionOCID,
		target:    session.Target{PrivateIP: info.PrivateEndpoint, Port: k8sAPIPort},
		dialTo:    fmt.Sprintf("%s:%d", info.PrivateEndpoint, k8sAPIPort),
	}
	wiring := &stateWiring{
		inner: &liveWiring{
			originalContext: cluster.KubeContext,
			privateEndpoint: info.PrivateEndpoint,
		},
		statePath: p.State(),
		startedAt: time.Now(),
		out:       out,
	}

	if rerr := supervisor.Run(ctx, builder, wiring, out); rerr != nil && !errors.Is(rerr, context.Canceled) {
		return rerr
	}
	return nil
}
