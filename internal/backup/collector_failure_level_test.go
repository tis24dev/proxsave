package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A REAL copy failure of a single critical file (/etc/shadow class), of
// /etc/network/interfaces, or of a CUSTOM_BACKUP_PATHS entry was Debug-only since
// the initial Go port: WarningCount, exit code, notifications and healthchecks all
// stayed green while the archive silently lacked the file. The same recipe already
// warns loudly for fstab, logrotate and mount units, so these were the odd mute
// ones out, not a doctrine. Not-found stays silent by design (safeCopyFile returns
// nil for it); only real failures speak.

func levelOf(line string) string {
	_, rest, found := strings.Cut(line, "] ")
	if !found {
		return ""
	}
	f := strings.Fields(rest)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func warningsMatching(out, needle string) []string {
	var hits []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, needle) && levelOf(l) == "WARNING" {
			hits = append(hits, l)
		}
	}
	return hits
}

func failureLevelCollector(t *testing.T, sysRoot string, cfg *CollectorConfig) (*Collector, *bytes.Buffer) {
	t.Helper()
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	cfg.SystemRootPrefix = sysRoot
	return NewCollector(logger, cfg, t.TempDir(), types.ProxmoxVE, false), buf
}

func writeUnreadable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
}

func TestCriticalFileCopyFailureIsAWarning(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs non-root so mode 0000 denies the read")
	}
	sysRoot := t.TempDir()
	writeUnreadable(t, filepath.Join(sysRoot, "etc", "shadow"))

	c, buf := failureLevelCollector(t, sysRoot, &CollectorConfig{})
	if err := c.collectCriticalFiles(context.Background()); err != nil {
		t.Fatalf("collectCriticalFiles: %v", err)
	}
	out := buf.String()

	if hits := warningsMatching(out, "Failed to copy critical file"); len(hits) != 1 || !strings.Contains(hits[0], "etc/shadow") {
		t.Fatalf("an unreadable /etc/shadow must produce exactly one WARNING naming it, got %d:\n%s", len(hits), out)
	}
	// passwd/group/gshadow/sudoers are simply absent from this root: not-found
	// stays silent, no warning may name them.
	for _, quiet := range []string{"passwd", "gshadow", "sudoers"} {
		if hits := warningsMatching(out, quiet); len(hits) != 0 {
			t.Fatalf("a missing %s warned; not-found must stay silent:\n%s", quiet, out)
		}
	}
}

func TestInterfacesCopyFailureWarnsAndTellsTheTruth(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs non-root so mode 0000 denies the read")
	}
	sysRoot := t.TempDir()
	writeUnreadable(t, filepath.Join(sysRoot, "etc", "network", "interfaces"))

	c, buf := failureLevelCollector(t, sysRoot, &CollectorConfig{BackupNetworkConfigs: true})
	if err := c.collectSystemNetworkStatic(context.Background()); err != nil {
		t.Fatalf("collectSystemNetworkStatic: %v", err)
	}
	out := buf.String()

	if hits := warningsMatching(out, "Failed to collect /etc/network/interfaces:"); len(hits) != 1 {
		t.Fatalf("an unreadable interfaces file must WARN with the real cause, got %d:\n%s", len(hits), out)
	}
	if strings.Contains(out, "No /etc/network/interfaces found") {
		t.Fatalf("the lying not-found message is still emitted for a real failure:\n%s", out)
	}
}

func TestCustomPathCopyFailureIsAWarning(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs non-root so mode 0000 denies the read")
	}
	sysRoot := t.TempDir()
	writeUnreadable(t, filepath.Join(sysRoot, "data", "app.key"))

	c, buf := failureLevelCollector(t, sysRoot, &CollectorConfig{CustomBackupPaths: []string{"/data/app.key"}})
	if err := c.collectCustomPaths(context.Background()); err != nil {
		t.Fatalf("collectCustomPaths: %v", err)
	}
	if hits := warningsMatching(buf.String(), "Failed to copy custom file"); len(hits) != 1 || !strings.Contains(hits[0], "app.key") {
		t.Fatalf("an unreadable custom file must WARN naming it, got %d:\n%s", len(hits), buf.String())
	}
}

// When a brick returns an error the recipe aborts, and everything scheduled after
// it used to vanish with no trace at any level (nor in the manifest: bricks that
// never ran record nothing). The abort now names the recipe, the failed brick and
// how many bricks never ran, so the absence is visible in the log.
func TestRunRecipeAbortTracesUnattemptedBricks(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	c := NewCollector(logger, &CollectorConfig{}, t.TempDir(), types.ProxmoxVE, false)
	state := newCollectionState(c)

	ran := []string{}
	mk := func(id string, fail bool) collectionBrick {
		return brick(BrickID(id), id, func(context.Context, *collectionState) error {
			ran = append(ran, id)
			if fail {
				return errors.New("boom")
			}
			return nil
		})
	}
	r := recipe{Name: "system", Bricks: []collectionBrick{mk("a", false), mk("b", true), mk("c", false), mk("d", false)}}

	if err := runRecipe(context.Background(), r, state); err == nil {
		t.Fatal("runRecipe must still propagate the brick error")
	}
	if len(ran) != 2 {
		t.Fatalf("bricks ran = %v; the abort semantics must not change", ran)
	}
	out := buf.String()
	hits := warningsMatching(out, "aborted at brick")
	if len(hits) != 1 || !strings.Contains(hits[0], "system") || !strings.Contains(hits[0], "\"b\"") || !strings.Contains(hits[0], "2 ") {
		t.Fatalf("the abort must WARN naming recipe, failed brick and the 2 unattempted bricks, got:\n%s", out)
	}
	for _, id := range []string{"c", "d"} {
		found := false
		for _, l := range strings.Split(out, "\n") {
			if levelOf(l) == "DEBUG" && strings.Contains(l, "never ran") && strings.Contains(l, "\""+id+"\"") {
				found = true
			}
		}
		if !found {
			t.Fatalf("unattempted brick %q has no Debug trace:\n%s", id, out)
		}
	}
}
