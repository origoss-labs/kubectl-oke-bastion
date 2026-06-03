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
)

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

			info, err := kubeconfig.Current()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			handle, err := bastion.Get(ctx, provider, bastionID)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(),
				"context:          %s\n"+
					"private endpoint: %s\n"+
					"cluster OCID:     %s\n"+
					"region:           %s\n"+
					"bastion:          %s (%s)\n",
				info.ContextName, info.PrivateEndpoint, info.ClusterOCID, info.Region,
				handle.Name, handle.State)
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
