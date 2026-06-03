package store

import (
	"os"
	"path/filepath"
	"testing"
)

// tempStore returns a Store backed by a fresh file under t.TempDir(), so each
// test gets an isolated mapping with no global state.
func tempStore(t *testing.T) *Store {
	t.Helper()
	return Open(filepath.Join(t.TempDir(), "bastions.json"))
}

func TestPutGet_RoundTrip(t *testing.T) {
	s := tempStore(t)
	if err := s.Put("ocid1.cluster..a", "ocid1.bastion..x"); err != nil {
		t.Fatalf("Put returned error: %v", err)
	}

	got, ok, err := s.Get("ocid1.cluster..a")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if !ok {
		t.Fatal("Get reported the key missing right after Put")
	}
	if got != "ocid1.bastion..x" {
		t.Errorf("Get = %q, want %q", got, "ocid1.bastion..x")
	}
}

func TestGet_MissingKeyIsNotAnError(t *testing.T) {
	s := tempStore(t)
	// A cluster never mapped, against a store file that does not yet exist.
	got, ok, err := s.Get("ocid1.cluster..unknown")
	if err != nil {
		t.Fatalf("Get returned error for a missing key: %v", err)
	}
	if ok {
		t.Errorf("Get reported found=true for an unmapped cluster (value %q)", got)
	}
}

func TestPut_OverwritesExistingMapping(t *testing.T) {
	s := tempStore(t)
	if err := s.Put("ocid1.cluster..a", "ocid1.bastion..old"); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if err := s.Put("ocid1.cluster..a", "ocid1.bastion..new"); err != nil {
		t.Fatalf("second Put: %v", err)
	}

	got, _, err := s.Get("ocid1.cluster..a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "ocid1.bastion..new" {
		t.Errorf("Get = %q, want the updated value %q", got, "ocid1.bastion..new")
	}
}

func TestPut_KeepsOtherClusters(t *testing.T) {
	s := tempStore(t)
	if err := s.Put("ocid1.cluster..a", "ocid1.bastion..x"); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	if err := s.Put("ocid1.cluster..b", "ocid1.bastion..y"); err != nil {
		t.Fatalf("Put b: %v", err)
	}

	// The second Put must not clobber the first cluster's mapping.
	got, ok, err := s.Get("ocid1.cluster..a")
	if err != nil || !ok {
		t.Fatalf("Get a: ok=%v err=%v", ok, err)
	}
	if got != "ocid1.bastion..x" {
		t.Errorf("cluster a = %q, want %q after mapping a second cluster", got, "ocid1.bastion..x")
	}
}

func TestPut_CreatesMissingParentDir(t *testing.T) {
	// The real store lives under a per-app config subdir that won't exist on a
	// first run; Put must create it rather than fail.
	path := filepath.Join(t.TempDir(), "kubectl-oke-bastion", "bastions.json")
	s := Open(path)
	if err := s.Put("ocid1.cluster..a", "ocid1.bastion..x"); err != nil {
		t.Fatalf("Put into a missing dir: %v", err)
	}
	if _, ok, _ := s.Get("ocid1.cluster..a"); !ok {
		t.Error("value not persisted after creating the parent dir")
	}
}

func TestGet_CorruptFileErrorsNotPanics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bastions.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("seeding corrupt file: %v", err)
	}
	s := Open(path)

	if _, _, err := s.Get("ocid1.cluster..a"); err == nil {
		t.Fatal("expected an error reading a corrupt store file, got nil")
	}
}
