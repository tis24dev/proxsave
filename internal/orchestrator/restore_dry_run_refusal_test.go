package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// setupRecoveryRestoreFixture prepares a selective RECOVERY restore of pve_cluster +
// ssh (both direct-write categories, not in staging.go) + filesystem (Smart Merge)
// on a FakeFS, with live copies of config.db, sshd_config and fstab that differ from
// the ones in the archive. Before the dry-run refusal, a restore run with
// cfg.DryRun=true on this fixture stopped pve-cluster, unmounted /etc/pve, wrote
// config.db and sshd_config over the live copies and created a safety backup.
//
// No staged category is selected: on a FakeFS collapseStagingWhenUnavailable turns a
// staged category into a direct write, which is not what production does.
// Same fixture as TestRunRestoreWorkflow_ClusterRecoveryModeStopsAndRestartsServices.
func setupRecoveryRestoreFixture(t *testing.T) (*FakeFS, *FakeCommandRunner, *fakeRestoreWorkflowUI) {
	t.Helper()
	origRestoreFS, origRestoreCmd, origRestoreSystem, origRestoreTime := restoreFS, restoreCmd, restoreSystem, restoreTime
	origCompatFS, origPrepare, origSafetyFS, origSafetyNow := compatFS, prepareRestoreBundleFunc, safetyFS, safetyNow
	t.Cleanup(func() {
		restoreFS, restoreCmd, restoreSystem, restoreTime = origRestoreFS, origRestoreCmd, origRestoreSystem, origRestoreTime
		compatFS, prepareRestoreBundleFunc, safetyFS, safetyNow = origCompatFS, origPrepare, origSafetyFS, origSafetyNow
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS, compatFS, safetyFS = fakeFS, fakeFS, fakeFS
	fakeNow := &FakeTime{Current: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	restoreTime = fakeNow
	safetyNow = fakeNow.Now
	if err := fakeFS.AddFile("/usr/bin/qm", []byte("x")); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}

	cmd := &FakeCommandRunner{
		Outputs: map[string][]byte{"umount /etc/pve": []byte("not mounted\n")},
		Errors:  map[string]error{"umount /etc/pve": errors.New("not mounted")},
	}
	for _, svc := range []string{"pve-ha-lrm", "pve-ha-crm", "pve-cluster", "pvedaemon", "pveproxy", "pvestatd"} {
		cmd.Outputs["systemctl stop --no-block "+svc] = []byte("ok")
		cmd.Outputs["systemctl is-active "+svc] = []byte("inactive\n")
		cmd.Errors["systemctl is-active "+svc] = errors.New("inactive")
		cmd.Outputs["systemctl reset-failed "+svc] = []byte("ok")
		cmd.Outputs["systemctl start "+svc] = []byte("ok")
	}
	restoreCmd = cmd

	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/ssh/sshd_config":           "PermitRootLogin backup\n",
		"var/lib/pve-cluster/config.db": "db-from-backup\n",
		"etc/fstab":                     "UUID=r / ext4 defaults 0 1\nserver:/export /mnt/nas nfs defaults 0 0\n",
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}
	tarBytes, err := os.ReadFile(tmpTar)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := fakeFS.WriteFile("/bundle.tar", tarBytes, 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	for path, body := range map[string]string{
		"/etc/ssh/sshd_config":           "PermitRootLogin live\n",
		"/var/lib/pve-cluster/config.db": "db-live\n",
		"/etc/fstab":                     "UUID=r / ext4 defaults 0 1\n",
	} {
		if err := fakeFS.AddFile(path, []byte(body)); err != nil {
			t.Fatalf("AddFile %s: %v", path, err)
		}
	}

	prepareRestoreBundleFunc = func(ctx context.Context, cfg *config.Config, logger *logging.Logger, version string, ui RestoreWorkflowUI) (*backupCandidate, *preparedBundle, error) {
		return &backupCandidate{
				DisplayBase: "test",
				Manifest:    &backup.Manifest{CreatedAt: fakeNow.Now(), ClusterMode: "cluster", ProxmoxType: "pve", ScriptVersion: "vtest"},
			}, &preparedBundle{
				ArchivePath: "/bundle.tar",
				Manifest:    backup.Manifest{ArchivePath: "/bundle.tar"},
				cleanup:     func() {},
			}, nil
	}

	ui := &fakeRestoreWorkflowUI{
		mode: RestoreModeCustom,
		categories: []Category{
			mustCategoryByID(t, "pve_cluster"),
			mustCategoryByID(t, "ssh"),
			mustCategoryByID(t, "filesystem"),
		},
		confirmRestore:    true,
		confirmFstabMerge: true,
		clusterMode:       ClusterRestoreRecovery,
	}
	return fakeFS, cmd, ui
}

type recoveryRestoreOutcome struct {
	configDB, sshd       string
	fstabHasNAS          bool
	stoppedPVE, umounted bool
	safetyBackups        int
}

// untouchedRecoveryRestoreOutcome is the fixture's live state: what a restore that
// never started leaves behind.
var untouchedRecoveryRestoreOutcome = recoveryRestoreOutcome{configDB: "db-live", sshd: "PermitRootLogin live"}

func observeRecoveryRestore(t *testing.T, fs *FakeFS, cmd *FakeCommandRunner) recoveryRestoreOutcome {
	t.Helper()
	read := func(p string) string {
		b, err := fs.ReadFile(p)
		if err != nil {
			return "<missing>"
		}
		return strings.TrimSpace(string(b))
	}
	var o recoveryRestoreOutcome
	o.configDB = read("/var/lib/pve-cluster/config.db")
	o.sshd = read("/etc/ssh/sshd_config")
	o.fstabHasNAS = strings.Contains(read("/etc/fstab"), "/mnt/nas")
	for _, c := range cmd.Calls {
		if c == "systemctl stop --no-block pve-cluster" {
			o.stoppedPVE = true
		}
		if c == "umount /etc/pve" {
			o.umounted = true
		}
	}
	matches, _ := filepath.Glob(filepath.Join(fs.Root, RestoreRunDir(), "restore_backup_*.tar.gz"))
	o.safetyBackups = len(matches)
	return o
}

// A restore cannot run without modifying the system, so under dry-run it is refused
// before it starts. Before the refusal, this exact run stopped pve-cluster,
// unmounted /etc/pve, wrote config.db and sshd_config over the live copies and
// created a safety backup, while the operator had been told nothing would change.
func TestRestoreRefusesDryRunBeforeTouchingTheSystem(t *testing.T) {
	fs, cmd, ui := setupRecoveryRestoreFixture(t)
	cfg := &config.Config{BaseDir: "/base", DryRun: true}

	err := runRestoreWorkflowWithUI(context.Background(), cfg, logging.New(types.LogLevelError, false), "vtest", ui, "")

	if !errors.Is(err, ErrRestoreDryRun) {
		t.Errorf("err = %v, want ErrRestoreDryRun", err)
	}
	if got := observeRecoveryRestore(t, fs, cmd); got != untouchedRecoveryRestoreOutcome {
		t.Errorf("dry-run restore touched the system: got %+v, want %+v", got, untouchedRecoveryRestoreOutcome)
	}
}

// Control for the test above: the same fixture without dry-run does reach the
// system, so "nothing was touched" there is the refusal and not a fixture that
// never writes.
func TestRestoreWithoutDryRunStillReachesTheSystem(t *testing.T) {
	fs, cmd, ui := setupRecoveryRestoreFixture(t)
	cfg := &config.Config{BaseDir: "/base"}

	if err := runRestoreWorkflowWithUI(context.Background(), cfg, logging.New(types.LogLevelError, false), "vtest", ui, ""); err != nil {
		t.Fatalf("runRestoreWorkflowWithUI: %v", err)
	}
	want := recoveryRestoreOutcome{configDB: "db-from-backup", sshd: "PermitRootLogin backup", fstabHasNAS: true, stoppedPVE: true, umounted: true, safetyBackups: 1}
	if got := observeRecoveryRestore(t, fs, cmd); got != want {
		t.Fatalf("control broken: got %+v, want %+v", got, want)
	}
}

// The CLI entry refuses before its first prompt. Stdin is an already closed pipe:
// a workflow that reached a prompt would read EOF and come back as
// ErrRestoreAborted instead of the refusal.
func TestRunRestoreWorkflowRefusesDryRunBeforeReadingInput(t *testing.T) {
	fs, cmd, _ := setupRecoveryRestoreFixture(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	_ = w.Close()
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = origStdin
		_ = r.Close()
	})
	cfg := &config.Config{BaseDir: "/base", DryRun: true}

	err = RunRestoreWorkflow(context.Background(), cfg, logging.New(types.LogLevelError, false), "vtest", "")

	if !errors.Is(err, ErrRestoreDryRun) {
		t.Errorf("err = %v, want ErrRestoreDryRun", err)
	}
	if got := observeRecoveryRestore(t, fs, cmd); got != untouchedRecoveryRestoreOutcome {
		t.Errorf("dry-run restore touched the system: got %+v, want %+v", got, untouchedRecoveryRestoreOutcome)
	}
}

// The TUI entry refuses before it opens its session. Opening it would already adopt
// the dashboard's handed-off session and take over the screen for a restore that is
// not going to run.
func TestRunRestoreWorkflowTUIRefusesDryRunBeforeOpeningASession(t *testing.T) {
	orig := newUISession
	newUISession = func(ctx context.Context, cfg shell.Config) *shell.Session {
		t.Fatalf("the TUI restore opened a UI session under dry-run")
		return nil
	}
	t.Cleanup(func() { newUISession = orig })
	cfg := &config.Config{BaseDir: "/base", DryRun: true}

	err := RunRestoreWorkflowTUI(context.Background(), cfg, logging.New(types.LogLevelError, false), "vtest", "", "sig", "")

	if !errors.Is(err, ErrRestoreDryRun) {
		t.Errorf("err = %v, want ErrRestoreDryRun", err)
	}
}
