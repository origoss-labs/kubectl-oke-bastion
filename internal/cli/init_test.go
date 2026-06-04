package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/config"
	"github.com/origoss-labs/kubectl-oke-bastion/internal/ociauth"
)

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

// runInit is the testable core of the init command: it reads sections from the
// OCI config, drives the prompt over the supplied reader/writer, and writes the
// resulting config. End-to-end over fixtures, no live OCI.
func TestRunInit_WritesChosenProfileAndDetectedMethod(t *testing.T) {
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
	if err := runInit(strings.NewReader("2\n"), &out, ociPath, cfgPath); err != nil {
		t.Fatalf("runInit: %v", err)
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

func TestRunInit_MissingOCIConfigErrors(t *testing.T) {
	dir := t.TempDir()
	err := runInit(strings.NewReader("1\n"), &strings.Builder{},
		filepath.Join(dir, "absent"), filepath.Join(dir, "config.yaml"))
	if err == nil {
		t.Fatal("expected an error when the OCI config is missing, got nil")
	}
}
