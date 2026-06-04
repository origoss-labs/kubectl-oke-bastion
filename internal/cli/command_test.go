package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/ociauth"
)

// A bare `kubectl oke bastion` (no subcommand) must print help and not error:
// the old derive-from-current-context foreground flow is retired (ADR-0011), so
// the root only routes to init/up/down/status.
func TestRootBareCommandPrintsHelp(t *testing.T) {
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("bare command returned error: %v", err)
	}
	got := out.String()
	// Usage must advertise the subcommands the operator needs to discover.
	for _, want := range []string{"Usage", "up", "down", "status", "init"} {
		if !strings.Contains(got, want) {
			t.Errorf("bare-command help missing %q; got:\n%s", want, got)
		}
	}
}

// `up` must wire the Slice D flags: --foreground (debug) and the CI auth
// overrides --profile / --instance-principal. Asserting the flags parse cleanly
// proves cobra wiring without touching disk or OCI.
func TestUpCmdFlags(t *testing.T) {
	cmd := newUpCmd()
	for _, name := range []string{"foreground", "profile", "instance-principal"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("up command is missing --%s flag", name)
		}
	}

	// The flags must parse without error (value binding wired correctly).
	if err := cmd.Flags().Parse([]string{"--foreground", "--profile", "ci", "--instance-principal"}); err != nil {
		t.Fatalf("parsing up flags: %v", err)
	}
	if got, _ := cmd.Flags().GetBool("foreground"); !got {
		t.Error("--foreground did not bind to true")
	}
	if got, _ := cmd.Flags().GetString("profile"); got != "ci" {
		t.Errorf("--profile = %q, want ci", got)
	}
	if got, _ := cmd.Flags().GetBool("instance-principal"); !got {
		t.Error("--instance-principal did not bind to true")
	}
}

// daemonArgs builds the argv `up` threads into the detached __daemon re-exec.
// The cluster key is always the positional; the CI auth flags are appended only
// when set. This is the seam that fixes "background up drops --profile/-ip".
func TestDaemonArgs(t *testing.T) {
	cases := []struct {
		name              string
		cluster           string
		profile           string
		instancePrincipal bool
		want              []string
	}{
		{"bare cluster only", "ctx", "", false, []string{"ctx"}},
		{"profile override", "ctx", "ci", false, []string{"ctx", "--profile", "ci"}},
		{"instance principal", "ctx", "", true, []string{"ctx", "--instance-principal"}},
		{"both overrides", "ctx", "ci", true, []string{"ctx", "--profile", "ci", "--instance-principal"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := daemonArgs(tc.cluster, tc.profile, tc.instancePrincipal)
			if len(got) != len(tc.want) {
				t.Fatalf("daemonArgs = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("daemonArgs[%d] = %q, want %q (full %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// The hidden __daemon command must accept the CI auth flags `up` threads into
// the re-exec argv and split them from the cluster positional, so a detached
// daemon resolves the overridden auth. This exercises the re-exec contract at
// the cobra layer (the actual fork/exec stays integration).
func TestDaemonCmdParsesThreadedFlags(t *testing.T) {
	cmd := newDaemonCmd()
	for _, name := range []string{"profile", "instance-principal"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("__daemon command is missing --%s flag", name)
		}
	}

	// Mirror exactly what up→Spawn produces (Spawn prepends "__daemon"):
	// the cluster key positional followed by the threaded override flags.
	argv := daemonArgs("my-ctx", "ci", true)
	if err := cmd.ParseFlags(argv); err != nil {
		t.Fatalf("__daemon ParseFlags(%v): %v", argv, err)
	}
	if got, _ := cmd.Flags().GetString("profile"); got != "ci" {
		t.Errorf("__daemon --profile = %q, want ci", got)
	}
	if got, _ := cmd.Flags().GetBool("instance-principal"); !got {
		t.Error("__daemon --instance-principal did not bind to true")
	}
	// The cluster key survives as the lone positional, not swallowed by a flag.
	if pos := cmd.Flags().Args(); len(pos) != 1 || pos[0] != "my-ctx" {
		t.Errorf("__daemon positional args = %v, want [my-ctx]", pos)
	}
}

// applyAuthOverrides must leave the configured spec untouched when no flag is
// set, and override profile/method when flags are supplied — the CI seam.
func TestApplyAuthOverrides(t *testing.T) {
	base := ociauth.Spec{Method: ociauth.APIKey, Profile: "configured"}

	if got := applyAuthOverrides(base, "", false); got != base {
		t.Errorf("no overrides changed the spec: %+v, want %+v", got, base)
	}
	if got := applyAuthOverrides(base, "ci", false); got.Profile != "ci" {
		t.Errorf("--profile override: Profile = %q, want ci", got.Profile)
	}
	got := applyAuthOverrides(base, "", true)
	if got.Method != ociauth.InstancePrincipal {
		t.Errorf("--instance-principal override: Method = %q, want instance_principal", got.Method)
	}
}
