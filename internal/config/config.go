// Package config persists the operator's init choices as a single config.yaml —
// the source of truth for later commands (ADR-0011). This slice writes only the
// chosen profile and its detected auth method; the clusters list is defined now
// for forward compatibility (later slices append a discovered cluster, region,
// compartment, bastion, and kube context per entry) but stays empty here.
//
// Like the bastion store, the file is owner-only plumbing, not a security
// boundary: it names a profile and OCIDs, no secrets. It is written atomically
// (temp file + rename) so a crash mid-write can never leave a truncated file
// that load would reject and lock the operator out of their config.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/ociauth"
)

// Config is the on-disk init result.
type Config struct {
	// Profile is the chosen ~/.oci/config section name.
	Profile string `yaml:"profile"`
	// Method is the auth method detected for Profile (api_key or security_token).
	Method ociauth.Method `yaml:"method"`
	// Clusters is the forward-compatible list of configured tunnel targets;
	// empty in this slice (init does no cluster discovery yet).
	Clusters []Cluster `yaml:"clusters"`
}

// Cluster is one configured tunnel target. It is defined now so the file shape
// is stable across slices; no field is populated until cluster/bastion
// discovery lands.
type Cluster struct {
	ClusterOCID     string `yaml:"cluster_ocid"`
	Region          string `yaml:"region"`
	CompartmentOCID string `yaml:"compartment_ocid"`
	BastionOCID     string `yaml:"bastion_ocid"`
	KubeContext     string `yaml:"kube_context"`
}

// DefaultPath is the standard config location under the user config dir
// (e.g. ~/.config/kubectl-oke-bastion/config.yaml on Linux), mirroring
// store.DefaultPath so the two files sit side by side.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating user config dir: %w", err)
	}
	return filepath.Join(dir, "kubectl-oke-bastion", "config.yaml"), nil
}

// Load reads the config at path. A missing file is not an error — it yields a
// zero Config so a fresh install reads as "nothing configured yet". A file that
// exists but cannot be decoded is reported as a wrapped error, never a panic.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("config %s is corrupt: %w", path, err)
	}
	return cfg, nil
}

// Save writes cfg to path atomically: it marshals to YAML, writes a temp file
// in the same dir with owner-only perms, then renames it into place. The
// directory is created 0700 and the file 0600, matching the bastion store.
func Save(path string, cfg Config) error {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("creating config temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing config temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting config permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing config temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing config %s: %w", path, err)
	}
	return nil
}
