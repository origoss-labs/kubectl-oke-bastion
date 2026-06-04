// Package cli wires the kubectl-oke-bastion command surface.
package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/kubeconfig"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/session"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/sshkey"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/supervisor"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/tunnel"
)

// k8sAPIPort is the OKE private API endpoint port the session forwards to.
const k8sAPIPort = 6443

// NewRootCmd builds the root command, invoked as `kubectl oke bastion`. Bare
// (no subcommand) it prints help: config.yaml + the up/down/status/daemon
// commands superseded the old derive-from-current-context foreground flow
// (ADR-0011), so the root no longer runs a tunnel itself.
func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "kubectl-oke-bastion",
		Short:        "Open and supervise an OCI Bastion tunnel to a private OKE cluster",
		SilenceUsage: true,
		// No RunE: cobra prints usage for a bare invocation, which is the help
		// the operator needs to discover init/up/down/status.
	}
	cmd.AddCommand(newInitCmd())
	cmd.AddCommand(newUpCmd(), newDownCmd(), newStatusCmd(), newDaemonCmd())
	return cmd
}

// liveBuilder is the production supervisor.Builder: it mints sessions against a
// real OCI bastion and opens in-process SSH forwards through them, reusing one
// ephemeral key for the invocation.
type liveBuilder struct {
	client    session.BastionClient
	key       sshkey.KeyPair
	bastionID string
	target    session.Target
	dialTo    string // <private endpoint>:6443, the tunnel's far end
}

func (b *liveBuilder) NewSession(ctx context.Context) (supervisor.Session, error) {
	return session.Open(ctx, b.client, session.Params{
		BastionID:        b.bastionID,
		Target:           b.target,
		PublicKeyOpenSSH: b.key.PublicKeyOpenSSH,
	})
}

func (b *liveBuilder) OpenTunnel(ctx context.Context, s supervisor.Session, localPort int) (supervisor.Tunnel, error) {
	sess, ok := s.(*session.Session)
	if !ok {
		return nil, fmt.Errorf("supervisor passed an unexpected session type %T", s)
	}
	host, user, err := parseBastionSSH(sess.SSHMeta["command"])
	if err != nil {
		return nil, err
	}
	// localPort is 0 on the first open (let the OS choose) and the previously
	// assigned port on every redial, keeping the local endpoint stable so the
	// wired -bastion context stays valid across rebuilds.
	tun, err := tunnel.Open(ctx, tunnel.Params{
		BastionHost: host,
		User:        user,
		Signer:      b.key.Signer,
		Target:      b.dialTo,
		LocalPort:   localPort,
	})
	if err != nil {
		return nil, err
	}
	return liveTunnel{tun}, nil
}

// liveTunnel adapts *tunnel.Tunnel (which exposes LocalPort as a field) to the
// supervisor.Tunnel interface (which wants it as a method).
type liveTunnel struct{ *tunnel.Tunnel }

func (t liveTunnel) LocalPort() int { return t.Tunnel.LocalPort }

// liveWiring is the production supervisor.Wiring: it adds and removes the
// non-destructive -bastion kubeconfig context, remembering the context name
// between Wire and Unwire.
type liveWiring struct {
	originalContext string
	privateEndpoint string
	ctxName         string
}

func (w *liveWiring) Wire(localPort int) error {
	name, err := kubeconfig.WireBastion(kubeconfig.BastionWiring{
		OriginalContext: w.originalContext,
		PrivateEndpoint: w.privateEndpoint,
		LocalPort:       localPort,
	})
	if err != nil {
		return err
	}
	w.ctxName = name
	return nil
}

func (w *liveWiring) Unwire() error {
	if w.ctxName == "" {
		return nil
	}
	return kubeconfig.UnwireBastion(w.ctxName)
}

// parseBastionSSH extracts the bastion SSH host (with :22) and user from a
// session's ssh-metadata command, which ends in `<session-ocid>@<host>`. The
// SSH user is the session OCID; the host is OCI's regional bastion endpoint.
func parseBastionSSH(command string) (host, user string, err error) {
	for _, tok := range strings.Fields(command) {
		at := strings.LastIndex(tok, "@")
		if at <= 0 || at == len(tok)-1 {
			continue
		}
		return tok[at+1:] + ":22", tok[:at], nil
	}
	return "", "", fmt.Errorf("session ssh-metadata command has no user@host: %q", command)
}
