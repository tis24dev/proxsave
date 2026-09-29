package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ProxSave writes only under BASE_DIR and /tmp/proxsave: the restore run directories
// and the mount guards are both placed under BASE_DIR, and an unknown BASE_DIR falls
// back to /opt/proxsave, as the export directory does.
func TestSetBaseDirPlacesRestoreAndGuardsUnderBaseDir(t *testing.T) {
	origRestoreBase, origRunDir, origGuards := restoreRunBaseDir, restoreRunDirPath, mountGuardBaseDir
	t.Cleanup(func() {
		restoreRunBaseDir, restoreRunDirPath, mountGuardBaseDir = origRestoreBase, origRunDir, origGuards
	})

	SetBaseDir("/srv/proxsave/")
	restoreRunDirPath = ""
	if got := filepath.Dir(RestoreRunDir()); got != "/srv/proxsave/restore" {
		t.Fatalf("RestoreRunDir() parent = %s, want /srv/proxsave/restore", got)
	}
	if mountGuardBaseDir != "/srv/proxsave/guards" {
		t.Fatalf("mountGuardBaseDir = %s, want /srv/proxsave/guards", mountGuardBaseDir)
	}
	if got := mountGuardChattrTargetsPath(); got != "/srv/proxsave/guards/chattr-targets" {
		t.Fatalf("chattr index = %s, want /srv/proxsave/guards/chattr-targets", got)
	}

	SetBaseDir("  ")
	if restoreRunBaseDir != "/opt/proxsave/restore" || mountGuardBaseDir != "/opt/proxsave/guards" {
		t.Fatalf("empty BASE_DIR: restore=%s guards=%s, want /opt/proxsave/restore and /opt/proxsave/guards", restoreRunBaseDir, mountGuardBaseDir)
	}
}

// The root field of /proc/self/mountinfo is the bind source INSIDE its filesystem.
// With /opt on a filesystem of its own, a guard under /opt/proxsave/guards reads
// root=/proxsave/guards/<name>; matching it against the absolute path missed it. A
// bind of the same relative path on another filesystem is not a guard.
func TestGuardMountpointsFromMountinfoOnSeparateFilesystem(t *testing.T) {
	origEval := cleanupEvalSymlinks
	cleanupEvalSymlinks = func(p string) (string, error) { return p, nil }
	t.Cleanup(func() { cleanupEvalSymlinks = origEval })

	dir := "/opt/proxsave/guards"
	mountinfo := strings.Join([]string{
		"1 0 8:1 / / rw - ext4 /dev/sda1 rw",
		"2 1 8:2 / /opt rw - ext4 /dev/sda2 rw",
		"40 2 8:2 /proxsave/guards/datastore-0011223344556677 /mnt/datastore ro - ext4 /dev/sda2 rw",
		"41 1 8:1 /proxsave/guards/other /mnt/other ro - ext4 /dev/sda1 rw",
	}, "\n")

	visible, hidden, mountsIn := guardMountpointsFromMountinfo(mountinfo, guardRootMatchersFor(mountinfo, []string{dir}))
	if len(visible) != 1 || visible[0] != "/mnt/datastore" {
		t.Fatalf("visible = %#v, want [/mnt/datastore]", visible)
	}
	if len(hidden) != 0 {
		t.Fatalf("hidden = %#v, want none", hidden)
	}
	if mountsIn[dir] != 1 {
		t.Fatalf("guard mounts in %s = %d, want 1", dir, mountsIn[dir])
	}

	// On the root filesystem the root field is the absolute path, as before.
	rootInfo := "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n" +
		"40 1 8:1 /opt/proxsave/guards/datastore-0011223344556677 /mnt/datastore ro - ext4 /dev/sda1 rw\n"
	visible, _, _ = guardMountpointsFromMountinfo(rootInfo, guardRootMatchersFor(rootInfo, []string{dir}))
	if len(visible) != 1 || visible[0] != "/mnt/datastore" {
		t.Fatalf("root filesystem: visible = %#v, want [/mnt/datastore]", visible)
	}
}

// A host upgraded from a version that created guards in /var/lib/proxsave/guards can
// still have one there, bind-mounted until the next reboot. --cleanup-guards finds it,
// unmounts it and removes both directories once they are empty of guards.
func TestCleanupMountGuardsAlsoClearsTheLegacyDirectory(t *testing.T) {
	current := filepath.Join(t.TempDir(), "opt", "proxsave", "guards")
	legacy := filepath.Join(t.TempDir(), "var", "lib", "proxsave", "guards")
	for _, dir := range []string{current, legacy} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	origCurrent, origLegacy := mountGuardBaseDir, legacyMountGuardBaseDir
	origGeteuid, origReadFile, origRemoveAll, origUnmount := cleanupGeteuid, cleanupReadFile, cleanupRemoveAll, cleanupSysUnmount
	t.Cleanup(func() {
		mountGuardBaseDir, legacyMountGuardBaseDir = origCurrent, origLegacy
		cleanupGeteuid, cleanupReadFile, cleanupRemoveAll, cleanupSysUnmount = origGeteuid, origReadFile, origRemoveAll, origUnmount
	})
	mountGuardBaseDir, legacyMountGuardBaseDir = current, legacy

	cleanupGeteuid = func() int { return 0 }
	reads := 0
	cleanupReadFile = func(string) ([]byte, error) {
		reads++
		if reads == 1 {
			return []byte("40 1 0:1 " + legacy + "/datastore-0011223344556677 /mnt/datastore ro - ext4 /dev/sda1 rw\n"), nil
		}
		return []byte(""), nil
	}
	var unmounted, removed []string
	cleanupSysUnmount = func(target string, _ int) error {
		unmounted = append(unmounted, target)
		return nil
	}
	cleanupRemoveAll = func(path string) error {
		removed = append(removed, path)
		return nil
	}

	report, err := cleanupMountGuards(context.Background(), newTestLogger(), false)
	if err != nil {
		t.Fatalf("cleanupMountGuards: %v", err)
	}
	if len(unmounted) != 1 || unmounted[0] != "/mnt/datastore" {
		t.Fatalf("unmounted = %#v, want [/mnt/datastore]", unmounted)
	}
	if len(removed) != 2 || removed[0] != current || removed[1] != legacy {
		t.Fatalf("removed = %#v, want [%s %s]", removed, current, legacy)
	}
	if !report.GuardDirPresent || report.BindGuards != 1 || report.Unmounted != 1 || !report.DirRemoved {
		t.Fatalf("report = %+v, want a present dir, 1 bind guard unmounted, dirs removed", report)
	}
}
