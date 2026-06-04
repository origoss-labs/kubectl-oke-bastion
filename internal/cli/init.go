package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	ocibastion "github.com/oracle/oci-go-sdk/v65/bastion"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/containerengine"
	"github.com/oracle/oci-go-sdk/v65/identity"
	"github.com/spf13/cobra"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/config"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/discover"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/kubeconfig"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/ociauth"
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
			kubePath := kubeconfig.DefaultKubeconfigPath()
			return runInit(cmd.InOrStdin(), cmd.OutOrStdout(), ociPath, cfgPath, kubePath)
		},
	}
}

// pickProfile is the Slice A leg of init, kept separate so it stays testable
// over OCI-config fixtures without a live tenancy: parse the OCI config at
// ociPath, prompt the operator (over br/out) to pick a profile, and write the
// chosen profile + detected method to cfgPath. It returns the chosen section so
// runInit can build the OCI clients for it.
func pickProfile(br *bufio.Reader, out io.Writer, ociPath, cfgPath string) (ociconfig.Section, error) {
	sections, err := ociconfig.ParseFile(ociPath)
	if err != nil {
		return ociconfig.Section{}, err
	}
	if len(sections) == 0 {
		return ociconfig.Section{}, fmt.Errorf("OCI config %s defines no profiles", ociPath)
	}

	names := make([]string, len(sections))
	for i, s := range sections {
		names[i] = s.Name
	}
	idx, err := prompt.Select(br, out, "Select an OCI profile:", names)
	if err != nil {
		return ociconfig.Section{}, err
	}
	chosen := sections[idx]

	if err := config.Save(cfgPath, config.Config{
		Profile: chosen.Name,
		Method:  chosen.Method,
	}); err != nil {
		return ociconfig.Section{}, err
	}
	if _, err := fmt.Fprintf(out, "\nWrote %s (profile %q, auth %s)\n",
		cfgPath, chosen.Name, chosen.Method); err != nil {
		return ociconfig.Section{}, err
	}
	return chosen, nil
}

// runInit is the I/O-parameterized core of init: parse the OCI config at
// ociPath, prompt the operator (over in/out) to pick a profile, write the
// chosen profile and method to cfgPath, then build the OCI clients for that
// profile and run cluster/bastion discovery, merging the chosen cluster's
// kubeconfig and appending its entry to cfgPath. Splitting it out keeps the
// profile-pick + orchestration testable; the live OCI client construction is
// the only piece that touches a real tenancy and is not unit-tested.
//
// A single buffered reader is wrapped over in once here and shared across every
// prompt: the prompt package over-reads a raw stream, so issuing several Select
// calls on a fresh os.Stdin can drop buffered input. One bufio.Reader threaded
// through all prompts keeps the operator's queued keystrokes intact.
func runInit(in io.Reader, out io.Writer, ociPath, cfgPath, kubePath string) error {
	br := bufio.NewReader(in)

	chosen, err := pickProfile(br, out, ociPath, cfgPath)
	if err != nil {
		return err
	}

	// Build the OCI clients for the chosen profile+method and run discovery.
	// This is the live-wiring boundary; everything past the constructed client
	// is the unit-tested discoverAndConfigure.
	provider, err := ociauth.Provider(ociauth.Spec{Method: chosen.Method, Profile: chosen.Name})
	if err != nil {
		return err
	}
	tenancyID, err := provider.TenancyOCID()
	if err != nil {
		return fmt.Errorf("reading tenancy OCID from profile %q: %w", chosen.Name, err)
	}
	client, err := newOCIClient(provider)
	if err != nil {
		return err
	}

	// No overall deadline here: discoverAndConfigure interleaves network calls
	// with interactive prompts, and it times only the network phases itself
	// (operator think-time must not expire a later API call).
	return discoverAndConfigure(context.Background(), br, out, client, tenancyID, kubePath, cfgPath)
}

// networkTimeout bounds a single group of OCI network calls within init. It is
// applied per phase (compartment walk, cluster walk, facts+bastions+kubeconfig)
// rather than across the whole flow, so a slow operator at a prompt cannot
// cause a subsequent API call to fail with a context-deadline error.
const networkTimeout = 2 * time.Minute

// discoverAndConfigure is the I/O- and client-parameterized heart of init's
// discovery (testable against a fake discover.Client): walk compartments,
// aggregate ACTIVE clusters with a progress indicator, prompt the operator to
// pick one, read its facts, list+pick (or auto-select) a bastion, generate and
// merge its kubeconfig into kubePath, and append the cluster to cfgPath. br is
// the shared buffered reader so the cluster and bastion prompts read from one
// stream without losing input.
//
// parent carries no deadline: each network phase derives its own networkTimeout
// child (released immediately after the phase) so operator think-time at a
// prompt can never expire a subsequent OCI call.
func discoverAndConfigure(parent context.Context, br *bufio.Reader, out io.Writer, client discover.Client, tenancyID, kubePath, cfgPath string) error {
	// Phase 1: walk compartments and aggregate clusters (no prompt in between).
	walkCtx, cancelWalk := context.WithTimeout(parent, networkTimeout)
	compartments, err := discover.Compartments(walkCtx, client, tenancyID, "(tenancy root)")
	if err != nil {
		cancelWalk()
		return err
	}
	clusters, err := discover.Clusters(walkCtx, client, compartments, discover.WriterProgress(out))
	cancelWalk()
	if err != nil {
		return err
	}
	if len(clusters) == 0 {
		return fmt.Errorf("no ACTIVE OKE clusters found in any accessible compartment")
	}

	labels := make([]string, len(clusters))
	for i, c := range clusters {
		labels[i] = c.Label()
	}
	idx, err := prompt.Select(br, out, "Select an OKE cluster:", labels)
	if err != nil {
		return err
	}
	cluster := clusters[idx]

	// Phase 2: read the cluster's facts and find its bastions (no prompt yet).
	factsCtx, cancelFacts := context.WithTimeout(parent, networkTimeout)
	facts, err := discover.ClusterFacts(factsCtx, client, cluster.OCID)
	if err != nil {
		cancelFacts()
		return err
	}
	// Bastions can live in a hub/network compartment, so search the whole
	// accessible-compartment list and match by VCN, not by the cluster's own
	// compartment.
	bastionRes, err := discover.Bastions(factsCtx, client, compartments, facts.VcnID)
	cancelFacts()
	if err != nil {
		return err
	}

	var picked discover.Bastion
	if bastionRes.AutoSelected {
		picked = bastionRes.Bastions[0]
		if _, err := fmt.Fprintf(out, "Auto-selected the only bastion: %s\n", picked.Name); err != nil {
			return err
		}
	} else {
		bLabels := make([]string, len(bastionRes.Bastions))
		for i, b := range bastionRes.Bastions {
			bLabels[i] = b.Name
		}
		bIdx, err := prompt.Select(br, out, "Select a bastion:", bLabels)
		if err != nil {
			return err
		}
		picked = bastionRes.Bastions[bIdx]
	}

	// Phase 3: render the kubeconfig (no prompt after this).
	kubeCtx, cancelKube := context.WithTimeout(parent, networkTimeout)
	raw, err := discover.Kubeconfig(kubeCtx, client, cluster.OCID)
	cancelKube()
	if err != nil {
		return err
	}
	info, err := kubeconfig.Parse(raw)
	if err != nil {
		return err
	}
	if err := kubeconfig.MergeKubeconfig(kubePath, raw); err != nil {
		return err
	}

	// Prefer the region OKE baked into the kubeconfig exec args (authoritative);
	// fall back to the OCID-parsed segment only if the kubeconfig carries none.
	// Reconciling here means a disagreement can never silently persist the
	// brittle OCID-derived value.
	region := info.Region
	if region == "" {
		region = facts.Region
	}

	if err := config.UpsertCluster(cfgPath, config.Cluster{
		ClusterOCID:     cluster.OCID,
		Region:          region,
		CompartmentOCID: cluster.CompartmentOCID,
		BastionOCID:     picked.OCID,
		KubeContext:     info.ContextName,
	}); err != nil {
		return err
	}

	_, err = fmt.Fprintf(out,
		"\nConfigured cluster %q:\n"+
			"  context:    %s (merged into %s)\n"+
			"  region:     %s\n"+
			"  bastion:    %s\n"+
			"  saved to:   %s\n",
		cluster.Name, info.ContextName, kubePath, region, picked.Name, cfgPath)
	return err
}

// ociClients composes the three concrete OCI SDK clients discovery uses into
// one value satisfying discover.Client. It is the live-wiring counterpart to
// the test fake; only its construction touches a real tenancy.
type ociClients struct {
	identity.IdentityClient
	containerengine.ContainerEngineClient
	ocibastion.BastionClient
}

// newOCIClient builds the identity, container-engine, and bastion clients from
// provider and bundles them as a discover.Client. Region is left at the
// provider's default for the tenancy-wide compartment/cluster walk; the
// per-cluster bastion client is pinned elsewhere when a tunnel is opened.
func newOCIClient(provider common.ConfigurationProvider) (discover.Client, error) {
	id, err := identity.NewIdentityClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, fmt.Errorf("creating identity client: %w", err)
	}
	ce, err := containerengine.NewContainerEngineClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, fmt.Errorf("creating container engine client: %w", err)
	}
	b, err := ocibastion.NewBastionClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, fmt.Errorf("creating bastion client: %w", err)
	}
	return &ociClients{IdentityClient: id, ContainerEngineClient: ce, BastionClient: b}, nil
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
