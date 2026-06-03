package cli

import (
	"path/filepath"
	"testing"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/store"
)

func tempStore(t *testing.T) *store.Store {
	t.Helper()
	return store.Open(filepath.Join(t.TempDir(), "bastions.json"))
}

const (
	clusterA = "ocid1.cluster.oc1..a"
	bastionX = "ocid1.bastion.oc1..x"
	bastionY = "ocid1.bastion.oc1..y"
)

func TestResolveBastion_FlagPersists(t *testing.T) {
	s := tempStore(t)
	got, err := resolveBastion(s, clusterA, bastionX)
	if err != nil {
		t.Fatalf("resolveBastion: %v", err)
	}
	if got != bastionX {
		t.Errorf("returned %q, want the flag value %q", got, bastionX)
	}
	// A later run without the flag must find the persisted mapping.
	stored, ok, _ := s.Get(clusterA)
	if !ok || stored != bastionX {
		t.Errorf("flag was not persisted: stored=%q ok=%v", stored, ok)
	}
}

func TestResolveBastion_FromStoreWhenFlagEmpty(t *testing.T) {
	s := tempStore(t)
	if err := s.Put(clusterA, bastionX); err != nil {
		t.Fatalf("seeding store: %v", err)
	}
	got, err := resolveBastion(s, clusterA, "")
	if err != nil {
		t.Fatalf("resolveBastion: %v", err)
	}
	if got != bastionX {
		t.Errorf("returned %q, want the stored value %q", got, bastionX)
	}
}

func TestResolveBastion_FlagOverridesStore(t *testing.T) {
	s := tempStore(t)
	if err := s.Put(clusterA, bastionX); err != nil {
		t.Fatalf("seeding store: %v", err)
	}
	got, err := resolveBastion(s, clusterA, bastionY)
	if err != nil {
		t.Fatalf("resolveBastion: %v", err)
	}
	if got != bastionY {
		t.Errorf("returned %q, want the overriding flag value %q", got, bastionY)
	}
	if stored, _, _ := s.Get(clusterA); stored != bastionY {
		t.Errorf("store not updated to flag value: stored=%q", stored)
	}
}

func TestResolveBastion_ErrorsWhenNeitherFlagNorStore(t *testing.T) {
	s := tempStore(t)
	if _, err := resolveBastion(s, clusterA, ""); err == nil {
		t.Fatal("expected an error when no flag and no stored mapping, got nil")
	}
}
