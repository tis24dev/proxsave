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
	for l := range strings.SplitSeq(out, "\n") {
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

func TestOptionalNetworkCopyFailuresAreWarnings(t *testing.T) {
	tests := []struct {
		path             string
		isDir            bool
		warningNeedle    string
		falseMissingText string
	}{
		{
			path:          "/etc/cloud/cloud.cfg.d/99-disable-network-config.cfg",
			warningNeedle: "Failed to collect /etc/cloud/cloud.cfg.d/99-disable-network-config.cfg:",
		},
		{
			path:          "/etc/dnsmasq.d/lxc-vmbr1.conf",
			warningNeedle: "Failed to collect /etc/dnsmasq.d/lxc-vmbr1.conf:",
		},
		{
			path:             "/etc/netplan",
			isDir:            true,
			warningNeedle:    "Failed to copy directory Netplan configuration:",
			falseMissingText: "No /etc/netplan found",
		},
		{
			path:             "/etc/systemd/network",
			isDir:            true,
			warningNeedle:    "Failed to copy directory systemd-networkd configuration:",
			falseMissingText: "No /etc/systemd/network found",
		},
		{
			path:             "/etc/NetworkManager/system-connections",
			isDir:            true,
			warningNeedle:    "Failed to copy directory NetworkManager connections:",
			falseMissingText: "No NetworkManager system-connections found",
		},
	}

	for _, tt := range tests {
		t.Run(strings.TrimPrefix(tt.path, "/etc/"), func(t *testing.T) {
			sysRoot := t.TempDir()
			source := filepath.Join(sysRoot, strings.TrimPrefix(tt.path, "/"))
			if tt.isDir {
				if err := os.MkdirAll(source, 0o755); err != nil {
					t.Fatalf("create source directory: %v", err)
				}
				if err := os.WriteFile(filepath.Join(source, "entry.conf"), []byte("data\n"), 0o600); err != nil {
					t.Fatalf("create source entry: %v", err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
					t.Fatalf("create source parent: %v", err)
				}
				if err := os.WriteFile(source, []byte("data\n"), 0o600); err != nil {
					t.Fatalf("create source file: %v", err)
				}
			}

			c, buf := failureLevelCollector(t, sysRoot, &CollectorConfig{BackupNetworkConfigs: true})
			destination := filepath.Join(c.tempDir, strings.TrimPrefix(tt.path, "/"))
			if tt.isDir {
				if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
					t.Fatalf("create destination parent: %v", err)
				}
				if err := os.WriteFile(destination, []byte("block"), 0o600); err != nil {
					t.Fatalf("create destination blocker: %v", err)
				}
			} else if err := os.MkdirAll(destination, 0o755); err != nil {
				t.Fatalf("create destination blocker: %v", err)
			}

			if err := c.collectSystemNetworkStatic(context.Background()); err != nil {
				t.Fatalf("collectSystemNetworkStatic: %v", err)
			}
			out := buf.String()
			if hits := warningsMatching(out, tt.warningNeedle); len(hits) != 1 {
				t.Fatalf("a real copy failure for %s must produce one WARNING with its cause, got %d:\n%s", tt.path, len(hits), buf.String())
			}
			if tt.falseMissingText != "" && strings.Contains(out, tt.falseMissingText) {
				t.Fatalf("a real copy failure for %s was mislabeled as missing:\n%s", tt.path, out)
			}
		})
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
		for l := range strings.SplitSeq(out, "\n") {
			if levelOf(l) == "DEBUG" && strings.Contains(l, "never ran") && strings.Contains(l, "\""+id+"\"") {
				found = true
			}
		}
		if !found {
			t.Fatalf("unattempted brick %q has no Debug trace:\n%s", id, out)
		}
	}
}

// A custom path below a directory the backup already holds as a symlink with an
// absolute target (/opt/app -> /data/app) is not copied through the link: the copy
// would write into the live host, truncating the very file it reads and giving its
// directory the symlink's 0777. It WARNs instead, and the live tree is untouched.
func TestCustomPathUnderAStagedSymlinkLeavesTheLiveTreeAlone(t *testing.T) {
	sysRoot := t.TempDir()
	live := filepath.Join(sysRoot, "data", "app")
	if err := os.MkdirAll(filepath.Join(live, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"config.yml", "sub/inner.yml"} {
		if err := os.WriteFile(filepath.Join(live, f), []byte("live content of "+f), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(sysRoot, "opt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(live, filepath.Join(sysRoot, "opt", "app")); err != nil {
		t.Fatal(err)
	}

	c, buf := failureLevelCollector(t, sysRoot, &CollectorConfig{CustomBackupPaths: []string{"/opt/app", "/opt/app/config.yml", "/opt/app/sub"}})
	if err := c.collectCustomPaths(context.Background()); err != nil {
		t.Fatalf("collectCustomPaths: %v", err)
	}
	for _, f := range []string{"config.yml", "sub/inner.yml"} {
		if got, err := os.ReadFile(filepath.Join(live, f)); err != nil || string(got) != "live content of "+f {
			t.Fatalf("live %s = %q (err %v): the copy wrote into the live host", f, got, err)
		}
	}
	for _, d := range []string{live, filepath.Join(live, "sub")} {
		if st, err := os.Stat(d); err != nil || st.Mode().Perm() != 0o750 {
			t.Fatalf("live %s mode %v err %v, want 0750 untouched", d, st.Mode().Perm(), err)
		}
	}
	for _, want := range []string{
		"Failed to copy custom file " + filepath.Join(sysRoot, "opt/app/config.yml") + ": it is under /opt/app, a symlink in the backup",
		"Failed to copy custom directory " + filepath.Join(sysRoot, "opt/app/sub") + ": it is under /opt/app, a symlink in the backup",
	} {
		if hits := warningsMatching(buf.String(), want); len(hits) != 1 {
			t.Fatalf("want one WARNING %q, got %d:\n%s", want, len(hits), buf.String())
		}
	}
}

// A relative directory symlink that stays inside the staging tree (/opt/cur -> app)
// is followed as before: a custom path below it is copied into the staging tree.
func TestCustomPathUnderAStagedRelativeSymlinkIsCopied(t *testing.T) {
	sysRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysRoot, "opt", "app"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysRoot, "opt", "app", "config.yml"), []byte("cfg"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("app", filepath.Join(sysRoot, "opt", "cur")); err != nil {
		t.Fatal(err)
	}
	c, buf := failureLevelCollector(t, sysRoot, &CollectorConfig{CustomBackupPaths: []string{"/opt/app", "/opt/cur", "/opt/cur/config.yml"}})
	if err := c.collectCustomPaths(context.Background()); err != nil {
		t.Fatalf("collectCustomPaths: %v", err)
	}
	if hits := warningsMatching(buf.String(), "Failed to copy custom"); len(hits) != 0 {
		t.Fatalf("no warning expected for a link inside the staging tree, got:\n%s", buf.String())
	}
	if got, err := os.ReadFile(filepath.Join(c.tempDir, "opt", "app", "config.yml")); err != nil || string(got) != "cfg" {
		t.Fatalf("staged config.yml = %q (err %v)", got, err)
	}
}

// A relative link that points at a staged link with an absolute target (/opt/cur ->
// app, /opt/app -> /data/app) leads out of the staging tree too: nothing below it is
// written into the live host.
func TestCustomPathUnderAChainOfStagedSymlinksLeavesTheLiveTreeAlone(t *testing.T) {
	sysRoot := t.TempDir()
	live := filepath.Join(sysRoot, "data", "app")
	if err := os.MkdirAll(live, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "config.yml"), []byte("live"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sysRoot, "opt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(live, filepath.Join(sysRoot, "opt", "app")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("app", filepath.Join(sysRoot, "opt", "cur")); err != nil {
		t.Fatal(err)
	}
	c, buf := failureLevelCollector(t, sysRoot, &CollectorConfig{CustomBackupPaths: []string{"/opt/app", "/opt/cur", "/opt/cur/config.yml"}})
	if err := c.collectCustomPaths(context.Background()); err != nil {
		t.Fatalf("collectCustomPaths: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(live, "config.yml")); err != nil || string(got) != "live" {
		t.Fatalf("live config.yml = %q (err %v): the copy wrote into the live host", got, err)
	}
	if hits := warningsMatching(buf.String(), "it is under /opt/cur, a symlink in the backup"); len(hits) != 1 {
		t.Fatalf("want one WARNING naming /opt/cur, got %d:\n%s", len(hits), buf.String())
	}
}

// A CUSTOM_BACKUP_PATHS entry or a /home entry that is a symlink to a directory is
// collected as the link and, at its own path, as the directory it points at: the
// link alone holds none of the content.
func TestSymlinkedDirectoriesAreCollectedWithTheirTarget(t *testing.T) {
	sysRoot := t.TempDir()
	for _, f := range []string{"data/app/config.yml", "data/home/alice/notes.txt"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(sysRoot, f)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sysRoot, f), []byte(f), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{"opt/app": "/data/app", "home/alice": "/data/home/alice"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(sysRoot, link)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(sysRoot, link)); err != nil {
			t.Fatal(err)
		}
	}
	c, buf := failureLevelCollector(t, sysRoot, &CollectorConfig{CustomBackupPaths: []string{"/opt/app"}})
	if err := c.collectCustomPaths(context.Background()); err != nil {
		t.Fatalf("collectCustomPaths: %v", err)
	}
	if err := c.collectUserHomes(context.Background()); err != nil {
		t.Fatalf("collectUserHomes: %v", err)
	}
	for link, target := range map[string]string{"opt/app": "/data/app", "home/alice": "/data/home/alice"} {
		if got, err := os.Readlink(filepath.Join(c.tempDir, link)); err != nil || got != target {
			t.Fatalf("staged %s link = %q (err %v), want -> %s\n%s", link, got, err, target, buf.String())
		}
	}
	for _, f := range []string{"data/app/config.yml", "data/home/alice/notes.txt"} {
		if got, err := os.ReadFile(filepath.Join(c.tempDir, f)); err != nil || string(got) != f {
			t.Fatalf("staged %s = %q (err %v): the target directory was not collected\n%s", f, got, err, buf.String())
		}
	}
}

// The target of a collected symlink is not collected when it contains the staging
// tree (a link to "/") or when the link itself is excluded by the patterns.
func TestSymlinkedDirectoryTargetsThatAreNotCollected(t *testing.T) {
	sysRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysRoot, "data", "app"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysRoot, "data", "app", "config.yml"), []byte("cfg"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sysRoot, "opt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/data/app", filepath.Join(sysRoot, "opt", "app")); err != nil {
		t.Fatal(err)
	}
	c, _ := failureLevelCollector(t, sysRoot, &CollectorConfig{CustomBackupPaths: []string{"/opt/app"}, ExcludePatterns: []string{"/opt/app"}})
	if err := c.collectCustomPaths(context.Background()); err != nil {
		t.Fatalf("collectCustomPaths: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.tempDir, "data", "app", "config.yml")); err == nil {
		t.Fatal("the target of an excluded link was collected")
	}

	// A link whose target contains the staging tree.
	c2 := NewCollector(logging.New(types.LogLevelError, false), &CollectorConfig{}, t.TempDir(), types.ProxmoxVE, false)
	link := filepath.Join(t.TempDir(), "rootlink")
	if err := os.Symlink(filepath.Dir(c2.tempDir), link); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c2.symlinkedDirectory(link); ok {
		t.Fatal("a target containing the staging tree must not be collected")
	}
}
