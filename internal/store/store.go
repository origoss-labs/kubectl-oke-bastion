// Package store persists the cluster-OCID → bastion-OCID mapping so that
// --bastion-id need only be supplied once. The mapping lives in a small JSON
// file under the user config dir; it is plumbing, not a security boundary —
// the bastion OCID it caches is not a secret.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Store reads and writes the cluster→bastion mapping at a fixed file path.
type Store struct {
	path string
}

// Open returns a Store backed by the file at path. The file need not exist yet;
// it is created on the first Put.
func Open(path string) *Store {
	return &Store{path: path}
}

// DefaultPath is the standard store location under the user config dir
// (e.g. ~/.config/kubectl-oke-bastion/bastions.json on Linux).
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating user config dir: %w", err)
	}
	return filepath.Join(dir, "kubectl-oke-bastion", "bastions.json"), nil
}

// Put records bastionOCID as the bastion for clusterOCID, creating or updating
// the mapping file. An existing entry for the cluster is overwritten.
func (s *Store) Put(clusterOCID, bastionOCID string) error {
	m, err := s.load()
	if err != nil {
		return err
	}
	m[clusterOCID] = bastionOCID
	return s.save(m)
}

// Get returns the bastion OCID stored for clusterOCID. The bool is false when
// no mapping exists for the cluster; a corrupt file is reported as an error.
func (s *Store) Get(clusterOCID string) (string, bool, error) {
	m, err := s.load()
	if err != nil {
		return "", false, err
	}
	v, ok := m[clusterOCID]
	return v, ok, nil
}

// load reads the mapping file, treating a missing file as an empty mapping.
func (s *Store) load() (map[string]string, error) {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading bastion store %s: %w", s.path, err)
	}
	m := map[string]string{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("bastion store %s is corrupt: %w", s.path, err)
	}
	return m, nil
}

// save writes the mapping back to the file.
func (s *Store) save(m map[string]string) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding bastion store: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("creating bastion store dir: %w", err)
	}
	if err := os.WriteFile(s.path, raw, 0o600); err != nil {
		return fmt.Errorf("writing bastion store %s: %w", s.path, err)
	}
	return nil
}
