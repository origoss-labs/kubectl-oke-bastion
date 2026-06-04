package cli

import (
	"strings"
	"testing"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/config"
)

// resolveCluster picks which configured cluster a daemon command targets. The
// addressable name is the kube context (config.Cluster.KubeContext). It is the
// only unit-testable seam of the daemon cli wiring (the commands themselves
// re-exec/signal), so it carries the defaulting rules.
func TestResolveCluster(t *testing.T) {
	cfg := func(ctxs ...string) config.Config {
		c := config.Config{}
		for _, name := range ctxs {
			c.Clusters = append(c.Clusters, config.Cluster{KubeContext: name})
		}
		return c
	}

	t.Run("zero clusters errors", func(t *testing.T) {
		if _, err := resolveCluster(cfg(), ""); err == nil {
			t.Fatal("expected an error with no clusters configured, got nil")
		}
	})

	t.Run("one cluster defaults when no arg", func(t *testing.T) {
		got, err := resolveCluster(cfg("only"), "")
		if err != nil {
			t.Fatalf("resolveCluster: %v", err)
		}
		if got.KubeContext != "only" {
			t.Errorf("KubeContext = %q, want only", got.KubeContext)
		}
	})

	t.Run("many clusters with no arg errors and lists them", func(t *testing.T) {
		_, err := resolveCluster(cfg("a", "b"), "")
		if err == nil {
			t.Fatal("expected an error with many clusters and no arg, got nil")
		}
		// The error must name the candidates so the operator knows what to pass.
		if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
			t.Errorf("error %q does not list the configured contexts a, b", err)
		}
	})

	t.Run("explicit arg selects that cluster", func(t *testing.T) {
		got, err := resolveCluster(cfg("a", "b"), "b")
		if err != nil {
			t.Fatalf("resolveCluster: %v", err)
		}
		if got.KubeContext != "b" {
			t.Errorf("KubeContext = %q, want b", got.KubeContext)
		}
	})

	t.Run("explicit arg that matches nothing errors", func(t *testing.T) {
		if _, err := resolveCluster(cfg("a", "b"), "nope"); err == nil {
			t.Fatal("expected an error for an unknown cluster arg, got nil")
		}
	})
}
