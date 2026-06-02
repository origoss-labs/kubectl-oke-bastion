// Package cli wires the kubectl-oke-bastion command surface.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/kubeconfig"
)

// NewRootCmd builds the root command, invoked as `kubectl oke bastion`.
func NewRootCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "kubectl-oke-bastion",
		Short:        "Open and supervise an OCI Bastion tunnel to a private OKE cluster",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := kubeconfig.Current()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "context:          %s\n", info.ContextName)
			fmt.Fprintf(out, "private endpoint: %s\n", info.PrivateEndpoint)
			fmt.Fprintf(out, "cluster OCID:     %s\n", info.ClusterOCID)
			fmt.Fprintf(out, "region:           %s\n", info.Region)
			return nil
		},
	}
}
