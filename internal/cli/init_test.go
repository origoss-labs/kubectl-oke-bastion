package cli

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocibastion "github.com/oracle/oci-go-sdk/v65/bastion"
	"github.com/oracle/oci-go-sdk/v65/containerengine"
	"github.com/oracle/oci-go-sdk/v65/identity"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/config"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/ociauth"
)

// fakeDiscoverClient is a scripted discover.Client for the init orchestration
// tests: it returns one compartment with one ACTIVE cluster, a cluster object
// with a private endpoint + VCN, a configurable bastion list, and a kubeconfig
// blob — no live OCI call.
type fakeDiscoverClient struct {
	clusterName   string
	clusterOCID   string
	compartmentID string
	vcnID         string
	bastions      []ocibastion.BastionSummary
	kubeconfig    string
}

func sptr(s string) *string { return &s }

func (f *fakeDiscoverClient) ListCompartments(_ context.Context, _ identity.ListCompartmentsRequest) (identity.ListCompartmentsResponse, error) {
	return identity.ListCompartmentsResponse{Items: []identity.Compartment{
		{Id: sptr(f.compartmentID), Name: sptr("team-a")},
	}}, nil
}

func (f *fakeDiscoverClient) ListClusters(_ context.Context, req containerengine.ListClustersRequest) (containerengine.ListClustersResponse, error) {
	if *req.CompartmentId != f.compartmentID {
		return containerengine.ListClustersResponse{}, nil
	}
	return containerengine.ListClustersResponse{Items: []containerengine.ClusterSummary{{
		Id:             sptr(f.clusterOCID),
		Name:           sptr(f.clusterName),
		CompartmentId:  sptr(f.compartmentID),
		LifecycleState: containerengine.ClusterLifecycleStateActive,
	}}}, nil
}

func (f *fakeDiscoverClient) GetCluster(_ context.Context, _ containerengine.GetClusterRequest) (containerengine.GetClusterResponse, error) {
	return containerengine.GetClusterResponse{Cluster: containerengine.Cluster{
		Id:        sptr(f.clusterOCID),
		Name:      sptr(f.clusterName),
		VcnId:     sptr(f.vcnID),
		Endpoints: &containerengine.ClusterEndpoints{PrivateEndpoint: sptr("10.0.0.6:6443")},
	}}, nil
}

func (f *fakeDiscoverClient) ListBastions(_ context.Context, req ocibastion.ListBastionsRequest) (ocibastion.ListBastionsResponse, error) {
	// Bastions now fans out across every accessible compartment; serve the
	// scripted bastions only for the cluster's compartment so they are not
	// duplicated across the (root + team-a) walk.
	if req.CompartmentId == nil || *req.CompartmentId != f.compartmentID {
		return ocibastion.ListBastionsResponse{}, nil
	}
	return ocibastion.ListBastionsResponse{Items: f.bastions}, nil
}

func (f *fakeDiscoverClient) CreateKubeconfig(_ context.Context, _ containerengine.CreateKubeconfigRequest) (containerengine.CreateKubeconfigResponse, error) {
	return containerengine.CreateKubeconfigResponse{Content: io.NopCloser(strings.NewReader(f.kubeconfig))}, nil
}

const initFakeKubeconfig = `apiVersion: v1
kind: Config
current-context: ctx-prod
clusters:
- name: cluster-prod
  cluster:
    server: https://10.0.0.6:6443
    certificate-authority-data: ZmFrZS1jYQ==
contexts:
- name: ctx-prod
  context:
    cluster: cluster-prod
    user: user-prod
users:
- name: user-prod
  user:
    exec:
      command: oci
      args: [ce, cluster, generate-token, --cluster-id, ocid1.cluster.oc1.eu-frankfurt-1.aaaa, --region, eu-frankfurt-1]
`

// TestDiscoverAndConfigure_SingleBastionAutoSelected drives the full
// orchestration through a fake OCI client and a buffered reader: pick the sole
// cluster, auto-select the sole bastion, merge the kubeconfig into a TempDir
// file, and append the cluster to config.yaml. End-to-end, no live OCI, never
// touching the real ~/.kube/config.
func TestDiscoverAndConfigure_SingleBastionAutoSelected(t *testing.T) {
	dir := t.TempDir()
	kubePath := filepath.Join(dir, "kube", "config")
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := config.Save(cfgPath, config.Config{Profile: "P", Method: ociauth.APIKey}); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	fake := &fakeDiscoverClient{
		clusterName:   "prod",
		clusterOCID:   "ocid1.cluster.oc1.eu-frankfurt-1.aaaa",
		compartmentID: "ocid1.compartment.oc1..a",
		vcnID:         "ocid1.vcn.oc1..v",
		bastions: []ocibastion.BastionSummary{{
			Id:             sptr("ocid1.bastion.oc1..only"),
			Name:           sptr("the-bastion"),
			TargetVcnId:    sptr("ocid1.vcn.oc1..v"),
			LifecycleState: ocibastion.BastionLifecycleStateActive,
		}},
		kubeconfig: initFakeKubeconfig,
	}

	var out strings.Builder
	// Only one prompt is needed: the cluster pick (option 1). The single
	// bastion is auto-selected, so no second prompt line is consumed.
	in := bufio.NewReader(strings.NewReader("1\n"))
	err := discoverAndConfigure(context.Background(), in, &out, fake,
		"ocid1.tenancy.oc1..root", kubePath, cfgPath)
	if err != nil {
		t.Fatalf("discoverAndConfigure: %v", err)
	}

	// The cluster entry was appended with all facts.
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if len(got.Clusters) != 1 {
		t.Fatalf("clusters = %d, want 1", len(got.Clusters))
	}
	cl := got.Clusters[0]
	if cl.ClusterOCID != fake.clusterOCID {
		t.Errorf("ClusterOCID = %q, want %q", cl.ClusterOCID, fake.clusterOCID)
	}
	if cl.Region != "eu-frankfurt-1" {
		t.Errorf("Region = %q, want eu-frankfurt-1", cl.Region)
	}
	if cl.CompartmentOCID != fake.compartmentID {
		t.Errorf("CompartmentOCID = %q, want %q", cl.CompartmentOCID, fake.compartmentID)
	}
	if cl.BastionOCID != "ocid1.bastion.oc1..only" {
		t.Errorf("BastionOCID = %q, want the auto-selected bastion", cl.BastionOCID)
	}
	if cl.KubeContext != "ctx-prod" {
		t.Errorf("KubeContext = %q, want ctx-prod", cl.KubeContext)
	}

	// The kubeconfig was merged into the TempDir file, not the real one.
	if _, err := os.Stat(kubePath); err != nil {
		t.Fatalf("kubeconfig not written to the target path: %v", err)
	}
	if !strings.Contains(out.String(), "the-bastion") {
		t.Errorf("summary %q does not mention the auto-selected bastion", out.String())
	}
}

// TestDiscoverAndConfigure_PersistsKubeconfigRegion proves the persisted region
// comes from the kubeconfig (the value OKE itself baked into the exec args),
// not the brittle OCID-parsed segment. The fixture's exec --region disagrees
// with the cluster OCID's region segment, so only one source can satisfy this.
func TestDiscoverAndConfigure_PersistsKubeconfigRegion(t *testing.T) {
	dir := t.TempDir()
	kubePath := filepath.Join(dir, "config")
	cfgPath := filepath.Join(dir, "config.yaml")

	// OCID parses to eu-frankfurt-1; kubeconfig exec --region says us-ashburn-1.
	const divergentKubeconfig = `apiVersion: v1
kind: Config
current-context: ctx-prod
clusters:
- name: cluster-prod
  cluster:
    server: https://10.0.0.6:6443
    certificate-authority-data: ZmFrZS1jYQ==
contexts:
- name: ctx-prod
  context:
    cluster: cluster-prod
    user: user-prod
users:
- name: user-prod
  user:
    exec:
      command: oci
      args: [ce, cluster, generate-token, --cluster-id, ocid1.cluster.oc1.eu-frankfurt-1.aaaa, --region, us-ashburn-1]
`

	fake := &fakeDiscoverClient{
		clusterName:   "prod",
		clusterOCID:   "ocid1.cluster.oc1.eu-frankfurt-1.aaaa",
		compartmentID: "ocid1.compartment.oc1..a",
		vcnID:         "ocid1.vcn.oc1..v",
		bastions: []ocibastion.BastionSummary{{
			Id:             sptr("ocid1.bastion.oc1..only"),
			Name:           sptr("the-bastion"),
			TargetVcnId:    sptr("ocid1.vcn.oc1..v"),
			LifecycleState: ocibastion.BastionLifecycleStateActive,
		}},
		kubeconfig: divergentKubeconfig,
	}

	var out strings.Builder
	in := bufio.NewReader(strings.NewReader("1\n"))
	if err := discoverAndConfigure(context.Background(), in, &out, fake,
		"ocid1.tenancy.oc1..root", kubePath, cfgPath); err != nil {
		t.Fatalf("discoverAndConfigure: %v", err)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if len(got.Clusters) != 1 {
		t.Fatalf("clusters = %d, want 1", len(got.Clusters))
	}
	if got.Clusters[0].Region != "us-ashburn-1" {
		t.Errorf("persisted Region = %q, want the kubeconfig's us-ashburn-1 (not the OCID-parsed eu-frankfurt-1)", got.Clusters[0].Region)
	}
}

// TestDiscoverAndConfigure_PromptsWhenMultipleBastions proves the second prompt
// (bastion pick) is read from the SAME buffered reader as the cluster pick, so
// buffered input is not lost between prompts.
func TestDiscoverAndConfigure_PromptsWhenMultipleBastions(t *testing.T) {
	dir := t.TempDir()
	kubePath := filepath.Join(dir, "config")
	cfgPath := filepath.Join(dir, "config.yaml")

	fake := &fakeDiscoverClient{
		clusterName:   "prod",
		clusterOCID:   "ocid1.cluster.oc1.eu-frankfurt-1.aaaa",
		compartmentID: "ocid1.compartment.oc1..a",
		vcnID:         "ocid1.vcn.oc1..v",
		bastions: []ocibastion.BastionSummary{
			{Id: sptr("ocid1.bastion.oc1..one"), Name: sptr("alpha"), TargetVcnId: sptr("ocid1.vcn.oc1..v"), LifecycleState: ocibastion.BastionLifecycleStateActive},
			{Id: sptr("ocid1.bastion.oc1..two"), Name: sptr("bravo"), TargetVcnId: sptr("ocid1.vcn.oc1..v"), LifecycleState: ocibastion.BastionLifecycleStateActive},
		},
		kubeconfig: initFakeKubeconfig,
	}

	var out strings.Builder
	// Two prompts on one reader: cluster #1, then bastion #2 (bravo).
	in := bufio.NewReader(strings.NewReader("1\n2\n"))
	if err := discoverAndConfigure(context.Background(), in, &out, fake,
		"ocid1.tenancy.oc1..root", kubePath, cfgPath); err != nil {
		t.Fatalf("discoverAndConfigure: %v", err)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if len(got.Clusters) != 1 {
		t.Fatalf("clusters = %d, want 1", len(got.Clusters))
	}
	if got.Clusters[0].BastionOCID != "ocid1.bastion.oc1..two" {
		t.Errorf("BastionOCID = %q, want the second (prompted) bastion — buffered input may have been lost between prompts", got.Clusters[0].BastionOCID)
	}
}

func TestOCIConfigPath_HonorsEnvVars(t *testing.T) {
	// Both env vars must be cleared first so the test is independent of the
	// developer's own environment; t.Setenv restores them after the test.
	t.Setenv("OCI_CLI_CONFIG_FILE", "")
	t.Setenv("OCI_CONFIG_FILE", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	defaultPath := filepath.Join(home, ".oci", "config")

	t.Run("default when no env set", func(t *testing.T) {
		got, err := ociConfigPath()
		if err != nil {
			t.Fatalf("ociConfigPath: %v", err)
		}
		if got != defaultPath {
			t.Errorf("path = %q, want default %q", got, defaultPath)
		}
	})

	t.Run("OCI_CONFIG_FILE wins over default", func(t *testing.T) {
		t.Setenv("OCI_CONFIG_FILE", "/custom/sdk/config")
		got, err := ociConfigPath()
		if err != nil {
			t.Fatalf("ociConfigPath: %v", err)
		}
		if got != "/custom/sdk/config" {
			t.Errorf("path = %q, want /custom/sdk/config", got)
		}
	})

	t.Run("OCI_CLI_CONFIG_FILE wins over OCI_CONFIG_FILE", func(t *testing.T) {
		t.Setenv("OCI_CONFIG_FILE", "/custom/sdk/config")
		t.Setenv("OCI_CLI_CONFIG_FILE", "/custom/cli/config")
		got, err := ociConfigPath()
		if err != nil {
			t.Fatalf("ociConfigPath: %v", err)
		}
		if got != "/custom/cli/config" {
			t.Errorf("path = %q, want /custom/cli/config", got)
		}
	})
}

// pickProfile is the Slice A leg of the init command: it reads sections from
// the OCI config, drives the prompt over the supplied reader/writer, and writes
// the resulting profile+method. End-to-end over fixtures, no live OCI. (The
// discovery leg is exercised by TestDiscoverAndConfigure_* against a fake.)
func TestPickProfile_WritesChosenProfileAndDetectedMethod(t *testing.T) {
	dir := t.TempDir()
	ociPath := filepath.Join(dir, "oci-config")
	const ociRaw = `[DEFAULT]
key_file=/d.pem

[TOKEN]
security_token_file=/s/token
`
	if err := os.WriteFile(ociPath, []byte(ociRaw), 0o600); err != nil {
		t.Fatalf("writing oci config: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")

	var out strings.Builder
	// Pick option 2 (TOKEN) → security_token.
	if _, err := pickProfile(bufio.NewReader(strings.NewReader("2\n")), &out, ociPath, cfgPath); err != nil {
		t.Fatalf("pickProfile: %v", err)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading written config: %v", err)
	}
	if got.Profile != "TOKEN" {
		t.Errorf("Profile = %q, want TOKEN", got.Profile)
	}
	if got.Method != ociauth.SecurityToken {
		t.Errorf("Method = %q, want %q", got.Method, ociauth.SecurityToken)
	}
	// The operator should be told where the config was written.
	if !strings.Contains(out.String(), cfgPath) {
		t.Errorf("output %q does not mention the written path %q", out.String(), cfgPath)
	}
}

func TestPickProfile_MissingOCIConfigErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := pickProfile(bufio.NewReader(strings.NewReader("1\n")), &strings.Builder{},
		filepath.Join(dir, "absent"), filepath.Join(dir, "config.yaml"))
	if err == nil {
		t.Fatal("expected an error when the OCI config is missing, got nil")
	}
}
