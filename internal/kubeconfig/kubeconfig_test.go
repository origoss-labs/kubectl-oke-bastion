package kubeconfig

import "testing"

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

// Same cluster, but the oci exec args use the --flag=value form.
const okeKubeconfigEqualsForm = `apiVersion: v1
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
      - --cluster-id=ocid1.cluster.oc1.eu-frankfurt-1.aaaa
      - --region=eu-frankfurt-1
`

func TestParse_EqualsFormFlags(t *testing.T) {
	got, err := Parse([]byte(okeKubeconfigEqualsForm))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if got.ClusterOCID != "ocid1.cluster.oc1.eu-frankfurt-1.aaaa" || got.Region != "eu-frankfurt-1" {
		t.Errorf("equals-form not parsed: got OCID=%q region=%q", got.ClusterOCID, got.Region)
	}
}

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
