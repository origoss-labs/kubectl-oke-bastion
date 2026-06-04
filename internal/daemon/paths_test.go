package daemon

import (
	"path/filepath"
	"strings"
	"testing"
)

// Paths must place every per-cluster file under one cluster-named subdir of the
// injected base, so a temp base fully isolates tests from the real config dir.
func TestPaths_AllUnderClusterDirOfBase(t *testing.T) {
	base := t.TempDir()
	p := NewPaths(base, "prod")

	wantDir := filepath.Join(base, "daemons", "prod")
	if p.Dir() != wantDir {
		t.Errorf("Dir() = %q, want %q", p.Dir(), wantDir)
	}
	for name, got := range map[string]string{
		"state": p.State(),
		"pid":   p.PID(),
		"log":   p.Log(),
	} {
		if filepath.Dir(got) != wantDir {
			t.Errorf("%s path %q is not under cluster dir %q", name, got, wantDir)
		}
	}
	// The three files must be distinct, or one would clobber another.
	if p.State() == p.PID() || p.State() == p.Log() || p.PID() == p.Log() {
		t.Errorf("state/pid/log paths collide: %q %q %q", p.State(), p.PID(), p.Log())
	}
}

// A kube-context name can contain a slash (e.g. "ctx/sub"); using it raw would
// escape the cluster dir, so it must be sanitized into a single path element.
func TestPaths_SanitizesClusterKeyWithSeparators(t *testing.T) {
	base := t.TempDir()
	p := NewPaths(base, "ctx/with/slashes")

	rel, err := filepath.Rel(filepath.Join(base, "daemons"), p.Dir())
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if strings.Contains(rel, string(filepath.Separator)) {
		t.Errorf("cluster dir %q escaped the daemons dir (rel %q has a separator)", p.Dir(), rel)
	}
}
