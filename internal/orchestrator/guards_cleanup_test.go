package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestGuardMountpointsFromMountinfo_VisibleAndHidden(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
