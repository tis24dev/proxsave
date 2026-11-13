package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// An operator exclusion that matched the dedup manifest (*.json, *manifest*,
// /var/lib/**) dropped it from the archive while the dedup symlinks shipped: on restore
// the duplicates came back as links, or dangling ones, with no error. Dedup is now
// skipped for such a run, with an INFO line naming the pattern.
func TestDedupIsSkippedWhenTheManifestWouldBeExcluded(t *testing.T) {
	for _, tc := range []struct {
		patterns    []string
		wantSymlink bool
	}{
		{nil, true},
		{[]string{"*.json"}, false},
		{[]string{"/var/lib/**"}, false},
	} {
		root := t.TempDir()
		for _, user := range []string{"alice", "bob"} {
			dir := filepath.Join(root, "home", user)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".profile"), []byte("same content for both users\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		var out bytes.Buffer
		logger := logging.New(types.LogLevelDebug, false)
		logger.SetOutput(&out)
		cfg := OptimizationConfig{EnableDeduplication: true, ExcludePatterns: tc.patterns}
		if _, err := ApplyOptimizations(context.Background(), logger, root, cfg); err != nil {
			t.Fatalf("%v: ApplyOptimizations: %v", tc.patterns, err)
		}
		links := 0
		for _, user := range []string{"alice", "bob"} {
			if fi, err := os.Lstat(filepath.Join(root, "home", user, ".profile")); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				links++
			}
		}
		if (links > 0) != tc.wantSymlink {
			t.Fatalf("%v: dedup symlinks=%d, want symlink=%v; log:\n%s", tc.patterns, links, tc.wantSymlink, out.String())
		}
		if !tc.wantSymlink && !strings.Contains(out.String(), "Deduplication skipped: the dedup manifest var/lib/proxsave-info/dedup_manifest.json matches exclusion pattern "+tc.patterns[0]) {
			t.Fatalf("%v: no skip line; log:\n%s", tc.patterns, out.String())
		}
	}
}
