// Package cli wires the kubectl-oke-bastion command surface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/bastion"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/kubeconfig"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/ociauth"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/session"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/sshkey"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/store"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/supervisor"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/tunnel"
)

// k8sAPIPort is the OKE private API endpoint port the session forwards to.
const k8sAPIPort = 6443

// NewRootCmd builds the root command, invoked as `kubectl oke bastion`.
func NewRootCmd() *cobra.Command {
	var (
		authMethod string
		profile    string
		bastionID  string
		localPort  int
	)
	cmd := &cobra.Command{
		Use:          "kubectl-oke-bastion",
		Short:        "Open and supervise an OCI Bastion tunnel to a private OKE cluster",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			provider, err := ociauth.Provider(ociauth.Spec{
				Method:  ociauth.Method(authMethod),
				Profile: profile,
			})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			// Cluster facts come first: the private endpoint is the session
			// target, and the cluster OCID keys the persisted bastion mapping.
			info, err := kubeconfig.Current()
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(out,
				"context:          %s\n"+
					"private endpoint: %s\n"+
					"cluster OCID:     %s\n"+
					"region:           %s\n",
				info.ContextName, info.PrivateEndpoint, info.ClusterOCID, info.Region); err != nil {
				return err
			}

			// Resolve the bastion for this cluster: a --bastion-id flag wins and
			// is remembered; otherwise fall back to the stored mapping so the
			// flag need only be supplied once (slice 6).
			storePath, err := store.DefaultPath()
			if err != nil {
				return err
			}
			bastionID, err = resolveBastion(store.Open(storePath), info.ClusterOCID, bastionID)
			if err != nil {
				return err
			}

			// Prove the bastion is reachable with these credentials.
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			handle, err := bastion.Get(ctx, provider, info.Region, bastionID)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(out, "bastion:          %s (%s)\n", handle.Name, handle.State); err != nil {
				return err
			}

			// The supervisor owns the session+tunnel lifecycle for the duration
			// of this invocation: it brings the tunnel up, holds it in the
			// foreground, rebuilds it on a break (redial) or session expiry
			// (recreate), and tears everything down on exit (ADR-0003, ADR-0006).
			key, err := sshkey.Generate()
			if err != nil {
				return err
			}
			client, err := bastion.NewClient(provider, info.Region)
			if err != nil {
				return err
			}

			bastionCtxName := info.ContextName + "-bastion"
			if _, err := fmt.Fprintf(out,
				"\nbringing up tunnel — when active, use:  kubectl --context %s get nodes\n"+
					"Ctrl-C to tear down.\n\n", bastionCtxName); err != nil {
				return err
			}

			builder := &liveBuilder{
				client:     client,
				key:        key,
				bastionID:  bastionID,
				target:     session.Target{PrivateIP: info.PrivateEndpoint, Port: k8sAPIPort},
				dialTo:     fmt.Sprintf("%s:%d", info.PrivateEndpoint, k8sAPIPort),
				pinnedPort: localPort,
			}
			wiring := &liveWiring{
				originalContext: info.ContextName,
				privateEndpoint: info.PrivateEndpoint,
			}

			runCtx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			if rerr := supervisor.Run(runCtx, builder, wiring, out); rerr != nil && !errors.Is(rerr, context.Canceled) {
				return rerr
			}
			_, _ = fmt.Fprintln(out, "torn down.")
			return nil
		},
	}
	cmd.Flags().StringVar(&authMethod, "auth", string(ociauth.APIKey),
		"OCI auth method: api_key, security_token, or instance_principal")
	cmd.Flags().StringVar(&profile, "profile", "",
		"OCI config profile (api_key/security_token); empty uses DEFAULT")
	cmd.Flags().StringVar(&bastionID, "bastion-id", "",
		"OCID of the pre-existing OCI Bastion to tunnel through")
	cmd.Flags().IntVar(&localPort, "local-port", 0,
		"local loopback port for the tunnel; 0 lets the OS assign one")
	cmd.AddCommand(newInitCmd())
	cmd.AddCommand(newUpCmd(), newDownCmd(), newStatusCmd(), newDaemonCmd())
	return cmd
}

// resolveBastion determines which bastion OCID to tunnel through for the
// cluster identified by clusterOCID. A non-empty flag wins and is persisted so
// later runs need not repeat it; an empty flag falls back to the stored
// mapping. It errors when neither a flag nor a stored mapping is available.
func resolveBastion(s *store.Store, clusterOCID, flag string) (string, error) {
	id, ok, err := s.Get(clusterOCID)
	if err != nil {
		return "", err
	}
	if flag != "" {
		// Persist only when the mapping actually changes, so the common
		// repeat-run case doesn't rewrite the file on every invocation.
		if flag != id {
			if err := s.Put(clusterOCID, flag); err != nil {
				return "", err
			}
		}
		return flag, nil
	}
	if !ok {
		return "", fmt.Errorf("no bastion known for cluster %s: supply one once with --bastion-id <OCID>", clusterOCID)
	}
	return id, nil
}

// liveBuilder is the production supervisor.Builder: it mints sessions against a
// real OCI bastion and opens in-process SSH forwards through them, reusing one
// ephemeral key for the invocation.
type liveBuilder struct {
	client     session.BastionClient
	key        sshkey.KeyPair
	bastionID  string
	target     session.Target
	dialTo     string // <private endpoint>:6443, the tunnel's far end
	pinnedPort int    // --local-port, or 0 to let the OS assign on first open
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
	// localPort is 0 on the first open (use the pin, or let the OS choose) and
	// the previously assigned port on every redial, keeping the local endpoint
	// stable so the wired -bastion context stays valid across rebuilds.
	port := localPort
	if port == 0 {
		port = b.pinnedPort
	}
	tun, err := tunnel.Open(ctx, tunnel.Params{
		BastionHost: host,
		User:        user,
		Signer:      b.key.Signer,
		Target:      b.dialTo,
		LocalPort:   port,
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
