package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The directory snapshot lists the entries by name.
func TestAppendDirSnapshotListsEntriesByName(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })
	restoreFS = osFS{}
	dir := t.TempDir()
	for _, name := range []string{"b.cfg", "a.cfg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	var b strings.Builder
	appendDirSnapshot(nil, "test", &b, dir, 1024)
	out := b.String()
	a, bb := strings.Index(out, "- a.cfg ("), strings.Index(out, "- b.cfg (")
	if a < 0 || bb < 0 || a > bb {
		t.Fatalf("want a.cfg listed before b.cfg, got:\n%s", out)
	}
}
