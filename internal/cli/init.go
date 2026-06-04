package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/config"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/ociconfig"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/prompt"
)

// newInitCmd builds `kubectl oke bastion init`: the first leg of onboarding
// (ADR-0011). It reads the profiles from ~/.oci/config, lets the operator pick
// one, detects that profile's auth method, and writes config.yaml. Cluster and
// bastion discovery land in later slices; this slice writes profile + method
// only.
func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "init",
		Short:        "Pick an OCI profile and write the initial config.yaml",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ociPath, err := ociConfigPath()
			if err != nil {
				return err
			}
			cfgPath, err := config.DefaultPath()
			if err != nil {
				return err
			}
			return runInit(cmd.InOrStdin(), cmd.OutOrStdout(), ociPath, cfgPath)
		},
	}
}

// runInit is the I/O-parameterized core of init: parse the OCI config at
// ociPath, prompt the operator (over in/out) to pick a profile, and write the
// chosen profile and its detected method to cfgPath. Splitting it out keeps the
// logic testable over fixtures without touching the real home dir.
func runInit(in io.Reader, out io.Writer, ociPath, cfgPath string) error {
	sections, err := ociconfig.ParseFile(ociPath)
	if err != nil {
		return err
	}
	if len(sections) == 0 {
		return fmt.Errorf("OCI config %s defines no profiles", ociPath)
	}

	names := make([]string, len(sections))
	for i, s := range sections {
		names[i] = s.Name
	}
	idx, err := prompt.Select(in, out, "Select an OCI profile:", names)
	if err != nil {
		return err
	}
	chosen := sections[idx]

	if err := config.Save(cfgPath, config.Config{
		Profile: chosen.Name,
		Method:  chosen.Method,
	}); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "\nWrote %s (profile %q, auth %s)\n",
		cfgPath, chosen.Name, chosen.Method); err != nil {
		return err
	}
	return nil
}

// ociConfigPath locates the OCI config file. It honors OCI_CLI_CONFIG_FILE (the
// `oci` CLI's override) first, then OCI_CONFIG_FILE (the Go SDK's), and finally
// falls back to ~/.oci/config — the same precedence operators expect from the
// rest of the OCI tooling.
func ociConfigPath() (string, error) {
	if p := os.Getenv("OCI_CLI_CONFIG_FILE"); p != "" {
		return p, nil
	}
	if p := os.Getenv("OCI_CONFIG_FILE"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home dir for ~/.oci/config: %w", err)
	}
	return filepath.Join(home, ".oci", "config"), nil
}
