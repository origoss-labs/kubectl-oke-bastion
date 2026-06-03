// Package cli wires the kubectl-oke-bastion command surface.
package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/bastion"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/kubeconfig"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/ociauth"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/session"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/sshkey"
)

// k8sAPIPort is the OKE private API endpoint port the session forwards to.
const k8sAPIPort = 6443

// NewRootCmd builds the root command, invoked as `kubectl oke bastion`.
func NewRootCmd() *cobra.Command {
	var (
		authMethod string
		profile    string
		bastionID  string
	)
	cmd := &cobra.Command{
		Use:          "kubectl-oke-bastion",
		Short:        "Open and supervise an OCI Bastion tunnel to a private OKE cluster",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if bastionID == "" {
				return fmt.Errorf("no bastion known: supply one with --bastion-id <OCID>")
			}
			provider, err := ociauth.Provider(ociauth.Spec{
				Method:  ociauth.Method(authMethod),
				Profile: profile,
			})
			if err != nil {
				return err
			}

			// Proving the bastion is reachable with these credentials is this
			// command's deliverable; it does not depend on a kubeconfig.
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			handle, err := bastion.Get(ctx, provider, bastionID)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "bastion:          %s (%s)\n", handle.Name, handle.State); err != nil {
				return err
			}

			// The session targets the cluster's private endpoint, so cluster
			// facts are now a hard requirement, not informational continuity.
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
				"session:          %s (ACTIVE)\n"+
					"ssh user:         %s\n",
				sess.ID, sess.Username); err != nil {
				return err
			}
			sshCmd := sess.SSHMeta["command"]
			if sshCmd == "" {
				return nil
			}
			_, err = fmt.Fprintf(out, "ssh command:      %s\n", sshCmd)
			return err
		},
	}
	cmd.Flags().StringVar(&authMethod, "auth", string(ociauth.APIKey),
		"OCI auth method: api_key, security_token, or instance_principal")
	cmd.Flags().StringVar(&profile, "profile", "",
		"OCI config profile (api_key/security_token); empty uses DEFAULT")
	cmd.Flags().StringVar(&bastionID, "bastion-id", "",
		"OCID of the pre-existing OCI Bastion to tunnel through")
	return cmd
}
