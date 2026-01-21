package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func mustCategoryByID(t *testing.T, id string) Category {
	t.Helper()
	for _, cat := range GetAllCategories() {
		if cat.ID == id {
			return cat
		}
	}
	t.Fatalf("missing category id %q", id)
	return Category{}
}

func TestRunRestoreWorkflow_ClusterBackupSafeMode_ExportsClusterAndRestoresNetwork(t *testing.T) {
	origRestoreFS := restoreFS
	origRestoreCmd := restoreCmd
	origRestorePrompter := restorePrompter
	origRestoreSystem := restoreSystem
	origRestoreTime := restoreTime
	origCompatFS := compatFS
	origPrepare := prepareDecryptedBackupFunc
	origSafetyFS := safetyFS
	origSafetyNow := safetyNow
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		restoreCmd = origRestoreCmd
		restorePrompter = origRestorePrompter
		restoreSystem = origRestoreSystem
		restoreTime = origRestoreTime
		compatFS = origCompatFS
		prepareDecryptedBackupFunc = origPrepare
		safetyFS = origSafetyFS
		safetyNow = origSafetyNow
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	compatFS = fakeFS
	safetyFS = fakeFS

	fakeNow := &FakeTime{Current: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	restoreTime = fakeNow
	safetyNow = fakeNow.Now

	// Make compatibility detection treat this as PVE.
	if err := fakeFS.AddFile("/usr/bin/qm", []byte("x")); err != nil {
		t.Fatalf("fakeFS.AddFile: %v", err)
	}

	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}
	restoreCmd = runOnlyRunner{}

	// Prepare an uncompressed tar archive inside the fake FS.
	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/hosts":                     "127.0.0.1 localhost\n",
		"etc/pve/jobs.cfg":              "jobs\n",
		"var/lib/pve-cluster/config.db": "db\n",
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}
	tarBytes, err := os.ReadFile(tmpTar)
	if err != nil {
		t.Fatalf("ReadFile tar: %v", err)
	}
	if err := fakeFS.WriteFile("/bundle.tar", tarBytes, 0o640); err != nil {
		t.Fatalf("fakeFS.WriteFile: %v", err)
	}

	restorePrompter = fakeRestorePrompter{
		mode: RestoreModeCustom,
		categories: []Category{
			mustCategoryByID(t, "network"),
			mustCategoryByID(t, "pve_cluster"),
			mustCategoryByID(t, "pve_config_export"),
		},
		confirmed: true,
	}

	prepareDecryptedBackupFunc = func(ctx context.Context, reader *bufio.Reader, cfg *config.Config, logger *logging.Logger, version string, requireEncrypted bool) (*decryptCandidate, *preparedBundle, error) {
		cand := &decryptCandidate{
			DisplayBase: "test",
			Manifest: &backup.Manifest{
				CreatedAt:     fakeNow.Now(),
				ClusterMode:   "cluster",
				ProxmoxType:   "pve",
				ScriptVersion: "vtest",
			},
		}
		prepared := &preparedBundle{
			ArchivePath: "/bundle.tar",
			Manifest:    backup.Manifest{ArchivePath: "/bundle.tar"},
			cleanup:     func() {},
		}
		return cand, prepared, nil
	}

	oldIn := os.Stdin
	oldOut := os.Stdout
	t.Cleanup(func() {
		os.Stdin = oldIn
		os.Stdout = oldOut
	})
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0o666)
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		t.Fatalf("OpenFile(%s): %v", os.DevNull, err)
	}
	os.Stdin = inR
	os.Stdout = out
	t.Cleanup(func() {
		_ = inR.Close()
		_ = out.Close()
	})

	// Cluster restore prompt -> SAFE mode.
	if _, err := inW.WriteString("1\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = inW.Close()

	t.Setenv("PATH", "") // ensure pvesh is not found for SAFE apply

	logger := logging.New(types.LogLevelError, false)
	cfg := &config.Config{BaseDir: "/base"}

	if err := RunRestoreWorkflow(context.Background(), cfg, logger, "vtest"); err != nil {
		t.Fatalf("RunRestoreWorkflow error: %v", err)
	}

	hosts, err := fakeFS.ReadFile("/etc/hosts")
	if err != nil {
		t.Fatalf("expected restored /etc/hosts: %v", err)
	}
	if string(hosts) != "127.0.0.1 localhost\n" {
		t.Fatalf("hosts=%q want %q", string(hosts), "127.0.0.1 localhost\n")
	}

	exportRoot := filepath.Join(cfg.BaseDir, "proxmox-config-export-20200102-030405")
	if _, err := fakeFS.Stat(exportRoot); err != nil {
		t.Fatalf("expected export root %s to exist: %v", exportRoot, err)
	}
	if _, err := fakeFS.ReadFile(filepath.Join(exportRoot, "etc/pve/jobs.cfg")); err != nil {
		t.Fatalf("expected exported jobs.cfg: %v", err)
	}
	if _, err := fakeFS.ReadFile(filepath.Join(exportRoot, "var/lib/pve-cluster/config.db")); err != nil {
		t.Fatalf("expected exported config.db: %v", err)
	}
}

func TestRunRestoreWorkflow_PBSStopsServicesAndChecksZFSWhenSelected(t *testing.T) {
	origRestoreFS := restoreFS
	origRestoreCmd := restoreCmd
	origRestorePrompter := restorePrompter
	origRestoreSystem := restoreSystem
	origRestoreTime := restoreTime
	origCompatFS := compatFS
	origPrepare := prepareDecryptedBackupFunc
	origSafetyFS := safetyFS
	origSafetyNow := safetyNow
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		restoreCmd = origRestoreCmd
		restorePrompter = origRestorePrompter
		restoreSystem = origRestoreSystem
		restoreTime = origRestoreTime
		compatFS = origCompatFS
		prepareDecryptedBackupFunc = origPrepare
		safetyFS = origSafetyFS
		safetyNow = origSafetyNow
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	compatFS = fakeFS
	safetyFS = fakeFS

	fakeNow := &FakeTime{Current: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	restoreTime = fakeNow
	safetyNow = fakeNow.Now

	// Make compatibility detection treat this as PBS.
	if err := fakeFS.AddDir("/etc/proxmox-backup"); err != nil {
		t.Fatalf("fakeFS.AddDir: %v", err)
	}

	restoreSystem = fakeSystemDetector{systemType: SystemTypePBS}

	cmd := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"which zpool":  []byte("/sbin/zpool\n"),
			"zpool import": []byte(""),
		},
		Errors: map[string]error{},
	}
	for _, svc := range []string{"proxmox-backup-proxy", "proxmox-backup"} {
		cmd.Outputs["systemctl stop --no-block "+svc] = []byte("ok")
		cmd.Outputs["systemctl is-active "+svc] = []byte("inactive\n")
		cmd.Errors["systemctl is-active "+svc] = errors.New("inactive")
		cmd.Outputs["systemctl reset-failed "+svc] = []byte("ok")
		cmd.Outputs["systemctl start "+svc] = []byte("ok")
	}
	restoreCmd = cmd

	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/proxmox-backup/sync.cfg": "sync\n",
		"etc/hostid":                  "hostid\n",
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}
	tarBytes, err := os.ReadFile(tmpTar)
	if err != nil {
		t.Fatalf("ReadFile tar: %v", err)
	}
	if err := fakeFS.WriteFile("/bundle.tar", tarBytes, 0o640); err != nil {
		t.Fatalf("fakeFS.WriteFile: %v", err)
	}

	restorePrompter = fakeRestorePrompter{
		mode: RestoreModeCustom,
		categories: []Category{
			mustCategoryByID(t, "pbs_jobs"),
			mustCategoryByID(t, "zfs"),
		},
		confirmed: true,
	}

	prepareDecryptedBackupFunc = func(ctx context.Context, reader *bufio.Reader, cfg *config.Config, logger *logging.Logger, version string, requireEncrypted bool) (*decryptCandidate, *preparedBundle, error) {
		cand := &decryptCandidate{
			DisplayBase: "test",
			Manifest: &backup.Manifest{
				CreatedAt:     fakeNow.Now(),
				ClusterMode:   "standalone",
				ProxmoxType:   "pbs",
				ScriptVersion: "vtest",
			},
		}
		prepared := &preparedBundle{
			ArchivePath: "/bundle.tar",
			Manifest:    backup.Manifest{ArchivePath: "/bundle.tar"},
			cleanup:     func() {},
		}
		return cand, prepared, nil
	}

	logger := logging.New(types.LogLevelError, false)
	cfg := &config.Config{BaseDir: "/base"}

	if err := RunRestoreWorkflow(context.Background(), cfg, logger, "vtest"); err != nil {
		t.Fatalf("RunRestoreWorkflow error: %v", err)
	}

	if _, err := fakeFS.ReadFile("/etc/proxmox-backup/sync.cfg"); err != nil {
		t.Fatalf("expected restored PBS sync.cfg: %v", err)
	}
	if _, err := fakeFS.ReadFile("/etc/hostid"); err != nil {
		t.Fatalf("expected restored hostid: %v", err)
	}

	expected := []string{
		"systemctl stop --no-block proxmox-backup-proxy",
		"systemctl is-active proxmox-backup-proxy",
		"systemctl reset-failed proxmox-backup-proxy",
		"systemctl stop --no-block proxmox-backup",
		"systemctl is-active proxmox-backup",
		"systemctl reset-failed proxmox-backup",
		"which zpool",
		"zpool import",
		"systemctl start proxmox-backup-proxy",
		"systemctl start proxmox-backup",
	}
	for _, want := range expected {
		found := false
		for _, call := range cmd.Calls {
			if call == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing command call %q; calls=%v", want, cmd.Calls)
		}
	}
}

func TestRunRestoreWorkflow_IncompatibilityAndSafetyBackupFailureCanContinue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based safety backup failure is not reliable on Windows")
	}

	origRestoreFS := restoreFS
	origRestoreCmd := restoreCmd
	origRestorePrompter := restorePrompter
	origRestoreSystem := restoreSystem
	origRestoreTime := restoreTime
	origCompatFS := compatFS
	origPrepare := prepareDecryptedBackupFunc
	origSafetyFS := safetyFS
	origSafetyNow := safetyNow
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		restoreCmd = origRestoreCmd
		restorePrompter = origRestorePrompter
		restoreSystem = origRestoreSystem
		restoreTime = origRestoreTime
		compatFS = origCompatFS
		prepareDecryptedBackupFunc = origPrepare
		safetyFS = origSafetyFS
		safetyNow = origSafetyNow
	})

	restoreSandbox := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(restoreSandbox.Root) })
	restoreFS = restoreSandbox
	compatFS = restoreSandbox

	safetySandbox := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(safetySandbox.Root) })
	if err := os.Chmod(safetySandbox.Root, 0o500); err != nil {
		t.Fatalf("chmod safety root: %v", err)
	}
	safetyFS = safetySandbox

	fakeNow := &FakeTime{Current: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	restoreTime = fakeNow
	safetyNow = fakeNow.Now

	// Make compatibility detection treat this as PVE.
	if err := restoreSandbox.AddFile("/usr/bin/qm", []byte("x")); err != nil {
		t.Fatalf("restoreSandbox.AddFile: %v", err)
	}
	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}
	restoreCmd = runOnlyRunner{}

	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/hosts": "127.0.0.1 localhost\n",
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}
	tarBytes, err := os.ReadFile(tmpTar)
	if err != nil {
		t.Fatalf("ReadFile tar: %v", err)
	}
	if err := restoreSandbox.WriteFile("/bundle.tar", tarBytes, 0o640); err != nil {
		t.Fatalf("restoreSandbox.WriteFile: %v", err)
	}

	restorePrompter = fakeRestorePrompter{
		mode: RestoreModeCustom,
		categories: []Category{
			mustCategoryByID(t, "network"),
		},
		confirmed: true,
	}

	prepareDecryptedBackupFunc = func(ctx context.Context, reader *bufio.Reader, cfg *config.Config, logger *logging.Logger, version string, requireEncrypted bool) (*decryptCandidate, *preparedBundle, error) {
		cand := &decryptCandidate{
			DisplayBase: "test",
			Manifest: &backup.Manifest{
				CreatedAt:     fakeNow.Now(),
				ProxmoxType:   "pbs",
				ClusterMode:   "standalone",
				ScriptVersion: "vtest",
			},
		}
		prepared := &preparedBundle{
			ArchivePath: "/bundle.tar",
			Manifest:    backup.Manifest{ArchivePath: "/bundle.tar"},
			cleanup:     func() {},
		}
		return cand, prepared, nil
	}

	oldIn := os.Stdin
	oldOut := os.Stdout
	t.Cleanup(func() {
		os.Stdin = oldIn
		os.Stdout = oldOut
	})
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0o666)
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		t.Fatalf("OpenFile(%s): %v", os.DevNull, err)
	}
	os.Stdin = inR
	os.Stdout = out
	t.Cleanup(func() {
		_ = inR.Close()
		_ = out.Close()
	})

	// Compatibility prompt -> continue; safety backup failure prompt -> continue.
	if _, err := inW.WriteString("yes\nyes\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = inW.Close()

	logger := logging.New(types.LogLevelError, false)
	cfg := &config.Config{BaseDir: "/base"}

	if err := RunRestoreWorkflow(context.Background(), cfg, logger, "vtest"); err != nil {
		t.Fatalf("RunRestoreWorkflow error: %v", err)
	}

	if _, err := restoreSandbox.ReadFile("/etc/hosts"); err != nil {
		t.Fatalf("expected restored /etc/hosts: %v", err)
	}
}

func TestRunRestoreWorkflow_ClusterRecoveryModeStopsAndRestartsServices(t *testing.T) {
	origRestoreFS := restoreFS
	origRestoreCmd := restoreCmd
	origRestorePrompter := restorePrompter
	origRestoreSystem := restoreSystem
	origRestoreTime := restoreTime
	origCompatFS := compatFS
	origPrepare := prepareDecryptedBackupFunc
	origSafetyFS := safetyFS
	origSafetyNow := safetyNow
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		restoreCmd = origRestoreCmd
		restorePrompter = origRestorePrompter
		restoreSystem = origRestoreSystem
		restoreTime = origRestoreTime
		compatFS = origCompatFS
		prepareDecryptedBackupFunc = origPrepare
		safetyFS = origSafetyFS
		safetyNow = origSafetyNow
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	compatFS = fakeFS
	safetyFS = fakeFS

	fakeNow := &FakeTime{Current: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	restoreTime = fakeNow
	safetyNow = fakeNow.Now

	if err := fakeFS.AddFile("/usr/bin/qm", []byte("x")); err != nil {
		t.Fatalf("fakeFS.AddFile: %v", err)
	}
	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}

	cmd := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"umount /etc/pve": []byte("not mounted\n"),
		},
		Errors: map[string]error{
			"umount /etc/pve": errors.New("not mounted"),
		},
	}
	for _, svc := range []string{"pve-cluster", "pvedaemon", "pveproxy", "pvestatd"} {
		cmd.Outputs["systemctl stop --no-block "+svc] = []byte("ok")
		cmd.Outputs["systemctl is-active "+svc] = []byte("inactive\n")
		cmd.Errors["systemctl is-active "+svc] = errors.New("inactive")
		cmd.Outputs["systemctl reset-failed "+svc] = []byte("ok")
		cmd.Outputs["systemctl start "+svc] = []byte("ok")
	}
	restoreCmd = cmd

	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/hosts":                     "127.0.0.1 localhost\n",
		"var/lib/pve-cluster/config.db": "db\n",
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}
	tarBytes, err := os.ReadFile(tmpTar)
	if err != nil {
		t.Fatalf("ReadFile tar: %v", err)
	}
	if err := fakeFS.WriteFile("/bundle.tar", tarBytes, 0o640); err != nil {
		t.Fatalf("fakeFS.WriteFile: %v", err)
	}

	restorePrompter = fakeRestorePrompter{
		mode: RestoreModeCustom,
		categories: []Category{
			mustCategoryByID(t, "network"),
			mustCategoryByID(t, "pve_cluster"),
		},
		confirmed: true,
	}

	prepareDecryptedBackupFunc = func(ctx context.Context, reader *bufio.Reader, cfg *config.Config, logger *logging.Logger, version string, requireEncrypted bool) (*decryptCandidate, *preparedBundle, error) {
		cand := &decryptCandidate{
			DisplayBase: "test",
			Manifest: &backup.Manifest{
				CreatedAt:     fakeNow.Now(),
				ClusterMode:   "cluster",
				ProxmoxType:   "pve",
				ScriptVersion: "vtest",
			},
		}
		prepared := &preparedBundle{
			ArchivePath: "/bundle.tar",
			Manifest:    backup.Manifest{ArchivePath: "/bundle.tar"},
			cleanup:     func() {},
		}
		return cand, prepared, nil
	}

	oldIn := os.Stdin
	oldOut := os.Stdout
	t.Cleanup(func() {
		os.Stdin = oldIn
		os.Stdout = oldOut
	})
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0o666)
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		t.Fatalf("OpenFile(%s): %v", os.DevNull, err)
	}
	os.Stdin = inR
	os.Stdout = out
	t.Cleanup(func() {
		_ = inR.Close()
		_ = out.Close()
	})

	// Cluster restore prompt -> RECOVERY mode.
	if _, err := inW.WriteString("2\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = inW.Close()

	logger := logging.New(types.LogLevelError, false)
	cfg := &config.Config{BaseDir: "/base"}

	if err := RunRestoreWorkflow(context.Background(), cfg, logger, "vtest"); err != nil {
		t.Fatalf("RunRestoreWorkflow error: %v", err)
	}

	for _, want := range []string{
		"systemctl stop --no-block pve-cluster",
		"systemctl stop --no-block pvedaemon",
		"systemctl stop --no-block pveproxy",
		"systemctl stop --no-block pvestatd",
		"umount /etc/pve",
		"systemctl start pve-cluster",
		"systemctl start pvedaemon",
		"systemctl start pveproxy",
		"systemctl start pvestatd",
	} {
		found := false
		for _, call := range cmd.Calls {
			if call == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing command call %q; calls=%v", want, cmd.Calls)
		}
	}
}
