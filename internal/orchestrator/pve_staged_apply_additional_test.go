package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const pveMountIntegrationStoragePrefix = "proxsave-it-"

// requireWritablePveMountRoot skips integration tests unless /mnt/pve accepts a real write/remove probe.
func requireWritablePveMountRoot(t *testing.T) {
	t.Helper()
	root := "/mnt/pve"
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Skipf("requires writable %s: %v", root, err)
	}
	probe := filepath.Join(root, fmt.Sprintf(".%sprobe-%d", pveMountIntegrationStoragePrefix, os.Getpid()))
	if err := os.WriteFile(probe, []byte("probe"), 0o600); err != nil {
		t.Skipf("requires writable %s: %v", root, err)
	}
	if f, err := os.Open(probe); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	if err := os.Remove(probe); err != nil {
		t.Skipf("requires writable %s (remove probe): %v", root, err)
	}
}

func uniquePveMountTestStorageID(t *testing.T, label string) string {
	t.Helper()
	label = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return '-'
		default:
			return '-'
		}
	}, strings.ToLower(strings.TrimSpace(label)))
	if label == "" {
		label = "x"
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", t.Name(), label, os.Getpid())))
	return pveMountIntegrationStoragePrefix + hex.EncodeToString(sum[:6]) + "-" + label
}

func pveMountTargetForStorageID(storageID string) string {
	return filepath.Join("/mnt/pve", storageID)
}

func isPveMountIntegrationTestStorageID(storageID string) bool {
	return strings.HasPrefix(storageID, pveMountIntegrationStoragePrefix)
}

func isPveMountIntegrationTestPath(path string) bool {
	path = filepath.Clean(path)
	if filepath.Dir(path) != "/mnt/pve" {
		return false
	}
	return isPveMountIntegrationTestStorageID(filepath.Base(path))
}

func cleanupPveMountTestTarget(t *testing.T, target string) {
	t.Helper()
	if !isPveMountIntegrationTestPath(target) {
		t.Fatalf("refusing cleanup of non-test mount target %q", target)
	}
	t.Cleanup(func() { _ = os.RemoveAll(target) })
}

func cleanupPveMountTestGuardDir(t *testing.T, target string) {
	t.Helper()
	if !isPveMountIntegrationTestPath(target) {
		t.Fatalf("refusing cleanup of guard dir for non-test mount target %q", target)
	}
	t.Cleanup(func() { _ = os.RemoveAll(guardDirForTarget(target)) })
}

func removePveMountTestPathIfExists(t *testing.T, path string) {
	t.Helper()
	if !isPveMountIntegrationTestPath(path) {
		t.Fatalf("refusing remove of non-test mount path %q", path)
	}
	_ = os.Remove(path)
}

func TestPveMountIntegrationTestPathSafety(t *testing.T) {
	id := uniquePveMountTestStorageID(t, "safety")
	target := pveMountTargetForStorageID(id)
	if !isPveMountIntegrationTestPath(target) {
		t.Fatalf("expected owned test path %q", target)
	}
	for _, path := range []string{
		"/mnt/pve/proxsave-offline",
		"/mnt/pve/activate-ok",
		"/mnt/pve",
		"/mnt/pool/nas",
	} {
		if isPveMountIntegrationTestPath(path) {
			t.Fatalf("path %q must not be treated as integration-test owned", path)
		}
	}
}

func pvePlan(needsClusterRestore bool, ids ...string) *RestorePlan {
	cats := make([]Category, 0, len(ids))
	for _, id := range ids {
		cats = append(cats, Category{ID: id, Type: CategoryTypePVE})
	}
	return &RestorePlan{
		SystemType:          SystemTypePVE,
		NormalCategories:    cats,
		NeedsClusterRestore: needsClusterRestore,
	}
}

func TestMaybeApplyPVEConfigsFromStage_EarlyReturnsAndSafeFlow(t *testing.T) {
	ctx := context.Background()
	logger := newTestLogger()

	if err := maybeApplyPVEConfigsFromStage(ctx, logger, nil, "/stage", "/", false); err != nil {
		t.Fatalf("nil plan: expected nil error, got %v", err)
	}

	wrongSystem := &RestorePlan{
		SystemType:       SystemTypePBS,
		NormalCategories: []Category{{ID: "storage_pve", Type: CategoryTypePVE}},
	}
	if err := maybeApplyPVEConfigsFromStage(ctx, logger, wrongSystem, "/stage", "/", false); err != nil {
		t.Fatalf("wrong system type: expected nil error, got %v", err)
	}

	noRelevantCategories := pvePlan(false, "unrelated")
	if err := maybeApplyPVEConfigsFromStage(ctx, logger, noRelevantCategories, "/stage", "/", false); err != nil {
		t.Fatalf("no relevant categories: expected nil error, got %v", err)
	}

	relevant := pvePlan(false, "storage_pve", "pve_jobs")
	if err := maybeApplyPVEConfigsFromStage(ctx, logger, relevant, "   ", "/", false); err != nil {
		t.Fatalf("blank stageRoot: expected nil error, got %v", err)
	}
	if err := maybeApplyPVEConfigsFromStage(ctx, logger, relevant, "/stage", "/tmp/not-root", false); err != nil {
		t.Fatalf("non-root destination: expected nil error, got %v", err)
	}
	if err := maybeApplyPVEConfigsFromStage(ctx, logger, relevant, "/stage", "/", true); err != nil {
		t.Fatalf("dry run: expected nil error, got %v", err)
	}

	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreCmd = &FakeCommandRunner{}
	if err := maybeApplyPVEConfigsFromStage(ctx, logger, relevant, "/stage", "/", false); err != nil {
		t.Fatalf("non-real restoreFS: expected nil error, got %v", err)
	}

	restoreFS = osFS{}
	restoreCmd = &FakeCommandRunner{}
	stageRoot := t.TempDir()
	if err := maybeApplyPVEConfigsFromStage(ctx, logger, relevant, stageRoot, "/", false); err != nil {
		t.Fatalf("realFS safe flow: expected nil error, got %v", err)
	}

	restoreCmd = &FakeCommandRunner{}
	clusterRecovery := pvePlan(true, "storage_pve", "pve_jobs")
	if err := maybeApplyPVEConfigsFromStage(ctx, logger, clusterRecovery, stageRoot, "/", false); err != nil {
		t.Fatalf("cluster recovery safe flow: expected nil error, got %v", err)
	}
}

func TestApplyPVEVzdumpConfFromStage_Branches(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	logger := newTestLogger()
	stageRoot := "/stage"

	if err := applyPVEVzdumpConfFromStage(logger, stageRoot); err != nil {
		t.Fatalf("missing staged file should be ignored: %v", err)
	}

	stagePath := filepath.Join(stageRoot, "etc/vzdump.conf")
	restoreFS = readFileFailFS{FS: fakeFS, failPath: stagePath, err: errors.New("boom")}
	if err := applyPVEVzdumpConfFromStage(logger, stageRoot); err == nil || !strings.Contains(err.Error(), "read staged etc/vzdump.conf") {
		t.Fatalf("expected staged read error, got %v", err)
	}
	restoreFS = fakeFS

	if err := fakeFS.AddFile("/etc/vzdump.conf", []byte("old\n")); err != nil {
		t.Fatalf("seed /etc/vzdump.conf: %v", err)
	}
	if err := fakeFS.AddFile(stagePath, []byte(" \n\t")); err != nil {
		t.Fatalf("write staged empty vzdump.conf: %v", err)
	}
	if err := applyPVEVzdumpConfFromStage(logger, stageRoot); err != nil {
		t.Fatalf("empty staged vzdump.conf: %v", err)
	}
	if _, err := fakeFS.Stat("/etc/vzdump.conf"); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected /etc/vzdump.conf removed; stat err=%v", err)
	}

	if err := fakeFS.AddFile(stagePath, []byte("  dumpdir /mnt/backup  \n\n")); err != nil {
		t.Fatalf("write staged non-empty vzdump.conf: %v", err)
	}
	if err := applyPVEVzdumpConfFromStage(logger, stageRoot); err != nil {
		t.Fatalf("non-empty staged vzdump.conf: %v", err)
	}
	got, err := fakeFS.ReadFile("/etc/vzdump.conf")
	if err != nil {
		t.Fatalf("read applied /etc/vzdump.conf: %v", err)
	}
	if string(got) != "dumpdir /mnt/backup\n" {
		t.Fatalf("applied /etc/vzdump.conf=%q want %q", string(got), "dumpdir /mnt/backup\n")
	}
}

func TestApplyPVEStorageCfgFromStage_Branches(t *testing.T) {
	ctx := context.Background()
	logger := newTestLogger()

	t.Run("pvesh missing", func(t *testing.T) {
		origFS := restoreFS
		origCmd := restoreCmd
		t.Cleanup(func() {
			restoreFS = origFS
			restoreCmd = origCmd
		})

		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		restoreCmd = &FakeCommandRunner{
			Errors: map[string]error{
				"which pvesh": errors.New("missing"),
			},
		}
		if err := applyPVEStorageCfgFromStage(ctx, logger, "/stage"); err != nil {
			t.Fatalf("expected nil when pvesh missing, got %v", err)
		}
	})

	t.Run("missing staged storage cfg", func(t *testing.T) {
		origFS := restoreFS
		origCmd := restoreCmd
		t.Cleanup(func() {
			restoreFS = origFS
			restoreCmd = origCmd
		})

		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS
		restoreCmd = &FakeCommandRunner{}

		if err := applyPVEStorageCfgFromStage(ctx, logger, "/stage"); err != nil {
			t.Fatalf("missing staged storage.cfg should be ignored: %v", err)
		}
	})

	t.Run("staged read error", func(t *testing.T) {
		origFS := restoreFS
		origCmd := restoreCmd
		t.Cleanup(func() {
			restoreFS = origFS
			restoreCmd = origCmd
		})

		baseFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(baseFS.Root) })
		stagePath := "/stage/etc/pve/storage.cfg"
		restoreFS = readFileFailFS{FS: baseFS, failPath: stagePath, err: syscall.EPERM}
		restoreCmd = &FakeCommandRunner{}

		if err := applyPVEStorageCfgFromStage(ctx, logger, "/stage"); err == nil || !strings.Contains(err.Error(), "read staged storage.cfg") {
			t.Fatalf("expected staged read error, got %v", err)
		}
	})

	t.Run("empty staged cfg", func(t *testing.T) {
		origFS := restoreFS
		origCmd := restoreCmd
		t.Cleanup(func() {
			restoreFS = origFS
			restoreCmd = origCmd
		})

		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS
		restoreCmd = &FakeCommandRunner{}
		if err := fakeFS.AddFile("/stage/etc/pve/storage.cfg", []byte(" \n\t")); err != nil {
			t.Fatalf("write staged storage.cfg: %v", err)
		}

		if err := applyPVEStorageCfgFromStage(ctx, logger, "/stage"); err != nil {
			t.Fatalf("empty storage.cfg should be ignored: %v", err)
		}
	})

	t.Run("applies staged cfg via pvesh", func(t *testing.T) {
		origFS := restoreFS
		origCmd := restoreCmd
		t.Cleanup(func() {
			restoreFS = origFS
			restoreCmd = origCmd
		})

		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		fakeCmd := &FakeCommandRunner{}
		restoreCmd = fakeCmd

		content := strings.Join([]string{
			"storage: local",
			"    type dir",
			"    path /var/lib/vz",
			"",
		}, "\n")
		if err := fakeFS.AddFile("/stage/etc/pve/storage.cfg", []byte(content)); err != nil {
			t.Fatalf("write staged storage.cfg: %v", err)
		}

		if err := applyPVEStorageCfgFromStage(ctx, logger, "/stage"); err != nil {
			t.Fatalf("applyPVEStorageCfgFromStage: %v", err)
		}
		calls := strings.Join(fakeCmd.CallsList(), "\n")
		if !strings.Contains(calls, "which pvesh") {
			t.Fatalf("missing which pvesh call; calls=%v", fakeCmd.CallsList())
		}
		if !strings.Contains(calls, "pvesh create /storage --storage=local --type=dir --path=/var/lib/vz") {
			t.Fatalf("missing pvesh create storage call; calls=%v", fakeCmd.CallsList())
		}
	})
}

func TestApplyPVEDatacenterCfgFromStage_Branches(t *testing.T) {
	ctx := context.Background()
	logger := newTestLogger()

	t.Run("pvesh missing", func(t *testing.T) {
		origFS := restoreFS
		origCmd := restoreCmd
		t.Cleanup(func() {
			restoreFS = origFS
			restoreCmd = origCmd
		})

		restoreFS = NewFakeFS()
		restoreCmd = &FakeCommandRunner{
			Errors: map[string]error{
				"which pvesh": errors.New("missing"),
			},
		}
		if err := applyPVEDatacenterCfgFromStage(ctx, logger, "/stage"); err != nil {
			t.Fatalf("expected nil when pvesh missing, got %v", err)
		}
	})

	t.Run("missing and empty staged datacenter cfg", func(t *testing.T) {
		origFS := restoreFS
		origCmd := restoreCmd
		t.Cleanup(func() {
			restoreFS = origFS
			restoreCmd = origCmd
		})

		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS
		restoreCmd = &FakeCommandRunner{}

		if err := applyPVEDatacenterCfgFromStage(ctx, logger, "/stage"); err != nil {
			t.Fatalf("missing staged datacenter.cfg should be ignored: %v", err)
		}

		if err := fakeFS.AddFile("/stage/etc/pve/datacenter.cfg", []byte(" \n\t")); err != nil {
			t.Fatalf("write staged datacenter.cfg: %v", err)
		}
		if err := applyPVEDatacenterCfgFromStage(ctx, logger, "/stage"); err != nil {
			t.Fatalf("empty staged datacenter.cfg should be ignored: %v", err)
		}
	})

	t.Run("runPvesh error and success", func(t *testing.T) {
		origFS := restoreFS
		origCmd := restoreCmd
		t.Cleanup(func() {
			restoreFS = origFS
			restoreCmd = origCmd
		})

		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		stagePath := "/stage/etc/pve/datacenter.cfg"
		if err := fakeFS.AddFile(stagePath, []byte("keyboard: it\n")); err != nil {
			t.Fatalf("write staged datacenter.cfg: %v", err)
		}

		failCmd := &FakeCommandRunner{
			Errors: map[string]error{
				"pvesh set /cluster/config -conf " + stagePath: errors.New("pvesh failed"),
			},
		}
		restoreCmd = failCmd
		if err := applyPVEDatacenterCfgFromStage(ctx, logger, "/stage"); err == nil || !strings.Contains(err.Error(), "pvesh [set /cluster/config -conf") {
			t.Fatalf("expected pvesh failure, got %v", err)
		}

		okCmd := &FakeCommandRunner{}
		restoreCmd = okCmd
		if err := applyPVEDatacenterCfgFromStage(ctx, logger, "/stage"); err != nil {
			t.Fatalf("expected success, got %v", err)
		}
		calls := strings.Join(okCmd.CallsList(), "\n")
		if !strings.Contains(calls, "pvesh set /cluster/config -conf "+stagePath) {
			t.Fatalf("missing datacenter apply call; calls=%v", okCmd.CallsList())
		}
	})
}

func TestApplyPVEBackupJobsFromStage_Branches(t *testing.T) {
	ctx := context.Background()
	logger := newTestLogger()

	t.Run("pvesh missing, stage missing, read error, empty, no jobs", func(t *testing.T) {
		origFS := restoreFS
		origCmd := restoreCmd
		t.Cleanup(func() {
			restoreFS = origFS
			restoreCmd = origCmd
		})

		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		restoreCmd = &FakeCommandRunner{
			Errors: map[string]error{"which pvesh": errors.New("missing")},
		}
		if err := applyPVEBackupJobsFromStage(ctx, logger, "/stage"); err != nil {
			t.Fatalf("expected nil when pvesh missing, got %v", err)
		}

		restoreCmd = &FakeCommandRunner{}
		if err := applyPVEBackupJobsFromStage(ctx, logger, "/stage"); err != nil {
			t.Fatalf("missing jobs.cfg should be ignored: %v", err)
		}

		stagePath := "/stage/etc/pve/jobs.cfg"
		restoreFS = readFileFailFS{FS: fakeFS, failPath: stagePath, err: syscall.EIO}
		if err := applyPVEBackupJobsFromStage(ctx, logger, "/stage"); err == nil || !strings.Contains(err.Error(), "read staged jobs.cfg") {
			t.Fatalf("expected staged read error, got %v", err)
		}
		restoreFS = fakeFS

		if err := fakeFS.AddFile(stagePath, []byte(" \n\t")); err != nil {
			t.Fatalf("write empty jobs.cfg: %v", err)
		}
		if err := applyPVEBackupJobsFromStage(ctx, logger, "/stage"); err != nil {
			t.Fatalf("empty jobs.cfg should be ignored: %v", err)
		}

		noJobs := "sendmail: relay\n    mailto root@example.com\n"
		if err := fakeFS.AddFile(stagePath, []byte(noJobs)); err != nil {
			t.Fatalf("write non-vzdump jobs.cfg: %v", err)
		}
		if err := applyPVEBackupJobsFromStage(ctx, logger, "/stage"); err != nil {
			t.Fatalf("non-vzdump jobs.cfg should be ignored: %v", err)
		}
	})

	t.Run("create fails then update succeeds", func(t *testing.T) {
		origFS := restoreFS
		origCmd := restoreCmd
		t.Cleanup(func() {
			restoreFS = origFS
			restoreCmd = origCmd
		})

		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		cfg := strings.Join([]string{
			"vzdump: job-update",
			"    node pve1",
			"    storage backup",
			"",
		}, "\n")
		if err := fakeFS.AddFile("/stage/etc/pve/jobs.cfg", []byte(cfg)); err != nil {
			t.Fatalf("write staged jobs.cfg: %v", err)
		}

		createCall := "pvesh create /cluster/backup --id job-update --node pve1 --storage backup"
		fakeCmd := &FakeCommandRunner{
			Errors: map[string]error{
				createCall: errors.New("already exists"),
			},
		}
		restoreCmd = fakeCmd
		if err := applyPVEBackupJobsFromStage(ctx, logger, "/stage"); err != nil {
			t.Fatalf("expected update fallback success, got %v", err)
		}
		calls := strings.Join(fakeCmd.CallsList(), "\n")
		if !strings.Contains(calls, createCall) {
			t.Fatalf("missing create call; calls=%v", fakeCmd.CallsList())
		}
		if !strings.Contains(calls, "pvesh set /cluster/backup/job-update --node pve1 --storage backup") {
			t.Fatalf("missing update fallback call; calls=%v", fakeCmd.CallsList())
		}
	})

	t.Run("create and update both fail", func(t *testing.T) {
		origFS := restoreFS
		origCmd := restoreCmd
		t.Cleanup(func() {
			restoreFS = origFS
			restoreCmd = origCmd
		})

		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		cfg := strings.Join([]string{
			"vzdump: job-fail",
			"    node pve1",
			"    storage backup",
			"",
		}, "\n")
		if err := fakeFS.AddFile("/stage/etc/pve/jobs.cfg", []byte(cfg)); err != nil {
			t.Fatalf("write staged jobs.cfg: %v", err)
		}

		fakeCmd := &FakeCommandRunner{
			Errors: map[string]error{
				"pvesh create /cluster/backup --id job-fail --node pve1 --storage backup": errors.New("create failed"),
				"pvesh set /cluster/backup/job-fail --node pve1 --storage backup":         errors.New("set failed"),
			},
		}
		restoreCmd = fakeCmd

		err := applyPVEBackupJobsFromStage(ctx, logger, "/stage")
		if err == nil || !strings.Contains(err.Error(), "applied=0 failed=1") {
			t.Fatalf("expected aggregate apply error, got %v", err)
		}
	})
}

func TestPVEStorageMountGuardCandidatesFromSections_CoversDirAndNonDir(t *testing.T) {
	sections := []proxmoxNotificationSection{
		{
			Type: "dir",
			Name: "local-dir",
			Entries: []proxmoxNotificationEntry{
				{Key: "path", Value: " /mnt/storage/pve/local-dir "},
			},
		},
		{
			Type: "nfs",
			Name: "nas01",
		},
		{
			Type: "  ",
			Name: "ignored",
		},
		{
			Type: "dir",
			Name: "",
			Entries: []proxmoxNotificationEntry{
				{Key: "path", Value: "/mnt/ignored"},
			},
		},
	}

	got := pveStorageMountGuardCandidatesFromSections(sections)
	if len(got) != 2 {
		t.Fatalf("candidates len=%d; want 2 (%v)", len(got), got)
	}
	if got[0].StorageID != "local-dir" || got[0].StorageType != "dir" || got[0].Path != "/mnt/storage/pve/local-dir" {
		t.Fatalf("unexpected dir candidate: %+v", got[0])
	}
	if got[1].StorageID != "nas01" || got[1].StorageType != "nfs" || got[1].Path != "" {
		t.Fatalf("unexpected non-dir candidate: %+v", got[1])
	}
}

func TestPVEStorageMountGuardItems_CoversFilteringAndFallbacks(t *testing.T) {
	candidates := []pveStorageMountGuardCandidate{
		{StorageID: "local-ok", StorageType: "dir", Path: "/mnt/pool/datastore/local-ok"},
		{StorageID: "local-no-fstab", StorageType: "dir", Path: "/mnt/other/datastore/nope"},
		{StorageID: "invalid-root", StorageType: "dir", Path: "/"},
		{StorageID: "nas-net", StorageType: "nfs"},
	}

	t.Run("with fstab map", func(t *testing.T) {
		items := pveStorageMountGuardItems(
			candidates,
			[]string{"/mnt/pool", "/mnt/other"},
			map[string]struct{}{
				"/mnt/pool": {},
			},
		)
		if len(items) != 2 {
			t.Fatalf("items len=%d; want 2 (%v)", len(items), items)
		}
		targets := []string{items[0].GuardTarget, items[1].GuardTarget}
		if !(containsGuardTarget(targets, "/mnt/pool") && containsGuardTarget(targets, "/mnt/pve/nas-net")) {
			t.Fatalf("unexpected guard targets: %v", targets)
		}
	})

	t.Run("without fstab map keeps only network", func(t *testing.T) {
		items := pveStorageMountGuardItems(candidates, []string{"/mnt/pool", "/mnt/other"}, nil)
		if len(items) != 1 || items[0].GuardTarget != "/mnt/pve/nas-net" {
			t.Fatalf("items=%v; want only network guard target", items)
		}
	})
}

func containsGuardTarget(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func TestMaybeApplyPVEStorageMountGuardsFromStage_EarlyAndFallback(t *testing.T) {
	ctx := context.Background()
	logger := newTestLogger()

	if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, nil, "/stage", "/"); err != nil {
		t.Fatalf("nil plan: expected nil error, got %v", err)
	}
	if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, &RestorePlan{SystemType: SystemTypePBS}, "/stage", "/"); err != nil {
		t.Fatalf("wrong system type: expected nil error, got %v", err)
	}
	if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, pvePlan(false, "pve_jobs"), "/stage", "/"); err != nil {
		t.Fatalf("missing storage category: expected nil error, got %v", err)
	}
	if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, pvePlan(false, "storage_pve"), "   ", "/"); err != nil {
		t.Fatalf("blank stageRoot: expected nil error, got %v", err)
	}
	if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, pvePlan(false, "storage_pve"), "/stage", "/tmp/not-root"); err != nil {
		t.Fatalf("non-root destination: expected nil error, got %v", err)
	}
	requireWritablePveMountRoot(t)

	origFS := restoreFS
	origCmd := restoreCmd
	origReadFile := mountGuardReadFile
	origMkdirAll := mountGuardMkdirAll
	origSysMount := mountGuardSysMount
	origSysUnmount := mountGuardSysUnmount
	origGeteuid := mountGuardGeteuid
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
		mountGuardReadFile = origReadFile
		mountGuardMkdirAll = origMkdirAll
		mountGuardSysMount = origSysMount
		mountGuardSysUnmount = origSysUnmount
		mountGuardGeteuid = origGeteuid
	})
	mountGuardGeteuid = func() int { return 0 }

	nonRealFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(nonRealFS.Root) })
	restoreFS = nonRealFS
	restoreCmd = &FakeCommandRunner{}
	if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, pvePlan(false, "storage_pve"), "/stage", "/"); err != nil {
		t.Fatalf("non-real FS: expected nil error, got %v", err)
	}

	restoreFS = osFS{}
	stageRoot := t.TempDir()
	if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, pvePlan(false, "storage_pve"), stageRoot, "/"); err != nil {
		t.Fatalf("missing staged storage.cfg should be ignored: %v", err)
	}

	stageCfgPath := filepath.Join(stageRoot, "etc/pve/storage.cfg")
	if err := os.MkdirAll(filepath.Dir(stageCfgPath), 0o755); err != nil {
		t.Fatalf("mkdir staged storage.cfg dir: %v", err)
	}
	offlineID := uniquePveMountTestStorageID(t, "offline")
	if err := os.WriteFile(stageCfgPath, []byte("nfs: "+offlineID+"\n"), 0o644); err != nil {
		t.Fatalf("write staged storage.cfg: %v", err)
	}

	target := pveMountTargetForStorageID(offlineID)
	cleanupPveMountTestTarget(t, target)
	cleanupPveMountTestGuardDir(t, target)

	fakeCmd := &FakeCommandRunner{
		Errors: map[string]error{
			"which pvesm":          errors.New("missing"),
			"mount " + target:      errors.New("offline"),
			"chattr +i " + target:  nil,
		},
	}
	restoreCmd = fakeCmd

	mountGuardReadFile = func(path string) ([]byte, error) {
		switch path {
		case "/proc/self/mountinfo", "/proc/mounts":
			return []byte(""), nil
		default:
			return nil, os.ErrNotExist
		}
	}
	mountGuardMkdirAll = func(path string, perm os.FileMode) error { return nil }
	mountGuardSysMount = func(source, target, fstype string, flags uintptr, data string) error {
		return errors.New("blocked mount")
	}
	mountGuardSysUnmount = func(target string, flags int) error { return nil }

	if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, pvePlan(false, "storage_pve"), stageRoot, "/"); err != nil {
		t.Fatalf("fallback guard path should not return fatal error, got %v", err)
	}
	calls := strings.Join(fakeCmd.CallsList(), "\n")
	if !strings.Contains(calls, "mount "+target) {
		t.Fatalf("missing offline mount attempt; calls=%v", fakeCmd.CallsList())
	}
	if !strings.Contains(calls, "chattr +i "+target) {
		t.Fatalf("missing chattr fallback call; calls=%v", fakeCmd.CallsList())
	}
}

func TestMaybeApplyPVEStorageMountGuardsFromStage_ReadAndNoopBranches(t *testing.T) {
	ctx := context.Background()
	logger := newTestLogger()
	plan := pvePlan(false, "storage_pve")

	origFS := restoreFS
	origCmd := restoreCmd
	origGeteuid := mountGuardGeteuid
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
		mountGuardGeteuid = origGeteuid
	})

	restoreFS = osFS{}
	mountGuardGeteuid = func() int { return 0 }
	restoreCmd = &FakeCommandRunner{}

	stageRoot := t.TempDir()
	stageCfgPath := filepath.Join(stageRoot, "etc/pve/storage.cfg")
	if err := os.MkdirAll(filepath.Dir(stageCfgPath), 0o755); err != nil {
		t.Fatalf("mkdir stage cfg dir: %v", err)
	}

	if err := os.Mkdir(stageCfgPath, 0o755); err != nil {
		t.Fatalf("mkdir stage cfg path as directory: %v", err)
	}
	err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, plan, stageRoot, "/")
	if err == nil || !strings.Contains(err.Error(), "read staged storage.cfg") {
		t.Fatalf("expected staged read error, got %v", err)
	}

	if err := os.Remove(stageCfgPath); err != nil {
		t.Fatalf("remove stage cfg directory: %v", err)
	}
	if err := os.WriteFile(stageCfgPath, []byte(" \n\t"), 0o644); err != nil {
		t.Fatalf("write empty staged storage.cfg: %v", err)
	}
	if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, plan, stageRoot, "/"); err != nil {
		t.Fatalf("empty staged storage.cfg should be ignored: %v", err)
	}

	if err := os.WriteFile(stageCfgPath, []byte("not_a_section_header line\nkey value\n"), 0o644); err != nil {
		t.Fatalf("write invalid/no-section storage.cfg: %v", err)
	}
	if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, plan, stageRoot, "/"); err != nil {
		t.Fatalf("no section candidates should be ignored: %v", err)
	}
}

func TestMaybeApplyPVEStorageMountGuardsFromStage_ActivateAndGuardBranches(t *testing.T) {
	ctx := context.Background()
	logger := newTestLogger()
	plan := pvePlan(false, "storage_pve")
	requireWritablePveMountRoot(t)

	origFS := restoreFS
	origCmd := restoreCmd
	origReadFile := mountGuardReadFile
	origMkdirAll := mountGuardMkdirAll
	origSysMount := mountGuardSysMount
	origSysUnmount := mountGuardSysUnmount
	origGeteuid := mountGuardGeteuid
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
		mountGuardReadFile = origReadFile
		mountGuardMkdirAll = origMkdirAll
		mountGuardSysMount = origSysMount
		mountGuardSysUnmount = origSysUnmount
		mountGuardGeteuid = origGeteuid
	})

	restoreFS = osFS{}
	mountGuardGeteuid = func() int { return 0 }
	mountGuardMkdirAll = os.MkdirAll
	mountGuardSysUnmount = func(target string, flags int) error { return nil }

	t.Run("network activate succeeds and skips guard", func(t *testing.T) {
		stageRoot := t.TempDir()
		stageCfgPath := filepath.Join(stageRoot, "etc/pve/storage.cfg")
		if err := os.MkdirAll(filepath.Dir(stageCfgPath), 0o755); err != nil {
			t.Fatalf("mkdir stage cfg dir: %v", err)
		}
		activateID := uniquePveMountTestStorageID(t, "activate-ok")
		if err := os.WriteFile(stageCfgPath, []byte("nfs: "+activateID+"\n"), 0o644); err != nil {
			t.Fatalf("write staged storage.cfg: %v", err)
		}

		target := pveMountTargetForStorageID(activateID)
		cleanupPveMountTestTarget(t, target)
		cleanupPveMountTestGuardDir(t, target)

		fakeCmd := &FakeCommandRunner{}
		restoreCmd = fakeCmd

		readCalls := 0
		mountGuardReadFile = func(path string) ([]byte, error) {
			if path == "/proc/self/mountinfo" {
				readCalls++
				if readCalls >= 2 {
					line := "24 33 0:20 / " + target + " rw,relatime - tmpfs tmpfs rw\n"
					return []byte(line), nil
				}
				return []byte(""), nil
			}
			if path == "/proc/mounts" {
				return []byte(""), nil
			}
			return nil, os.ErrNotExist
		}
		mountGuardSysMount = func(source, target, fstype string, flags uintptr, data string) error {
			t.Fatalf("guard mount should not be attempted when activation makes target mounted")
			return nil
		}

		if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, plan, stageRoot, "/"); err != nil {
			t.Fatalf("expected successful activate path, got %v", err)
		}
		calls := strings.Join(fakeCmd.CallsList(), "\n")
		if !strings.Contains(calls, "which pvesm") {
			t.Fatalf("missing which pvesm call; calls=%v", fakeCmd.CallsList())
		}
		if !strings.Contains(calls, "pvesm activate "+activateID) {
			t.Fatalf("missing pvesm activate call; calls=%v", fakeCmd.CallsList())
		}
		if strings.Contains(calls, "chattr +i "+target) {
			t.Fatalf("chattr fallback should not run on activation success; calls=%v", fakeCmd.CallsList())
		}
	})

	t.Run("mkdir failure and off-root symlink are skipped", func(t *testing.T) {
		mkdirFailID := uniquePveMountTestStorageID(t, "mkdir-fail")
		offrootID := uniquePveMountTestStorageID(t, "offroot-link")
		stageRoot := t.TempDir()
		stageCfgPath := filepath.Join(stageRoot, "etc/pve/storage.cfg")
		if err := os.MkdirAll(filepath.Dir(stageCfgPath), 0o755); err != nil {
			t.Fatalf("mkdir stage cfg dir: %v", err)
		}
		cfg := strings.Join([]string{
			"nfs: " + mkdirFailID,
			"nfs: " + offrootID,
			"",
		}, "\n")
		if err := os.WriteFile(stageCfgPath, []byte(cfg), 0o644); err != nil {
			t.Fatalf("write staged storage.cfg: %v", err)
		}

		mkdirFailTarget := pveMountTargetForStorageID(mkdirFailID)
		offrootTarget := pveMountTargetForStorageID(offrootID)
		if err := os.WriteFile(mkdirFailTarget, []byte("file blocks mkdir"), 0o644); err != nil {
			t.Fatalf("seed mkdir-fail target file: %v", err)
		}
		t.Cleanup(func() { removePveMountTestPathIfExists(t, mkdirFailTarget) })

		removePveMountTestPathIfExists(t, offrootTarget)
		if err := os.Symlink("/proc", offrootTarget); err != nil {
			t.Fatalf("create offroot symlink: %v", err)
		}
		t.Cleanup(func() { removePveMountTestPathIfExists(t, offrootTarget) })

		fakeCmd := &FakeCommandRunner{
			Errors: map[string]error{
				"which pvesm": errors.New("missing"),
			},
		}
		restoreCmd = fakeCmd
		mountGuardReadFile = func(path string) ([]byte, error) {
			switch path {
			case "/proc/self/mountinfo", "/proc/mounts":
				return []byte(""), nil
			default:
				return nil, os.ErrNotExist
			}
		}
		mountGuardSysMount = func(source, target, fstype string, flags uintptr, data string) error {
			t.Fatalf("guard mount should not be attempted for skipped targets")
			return nil
		}

		if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, plan, stageRoot, "/"); err != nil {
			t.Fatalf("expected skips for mkdir failure/off-root symlink, got %v", err)
		}
		calls := strings.Join(fakeCmd.CallsList(), "\n")
		if strings.Contains(calls, "mount "+mkdirFailTarget) || strings.Contains(calls, "mount "+offrootTarget) {
			t.Fatalf("mount attempts should not happen for skipped targets; calls=%v", fakeCmd.CallsList())
		}
	})

	t.Run("guard fallback chattr failure and guard bind success", func(t *testing.T) {
		stageRoot := t.TempDir()
		stageCfgPath := filepath.Join(stageRoot, "etc/pve/storage.cfg")
		if err := os.MkdirAll(filepath.Dir(stageCfgPath), 0o755); err != nil {
			t.Fatalf("mkdir stage cfg dir: %v", err)
		}
		chattrFailID := uniquePveMountTestStorageID(t, "chattr-fail")
		guardOKID := uniquePveMountTestStorageID(t, "guard-ok")
		cfg := strings.Join([]string{
			"nfs: " + chattrFailID,
			"nfs: " + guardOKID,
			"",
		}, "\n")
		if err := os.WriteFile(stageCfgPath, []byte(cfg), 0o644); err != nil {
			t.Fatalf("write staged storage.cfg: %v", err)
		}

		chattrFailTarget := pveMountTargetForStorageID(chattrFailID)
		guardOKTarget := pveMountTargetForStorageID(guardOKID)
		cleanupPveMountTestTarget(t, chattrFailTarget)
		cleanupPveMountTestTarget(t, guardOKTarget)
		cleanupPveMountTestGuardDir(t, chattrFailTarget)
		cleanupPveMountTestGuardDir(t, guardOKTarget)

		fakeCmd := &FakeCommandRunner{
			Errors: map[string]error{
				"which pvesm":                  errors.New("missing"),
				"mount " + chattrFailTarget:    errors.New("offline"),
				"chattr +i " + chattrFailTarget: errors.New("chattr denied"),
				"mount " + guardOKTarget:       errors.New("offline"),
			},
		}
		restoreCmd = fakeCmd
		mountGuardReadFile = func(path string) ([]byte, error) {
			switch path {
			case "/proc/self/mountinfo", "/proc/mounts":
				return []byte(""), nil
			default:
				return nil, os.ErrNotExist
			}
		}
		mountGuardSysMount = func(source, target, fstype string, flags uintptr, data string) error {
			if target == chattrFailTarget {
				return errors.New("bind denied")
			}
			return nil
		}

		if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, plan, stageRoot, "/"); err != nil {
			t.Fatalf("expected non-fatal guard fallback handling, got %v", err)
		}
		calls := strings.Join(fakeCmd.CallsList(), "\n")
		if !strings.Contains(calls, "chattr +i "+chattrFailTarget) {
			t.Fatalf("missing chattr fallback call for failing guard target; calls=%v", fakeCmd.CallsList())
		}
		if strings.Contains(calls, "chattr +i "+guardOKTarget) {
			t.Fatalf("chattr should not run when guard bind succeeds; calls=%v", fakeCmd.CallsList())
		}
	})
}
