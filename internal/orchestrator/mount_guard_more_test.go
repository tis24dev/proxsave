package orchestrator

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGuardDirForTarget(t *testing.T) {
	t.Parallel()

	target := "/mnt/datastore"
	sum := sha256.Sum256([]byte(target))
	id := fmt.Sprintf("%x", sum[:8])
	want := filepath.Join(mountGuardBaseDir, fmt.Sprintf("%s-%s", filepath.Base(target), id))
	if got := guardDirForTarget(target); got != want {
		t.Fatalf("guardDirForTarget(%q)=%q want %q", target, got, want)
	}

	rootTarget := "/"
	sum = sha256.Sum256([]byte(rootTarget))
	id = fmt.Sprintf("%x", sum[:8])
	want = filepath.Join(mountGuardBaseDir, fmt.Sprintf("%s-%s", "guard", id))
	if got := guardDirForTarget(rootTarget); got != want {
		t.Fatalf("guardDirForTarget(%q)=%q want %q", rootTarget, got, want)
	}
}

func TestIsMountedFromMountinfo(t *testing.T) {
	t.Parallel()

	mountinfo := strings.Join([]string{
		"36 25 0:32 / / rw,relatime - ext4 /dev/sda1 rw",
		`37 36 0:33 / /mnt/pbs\040datastore rw,relatime - ext4 /dev/sdb1 rw`,
		"bad line",
		"",
	}, "\n")

	if got := isMountedFromMountinfo(mountinfo, "/"); !got {
		t.Fatalf("expected / to be mounted")
	}
	if got := isMountedFromMountinfo(mountinfo, "/mnt/pbs datastore"); !got {
		t.Fatalf("expected escaped mountpoint to match")
	}
	if got := isMountedFromMountinfo(mountinfo, "/not-mounted"); got {
		t.Fatalf("expected /not-mounted to be unmounted")
	}
	if got := isMountedFromMountinfo(mountinfo, ""); got {
		t.Fatalf("expected empty path to be unmounted")
	}
}

func TestFstabMountpointsSet(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "fstab")
	content := strings.Join([]string{
		"# comment",
		"UUID=abc / ext4 defaults 0 1",
		"/dev/sdb1 /mnt/data/ ext4 defaults 0 2",
		"/dev/sdc1 /mnt/data2 ext4 defaults 0 2 # inline comment",
		"/dev/sdd1 . ext4 defaults 0 0",
		"invalidline",
		"",
	}, "\n")
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp fstab: %v", err)
	}

	mps, err := fstabMountpointsSet(tmp)
	if err != nil {
		t.Fatalf("fstabMountpointsSet error: %v", err)
	}

	for _, mp := range []string{"/", "/mnt/data", "/mnt/data2"} {
		if _, ok := mps[mp]; !ok {
			t.Fatalf("expected mountpoint %s to be present", mp)
		}
	}
	if _, ok := mps["."]; ok {
		t.Fatalf("expected dot mountpoint to be skipped")
	}
}

func TestFstabMountpointsSet_Error(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = NewFakeFS()

	if _, err := fstabMountpointsSet("/does-not-exist"); err == nil {
		t.Fatalf("expected error")
	}
}

func TestSplitPathAndMountRootWithPrefix(t *testing.T) {
	t.Parallel()

	if got := splitPath("a//b/ /c/"); strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("splitPath unexpected: %#v", got)
	}
	if got := mountRootWithPrefix("/mnt/datastore/Data1", "/mnt/"); got != "/mnt/datastore" {
		t.Fatalf("mountRootWithPrefix got %q want %q", got, "/mnt/datastore")
	}
	if got := mountRootWithPrefix("/mnt/", "/mnt/"); got != "" {
		t.Fatalf("mountRootWithPrefix(/mnt/)=%q want empty", got)
	}
}

func TestSortByLengthDesc(t *testing.T) {
	t.Parallel()

	items := []string{"a", "abc", "ab"}
	sortByLengthDesc(items)
	if len(items) != 3 {
		t.Fatalf("unexpected len: %d", len(items))
	}
	if !(len(items[0]) >= len(items[1]) && len(items[1]) >= len(items[2])) {
		t.Fatalf("expected non-increasing lengths, got %#v", items)
	}
}

func TestFirstFstabMountpointMatch(t *testing.T) {
	t.Parallel()

	mountpoints := []string{"/mnt/storage/pbs", "/mnt/storage", "/"}
	if got := firstFstabMountpointMatch("/mnt/storage/pbs/ds1/data", mountpoints); got != "/mnt/storage/pbs" {
		t.Fatalf("firstFstabMountpointMatch got %q want %q", got, "/mnt/storage/pbs")
	}
	if got := firstFstabMountpointMatch(" ", mountpoints); got != "" {
		t.Fatalf("firstFstabMountpointMatch empty got %q want empty", got)
	}
}

func TestIsMounted_Variants(t *testing.T) {
	origReadFile := mountGuardReadFile
	t.Cleanup(func() { mountGuardReadFile = origReadFile })

	t.Run("prefers mountinfo", func(t *testing.T) {
		mountGuardReadFile = func(path string) ([]byte, error) {
			if path != "/proc/self/mountinfo" {
				t.Fatalf("unexpected read path: %s", path)
			}
			return []byte("1 2 3:4 / /mnt/target rw - ext4 /dev/sda1 rw\n"), nil
		}
		mounted, err := isMounted("/mnt/target")
		if err != nil {
			t.Fatalf("isMounted error: %v", err)
		}
		if !mounted {
			t.Fatalf("expected mounted")
		}
	})

	t.Run("falls back to proc mounts", func(t *testing.T) {
		mountGuardReadFile = func(path string) ([]byte, error) {
			switch path {
			case "/proc/self/mountinfo":
				return nil, os.ErrNotExist
			case "/proc/mounts":
				return []byte("/dev/sda1 /mnt/target ext4 rw 0 0\n"), nil
			default:
				t.Fatalf("unexpected read path: %s", path)
				return nil, nil
			}
		}
		mounted, err := isMounted("/mnt/target")
		if err != nil {
			t.Fatalf("isMounted error: %v", err)
		}
		if !mounted {
			t.Fatalf("expected mounted")
		}
	})

	t.Run("reports mounts error when mountinfo missing", func(t *testing.T) {
		wantErr := errors.New("mounts read failed")
		mountGuardReadFile = func(path string) ([]byte, error) {
			switch path {
			case "/proc/self/mountinfo":
				return nil, os.ErrNotExist
			case "/proc/mounts":
				return nil, wantErr
			default:
				t.Fatalf("unexpected read path: %s", path)
				return nil, nil
			}
		}
		_, err := isMounted("/mnt/target")
		if !errors.Is(err, wantErr) {
			t.Fatalf("expected mounts error, got %v", err)
		}
	})

	t.Run("includes both errors when mountinfo read fails", func(t *testing.T) {
		mountErr := errors.New("mountinfo boom")
		mountsErr := errors.New("mounts boom")
		mountGuardReadFile = func(path string) ([]byte, error) {
			switch path {
			case "/proc/self/mountinfo":
				return nil, mountErr
			case "/proc/mounts":
				return nil, mountsErr
			default:
				t.Fatalf("unexpected read path: %s", path)
				return nil, nil
			}
		}
		_, err := isMounted("/mnt/target")
		if err == nil || !strings.Contains(err.Error(), "mountinfo boom") || !strings.Contains(err.Error(), "mounts boom") {
			t.Fatalf("expected combined error, got %v", err)
		}
	})
}

func TestIsMountedFromProcMounts_Parsing(t *testing.T) {
	origReadFile := mountGuardReadFile
	t.Cleanup(func() { mountGuardReadFile = origReadFile })

	mountGuardReadFile = func(path string) ([]byte, error) {
		if path != "/proc/mounts" {
			t.Fatalf("unexpected read path: %s", path)
		}
		return []byte(strings.Join([]string{
			"",
			"invalid",
			"/dev/sda1 /mnt/other ext4 rw 0 0",
			"",
		}, "\n")), nil
	}

	mounted, err := isMountedFromProcMounts("/mnt/target")
	if err != nil {
		t.Fatalf("isMountedFromProcMounts error: %v", err)
	}
	if mounted {
		t.Fatalf("expected unmounted")
	}

	mounted, err = isMountedFromProcMounts(" ")
	if err != nil {
		t.Fatalf("isMountedFromProcMounts empty target error: %v", err)
	}
	if mounted {
		t.Fatalf("expected empty target to be unmounted")
	}
}

func TestGuardMountPoint(t *testing.T) {
	origReadFile := mountGuardReadFile
	origMkdirAll := mountGuardMkdirAll
	origMount := mountGuardSysMount
	origUnmount := mountGuardSysUnmount
	t.Cleanup(func() {
		mountGuardReadFile = origReadFile
		mountGuardMkdirAll = origMkdirAll
		mountGuardSysMount = origMount
		mountGuardSysUnmount = origUnmount
	})

	t.Run("rejects invalid target", func(t *testing.T) {
		if err := guardMountPoint(context.Background(), "/"); err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("nil context uses background", func(t *testing.T) {
		mountGuardReadFile = func(string) ([]byte, error) {
			return []byte("1 2 3:4 / / rw - ext4 /dev/sda1 rw\n"), nil
		}
		mountGuardMkdirAll = func(string, os.FileMode) error { return nil }
		mountGuardSysMount = func(string, string, string, uintptr, string) error { return nil }
		mountGuardSysUnmount = func(string, int) error {
			t.Fatalf("unexpected unmount call")
			return nil
		}

		if err := guardMountPoint(nil, "/mnt/nilctx"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("mount status check error", func(t *testing.T) {
		mountGuardReadFile = func(string) ([]byte, error) { return nil, errors.New("read failed") }
		if err := guardMountPoint(context.Background(), "/mnt/statuserr"); err == nil || !strings.Contains(err.Error(), "check mount status") {
			t.Fatalf("expected status check error, got %v", err)
		}
	})

	t.Run("mkdir guard dir failure", func(t *testing.T) {
		target := "/mnt/mkdir-guard-dir-fail"
		guardDir := guardDirForTarget(target)

		mountGuardReadFile = func(string) ([]byte, error) {
			return []byte("1 2 3:4 / / rw - ext4 /dev/sda1 rw\n"), nil
		}
		mountGuardMkdirAll = func(path string, _ os.FileMode) error {
			if filepath.Clean(path) == filepath.Clean(guardDir) {
				return errors.New("mkdir guard dir failed")
			}
			return nil
		}
		mountGuardSysMount = func(string, string, string, uintptr, string) error {
			t.Fatalf("unexpected mount call")
			return nil
		}

		if err := guardMountPoint(context.Background(), target); err == nil || !strings.Contains(err.Error(), "mkdir guard dir") {
			t.Fatalf("expected mkdir guard dir error, got %v", err)
		}
	})

	t.Run("mkdir target failure", func(t *testing.T) {
		target := "/mnt/mkdir-target-fail"
		guardDir := guardDirForTarget(target)

		mountGuardReadFile = func(string) ([]byte, error) {
			return []byte("1 2 3:4 / / rw - ext4 /dev/sda1 rw\n"), nil
		}
		mountGuardMkdirAll = func(path string, _ os.FileMode) error {
			switch filepath.Clean(path) {
			case filepath.Clean(guardDir):
				return nil
			case filepath.Clean(target):
				return errors.New("mkdir target failed")
			default:
				return nil
			}
		}
		mountGuardSysMount = func(string, string, string, uintptr, string) error {
			t.Fatalf("unexpected mount call")
			return nil
		}

		if err := guardMountPoint(context.Background(), target); err == nil || !strings.Contains(err.Error(), "mkdir target") {
			t.Fatalf("expected mkdir target error, got %v", err)
		}
	})

	t.Run("returns nil when already mounted", func(t *testing.T) {
		mountGuardReadFile = func(path string) ([]byte, error) {
			if path != "/proc/self/mountinfo" {
				return nil, os.ErrNotExist
			}
			return []byte("1 2 3:4 / /mnt/already rw - ext4 /dev/sda1 rw\n"), nil
		}
		mountGuardMkdirAll = func(string, os.FileMode) error {
			t.Fatalf("unexpected mkdir call")
			return nil
		}
		mountGuardSysMount = func(string, string, string, uintptr, string) error {
			t.Fatalf("unexpected mount call")
			return nil
		}

		if err := guardMountPoint(context.Background(), "/mnt/already"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("bind mount failure", func(t *testing.T) {
		mountGuardReadFile = func(path string) ([]byte, error) {
			return []byte("1 2 3:4 / / rw - ext4 /dev/sda1 rw\n"), nil
		}
		mountGuardMkdirAll = func(string, os.FileMode) error { return nil }
		mountGuardSysMount = func(_, _, _ string, flags uintptr, _ string) error {
			if flags == syscall.MS_BIND {
				return syscall.EPERM
			}
			return nil
		}
		mountGuardSysUnmount = func(string, int) error {
			t.Fatalf("unexpected unmount call")
			return nil
		}

		if err := guardMountPoint(context.Background(), "/mnt/failbind"); err == nil || !strings.Contains(err.Error(), "bind mount guard") {
			t.Fatalf("expected bind mount error, got %v", err)
		}
	})

	t.Run("remount failure unmounts", func(t *testing.T) {
		mountGuardReadFile = func(path string) ([]byte, error) {
			return []byte("1 2 3:4 / / rw - ext4 /dev/sda1 rw\n"), nil
		}
		mountGuardMkdirAll = func(string, os.FileMode) error { return nil }

		mountCalls := 0
		mountGuardSysMount = func(_, _, _ string, _ uintptr, _ string) error {
			mountCalls++
			if mountCalls == 2 {
				return syscall.EPERM
			}
			return nil
		}

		unmountCalls := 0
		mountGuardSysUnmount = func(target string, flags int) error {
			unmountCalls++
			if target != "/mnt/failremount" || flags != 0 {
				t.Fatalf("unexpected unmount args: target=%s flags=%d", target, flags)
			}
			return nil
		}

		if err := guardMountPoint(context.Background(), "/mnt/failremount"); err == nil || !strings.Contains(err.Error(), "remount guard read-only") {
			t.Fatalf("expected remount error, got %v", err)
		}
		if unmountCalls != 1 {
			t.Fatalf("expected 1 unmount call, got %d", unmountCalls)
		}
	})

	t.Run("context canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := guardMountPoint(ctx, "/mnt/ctx"); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context canceled, got %v", err)
		}
	})
}

type mountGuardCommandCall struct {
	name string
	args []string
}

type mountGuardCommandRunner struct {
	run   func(ctx context.Context, name string, args ...string) ([]byte, error)
	calls []mountGuardCommandCall
}

func (f *mountGuardCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, mountGuardCommandCall{name: name, args: append([]string{}, args...)})
	if f.run != nil {
		return f.run(ctx, name, args...)
	}
	return nil, nil
}

func TestMaybeApplyPBSDatastoreMountGuards_EarlyReturns(t *testing.T) {
	logger := newTestLogger()
	ctx := context.Background()
	plan := &RestorePlan{SystemType: SystemTypePBS, NormalCategories: []Category{{ID: "datastore_pbs"}}}

	if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, nil, "/stage", "/", false); err != nil {
		t.Fatalf("nil plan error: %v", err)
	}
	if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, &RestorePlan{SystemType: SystemTypePVE}, "/stage", "/", false); err != nil {
		t.Fatalf("wrong system type error: %v", err)
	}
	if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, &RestorePlan{SystemType: SystemTypePBS}, "/stage", "/", false); err != nil {
		t.Fatalf("missing category error: %v", err)
	}
	if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, plan, "", "/", false); err != nil {
		t.Fatalf("empty stageRoot error: %v", err)
	}
	if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, plan, "/stage", "/not-root", false); err != nil {
		t.Fatalf("destRoot not root error: %v", err)
	}
	if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, plan, "/stage", "/", true); err != nil {
		t.Fatalf("dryRun error: %v", err)
	}

	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = NewFakeFS()
	if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, plan, "/stage", "/", false); err != nil {
		t.Fatalf("non-real restoreFS error: %v", err)
	}

	origGeteuid := mountGuardGeteuid
	t.Cleanup(func() { mountGuardGeteuid = origGeteuid })
	mountGuardGeteuid = func() int { return 1 }
	restoreFS = origFS
	if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, plan, "/stage", "/", false); err != nil {
		t.Fatalf("non-root user error: %v", err)
	}
}

func TestMaybeApplyPBSDatastoreMountGuards_StagedDatastoreCfgHandling(t *testing.T) {
	logger := newTestLogger()
	ctx := context.Background()
	plan := &RestorePlan{SystemType: SystemTypePBS, NormalCategories: []Category{{ID: "datastore_pbs"}}}

	origGeteuid := mountGuardGeteuid
	t.Cleanup(func() { mountGuardGeteuid = origGeteuid })
	mountGuardGeteuid = func() int { return 0 }

	stageRoot := t.TempDir()

	// Missing file => no-op.
	if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, plan, stageRoot, "/", false); err != nil {
		t.Fatalf("missing staged file error: %v", err)
	}

	// Non-file error should propagate.
	stagePath := filepath.Join(stageRoot, "etc/proxmox-backup/datastore.cfg")
	if err := os.MkdirAll(stagePath, 0o755); err != nil {
		t.Fatalf("mkdir staged path: %v", err)
	}
	if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, plan, stageRoot, "/", false); err == nil || !strings.Contains(err.Error(), "read staged datastore.cfg") {
		t.Fatalf("expected read staged error, got %v", err)
	}

	// Empty file => no-op.
	if err := os.RemoveAll(filepath.Dir(stagePath)); err != nil {
		t.Fatalf("cleanup staged dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(stagePath), 0o755); err != nil {
		t.Fatalf("mkdir staged dir: %v", err)
	}
	if err := os.WriteFile(stagePath, []byte(" \n\t"), 0o600); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, plan, stageRoot, "/", false); err != nil {
		t.Fatalf("empty staged content error: %v", err)
	}
}

func TestMaybeApplyPBSDatastoreMountGuards_NoBlocks(t *testing.T) {
	logger := newTestLogger()
	ctx := context.Background()
	plan := &RestorePlan{SystemType: SystemTypePBS, NormalCategories: []Category{{ID: "datastore_pbs"}}}

	origGeteuid := mountGuardGeteuid
	t.Cleanup(func() { mountGuardGeteuid = origGeteuid })
	mountGuardGeteuid = func() int { return 0 }

	stageRoot := t.TempDir()
	stagePath := filepath.Join(stageRoot, "etc/proxmox-backup/datastore.cfg")
	if err := os.MkdirAll(filepath.Dir(stagePath), 0o755); err != nil {
		t.Fatalf("mkdir staged dir: %v", err)
	}
	if err := os.WriteFile(stagePath, []byte("# comment only\n"), 0o600); err != nil {
		t.Fatalf("write datastore.cfg: %v", err)
	}

	if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, plan, stageRoot, "/", false); err != nil {
		t.Fatalf("maybeApplyPBSDatastoreMountGuards error: %v", err)
	}
}

func TestMaybeApplyPBSDatastoreMountGuards_FstabParseErrorContinues(t *testing.T) {
	logger := newTestLogger()
	ctx := context.Background()
	plan := &RestorePlan{SystemType: SystemTypePBS, NormalCategories: []Category{{ID: "datastore_pbs"}}}

	origGeteuid := mountGuardGeteuid
	origFstab := mountGuardFstabMountpointsSet
	origMkdirAll := mountGuardMkdirAll
	origRootFS := mountGuardIsPathOnRootFilesystem
	t.Cleanup(func() {
		mountGuardGeteuid = origGeteuid
		mountGuardFstabMountpointsSet = origFstab
		mountGuardMkdirAll = origMkdirAll
		mountGuardIsPathOnRootFilesystem = origRootFS
	})

	mountGuardGeteuid = func() int { return 0 }
	mountGuardFstabMountpointsSet = func(string) (map[string]struct{}, error) {
		return nil, errors.New("fstab parse failed")
	}
	mountGuardMkdirAll = func(string, os.FileMode) error { return nil }
	mountGuardIsPathOnRootFilesystem = func(path string) (bool, string, error) {
		return false, filepath.Clean(path), nil
	}

	stageRoot := t.TempDir()
	stagePath := filepath.Join(stageRoot, "etc/proxmox-backup/datastore.cfg")
	if err := os.MkdirAll(filepath.Dir(stagePath), 0o755); err != nil {
		t.Fatalf("mkdir staged dir: %v", err)
	}
	if err := os.WriteFile(stagePath, []byte("datastore: ds\n    path /mnt/test/store\n"), 0o600); err != nil {
		t.Fatalf("write datastore.cfg: %v", err)
	}

	if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, plan, stageRoot, "/", false); err != nil {
		t.Fatalf("maybeApplyPBSDatastoreMountGuards error: %v", err)
	}
}

func TestMaybeApplyPBSDatastoreMountGuards_ParseBlocksError(t *testing.T) {
	logger := newTestLogger()
	ctx := context.Background()
	plan := &RestorePlan{SystemType: SystemTypePBS, NormalCategories: []Category{{ID: "datastore_pbs"}}}

	origGeteuid := mountGuardGeteuid
	origParse := mountGuardParsePBSDatastoreCfg
	t.Cleanup(func() {
		mountGuardGeteuid = origGeteuid
		mountGuardParsePBSDatastoreCfg = origParse
	})

	mountGuardGeteuid = func() int { return 0 }
	wantErr := errors.New("parse blocks failed")
	mountGuardParsePBSDatastoreCfg = func(string) ([]pbsDatastoreBlock, error) {
		return nil, wantErr
	}

	stageRoot := t.TempDir()
	stagePath := filepath.Join(stageRoot, "etc/proxmox-backup/datastore.cfg")
	if err := os.MkdirAll(filepath.Dir(stagePath), 0o755); err != nil {
		t.Fatalf("mkdir staged dir: %v", err)
	}
	if err := os.WriteFile(stagePath, []byte("datastore: ds\n    path /mnt/test/store\n"), 0o600); err != nil {
		t.Fatalf("write datastore.cfg: %v", err)
	}

	if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, plan, stageRoot, "/", false); !errors.Is(err, wantErr) {
		t.Fatalf("expected parse error %v, got %v", wantErr, err)
	}
}

func TestMaybeApplyPBSDatastoreMountGuards_FullFlow(t *testing.T) {
	logger := newTestLogger()
	ctx := context.Background()
	plan := &RestorePlan{SystemType: SystemTypePBS, NormalCategories: []Category{{ID: "datastore_pbs"}}}

	origGeteuid := mountGuardGeteuid
	origReadFile := mountGuardReadFile
	origMkdirAll := mountGuardMkdirAll
	origReadDir := mountGuardReadDir
	origMount := mountGuardSysMount
	origUnmount := mountGuardSysUnmount
	origFstab := mountGuardFstabMountpointsSet
	origRootFS := mountGuardIsPathOnRootFilesystem
	origCmd := restoreCmd
	t.Cleanup(func() {
		mountGuardGeteuid = origGeteuid
		mountGuardReadFile = origReadFile
		mountGuardMkdirAll = origMkdirAll
		mountGuardReadDir = origReadDir
		mountGuardSysMount = origMount
		mountGuardSysUnmount = origUnmount
		mountGuardFstabMountpointsSet = origFstab
		mountGuardIsPathOnRootFilesystem = origRootFS
		restoreCmd = origCmd
	})

	mountGuardGeteuid = func() int { return 0 }

	stageRoot := t.TempDir()
	stagePath := filepath.Join(stageRoot, "etc/proxmox-backup/datastore.cfg")
	if err := os.MkdirAll(filepath.Dir(stagePath), 0o755); err != nil {
		t.Fatalf("mkdir staged dir: %v", err)
	}
	cfg := strings.Join([]string{
		"datastore: ds-chattrsuccess",
		"    path /mnt/chattrsuccess/store",
		"datastore: ds-invalid",
		"    path /",
		"datastore: ds-nomountstyle",
		"    path /srv/pbs",
		"datastore: ds-storage",
		"    path /mnt/storage/pbs/ds1/data",
		"datastore: ds-media-skip-fstab",
		"    path /media/USB/PBS",
		"datastore: ds-mkdirerr",
		"    path /mnt/mkdirerr/store",
		"datastore: ds-deverr",
		"    path /mnt/deverr/store",
		"datastore: ds-notroot",
		"    path /mnt/notroot/store",
		"datastore: ds-mounted",
		"    path /mnt/mounted/store",
		"datastore: ds-mountok",
		"    path /mnt/mountok/store",
		"datastore: ds-mountok2",
		"    path /mnt/mountok2/store",
		"datastore: ds-chattrfail",
		"    path /mnt/chattrfail/store",
		"datastore: ds-guardok",
		"    path /mnt/guardok/store",
		"datastore: ds-guarddup",
		"    path /mnt/guardok/other",
		"",
	}, "\n")
	if err := os.WriteFile(stagePath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write datastore.cfg: %v", err)
	}

	mountGuardFstabMountpointsSet = func(string) (map[string]struct{}, error) {
		return map[string]struct{}{
			"/":                  {},
			"/srv":               {},
			"/mnt/storage":       {},
			"/mnt/storage/pbs":   {},
			"/mnt/mkdirerr":      {},
			"/mnt/deverr":        {},
			"/mnt/notroot":       {},
			"/mnt/mounted":       {},
			"/mnt/mountok":       {},
			"/mnt/mountok2":      {},
			"/mnt/chattrfail":    {},
			"/mnt/chattrsuccess": {},
			"/mnt/guardok":       {},
		}, nil
	}

	mountGuardMkdirAll = func(path string, _ os.FileMode) error {
		if filepath.Clean(path) == "/mnt/mkdirerr" {
			return errors.New("mkdir denied")
		}
		return nil
	}

	rootCalls := make(map[string]int)
	mountGuardIsPathOnRootFilesystem = func(path string) (bool, string, error) {
		path = filepath.Clean(path)
		rootCalls[path]++
		switch path {
		case "/mnt/deverr":
			return false, path, errors.New("stat failed")
		case "/mnt/notroot":
			return false, path, nil
		case "/mnt/mountok":
			if rootCalls[path] == 1 {
				return true, path, nil
			}
			return false, path, nil
		default:
			return true, path, nil
		}
	}

	mountedTargets := map[string]struct{}{
		"/mnt/mounted": {},
	}
	mountinfoReads := 0
	mountsReads := 0
	buildMountinfo := func() string {
		var b strings.Builder
		for mp := range mountedTargets {
			b.WriteString(fmt.Sprintf("1 2 3:4 / %s rw - ext4 /dev/sda1 rw\n", mp))
		}
		return b.String()
	}
	buildProcMounts := func() string {
		var b strings.Builder
		for mp := range mountedTargets {
			b.WriteString(fmt.Sprintf("/dev/sda1 %s ext4 rw 0 0\n", mp))
		}
		return b.String()
	}
	mountGuardReadFile = func(path string) ([]byte, error) {
		switch path {
		case "/proc/self/mountinfo":
			mountinfoReads++
			if mountinfoReads == 1 {
				return nil, errors.New("mountinfo read failed")
			}
			return []byte(buildMountinfo()), nil
		case "/proc/mounts":
			mountsReads++
			if mountsReads == 1 {
				return nil, errors.New("mounts read failed")
			}
			return []byte(buildProcMounts()), nil
		default:
			return nil, fmt.Errorf("unexpected read: %s", path)
		}
	}

	mountGuardReadDir = func(path string) ([]os.DirEntry, error) {
		if filepath.Clean(path) == "/mnt/guardok" {
			return []os.DirEntry{&fakeDirEntry{name: "nonempty"}}, nil
		}
		return nil, os.ErrNotExist
	}

	mountGuardSysMount = func(_, target, _ string, _ uintptr, _ string) error {
		switch filepath.Clean(target) {
		case "/mnt/chattrfail", "/mnt/chattrsuccess":
			return syscall.EPERM
		default:
			return nil
		}
	}
	mountGuardSysUnmount = func(string, int) error { return nil }

	cmd := &mountGuardCommandRunner{}
	cmd.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "mount":
			if len(args) != 1 {
				return nil, fmt.Errorf("unexpected mount args: %v", args)
			}
			target := filepath.Clean(args[0])
			switch target {
			case "/mnt/mountok", "/mnt/mountok2":
				if target == "/mnt/mountok2" {
					mountedTargets[target] = struct{}{}
				}
				return nil, nil
			case "/mnt/chattrfail":
				return []byte(" \n\t"), errors.New("mount failed")
			default:
				return []byte("mount: failed"), errors.New("mount failed")
			}
		case "chattr":
			if len(args) != 2 || args[0] != "+i" {
				return nil, fmt.Errorf("unexpected chattr args: %v", args)
			}
			target := filepath.Clean(args[1])
			if target == "/mnt/chattrfail" {
				return nil, errors.New("chattr failed")
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected command: %s", name)
		}
	}
	restoreCmd = cmd

	if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, plan, stageRoot, "/", false); err != nil {
		t.Fatalf("maybeApplyPBSDatastoreMountGuards error: %v", err)
	}

	// Ensure the longest fstab mountpoint match wins (/mnt/storage/pbs instead of /mnt/storage).
	foundStoragePBS := false
	for _, c := range cmd.calls {
		if c.name == "mount" && len(c.args) == 1 && filepath.Clean(c.args[0]) == "/mnt/storage/pbs" {
			foundStoragePBS = true
			break
		}
	}
	if !foundStoragePBS {
		t.Fatalf("expected mount attempt for /mnt/storage/pbs, calls=%#v", cmd.calls)
	}
}

func TestMaybeApplyPBSDatastoreMountGuards_MountAttemptTimeout(t *testing.T) {
	logger := newTestLogger()
	baseCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	t.Cleanup(cancel)
	plan := &RestorePlan{SystemType: SystemTypePBS, NormalCategories: []Category{{ID: "datastore_pbs"}}}

	origGeteuid := mountGuardGeteuid
	origReadFile := mountGuardReadFile
	origMkdirAll := mountGuardMkdirAll
	origMount := mountGuardSysMount
	origUnmount := mountGuardSysUnmount
	origFstab := mountGuardFstabMountpointsSet
	origRootFS := mountGuardIsPathOnRootFilesystem
	origCmd := restoreCmd
	t.Cleanup(func() {
		mountGuardGeteuid = origGeteuid
		mountGuardReadFile = origReadFile
		mountGuardMkdirAll = origMkdirAll
		mountGuardSysMount = origMount
		mountGuardSysUnmount = origUnmount
		mountGuardFstabMountpointsSet = origFstab
		mountGuardIsPathOnRootFilesystem = origRootFS
		restoreCmd = origCmd
	})

	mountGuardGeteuid = func() int { return 0 }
	mountGuardReadFile = func(string) ([]byte, error) { return []byte(""), nil }
	mountGuardMkdirAll = func(string, os.FileMode) error { return nil }
	mountGuardSysMount = func(string, string, string, uintptr, string) error { return nil }
	mountGuardSysUnmount = func(string, int) error { return nil }
	mountGuardIsPathOnRootFilesystem = func(path string) (bool, string, error) { return true, filepath.Clean(path), nil }
	mountGuardFstabMountpointsSet = func(string) (map[string]struct{}, error) {
		return map[string]struct{}{"/mnt/timeout": {}}, nil
	}

	stageRoot := t.TempDir()
	stagePath := filepath.Join(stageRoot, "etc/proxmox-backup/datastore.cfg")
	if err := os.MkdirAll(filepath.Dir(stagePath), 0o755); err != nil {
		t.Fatalf("mkdir staged dir: %v", err)
	}
	if err := os.WriteFile(stagePath, []byte("datastore: ds\n    path /mnt/timeout/store\n"), 0o600); err != nil {
		t.Fatalf("write datastore.cfg: %v", err)
	}

	restoreCmd = &mountGuardCommandRunner{
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "mount" {
				return nil, ctx.Err()
			}
			if name == "chattr" {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("unexpected command: %s", name)
		},
	}

	if err := maybeApplyPBSDatastoreMountGuards(baseCtx, logger, plan, stageRoot, "/", false); err != nil {
		t.Fatalf("maybeApplyPBSDatastoreMountGuards error: %v", err)
	}
}
