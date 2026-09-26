package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A restore closes by recommending a reboot, and on a host whose /tmp is a tmpfs
// (Debian 13, the base of PVE 9 and PBS 4, measured on pve-test) that reboot empties
// /tmp. The safety backup, the four rollback archives and the detailed logs are what
// the operator has to go back to after it, so none of them may live under /tmp, and
// all the files of one restore sit in one directory.
func TestRestorePersistentMaterialLivesOutsideTmpInOneDirectory(t *testing.T) {
	origSafetyFS, origSafetyNow, origRestoreFS, origRestoreTime := safetyFS, safetyNow, restoreFS, restoreTime
	t.Cleanup(func() {
		safetyFS, safetyNow, restoreFS, restoreTime = origSafetyFS, origSafetyNow, origRestoreFS, origRestoreTime
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	safetyFS, restoreFS = fakeFS, fakeFS
	now := &FakeTime{Current: time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)}
	safetyNow, restoreTime = now.Now, now

	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{"etc/ssh/sshd_config": "x\n"}); err != nil {
		t.Fatal(err)
	}
	tarBytes, err := os.ReadFile(tmpTar)
	if err != nil {
		t.Fatal(err)
	}
	if err := fakeFS.WriteFile("/bundle.tar", tarBytes, 0o640); err != nil {
		t.Fatal(err)
	}

	logger := logging.New(types.LogLevelError, false)
	cats := []Category{
		mustCategoryByID(t, "ssh"),
		mustCategoryByID(t, "network"),
		mustCategoryByID(t, "pve_firewall"),
		mustCategoryByID(t, "pve_ha"),
		mustCategoryByID(t, "pve_access_control"),
	}

	files := map[string]string{}
	for name, create := range map[string]func(*logging.Logger, []Category, string) (*SafetyBackupResult, error){
		"safety backup":                  CreateSafetyBackup,
		"network rollback backup":        CreateNetworkRollbackBackup,
		"firewall rollback backup":       CreateFirewallRollbackBackup,
		"HA rollback backup":             CreateHARollbackBackup,
		"access control rollback backup": CreatePVEAccessControlRollbackBackup,
	} {
		res, err := create(logger, cats, "/")
		if err != nil || res == nil {
			t.Fatalf("%s: result=%v err=%v", name, res, err)
		}
		files[name] = res.BackupPath
	}
	logPath, err := extractSelectiveArchive(context.Background(), "/bundle.tar", "/restore-target", []Category{mustCategoryByID(t, "ssh")}, RestoreModeCustom, logger)
	if err != nil {
		t.Fatalf("extractSelectiveArchive: %v", err)
	}
	files["detailed log"] = logPath

	runDir := filepath.Dir(files["safety backup"])
	for _, name := range []string{
		"restore_backup_location.txt",
		"network_rollback_backup_location.txt",
		"firewall_rollback_backup_location.txt",
		"ha_rollback_backup_location.txt",
		"pve_access_control_rollback_backup_location.txt",
	} {
		files[name] = filepath.Join(runDir, name)
	}

	if !strings.HasPrefix(runDir, "/var/lib/proxsave/restore/") {
		t.Errorf("run directory %s is not under /var/lib/proxsave/restore/", runDir)
	}
	if info, err := fakeFS.Stat(runDir); err != nil {
		t.Errorf("stat run directory %s: %v", runDir, err)
	} else if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("run directory %s mode = %o, want 700", runDir, got)
	}
	for name, p := range files {
		if strings.HasPrefix(p, "/tmp/") {
			t.Errorf("%s is under /tmp (%s) while the run recommends a reboot", name, p)
		}
		if filepath.Dir(p) != runDir {
			t.Errorf("%s is in %s, not in the run directory %s", name, filepath.Dir(p), runDir)
		}
		info, err := fakeFS.Stat(p)
		if err != nil {
			t.Errorf("%s: stat %s: %v", name, p, err)
			continue
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s %s mode = %o, want 600", name, p, got)
		}
	}
}
