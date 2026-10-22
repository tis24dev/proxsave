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

// pbsCtxRunner behaves like proxmox-backup-manager run under the restore context:
// the command matching cancelOn is the one running when the operator cancels, so
// it cancels the context and dies; any later command run with the cancelled
// context fails at once, as exec does. With finishes the cancelOn command
// completes as the operator cancels, with cancelErr it ends with that refusal.
// refused lists the commands PBS refuses.
type pbsCtxRunner struct {
	cancelOn  string
	finishes  bool
	cancelErr error
	cancel    context.CancelFunc
	list      string
	refused   map[string]error
	calls     []string
}

func (r *pbsCtxRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, call)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.cancelOn != "" && strings.HasPrefix(call, r.cancelOn) {
		r.cancel()
		if r.cancelErr != nil {
			return nil, r.cancelErr
		}
		if r.finishes {
			return nil, nil
		}
		return nil, errors.New("signal: killed")
	}
	if err, ok := r.refused[call]; ok {
		return nil, err
	}
	if strings.HasPrefix(call, "proxmox-backup-manager datastore list") {
		return []byte(r.list), nil
	}
	return nil, nil
}

// runPBSDatastoreCancelStep runs the PBS staged apply of datastore.cfg and
// remote.cfg through the restore step runner with runner as proxmox-backup-manager
// and the restore context it cancels. It returns the log and the step error.
func runPBSDatastoreCancelStep(t *testing.T, behavior PBSRestoreBehavior, staged string, runner *pbsCtxRunner) (string, error) {
	t.Helper()
	origFS, origCmd := restoreFS, restoreCmd
	origIsReal, origGeteuid := pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn
	origEnsure, origDS := pbsStagedApplyEnsurePBSServicesForAPIFn, pbsStagedApplyDatastoreCfgViaAPIFn
	origS3, origRemote := pbsStagedApplyS3CfgViaAPIFn, pbsStagedApplyRemoteCfgViaAPIFn
	t.Cleanup(func() {
		restoreFS, restoreCmd = origFS, origCmd
		pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn = origIsReal, origGeteuid
		pbsStagedApplyEnsurePBSServicesForAPIFn, pbsStagedApplyDatastoreCfgViaAPIFn = origEnsure, origDS
		pbsStagedApplyS3CfgViaAPIFn, pbsStagedApplyRemoteCfgViaAPIFn = origS3, origRemote
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreCmd = runner
	pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
	pbsStagedApplyGeteuidFn = func() int { return 0 }
	pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error { return nil }
	pbsStagedApplyDatastoreCfgViaAPIFn = applyPBSDatastoreCfgViaAPI
	pbsStagedApplyS3CfgViaAPIFn = func(context.Context, *logging.Logger, string, bool) error { return nil }
	pbsStagedApplyRemoteCfgViaAPIFn = applyPBSRemoteCfgViaAPI
	if err := fakeFS.WriteFile("/stage/etc/proxmox-backup/datastore.cfg", []byte(staged), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := fakeFS.WriteFile("/stage/etc/proxmox-backup/remote.cfg", []byte("remote: r1\n\thost 10.0.0.1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runner.cancel = cancel

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	plan := &RestorePlan{SystemType: SystemTypePBS, PBSRestoreBehavior: behavior, NormalCategories: []Category{{ID: "datastore_pbs"}, {ID: "pbs_remotes"}}}
	w := &restoreUIWorkflowRun{ctx: ctx, logger: logger, plan: plan}
	step := restoreStageApplyStep{name: "PBS staged config apply", run: func() error {
		return maybeApplyPBSConfigsFromStage(w.ctx, w.logger, w.plan, "/stage", false)
	}}
	err := w.runStageApplyStep(step)
	if _, statErr := fakeFS.Stat("/etc/proxmox-backup/datastore.cfg"); statErr == nil {
		t.Error("datastore.cfg must not be written after a cancellation")
	}
	return buf.String(), err
}

// A cancellation during the datastore block stops it with the cancellation: no
// line for the datastore being restored or the ones after it, no file fallback,
// no further command, and the restore step reads it as an abort.
func TestApplyPBSDatastoreCfgViaAPI_StopsOnCancellation(t *testing.T) {
	const (
		staged = "datastore: ds1\n\tpath /mnt/ds1\n\ndatastore: ds2\n\tpath /mnt/ds2\n"
		open1  = "INFO     Restoring PBS datastore configuration..."
		open2  = "INFO       Datastores to restore: 2"
		open3  = "INFO       Names: ds1, ds2"
		listDS = "proxmox-backup-manager datastore list --output-format=json"
	)
	cases := []struct {
		name     string
		behavior PBSRestoreBehavior
		list     string
		cancelOn string
		finishes bool
		visible  []string
		calls    []string
	}{
		{
			name: "list-clean", behavior: PBSRestoreBehaviorClean, list: `[]`, cancelOn: listDS,
			visible: []string{open1, open2, open3},
			calls:   []string{listDS},
		},
		{
			name: "list-merge", behavior: PBSRestoreBehaviorMerge, list: `[]`, cancelOn: listDS,
			visible: []string{open1, open2, open3},
			calls:   []string{listDS},
		},
		{
			name: "remove", behavior: PBSRestoreBehaviorClean, list: `[{"name":"ds0","path":"/mnt/ds0"}]`, cancelOn: "proxmox-backup-manager datastore remove ds0",
			visible: []string{open1, open2, open3},
			calls:   []string{listDS, "proxmox-backup-manager datastore remove ds0"},
		},
		{
			name: "remove-first-of-two", behavior: PBSRestoreBehaviorClean, list: `[{"name":"ds0","path":"/mnt/ds0"},{"name":"ds00","path":"/mnt/ds00"}]`, cancelOn: "proxmox-backup-manager datastore remove ds0",
			visible: []string{open1, open2, open3},
			calls:   []string{listDS, "proxmox-backup-manager datastore remove ds0"},
		},
		{
			name: "create", behavior: PBSRestoreBehaviorClean, list: `[{"name":"ds0","path":"/mnt/ds0"}]`, cancelOn: "proxmox-backup-manager datastore create ds1",
			visible: []string{open1, open2, open3,
				"INFO     Datastore ds0 is configured on this PBS but not present in the backup",
				"INFO     ✓ PBS datastore ds0: removed with its jobs"},
			calls: []string{listDS, "proxmox-backup-manager datastore remove ds0", "proxmox-backup-manager datastore create ds1 /mnt/ds1"},
		},
		{
			name: "create-last", behavior: PBSRestoreBehaviorClean, list: `[]`, cancelOn: "proxmox-backup-manager datastore create ds2",
			visible: []string{open1, open2, open3, "INFO     ✓ PBS datastore ds1: applied"},
			calls:   []string{listDS, "proxmox-backup-manager datastore create ds1 /mnt/ds1", "proxmox-backup-manager datastore create ds2 /mnt/ds2"},
		},
		{
			name: "create-finished-as-cancelled", behavior: PBSRestoreBehaviorClean, list: `[]`, cancelOn: "proxmox-backup-manager datastore create ds1", finishes: true,
			visible: []string{open1, open2, open3, "INFO     ✓ PBS datastore ds1: applied"},
			calls:   []string{listDS, "proxmox-backup-manager datastore create ds1 /mnt/ds1"},
		},
		{
			name: "update", behavior: PBSRestoreBehaviorMerge, list: `[{"name":"ds1","path":"/mnt/ds1"}]`, cancelOn: "proxmox-backup-manager datastore update ds1",
			visible: []string{open1, open2, open3},
			calls:   []string{listDS, "proxmox-backup-manager datastore update ds1"},
		},
		{
			name: "update-after-refused-create", behavior: PBSRestoreBehaviorMerge, list: `[]`, cancelOn: "proxmox-backup-manager datastore update ds1",
			visible: []string{open1, open2, open3},
			calls:   []string{listDS, "proxmox-backup-manager datastore create ds1 /mnt/ds1", "proxmox-backup-manager datastore update ds1"},
		},
		{
			name: "move-remove", behavior: PBSRestoreBehaviorClean, list: `[{"name":"ds1","path":"/mnt/a"}]`, cancelOn: "proxmox-backup-manager datastore remove ds1",
			visible: []string{open1, open2, open3},
			calls:   []string{listDS, "proxmox-backup-manager datastore remove ds1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &pbsCtxRunner{cancelOn: tc.cancelOn, finishes: tc.finishes, list: tc.list}
			if tc.name == "update-after-refused-create" {
				runner.refused = map[string]error{"proxmox-backup-manager datastore create ds1 /mnt/ds1": errors.New("exit status 255: Error: datastore 'ds1' already exists.")}
			}
			log, err := runPBSDatastoreCancelStep(t, tc.behavior, staged, runner)
			t.Logf("full log:\n%s", strings.Join(pbsBlockLogLines(log), "\n"))
			if !restoreAbortOrInput(err) {
				t.Fatalf("step error = %v; want an abort", err)
			}
			if got := pbsVisibleLines(log); strings.Join(got, "\n") != strings.Join(tc.visible, "\n") {
				t.Fatalf("visible lines\n--- got ---\n%s\n--- want ---\n%s", strings.Join(got, "\n"), strings.Join(tc.visible, "\n"))
			}
			if strings.Join(runner.calls, "\n") != strings.Join(tc.calls, "\n") {
				t.Fatalf("calls\n--- got ---\n%s\n--- want ---\n%s", strings.Join(runner.calls, "\n"), strings.Join(tc.calls, "\n"))
			}
		})
	}
}

// A move cancelled after the remove puts the datastore back at its path on this
// PBS even though the restore was cancelled, then stops: no line when that
// succeeds, the not-configured lines when PBS refuses it.
func TestApplyPBSDatastoreCfgViaAPI_CancelledMoveRestoresTheDatastore(t *testing.T) {
	const staged = "datastore: ds1\n\tpath /mnt/ds1\n\ndatastore: ds2\n\tpath /mnt/ds2\n"
	head := []string{
		"INFO     Restoring PBS datastore configuration...",
		"INFO       Datastores to restore: 2",
		"INFO       Names: ds1, ds2",
	}
	calls := []string{
		"proxmox-backup-manager datastore list --output-format=json",
		"proxmox-backup-manager datastore remove ds1",
		"proxmox-backup-manager datastore create ds1 /mnt/ds1",
		"proxmox-backup-manager datastore create ds1 /mnt/a",
	}
	refused := map[string]error{"proxmox-backup-manager datastore create ds1 /mnt/a": errors.New("exit status 255: Error: refused")}
	notConfigured := []string{
		"INFO     Datastore ds1 is at /mnt/a on this PBS and at /mnt/ds1 in the backup",
		"WARNING  ⚠ PBS datastore ds1: not configured, the data in /mnt/a is kept",
	}
	cases := []struct {
		name    string
		staged  string
		refused map[string]error
		visible []string
	}{
		{"rollback-applied", staged, nil, head},
		{"rollback-refused", staged, refused, append(append([]string(nil), head...), notConfigured...)},
		// The moved datastore is the last one: no block outcome after the cancellation.
		{"rollback-refused-last", "datastore: ds1\n\tpath /mnt/ds1\n", refused, append([]string{
			"INFO     Restoring PBS datastore configuration...",
			"INFO       Datastores to restore: 1",
			"INFO       Names: ds1",
		}, notConfigured...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &pbsCtxRunner{cancelOn: "proxmox-backup-manager datastore create ds1 /mnt/ds1", list: `[{"name":"ds1","path":"/mnt/a"}]`, refused: tc.refused}
			log, err := runPBSDatastoreCancelStep(t, PBSRestoreBehaviorClean, tc.staged, runner)
			t.Logf("full log:\n%s", strings.Join(pbsBlockLogLines(log), "\n"))
			if !restoreAbortOrInput(err) {
				t.Fatalf("step error = %v; want an abort", err)
			}
			if got := pbsVisibleLines(log); strings.Join(got, "\n") != strings.Join(tc.visible, "\n") {
				t.Fatalf("visible lines\n--- got ---\n%s\n--- want ---\n%s", strings.Join(got, "\n"), strings.Join(tc.visible, "\n"))
			}
			if strings.Join(runner.calls, "\n") != strings.Join(calls, "\n") {
				t.Fatalf("calls\n--- got ---\n%s\n--- want ---\n%s", strings.Join(runner.calls, "\n"), strings.Join(calls, "\n"))
			}
		})
	}
}
