package orchestrator

import (
	"bytes"
	"context"
	"errors"
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
// all the files of one restore sit in one directory. TMPDIR is set to show the
// directory does not follow it, as /tmp/proxsave did not either.
func TestRestorePersistentMaterialLivesOutsideTmpInOneDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)
	rebootLog := &bytes.Buffer{}
	rebootLogger := logging.New(types.LogLevelInfo, false)
	rebootLogger.SetOutput(rebootLog)
	(&restoreUIWorkflowRun{logger: rebootLogger}).logRebootRecommendation()
	if !strings.Contains(rebootLog.String(), "SYSTEM REBOOT RECOMMENDED") {
		t.Fatalf("control broken: the restore no longer recommends a reboot (%q)", rebootLog.String())
	}

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
	if strings.HasPrefix(runDir, tmpDir) {
		t.Errorf("run directory %s follows TMPDIR (%s)", runDir, tmpDir)
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

// What a restore hands to the operator on the network and PBS side must outlive the
// reboot it recommends as well: the copy of the network files taken before a NIC
// rename, the datastore definitions it did not apply, the network diagnostics and
// the log of each armed rollback. Marker and script of an armed rollback only matter
// inside its window and stay in /tmp/proxsave; the log is created 0600 by the script
// itself, without changing the umask of what the script runs.
func TestRestoreOperatorLeftoversLiveInTheRunDirectory(t *testing.T) {
	origRestoreFS, origRestoreCmd, origRestoreTime := restoreFS, restoreCmd, restoreTime
	t.Cleanup(func() { restoreFS, restoreCmd, restoreTime = origRestoreFS, origRestoreCmd, origRestoreTime })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreCmd = &FakeCommandRunner{}
	restoreTime = &FakeTime{Current: time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)}
	t.Setenv("PATH", t.TempDir())
	ctx := context.Background()
	logger := logging.New(types.LogLevelError, false)
	runDir := RestoreRunDir()

	if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\niface eno1 inet manual\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, nicBackup, err := rewriteIfupdownConfigFiles(logger, map[string]string{"eno1": "enp3s0"})
	if err != nil {
		t.Fatalf("rewriteIfupdownConfigFiles: %v", err)
	}
	deferred, err := writeDeferredPBSDatastoreCfg([]pbsDatastoreBlock{{Name: "DS1", Path: "/mnt/ds1", Lines: []string{"datastore: DS1", "    path /mnt/ds1"}}})
	if err != nil {
		t.Fatalf("writeDeferredPBSDatastoreCfg: %v", err)
	}
	diagDir, err := createNetworkDiagnosticsDir()
	if err != nil {
		t.Fatalf("createNetworkDiagnosticsDir: %v", err)
	}

	entries := map[string]string{
		"NIC repair backup":      nicBackup,
		"deferred datastore.cfg": deferred,
		"network diagnostics":    diagDir,
	}
	windowOnly := map[string]string{}
	scripts := map[string]string{}

	netHandle, err := armNetworkRollback(ctx, logger, "/backup.tgz", time.Second, "")
	if err != nil {
		t.Fatalf("armNetworkRollback: %v", err)
	}
	entries["network rollback log"] = netHandle.logPath
	windowOnly["network rollback marker"], scripts["network rollback"] = netHandle.markerPath, netHandle.scriptPath
	nowLog, err := rollbackNetworkFilesNow(ctx, logger, "/backup.tgz", "")
	if err != nil {
		t.Fatalf("rollbackNetworkFilesNow: %v", err)
	}
	entries["immediate network rollback log"] = nowLog
	haHandle, err := armHARollback(ctx, logger, "/backup.tgz", time.Second, "/tmp/proxsave")
	if err != nil {
		t.Fatalf("armHARollback: %v", err)
	}
	entries["HA rollback log"] = haHandle.logPath
	windowOnly["HA rollback marker"], scripts["HA rollback"] = haHandle.markerPath, haHandle.scriptPath
	fwHandle, err := armFirewallRollback(ctx, logger, "/backup.tgz", time.Second, "/tmp/proxsave")
	if err != nil {
		t.Fatalf("armFirewallRollback: %v", err)
	}
	entries["firewall rollback log"] = fwHandle.logPath
	windowOnly["firewall rollback marker"], scripts["firewall rollback"] = fwHandle.markerPath, fwHandle.scriptPath
	acHandle, err := armAccessControlRollback(ctx, logger, "/backup.tgz", time.Second, "/tmp/proxsave")
	if err != nil {
		t.Fatalf("armAccessControlRollback: %v", err)
	}
	entries["access control rollback log"] = acHandle.logPath
	windowOnly["access control rollback marker"], scripts["access control rollback"] = acHandle.markerPath, acHandle.scriptPath

	for name, p := range entries {
		if strings.HasPrefix(p, "/tmp/") {
			t.Errorf("%s is under /tmp (%s) while the run recommends a reboot", name, p)
		}
		if filepath.Dir(p) != runDir {
			t.Errorf("%s is in %s, not in the run directory %s", name, filepath.Dir(p), runDir)
		}
	}
	if info, err := fakeFS.Stat(deferred); err != nil {
		t.Errorf("stat %s: %v", deferred, err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("deferred datastore.cfg mode = %o, want 600", got)
	}
	for _, dir := range []string{nicBackup, diagDir} {
		if info, err := fakeFS.Stat(dir); err != nil {
			t.Errorf("stat %s: %v", dir, err)
		} else if got := info.Mode().Perm(); got != 0o700 {
			t.Errorf("%s mode = %o, want 700", dir, got)
		}
	}
	for name, p := range windowOnly {
		if !strings.HasPrefix(p, "/tmp/proxsave/") {
			t.Errorf("%s moved out of the /tmp/proxsave work dir: %s", name, p)
		}
	}
	const createLog = `(umask 077 && : >> "$LOG")`
	for name, p := range scripts {
		raw, err := fakeFS.ReadFile(p)
		if err != nil {
			t.Errorf("%s script %s: %v", name, p, err)
			continue
		}
		script := string(raw)
		at := strings.Index(script, createLog)
		first := strings.Index(script, `>> "$LOG"`)
		if at < 0 || at+len(createLog)-len(`>> "$LOG")`) != first {
			t.Errorf("%s script does not create its log 0600 before the first write to it", name)
		}
		if strings.Contains(script, "\numask ") {
			t.Errorf("%s script changes the umask of everything it runs", name)
		}
	}
}

// The interactive network apply keeps its diagnostics in the restore's own
// directory, but the marker and script of its rollbacks (armed and immediate) only
// matter inside the rollback window and stay in the /tmp/proxsave work dir, like
// those of HA, firewall and access control. Only their log goes to the run dir.
func TestRestoreNetworkRollbackWorkFilesStayOutOfTheRunDirectory(t *testing.T) {
	origRestoreFS, origRestoreCmd, origRestoreTime := restoreFS, restoreCmd, restoreTime
	t.Cleanup(func() { restoreFS, restoreCmd, restoreTime = origRestoreFS, origRestoreCmd, origRestoreTime })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	cmd := &FakeCommandRunner{}
	restoreCmd = cmd
	restoreTime = &FakeTime{Current: time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)}
	// Empty PATH: applyNetworkConfig fails and the armed rollback uses the nohup
	// fallback through the fake runner, as in TestArmRollbackAndApply_ApplyFailureSurfacesNotCommitted.
	t.Setenv("PATH", t.TempDir())

	diagDir, err := createNetworkDiagnosticsDir()
	if err != nil {
		t.Fatalf("createNetworkDiagnosticsDir: %v", err)
	}
	f := &networkRollbackUIApplyFlow{
		ctx:                 context.Background(),
		ui:                  &fakeRestoreWorkflowUI{},
		logger:              newDiscardLogger(),
		rollbackBackupPath:  "/safety-backup.tar",
		networkRollbackPath: "/network-backup.tar",
		timeout:             time.Hour,
		diagnosticsDir:      diagDir,
	}

	_ = f.armRollbackAndApply()
	if f.handle == nil {
		t.Fatalf("control broken: the rollback was not armed")
	}
	_ = f.rollbackStagedPreflightFailure(networkPreflightResult{})
	_ = f.rollbackStagedApplyFailure(errors.New("apply failed"))
	_ = f.rollbackPreflightFailureNow()

	for name, p := range map[string]string{"armed rollback marker": f.handle.markerPath, "armed rollback script": f.handle.scriptPath} {
		if !strings.HasPrefix(p, "/tmp/proxsave/") {
			t.Errorf("%s is %s, not in the /tmp/proxsave work dir", name, p)
		}
	}
	if filepath.Dir(f.handle.logPath) != RestoreRunDir() {
		t.Errorf("armed rollback log %s is not in the run directory %s", f.handle.logPath, RestoreRunDir())
	}
	immediate := 0
	for _, c := range cmd.CallsList() {
		if strings.HasPrefix(c, "sh ") && strings.Contains(c, "network_rollback_now_") {
			immediate++
			if !strings.HasPrefix(c, "sh /tmp/proxsave/") {
				t.Errorf("immediate rollback script ran from %q, not from the /tmp/proxsave work dir", c)
			}
		}
	}
	if immediate != 3 {
		t.Fatalf("control broken: %d immediate rollback scripts ran, want 3 (calls=%v)", immediate, cmd.CallsList())
	}
	entries, err := fakeFS.ReadDir(diagDir)
	if err != nil {
		t.Fatalf("read diagnostics dir %s: %v", diagDir, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "network_rollback") {
			t.Errorf("rollback work file %s left in the persistent diagnostics dir %s", e.Name(), diagDir)
		}
	}
}
