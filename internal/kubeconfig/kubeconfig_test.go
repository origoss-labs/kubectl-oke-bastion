package kubeconfig

import (
	"reflect"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

func TestAddBastionContext_LeavesOriginalUntouched(t *testing.T) {
	cfg, err := clientcmd.Load([]byte(okeKubeconfig))
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	origCluster := *cfg.Clusters["cluster-abc"]
	origCtx := *cfg.Contexts["ctx-abc"]
	origUser := *cfg.AuthInfos["user-abc"]
	origCurrent := cfg.CurrentContext

	if _, err := AddBastionContext(cfg, BastionWiring{
		OriginalContext: "ctx-abc",
		PrivateEndpoint: "10.0.0.6",
		LocalPort:       18443,
	}); err != nil {
		t.Fatalf("AddBastionContext returned error: %v", err)
	}

	if !reflect.DeepEqual(origCluster, *cfg.Clusters["cluster-abc"]) {
		t.Error("original cluster entry was mutated")
	}
	if !reflect.DeepEqual(origCtx, *cfg.Contexts["ctx-abc"]) {
		t.Error("original context entry was mutated")
	}
	if !reflect.DeepEqual(origUser, *cfg.AuthInfos["user-abc"]) {
		t.Error("original user entry was mutated")
	}
	if cfg.CurrentContext != origCurrent {
		t.Errorf("current-context changed to %q, want %q untouched", cfg.CurrentContext, origCurrent)
	}
}

func TestRemoveBastionContext_RestoresPreAddState(t *testing.T) {
	cfg, err := clientcmd.Load([]byte(okeKubeconfig))
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	before, err := clientcmd.Write(*cfg)
	if err != nil {
		t.Fatalf("serializing pre-add config: %v", err)
	}

	name, err := AddBastionContext(cfg, BastionWiring{
		OriginalContext: "ctx-abc",
		PrivateEndpoint: "10.0.0.6",
		LocalPort:       18443,
	})
	if err != nil {
		t.Fatalf("AddBastionContext returned error: %v", err)
	}
	if err := RemoveBastionContext(cfg, name); err != nil {
		t.Fatalf("RemoveBastionContext returned error: %v", err)
	}

	after, err := clientcmd.Write(*cfg)
	if err != nil {
		t.Fatalf("serializing post-remove config: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("config after add+remove differs from original:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

func TestAddBastionContext_OverwritesStaleLeftover(t *testing.T) {
	cfg, err := clientcmd.Load([]byte(okeKubeconfig))
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	// A crash left a -bastion entry pointing at a now-dead port.
	if _, err := AddBastionContext(cfg, BastionWiring{
		OriginalContext: "ctx-abc", PrivateEndpoint: "10.0.0.6", LocalPort: 11111,
	}); err != nil {
		t.Fatalf("seeding stale entry: %v", err)
	}

	name, err := AddBastionContext(cfg, BastionWiring{
		OriginalContext: "ctx-abc", PrivateEndpoint: "10.0.0.6", LocalPort: 22222,
	})
	if err != nil {
		t.Fatalf("AddBastionContext returned error: %v", err)
	}
	cl := cfg.Clusters[cfg.Contexts[name].Cluster]
	if cl.Server != "https://127.0.0.1:22222" {
		t.Errorf("stale entry not overwritten: Server = %q, want fresh port", cl.Server)
	}
}

func TestAddBastionContext_UnknownContext(t *testing.T) {
	cfg, err := clientcmd.Load([]byte(okeKubeconfig))
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	if _, err := AddBastionContext(cfg, BastionWiring{
		OriginalContext: "ghost", PrivateEndpoint: "10.0.0.6", LocalPort: 18443,
	}); err == nil {
		t.Fatal("expected an error for an unknown original context, got nil")
	}
}

func TestAddBastionContext_WiresLocalEndpoint(t *testing.T) {
	cfg, err := clientcmd.Load([]byte(okeKubeconfig))
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}

	name, err := AddBastionContext(cfg, BastionWiring{
		OriginalContext: "ctx-abc",
		PrivateEndpoint: "10.0.0.6",
		LocalPort:       18443,
	})
	if err != nil {
		t.Fatalf("AddBastionContext returned error: %v", err)
	}
	if name != "ctx-abc-bastion" {
		t.Errorf("returned context name = %q, want %q", name, "ctx-abc-bastion")
	}

	kctx, ok := cfg.Contexts[name]
	if !ok {
		t.Fatalf("config has no %q context", name)
	}
	if kctx.AuthInfo != "user-abc" {
		t.Errorf("bastion context AuthInfo = %q, want original user %q", kctx.AuthInfo, "user-abc")
	}

	cl, ok := cfg.Clusters[kctx.Cluster]
	if !ok {
		t.Fatalf("bastion context references unknown cluster %q", kctx.Cluster)
	}
	if cl.Server != "https://127.0.0.1:18443" {
		t.Errorf("bastion cluster Server = %q, want %q", cl.Server, "https://127.0.0.1:18443")
	}
	if cl.TLSServerName != "10.0.0.6" {
		t.Errorf("bastion cluster TLSServerName = %q, want %q", cl.TLSServerName, "10.0.0.6")
	}

	orig := cfg.Clusters["cluster-abc"]
	if string(cl.CertificateAuthorityData) != string(orig.CertificateAuthorityData) {
		t.Errorf("bastion cluster CA = %q, want reused original CA %q",
			cl.CertificateAuthorityData, orig.CertificateAuthorityData)
	}
}

// The bastion cluster must not share its CA backing array with the original;
// reflect.DeepEqual compares contents and would miss an alias, so mutate the
// bastion copy and assert the original is unaffected (ADR-0005).
func TestAddBastionContext_CADoesNotAliasOriginal(t *testing.T) {
	cfg, err := clientcmd.Load([]byte(okeKubeconfig))
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	orig := cfg.Clusters["cluster-abc"]
	want := append([]byte(nil), orig.CertificateAuthorityData...)

	name, err := AddBastionContext(cfg, BastionWiring{
		OriginalContext: "ctx-abc", PrivateEndpoint: "10.0.0.6", LocalPort: 18443,
	})
	if err != nil {
		t.Fatalf("AddBastionContext returned error: %v", err)
	}

	bastionCA := cfg.Clusters[cfg.Contexts[name].Cluster].CertificateAuthorityData
	if len(bastionCA) == 0 {
		t.Fatal("bastion cluster has no CA data")
	}
	bastionCA[0] ^= 0xff // corrupt the bastion copy

	if string(orig.CertificateAuthorityData) != string(want) {
		t.Error("mutating the bastion CA changed the original: the CA slice is aliased, not copied")
	}
}

// A realistic OKE kubeconfig as produced by
// `oci ce cluster create-kubeconfig --token-version 2.0.0`.
const okeKubeconfig = `apiVersion: v1
kind: Config
current-context: ctx-abc
clusters:
- name: cluster-abc
  cluster:
    server: https://10.0.0.6:6443
    certificate-authority-data: ZmFrZS1jYQ==
contexts:
- name: ctx-abc
  context:
    cluster: cluster-abc
    user: user-abc
users:
- name: user-abc
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: oci
      args:
      - ce
      - cluster
      - generate-token
      - --cluster-id
      - ocid1.cluster.oc1.eu-frankfurt-1.aaaa
      - --region
      - eu-frankfurt-1
`

func TestParse_NoCurrentContext(t *testing.T) {
	const noCurrent = `apiVersion: v1
kind: Config
clusters:
- name: cluster-abc
  cluster:
    server: https://10.0.0.6:6443
contexts:
- name: ctx-abc
  context:
    cluster: cluster-abc
    user: user-abc
`
	_, err := Parse([]byte(noCurrent))
	if err == nil {
		t.Fatal("expected an error when no current-context is set, got nil")
	}
}

func TestParse_NotAnOKECluster(t *testing.T) {
	// A perfectly valid kubeconfig whose user is a plain token, not an oci
	// exec credential — e.g. a non-OKE cluster the operator happens to be on.
	const nonOKE = `apiVersion: v1
kind: Config
current-context: ctx-abc
clusters:
- name: cluster-abc
  cluster:
    server: https://10.0.0.6:6443
contexts:
- name: ctx-abc
  context:
    cluster: cluster-abc
    user: user-abc
users:
- name: user-abc
  user:
    token: some-static-token
`
	_, err := Parse([]byte(nonOKE))
	if err == nil {
		t.Fatal("expected an error for a non-OKE context, got nil")
	}
}

func TestParse_MissingServer(t *testing.T) {
	const noServer = `apiVersion: v1
kind: Config
current-context: ctx-abc
clusters:
- name: cluster-abc
  cluster:
    certificate-authority-data: ZmFrZS1jYQ==
contexts:
- name: ctx-abc
  context:
    cluster: cluster-abc
    user: user-abc
users:
- name: user-abc
  user:
    exec:
      command: oci
      args:
      - --cluster-id
      - ocid1.cluster.oc1.eu-frankfurt-1.aaaa
      - --region
      - eu-frankfurt-1
`
	_, err := Parse([]byte(noServer))
	if err == nil {
		t.Fatal("expected an error when the cluster has no server, got nil")
	}
}

func TestParse_DanglingReferences(t *testing.T) {
	cases := map[string]string{
		"current-context names missing context": `apiVersion: v1
kind: Config
current-context: ghost
clusters:
- name: cluster-abc
  cluster:
    server: https://10.0.0.6:6443
`,
		"context references missing cluster": `apiVersion: v1
kind: Config
current-context: ctx-abc
contexts:
- name: ctx-abc
  context:
    cluster: ghost-cluster
    user: user-abc
`,
		"context references missing user": `apiVersion: v1
kind: Config
current-context: ctx-abc
clusters:
- name: cluster-abc
  cluster:
    server: https://10.0.0.6:6443
contexts:
- name: ctx-abc
  context:
    cluster: cluster-abc
    user: ghost-user
`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(raw)); err == nil {
				t.Fatalf("expected an error, got nil")
			}
		})
	}
}

func TestParse_NonOCIExecCredential(t *testing.T) {
	// A valid exec credential that is not the oci OKE token plugin, even though
	// it happens to carry a --cluster-id flag.
	const gkeStyle = `apiVersion: v1
kind: Config
current-context: ctx-abc
clusters:
- name: cluster-abc
  cluster:
    server: https://10.0.0.6:6443
contexts:
- name: ctx-abc
  context:
    cluster: cluster-abc
    user: user-abc
users:
- name: user-abc
  user:
    exec:
      command: gke-gcloud-auth-plugin
      args: [--cluster-id, not-an-ocid]
`
	if _, err := Parse([]byte(gkeStyle)); err == nil {
		t.Fatal("expected an error for a non-oci exec credential, got nil")
	}
}

func TestParse_MissingRegion(t *testing.T) {
	const noRegion = `apiVersion: v1
kind: Config
current-context: ctx-abc
clusters:
- name: cluster-abc
  cluster:
    server: https://10.0.0.6:6443
contexts:
- name: ctx-abc
  context:
    cluster: cluster-abc
    user: user-abc
users:
- name: user-abc
  user:
    exec:
      command: oci
      args: [ce, cluster, generate-token, --cluster-id, ocid1.cluster.oc1.eu-frankfurt-1.aaaa]
`
	if _, err := Parse([]byte(noRegion)); err == nil {
		t.Fatal("expected an error when --region is absent, got nil")
	}
}

func TestParse_FlagWithoutValue(t *testing.T) {
	// --region is the final arg with no value following it.
	const danglingFlag = `apiVersion: v1
kind: Config
current-context: ctx-abc
clusters:
- name: cluster-abc
  cluster:
    server: https://10.0.0.6:6443
contexts:
- name: ctx-abc
  context:
    cluster: cluster-abc
    user: user-abc
users:
- name: user-abc
  user:
    exec:
      command: oci
      args: [ce, cluster, generate-token, --cluster-id, ocid1.cluster.oc1.eu-frankfurt-1.aaaa, --region]
`
	if _, err := Parse([]byte(danglingFlag)); err == nil {
		t.Fatal("expected an error when --region has no value, got nil")
	}
}

func TestParse_OKEContext(t *testing.T) {
	got, err := Parse([]byte(okeKubeconfig))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	want := ClusterInfo{
		ContextName:     "ctx-abc",
		PrivateEndpoint: "10.0.0.6",
		ClusterOCID:     "ocid1.cluster.oc1.eu-frankfurt-1.aaaa",
		Region:          "eu-frankfurt-1",
	}
	if got != want {
		t.Errorf("Parse() = %+v, want %+v", got, want)
	}
}
