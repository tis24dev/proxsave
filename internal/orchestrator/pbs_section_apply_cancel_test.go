package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// cancellingRunner records the commands it is asked to run and, on the one that
// matches cancelOn, cancels the restore context and fails the way a command killed
// by that cancellation does.
type cancellingRunner struct {
	calls    []string
	cancelOn string
	cancel   context.CancelFunc
}

func (r *cancellingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, call)
	if strings.HasPrefix(call, r.cancelOn) {
		r.cancel()
		return nil, errors.New("signal: killed")
	}
	if strings.Contains(call, " list ") {
		return []byte("[]"), nil
	}
	return nil, nil
}

// A cancellation during the apply of a section config stops it with the
// cancellation: no update attempt, no next object, no "failed (continuing)"
// warning, and the caller can tell it from a failed object.
func TestApplyPBSSectionCfgViaAPI_StopsOnCancellation(t *testing.T) {
	origFS, origCmd := restoreFS, restoreCmd
	t.Cleanup(func() { restoreFS, restoreCmd = origFS, origCmd })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	if err := fakeFS.WriteFile("/stage/etc/proxmox-backup/prune.cfg", []byte("prune: p1\n\tstore ds1\n\nprune: p2\n\tstore ds1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &cancellingRunner{cancelOn: "proxmox-backup-manager prune-job create p1", cancel: cancel}
	restoreCmd = runner

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	err := applyPBSPruneCfgViaAPI(ctx, logger, "/stage", true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
	want := []string{
		"proxmox-backup-manager prune-job list --output-format=json",
		"proxmox-backup-manager prune-job create p1 --store ds1",
	}
	if strings.Join(runner.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls\n%s\nwant\n%s", strings.Join(runner.calls, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("no warning expected on cancellation:\n%s", buf.String())
	}
}

// The staged apply returns the cancellation of a section config at once: no
// warning, no file-based fallback, and the restore step reads it as an abort.
func TestMaybeApplyPBSConfigsFromStage_SectionCancellationAbortsTheStep(t *testing.T) {
	origFS := restoreFS
	origIsReal, origGeteuid := pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn
	origEnsure, origRemote := pbsStagedApplyEnsurePBSServicesForAPIFn, pbsStagedApplyRemoteCfgViaAPIFn
	t.Cleanup(func() {
		restoreFS = origFS
		pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn = origIsReal, origGeteuid
		pbsStagedApplyEnsurePBSServicesForAPIFn, pbsStagedApplyRemoteCfgViaAPIFn = origEnsure, origRemote
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
	pbsStagedApplyGeteuidFn = func() int { return 0 }
	pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error { return nil }
	if err := fakeFS.WriteFile("/stage/etc/proxmox-backup/remote.cfg", []byte("remote: r1\n\thost 10.0.0.10\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	pbsStagedApplyRemoteCfgViaAPIFn = func(context.Context, *logging.Logger, string, bool) error {
		cancel()
		return errors.New("signal: killed")
	}

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	plan := &RestorePlan{SystemType: SystemTypePBS, PBSRestoreBehavior: PBSRestoreBehaviorClean, NormalCategories: []Category{{ID: "pbs_remotes"}}}
	w := &restoreUIWorkflowRun{ctx: ctx, logger: logger, plan: plan}
	step := restoreStageApplyStep{name: "PBS staged config apply", run: func() error {
		return maybeApplyPBSConfigsFromStage(w.ctx, w.logger, w.plan, "/stage", false)
	}}
	err := w.runStageApplyStep(step)
	if !restoreAbortOrInput(err) {
		t.Fatalf("runStageApplyStep = %v; want an abort", err)
	}
	if strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("no warning expected on cancellation:\n%s", buf.String())
	}
	if _, statErr := fakeFS.Stat("/etc/proxmox-backup/remote.cfg"); statErr == nil {
		t.Fatal("the file-based fallback must not run after a cancellation")
	}
}
