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
)

// stageProbeFS records the moment the decrypted /etc/shadow lands in the staging
// tree (extraction writes a sibling temp and renames it into place), so a test can
// prove the stage really held it and can act at that point.
type stageProbeFS struct {
	*FakeFS
	stagedShadow   bool
	onShadowStaged func()
}

func (f *stageProbeFS) Rename(oldpath, newpath string) error {
	if err := f.FakeFS.Rename(oldpath, newpath); err != nil {
		return err
	}
	if strings.HasPrefix(newpath, "/tmp/proxsave/restore-stage-") && strings.HasSuffix(newpath, "/etc/shadow") {
		f.stagedShadow = true
		if f.onShadowStaged != nil {
			f.onShadowStaged()
		}
	}
	return nil
}

// stageProbeRunner lets the PBS services stop succeed and, from the second
// "which systemctl" on (the restart in the deferred services cleanup), records
// whether the stage still exists and fails fast so the restart does not wait.
type stageProbeRunner struct {
	FakeCommandRunner
	fs             *FakeFS
	stageRoot      func() string
	whichCalls     int
	stageAtRestart []bool
}

func (r *stageProbeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "which" && len(args) == 1 && args[0] == "systemctl" {
		r.whichCalls++
		if r.whichCalls > 1 {
			_, err := r.fs.Stat(r.stageRoot())
			r.stageAtRestart = append(r.stageAtRestart, err == nil)
			return nil, errors.New("probe: restart not needed")
		}
	}
	return r.FakeCommandRunner.Run(ctx, name, args...)
}

type stageRunOutcome struct {
	err            error
	stageRoot      string
	stagedShadow   bool
	stageLeft      bool
	exportRoot     string
	exportedKey    bool
	stageAtRestart []bool
}

// runSelectiveRestoreWithStage drives runSelectiveRestore (the production sequence)
// on a FakeFS with a plan built by PlanRestore and the PBS services stop enabled, so
// the deferred services cleanup that reads the stage runs too. prepareBundleAndPlan
// is skipped: on a FakeFS its collapseStagingWhenUnavailable turns every staged
// category into a direct write and no stage would exist. accounts is staged and its
// apply skips itself on a non-system FS, so only the extraction happens. With
// failAfterStaging the run is cancelled as soon as the stage holds /etc/shadow.
func runSelectiveRestoreWithStage(t *testing.T, failAfterStaging bool) stageRunOutcome {
	t.Helper()
	origRestoreFS, origRestoreCmd, origRestoreTime, origSafetyFS, origSafetyNow := restoreFS, restoreCmd, restoreTime, safetyFS, safetyNow
	t.Cleanup(func() {
		restoreFS, restoreCmd, restoreTime, safetyFS, safetyNow = origRestoreFS, origRestoreCmd, origRestoreTime, origSafetyFS, origSafetyNow
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	probeFS := &stageProbeFS{FakeFS: fakeFS}
	restoreFS, safetyFS = probeFS, fakeFS
	now := &FakeTime{Current: time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)}
	restoreTime, safetyNow = now, now.Now

	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/passwd":               "root:x:0:0::/root:/bin/bash\n",
		"etc/shadow":               "root:$6$DECRYPTED-HASH:19000::::::\n",
		"etc/fstab":                "UUID=r / ext4 defaults 0 1\n",
		"etc/pve/priv/authkey.key": "PRIVATE KEY\n",
	}); err != nil {
		t.Fatal(err)
	}
	tarBytes, err := os.ReadFile(tmpTar)
	if err != nil {
		t.Fatal(err)
	}
	if err := fakeFS.WriteFile("/bundle.tar", tarBytes, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := fakeFS.AddFile("/etc/fstab", []byte("UUID=r / ext4 defaults 0 1\n")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if failAfterStaging {
		probeFS.onShadowStaged = cancel
	}

	cats := []Category{mustCategoryByID(t, "accounts"), mustCategoryByID(t, "filesystem"), mustCategoryByID(t, "pve_config_export")}
	for i := range cats {
		cats[i].IsAvailable = true
	}
	cfg := &config.Config{BaseDir: "/opt/proxsave"}
	ui := &fakeRestoreWorkflowUI{mode: RestoreModeCustom, categories: cats, confirmRestore: true}
	w := newRestoreUIWorkflowRun(ctx, cfg, logging.New(types.LogLevelError, false), "vtest", ui, "")
	w.candidate = &backupCandidate{DisplayBase: "test", Manifest: &backup.Manifest{CreatedAt: now.Now(), ProxmoxType: "pve"}}
	w.prepared = &preparedBundle{ArchivePath: "/bundle.tar", Manifest: backup.Manifest{ArchivePath: "/bundle.tar"}, cleanup: func() {}}
	w.systemType = SystemTypePVE
	w.availableCategories = cats
	w.mode = RestoreModeCustom
	w.plan = PlanRestore(false, cats, SystemTypePVE, RestoreModeCustom)
	if len(w.plan.StagedCategories) != 1 || len(w.plan.ExportCategories) != 1 {
		t.Fatalf("fixture broken: plan=%+v", w.plan)
	}
	w.plan.NeedsPBSServices = true

	runner := &stageProbeRunner{
		FakeCommandRunner: FakeCommandRunner{Outputs: map[string][]byte{}, Errors: map[string]error{}},
		fs:                fakeFS,
		stageRoot:         func() string { return w.stageRoot },
	}
	for _, svc := range []string{"proxmox-backup-proxy", "proxmox-backup"} {
		runner.Outputs["systemctl is-active "+svc] = []byte("inactive\n")
		runner.Errors["systemctl is-active "+svc] = errors.New("inactive")
	}
	restoreCmd = runner

	var o stageRunOutcome
	o.err = w.runSelectiveRestore()
	o.stageRoot = w.stageRoot
	o.stagedShadow = probeFS.stagedShadow
	if o.stageRoot != "" {
		if _, err := fakeFS.Stat(o.stageRoot); err == nil {
			o.stageLeft = true
		}
	}
	o.exportRoot = w.exportRoot
	if _, err := fakeFS.ReadFile(filepath.Join(w.exportRoot, "etc/pve/priv/authkey.key")); err == nil {
		o.exportedKey = true
	}
	o.stageAtRestart = runner.stageAtRestart
	return o
}

// The stage holds the decrypted sensitive categories (/etc/shadow here). Once the
// restore has finished nothing reads it any more, so it is removed; the export, the
// product of SAFE, stays.
func TestRestoreRemovesDecryptedStageAfterSuccessfulRun(t *testing.T) {
	o := runSelectiveRestoreWithStage(t, false)
	t.Logf("%+v", o)
	if o.err != nil {
		t.Fatalf("runSelectiveRestore: %v", o.err)
	}
	if !o.stagedShadow {
		t.Fatalf("control broken: the stage never held etc/shadow (%+v)", o)
	}
	if o.stageLeft {
		t.Errorf("stage %s still exists after the run", o.stageRoot)
	}
	if !o.exportedKey {
		t.Errorf("export %s no longer holds etc/pve/priv/authkey.key: the export must stay", o.exportRoot)
	}
	if len(o.stageAtRestart) == 0 || !o.stageAtRestart[len(o.stageAtRestart)-1] {
		t.Errorf("the deferred PBS services cleanup did not find the stage (probes=%v)", o.stageAtRestart)
	}
}

// Same as above for a restore that fails once the stage holds /etc/shadow: the stage
// goes on failure too, still after the deferred PBS services cleanup.
func TestRestoreRemovesDecryptedStageAfterFailedRun(t *testing.T) {
	o := runSelectiveRestoreWithStage(t, true)
	t.Logf("%+v", o)
	if !errors.Is(o.err, context.Canceled) {
		t.Fatalf("control broken: the run did not fail after staging (err=%v)", o.err)
	}
	if !o.stagedShadow {
		t.Fatalf("control broken: the stage never held etc/shadow (%+v)", o)
	}
	if o.stageLeft {
		t.Errorf("stage %s still exists after the failed run", o.stageRoot)
	}
	if len(o.stageAtRestart) == 0 || !o.stageAtRestart[len(o.stageAtRestart)-1] {
		t.Errorf("the deferred PBS services cleanup did not find the stage (probes=%v)", o.stageAtRestart)
	}
}
