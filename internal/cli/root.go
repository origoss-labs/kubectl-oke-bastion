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
			_, err = fmt.Fprintf(cmd.OutOrStdout(),
				"context:          %s\n"+
					"private endpoint: %s\n"+
					"cluster OCID:     %s\n"+
					"region:           %s\n",
				info.ContextName, info.PrivateEndpoint, info.ClusterOCID, info.Region)
			return err
		},
	}
}
