package orchestrator

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestGuardMountpointsFromMountinfo_VisibleAndHidden(t *testing.T) {
	mountinfo := strings.Join([]string{
		"10 1 0:1 " + mountGuardBaseDir + "/g1 /mnt/visible rw - ext4 /dev/sda1 rw",
		"20 1 0:1 " + mountGuardBaseDir + "/g2 /mnt/hidden rw - ext4 /dev/sda1 rw",
		"30 1 0:1 / /mnt/hidden rw - ext4 /dev/sdb1 rw",
	}, "\n")

	visible, hidden, mounts := guardMountpointsFromMountinfo(mountinfo)
	if mounts != 2 {
		t.Fatalf("guard mounts=%d want 2 (visible+hidden guard entries)", mounts)
	}
	if len(visible) != 1 || visible[0] != "/mnt/visible" {
		t.Fatalf("visible=%#v want [\"/mnt/visible\"]", visible)
	}
	if len(hidden) != 1 || hidden[0] != "/mnt/hidden" {
		t.Fatalf("hidden=%#v want [\"/mnt/hidden\"]", hidden)
	}
}

func TestGuardMountpointsFromMountinfo_UnescapesMountpoint(t *testing.T) {
	mountinfo := "10 1 0:1 " + mountGuardBaseDir + "/g1 /mnt/with\\040space rw - ext4 /dev/sda1 rw\n"
	visible, hidden, mounts := guardMountpointsFromMountinfo(mountinfo)
	if mounts != 1 {
		t.Fatalf("guard mounts=%d want 1", mounts)
	}
	if len(hidden) != 0 {
		t.Fatalf("hidden=%#v want empty", hidden)
	}
	if len(visible) != 1 || visible[0] != "/mnt/with space" {
		t.Fatalf("visible=%#v want [\"/mnt/with space\"]", visible)
	}
}

func TestCleanupMountGuards_UnmountsVisibleAndRemovesDirWhenNoRemaining(t *testing.T) {
	origGeteuid := cleanupGeteuid
	origStat := cleanupStat
	origReadFile := cleanupReadFile
	origRemoveAll := cleanupRemoveAll
	origUnmount := cleanupSysUnmount
	t.Cleanup(func() {
		cleanupGeteuid = origGeteuid
		cleanupStat = origStat
		cleanupReadFile = origReadFile
		cleanupRemoveAll = origRemoveAll
		cleanupSysUnmount = origUnmount
	})

	cleanupGeteuid = func() int { return 0 }
	cleanupStat = func(string) (os.FileInfo, error) { return nil, nil }

	initialMountinfo := "10 1 0:1 " + mountGuardBaseDir + "/g1 /mnt/visible rw - ext4 /dev/sda1 rw\n"
	afterMountinfo := "30 1 0:1 / / rw - ext4 /dev/sda1 rw\n"
	readCount := 0
	cleanupReadFile = func(path string) ([]byte, error) {
		if path != "/proc/self/mountinfo" {
			return nil, os.ErrNotExist
		}
		readCount++
		if readCount == 1 {
			return []byte(initialMountinfo), nil
		}
		return []byte(afterMountinfo), nil
	}

	var unmounted []string
	cleanupSysUnmount = func(target string, flags int) error {
		_ = flags
		unmounted = append(unmounted, target)
		return nil
	}

	var removed []string
	cleanupRemoveAll = func(path string) error {
		removed = append(removed, path)
		return nil
	}

	logger := logging.New(types.LogLevelError, false)
	if err := CleanupMountGuards(context.Background(), logger, false); err != nil {
		t.Fatalf("CleanupMountGuards error: %v", err)
	}
	if len(unmounted) != 1 || unmounted[0] != "/mnt/visible" {
		t.Fatalf("unmounted=%#v want [\"/mnt/visible\"]", unmounted)
	}
	if len(removed) != 1 || removed[0] != mountGuardBaseDir {
		t.Fatalf("removed=%#v want [%q]", removed, mountGuardBaseDir)
	}
}

func TestCleanupMountGuards_DoesNotUnmountHiddenGuards(t *testing.T) {
	origGeteuid := cleanupGeteuid
	origStat := cleanupStat
	origReadFile := cleanupReadFile
	origRemoveAll := cleanupRemoveAll
	origUnmount := cleanupSysUnmount
	t.Cleanup(func() {
		cleanupGeteuid = origGeteuid
		cleanupStat = origStat
		cleanupReadFile = origReadFile
		cleanupRemoveAll = origRemoveAll
		cleanupSysUnmount = origUnmount
	})

	cleanupGeteuid = func() int { return 0 }
	cleanupStat = func(string) (os.FileInfo, error) { return nil, nil }

	hiddenMountinfo := strings.Join([]string{
		"20 1 0:1 " + mountGuardBaseDir + "/g2 /mnt/hidden rw - ext4 /dev/sda1 rw",
		"30 1 0:1 / /mnt/hidden rw - ext4 /dev/sdb1 rw",
	}, "\n")
	cleanupReadFile = func(string) ([]byte, error) { return []byte(hiddenMountinfo), nil }

	cleanupSysUnmount = func(string, int) error {
		t.Fatalf("unexpected unmount")
		return nil
	}

	cleanupRemoveAll = func(string) error {
		t.Fatalf("unexpected removeAll")
		return nil
	}

	logger := logging.New(types.LogLevelError, false)
	if err := CleanupMountGuards(context.Background(), logger, false); err != nil {
		t.Fatalf("CleanupMountGuards error: %v", err)
	}
}

// Kills: removing the fail-closed reread guard. If the verification reread of
// /proc/self/mountinfo fails, the old code left remaining=0 and removed the guard
// directory without confirming the guard mounts were gone. The fix keeps the
// directory, does NOT report "0 remaining", and returns nil. Here the first read
// (initial mount state) succeeds with a visible guard mount that is then unmounted,
// and the second read (verification) fails, so the directory must be kept.
func TestCleanupMountGuards_RereadFailureKeepsDir(t *testing.T) {
	origGeteuid := cleanupGeteuid
	origStat := cleanupStat
	origReadFile := cleanupReadFile
	origRemoveAll := cleanupRemoveAll
	origUnmount := cleanupSysUnmount
	t.Cleanup(func() {
		cleanupGeteuid = origGeteuid
		cleanupStat = origStat
		cleanupReadFile = origReadFile
		cleanupRemoveAll = origRemoveAll
		cleanupSysUnmount = origUnmount
	})

	cleanupGeteuid = func() int { return 0 }
	cleanupStat = func(string) (os.FileInfo, error) { return nil, nil }

	initialMountinfo := "10 1 0:1 " + mountGuardBaseDir + "/g1 /mnt/visible rw - ext4 /dev/sda1 rw\n"
	readCount := 0
	cleanupReadFile = func(path string) ([]byte, error) {
		if path != "/proc/self/mountinfo" {
			return nil, os.ErrNotExist
		}
		readCount++
		if readCount == 1 {
			return []byte(initialMountinfo), nil
		}
		// Verification reread fails: cannot confirm the guard mount is gone.
		return nil, os.ErrPermission
	}

	cleanupSysUnmount = func(string, int) error { return nil }

	removed := false
	cleanupRemoveAll = func(string) error {
		removed = true
		return nil
	}

	logger := logging.New(types.LogLevelError, false)
	if err := CleanupMountGuards(context.Background(), logger, false); err != nil {
		t.Fatalf("CleanupMountGuards must be non-fatal on reread failure, got %v", err)
	}
	if removed {
		t.Fatalf("guard directory must be kept when the verification reread fails (fail-closed)")
	}
}

// Kills: rendering the guardsRemaining=-1 fail-closed sentinel as a misleading
// "guards-remaining=0" (or "-1") in the summary line. On a reread failure the summary
// must report the remaining count as "unknown".
func TestCleanupMountGuards_RereadFailureSummaryUnknown(t *testing.T) {
	origGeteuid := cleanupGeteuid
	origStat := cleanupStat
	origReadFile := cleanupReadFile
	origRemoveAll := cleanupRemoveAll
	origUnmount := cleanupSysUnmount
	t.Cleanup(func() {
		cleanupGeteuid = origGeteuid
		cleanupStat = origStat
		cleanupReadFile = origReadFile
		cleanupRemoveAll = origRemoveAll
		cleanupSysUnmount = origUnmount
	})

	cleanupGeteuid = func() int { return 0 }
	cleanupStat = func(string) (os.FileInfo, error) { return nil, nil }
	initialMountinfo := "10 1 0:1 " + mountGuardBaseDir + "/g1 /mnt/visible rw - ext4 /dev/sda1 rw\n"
	readCount := 0
	cleanupReadFile = func(path string) ([]byte, error) {
		if path != "/proc/self/mountinfo" {
			return nil, os.ErrNotExist
		}
		readCount++
		if readCount == 1 {
			return []byte(initialMountinfo), nil
		}
		return nil, os.ErrPermission
	}
	cleanupSysUnmount = func(string, int) error { return nil }
	cleanupRemoveAll = func(string) error { return nil }

	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	if err := CleanupMountGuards(context.Background(), logger, false); err != nil {
		t.Fatalf("CleanupMountGuards: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "guards-remaining=unknown") {
		t.Fatalf("summary must report guards-remaining=unknown on reread failure; out=%q", out)
	}
	if strings.Contains(out, "guards-remaining=0") {
		t.Fatalf("summary must not falsely report guards-remaining=0 on reread failure; out=%q", out)
	}
	if !strings.Contains(out, "guard-dir=kept") {
		t.Fatalf("summary must report guard-dir=kept on reread failure; out=%q", out)
	}
}
