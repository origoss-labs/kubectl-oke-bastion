package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/ociauth"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{
			name: "profile and api_key method",
			cfg:  Config{Profile: "DEFAULT", Method: ociauth.APIKey},
		},
		{
			name: "profile and security_token method",
			cfg:  Config{Profile: "TOKEN", Method: ociauth.SecurityToken},
		},
		{
			// The clusters field is forward-compatible; an empty slice must
			// round-trip without becoming a non-nil/nil mismatch surprise.
			name: "zero config",
			cfg:  Config{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := Save(path, tc.cfg); err != nil {
				t.Fatalf("Save: %v", err)
			}
			got, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got.Profile != tc.cfg.Profile {
				t.Errorf("Profile = %q, want %q", got.Profile, tc.cfg.Profile)
			}
			if got.Method != tc.cfg.Method {
				t.Errorf("Method = %q, want %q", got.Method, tc.cfg.Method)
			}
			if len(got.Clusters) != len(tc.cfg.Clusters) {
				t.Errorf("Clusters len = %d, want %d", len(got.Clusters), len(tc.cfg.Clusters))
			}
		})
	}
}

func TestUpsertCluster_AppendsAndPreservesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, Config{Profile: "P", Method: ociauth.APIKey, Clusters: []Cluster{
		{ClusterOCID: "ocid1.cluster.oc1..a", KubeContext: "ctx-a"},
	}}); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	if err := UpsertCluster(path, Cluster{ClusterOCID: "ocid1.cluster.oc1..b", KubeContext: "ctx-b"}); err != nil {
		t.Fatalf("UpsertCluster: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Clusters) != 2 {
		t.Fatalf("clusters = %d, want 2 (existing preserved + new appended)", len(got.Clusters))
	}
	// The pre-existing entry must survive unchanged, the profile/method too.
	if got.Profile != "P" || got.Method != ociauth.APIKey {
		t.Errorf("profile/method changed: %+v", got)
	}
	if got.Clusters[0].ClusterOCID != "ocid1.cluster.oc1..a" {
		t.Errorf("existing cluster dropped or reordered: %+v", got.Clusters)
	}
	if got.Clusters[1].ClusterOCID != "ocid1.cluster.oc1..b" {
		t.Errorf("new cluster not appended: %+v", got.Clusters)
	}
}

func TestUpsertCluster_ReplacesSameOCID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, Config{Clusters: []Cluster{
		{ClusterOCID: "ocid1.cluster.oc1..a", BastionOCID: "ocid1.bastion.oc1..old", KubeContext: "ctx-a"},
	}}); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	// Re-running init for the same cluster with a new bastion must replace, not
	// duplicate.
	if err := UpsertCluster(path, Cluster{ClusterOCID: "ocid1.cluster.oc1..a", BastionOCID: "ocid1.bastion.oc1..new", KubeContext: "ctx-a"}); err != nil {
		t.Fatalf("UpsertCluster: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Clusters) != 1 {
		t.Fatalf("clusters = %d, want 1 (same OCID must replace, not append)", len(got.Clusters))
	}
	if got.Clusters[0].BastionOCID != "ocid1.bastion.oc1..new" {
		t.Errorf("bastion not updated on re-run: %+v", got.Clusters[0])
	}
}

func TestUpsertCluster_CreatesWhenAbsent(t *testing.T) {
	// A config that exists but has no clusters yet (the Slice A state): upsert
	// must add the first one.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, Config{Profile: "P", Method: ociauth.APIKey}); err != nil {
		t.Fatalf("seeding config: %v", err)
	}
	if err := UpsertCluster(path, Cluster{ClusterOCID: "ocid1.cluster.oc1..first"}); err != nil {
		t.Fatalf("UpsertCluster: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Clusters) != 1 || got.Clusters[0].ClusterOCID != "ocid1.cluster.oc1..first" {
		t.Errorf("first cluster not added: %+v", got.Clusters)
	}
}

func TestLoad_MissingFileIsZeroConfig(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("Load of a missing file should succeed with a zero config, got error: %v", err)
	}
	if got.Profile != "" || got.Method != "" || len(got.Clusters) != 0 {
		t.Errorf("missing file gave %+v, want a zero config", got)
	}
}

func TestLoad_CorruptFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	// Not YAML this struct can decode: a bare scalar where a mapping is wanted.
	if err := os.WriteFile(path, []byte("\t\t::: not: [valid: yaml"), 0o600); err != nil {
		t.Fatalf("writing corrupt fixture: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error loading a corrupt config, got nil")
	}
}

func TestSave_FilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, Config{Profile: "P", Method: ociauth.APIKey}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// 0600: the config names a profile but holds no secret; still, default to
	// owner-only like the store does.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 600", perm)
	}
}
