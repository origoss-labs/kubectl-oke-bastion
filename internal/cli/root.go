// Package cli wires the kubectl-oke-bastion command surface.
package cli

import (
	"context"
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
			handle, err := bastion.Get(ctx, provider, bastionID)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(out, "bastion:          %s (%s)\n", handle.Name, handle.State); err != nil {
				return err
			}

			// Mint the ephemeral key and bring a port-forwarding session to the
			// private API endpoint up to ACTIVE. Slice 3 proves the lifecycle
			// end-to-end; it opens no tunnel and deletes the session on exit.
			key, err := sshkey.Generate()
			if err != nil {
				return err
			}
			client, err := bastion.NewClient(provider)
			if err != nil {
				return err
			}

			sessCtx, sessCancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer sessCancel()
			sess, err := session.Open(sessCtx, client, session.Params{
				BastionID:        bastionID,
				Target:           session.Target{PrivateIP: info.PrivateEndpoint, Port: k8sAPIPort},
				PublicKeyOpenSSH: key.PublicKeyOpenSSH,
			})
			if err != nil {
				return err
			}
			// Delete the session on exit using a fresh context, since sessCtx
			// may already be spent by the time we tear down.
			defer func() {
				delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer delCancel()
				if cerr := sess.Close(delCtx); cerr != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: deleting session: %v\n", cerr)
				}
			}()

			if _, err := fmt.Fprintf(out,
				"session:          %s (ACTIVE)\n", sess.ID); err != nil {
				return err
			}

			// Open the in-process SSH forward through the ACTIVE session to the
			// private API endpoint. The bastion SSH host and user come from the
			// session's ssh-metadata command.
			bastionHost, sshUser, err := parseBastionSSH(sess.SSHMeta["command"])
			if err != nil {
				return err
			}
			tun, err := tunnel.Open(cmd.Context(), tunnel.Params{
				BastionHost: bastionHost,
				User:        sshUser,
				Signer:      key.Signer,
				Target:      fmt.Sprintf("%s:%d", info.PrivateEndpoint, k8sAPIPort),
				LocalPort:   localPort,
			})
			if err != nil {
				return err
			}
			defer func() { _ = tun.Close() }()

			// Wire the non-destructive -bastion context at the resolved local
			// port, and remove it on exit before the tunnel and session go.
			bastionCtx, err := kubeconfig.WireBastion(kubeconfig.BastionWiring{
				OriginalContext: info.ContextName,
				PrivateEndpoint: info.PrivateEndpoint,
				LocalPort:       tun.LocalPort,
			})
			if err != nil {
				return err
			}
			defer func() {
				if uerr := kubeconfig.UnwireBastion(bastionCtx); uerr != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: removing -bastion context: %v\n", uerr)
				}
			}()

			if _, err := fmt.Fprintf(out,
				"tunnel:           127.0.0.1:%d → %s:%d\n"+
					"context:          %s\n\n"+
					"use:  kubectl --context %s get nodes\n"+
					"Ctrl-C to tear down.\n",
				tun.LocalPort, info.PrivateEndpoint, k8sAPIPort,
				bastionCtx, bastionCtx); err != nil {
				return err
			}

			// Hold in the foreground until interrupted; the deferred teardown
			// then removes the context, closes the tunnel, and deletes the
			// session (in that order).
			holdCtx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			<-holdCtx.Done()
			_, _ = fmt.Fprintln(out, "\ntearing down…")
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
	return cmd
}

// resolveBastion determines which bastion OCID to tunnel through for the
// cluster identified by clusterOCID. A non-empty flag wins and is persisted so
// later runs need not repeat it; an empty flag falls back to the stored
// mapping. It errors when neither a flag nor a stored mapping is available.
func resolveBastion(s *store.Store, clusterOCID, flag string) (string, error) {
	if flag != "" {
		if err := s.Put(clusterOCID, flag); err != nil {
			return "", err
		}
		return flag, nil
	}
	id, ok, err := s.Get(clusterOCID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no bastion known for cluster %s: supply one once with --bastion-id <OCID>", clusterOCID)
	}
	return id, nil
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
