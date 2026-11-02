package backup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// symlinkedTmp builds a staging tempDir reached through a symlinked directory, the
// way it is when /tmp is a link (to /var/tmp, say): it returns the directory the link
// points at and the staging path through the link.
func symlinkedTmp(t *testing.T) (real, staging string) {
	t.Helper()
	base := t.TempDir()
	real = filepath.Join(base, "var-tmp")
	if err := os.MkdirAll(filepath.Join(real, "proxsave", "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(base, "tmp")); err != nil {
		t.Fatal(err)
	}
	return real, filepath.Join(base, "tmp", "proxsave", "run")
}

// A link whose target holds the staging tree is refused: its copy would copy the
// staging tree into itself. Under a symlinked /tmp the resolved target never matched
// the lexical tempDir, so a /home link to the link's target was followed and the
// staging tree copied into itself until the 4096-byte path limit.
func TestSymlinkedDirectoryRefusesTheStagingTreeUnderASymlinkedTmp(t *testing.T) {
	real, staging := symlinkedTmp(t)
	home := t.TempDir()
	link := filepath.Join(home, "alice")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	c := NewCollectorWithDeps(logging.New(types.LogLevelError, false), GetDefaultCollectorConfig(), staging, types.ProxmoxVE, false, CollectorDeps{})
	if target, _, ok := c.symlinkedDirectory(link); ok {
		t.Fatalf("symlinkedDirectory(%s) = %s, want refused: it holds the staging tree %s", link, target, staging)
	}
}

// The #56 guard: while CUSTOM_BACKUP_PATHS are copied, a source inside the staging
// tree is skipped. A source reached through the link's target (/var/tmp under a
// symlinked /tmp) is inside it too.
func TestCustomPathGuardMatchesTheResolvedStagingTree(t *testing.T) {
	real, staging := symlinkedTmp(t)
	c := NewCollectorWithDeps(logging.New(types.LogLevelError, false), GetDefaultCollectorConfig(), staging, types.ProxmoxVE, false, CollectorDeps{})
	c.collectingCustomPaths = true
	for _, path := range []string{
		filepath.Join(staging, "etc"),
		filepath.Join(real, "proxsave", "run", "etc"),
		filepath.Join(real, "proxsave", "run"),
	} {
		if !c.isWithinStagingDir(path) {
			t.Errorf("isWithinStagingDir(%s) = false, want true: the staging tree is %s", path, staging)
		}
	}
	if c.isWithinStagingDir(filepath.Join(real, "other")) {
		t.Errorf("isWithinStagingDir(%s) = true outside the staging tree", filepath.Join(real, "other"))
	}
}

// SYSTEM_ROOT_PREFIX given as a symlink: the resolved target was compared with the
// lexical prefix, never matched, and the link's directory was not collected.
func TestSymlinkedDirectoryUnderASymlinkedSystemRootPrefix(t *testing.T) {
	base := t.TempDir()
	hostRoot := filepath.Join(base, "host-real")
	if err := os.MkdirAll(filepath.Join(hostRoot, "data", "home", "alice"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(hostRoot, "home"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(hostRoot, "home", "alice")
	if err := os.Symlink("/data/home/alice", link); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(base, "host")
	if err := os.Symlink(hostRoot, prefix); err != nil {
		t.Fatal(err)
	}
	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = prefix
	c := NewCollectorWithDeps(logging.New(types.LogLevelError, false), cfg, t.TempDir(), types.ProxmoxVE, false, CollectorDeps{})
	target, logical, ok := c.symlinkedDirectory(filepath.Join(prefix, "home", "alice"))
	if !ok || logical != "/data/home/alice" || target != filepath.Join(hostRoot, "data", "home", "alice") {
		t.Fatalf("symlinkedDirectory = %q, %q, %v; want %s, /data/home/alice, true", target, logical, ok, filepath.Join(hostRoot, "data", "home", "alice"))
	}
}
