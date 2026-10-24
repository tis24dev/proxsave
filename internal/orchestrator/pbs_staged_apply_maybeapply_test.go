package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestMaybeApplyPBSConfigsFromStage_SkipsWhenNonRoot(t *testing.T) {
	origFS := restoreFS
	origIsReal := pbsStagedApplyIsRealRestoreFSFn
	origGeteuid := pbsStagedApplyGeteuidFn
	t.Cleanup(func() {
		restoreFS = origFS
		pbsStagedApplyIsRealRestoreFSFn = origIsReal
		pbsStagedApplyGeteuidFn = origGeteuid
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
	pbsStagedApplyGeteuidFn = func() int { return 1000 }

	stageRoot := "/stage"
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/acme/accounts/le", []byte(`{"account":{"status":"valid"}}`), 0o600); err != nil {
		t.Fatalf("write staged ACME account: %v", err)
	}

	plan := &RestorePlan{
		SystemType:         SystemTypePBS,
		PBSRestoreBehavior: PBSRestoreBehaviorClean,
		NormalCategories:   []Category{{ID: "pbs_host"}},
	}
	if err := maybeApplyPBSConfigsFromStage(context.Background(), newTestLogger(), plan, stageRoot, false); err != nil {
		t.Fatalf("maybeApplyPBSConfigsFromStage: %v", err)
	}

	if _, err := fakeFS.Stat("/etc/proxmox-backup/acme/accounts/le"); err == nil {
		t.Fatalf("expected no writes when non-root")
	}
}

func TestMaybeApplyPBSConfigsFromStage_CleanMode_ApiUnavailableFallsBackToFiles(t *testing.T) {
	origFS := restoreFS
	origIsReal := pbsStagedApplyIsRealRestoreFSFn
	origGeteuid := pbsStagedApplyGeteuidFn
	origEnsure := pbsStagedApplyEnsurePBSServicesForAPIFn
	t.Cleanup(func() {
		restoreFS = origFS
		pbsStagedApplyIsRealRestoreFSFn = origIsReal
		pbsStagedApplyGeteuidFn = origGeteuid
		pbsStagedApplyEnsurePBSServicesForAPIFn = origEnsure
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
	pbsStagedApplyGeteuidFn = func() int { return 0 }
	pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error {
		return errors.New("forced API unavailable")
	}

	stageRoot := "/stage"
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/acme/accounts/le", []byte(`{"account":{"status":"valid"}}`), 0o600); err != nil {
		t.Fatalf("write staged ACME account: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/traffic-control.cfg", []byte("traffic-control: tc1\n    rate 10mbit\n"), 0o640); err != nil {
		t.Fatalf("write staged traffic-control.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/node.cfg", []byte("node: n1\n    description test\n"), 0o640); err != nil {
		t.Fatalf("write staged node.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/s3.cfg", []byte("s3: r1\n    bucket test\n"), 0o640); err != nil {
		t.Fatalf("write staged s3.cfg: %v", err)
	}

	safeDir := t.TempDir()
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/datastore.cfg", []byte("datastore: DS1\npath "+safeDir+"\n"), 0o640); err != nil {
		t.Fatalf("write staged datastore.cfg: %v", err)
	}

	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/remote.cfg", []byte("remote: r1\n    host 10.0.0.10\n"), 0o640); err != nil {
		t.Fatalf("write staged remote.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/sync.cfg", []byte("sync: job1\n    remote r1\n"), 0o640); err != nil {
		t.Fatalf("write staged sync.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/verification.cfg", []byte("verification: v1\n    datastore DS1\n"), 0o640); err != nil {
		t.Fatalf("write staged verification.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/prune.cfg", []byte("prune: p1\n    keep-last 1\n"), 0o640); err != nil {
		t.Fatalf("write staged prune.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/tape.cfg", []byte("drive: d1\n    path /dev/nst0\n"), 0o640); err != nil {
		t.Fatalf("write staged tape.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/tape-job.cfg", []byte("tape-job: job1\n    drive d1\n"), 0o640); err != nil {
		t.Fatalf("write staged tape-job.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/media-pool.cfg", []byte("media-pool: pool1\n    retention 30\n"), 0o640); err != nil {
		t.Fatalf("write staged media-pool.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/tape-encryption-keys.json", []byte(`{"keys":[{"fingerprint":"abc"}]}`), 0o640); err != nil {
		t.Fatalf("write staged tape-encryption-keys.json: %v", err)
	}

	plan := &RestorePlan{
		SystemType:         SystemTypePBS,
		PBSRestoreBehavior: PBSRestoreBehaviorClean,
		NormalCategories: []Category{
			{ID: "pbs_host"},
			{ID: "datastore_pbs"},
			{ID: "pbs_remotes"},
			{ID: "pbs_jobs"},
			{ID: "pbs_tape"},
		},
	}

	// datastore.cfg is written by the datastore block from the backup file, whose
	// outcome is a ⚠ one already logged: the restore ends with warnings.
	err := maybeApplyPBSConfigsFromStage(context.Background(), newTestLogger(), plan, stageRoot, false)
	if _, ok := errors.AsType[*stageApplyOutcomeLoggedError](err); !ok {
		t.Fatalf("maybeApplyPBSConfigsFromStage: %v; want the datastore block outcome already logged", err)
	}

	for _, path := range []string{
		"/etc/proxmox-backup/acme/accounts/le",
		"/etc/proxmox-backup/traffic-control.cfg",
		"/etc/proxmox-backup/node.cfg",
		"/etc/proxmox-backup/s3.cfg",
		"/etc/proxmox-backup/datastore.cfg",
		"/etc/proxmox-backup/remote.cfg",
		"/etc/proxmox-backup/sync.cfg",
		"/etc/proxmox-backup/verification.cfg",
		"/etc/proxmox-backup/prune.cfg",
		"/etc/proxmox-backup/tape-encryption-keys.json",
	} {
		if _, err := fakeFS.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
}

func TestMaybeApplyPBSConfigsFromStage_MergeMode_ApiUnavailableSkipsApiCategories(t *testing.T) {
	origFS := restoreFS
	origIsReal := pbsStagedApplyIsRealRestoreFSFn
	origGeteuid := pbsStagedApplyGeteuidFn
	origEnsure := pbsStagedApplyEnsurePBSServicesForAPIFn
	t.Cleanup(func() {
		restoreFS = origFS
		pbsStagedApplyIsRealRestoreFSFn = origIsReal
		pbsStagedApplyGeteuidFn = origGeteuid
		pbsStagedApplyEnsurePBSServicesForAPIFn = origEnsure
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
	pbsStagedApplyGeteuidFn = func() int { return 0 }
	pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error {
		return errors.New("forced API unavailable")
	}

	stageRoot := "/stage"
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/acme/accounts/le", []byte(`{"account":{"status":"valid"}}`), 0o600); err != nil {
		t.Fatalf("write staged ACME account: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/node.cfg", []byte("node: n1\n    description test\n"), 0o640); err != nil {
		t.Fatalf("write staged node.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/s3.cfg", []byte("s3: r1\n    bucket test\n"), 0o640); err != nil {
		t.Fatalf("write staged s3.cfg: %v", err)
	}

	plan := &RestorePlan{
		SystemType:         SystemTypePBS,
		PBSRestoreBehavior: PBSRestoreBehaviorMerge,
		NormalCategories: []Category{
			{ID: "pbs_host"},
			{ID: "datastore_pbs"},
			{ID: "pbs_remotes"},
			{ID: "pbs_jobs"},
		},
	}

	if err := maybeApplyPBSConfigsFromStage(context.Background(), newTestLogger(), plan, stageRoot, false); err != nil {
		t.Fatalf("maybeApplyPBSConfigsFromStage: %v", err)
	}

	if _, err := fakeFS.Stat("/etc/proxmox-backup/acme/accounts/le"); err != nil {
		t.Fatalf("expected the ACME account file to exist: %v", err)
	}

	// Merge mode requires API for these categories, so they must not be file-applied.
	for _, path := range []string{
		"/etc/proxmox-backup/node.cfg",
		"/etc/proxmox-backup/s3.cfg",
		"/etc/proxmox-backup/datastore.cfg",
		"/etc/proxmox-backup/remote.cfg",
		"/etc/proxmox-backup/sync.cfg",
	} {
		if _, err := fakeFS.Stat(path); err == nil {
			t.Fatalf("did not expect %s to be applied in merge mode without API", path)
		}
	}
}

func TestMaybeApplyPBSConfigsFromStage_ApiErrorsTriggerFallbackOnlyInCleanMode(t *testing.T) {
	origFS := restoreFS
	origIsReal := pbsStagedApplyIsRealRestoreFSFn
	origGeteuid := pbsStagedApplyGeteuidFn
	origEnsure := pbsStagedApplyEnsurePBSServicesForAPIFn
	origTraffic := pbsStagedApplyTrafficControlCfgViaAPIFn
	origNode := pbsStagedApplyNodeCfgViaAPIFn
	origS3 := pbsStagedApplyS3CfgViaAPIFn
	origDS := pbsStagedApplyDatastoreCfgViaAPIFn
	origRemote := pbsStagedApplyRemoteCfgViaAPIFn
	origSync := pbsStagedApplySyncCfgViaAPIFn
	origVerify := pbsStagedApplyVerificationCfgViaAPIFn
	origPrune := pbsStagedApplyPruneCfgViaAPIFn
	t.Cleanup(func() {
		restoreFS = origFS
		pbsStagedApplyIsRealRestoreFSFn = origIsReal
		pbsStagedApplyGeteuidFn = origGeteuid
		pbsStagedApplyEnsurePBSServicesForAPIFn = origEnsure
		pbsStagedApplyTrafficControlCfgViaAPIFn = origTraffic
		pbsStagedApplyNodeCfgViaAPIFn = origNode
		pbsStagedApplyS3CfgViaAPIFn = origS3
		pbsStagedApplyDatastoreCfgViaAPIFn = origDS
		pbsStagedApplyRemoteCfgViaAPIFn = origRemote
		pbsStagedApplySyncCfgViaAPIFn = origSync
		pbsStagedApplyVerificationCfgViaAPIFn = origVerify
		pbsStagedApplyPruneCfgViaAPIFn = origPrune
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
	pbsStagedApplyGeteuidFn = func() int { return 0 }
	pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error { return nil }

	var strictArgsClean []bool
	var strictArgsMerge []bool
	strictSink := func(strict bool) {
		strictArgsClean = append(strictArgsClean, strict)
	}
	pbsStagedApplyTrafficControlCfgViaAPIFn = func(_ context.Context, _ *logging.Logger, _ string, strict bool) error {
		strictSink(strict)
		return errors.New("forced API error")
	}
	pbsStagedApplyNodeCfgViaAPIFn = func(context.Context, string, []string) error { return errors.New("forced API error") }
	pbsStagedApplyS3CfgViaAPIFn = func(_ context.Context, _ *logging.Logger, _ string, strict bool) error {
		strictSink(strict)
		return errors.New("forced API error")
	}
	pbsStagedApplyDatastoreCfgViaAPIFn = func(_ context.Context, _ *logging.Logger, _ string, strict bool) error {
		strictSink(strict)
		return errors.New("forced API error")
	}
	pbsStagedApplyRemoteCfgViaAPIFn = func(_ context.Context, _ *logging.Logger, _ string, strict bool) error {
		strictSink(strict)
		return errors.New("forced API error")
	}
	pbsStagedApplySyncCfgViaAPIFn = func(_ context.Context, _ *logging.Logger, _ string, strict bool) error {
		strictSink(strict)
		return errors.New("forced API error")
	}
	pbsStagedApplyVerificationCfgViaAPIFn = func(_ context.Context, _ *logging.Logger, _ string, strict bool) error {
		strictSink(strict)
		return errors.New("forced API error")
	}
	pbsStagedApplyPruneCfgViaAPIFn = func(_ context.Context, _ *logging.Logger, _ string, strict bool) error {
		strictSink(strict)
		return errors.New("forced API error")
	}

	stageRoot := "/stage"
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/acme/accounts/le", []byte(`{"account":{"status":"valid"}}`), 0o600); err != nil {
		t.Fatalf("write staged ACME account: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/traffic-control.cfg", []byte("traffic-control: tc1\n    rate 10mbit\n"), 0o640); err != nil {
		t.Fatalf("write staged traffic-control.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/node.cfg", []byte("node: n1\n    description test\n"), 0o640); err != nil {
		t.Fatalf("write staged node.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/s3.cfg", []byte("s3: r1\n    bucket test\n"), 0o640); err != nil {
		t.Fatalf("write staged s3.cfg: %v", err)
	}

	safeDir := t.TempDir()
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/datastore.cfg", []byte("datastore: DS1\npath "+safeDir+"\n"), 0o640); err != nil {
		t.Fatalf("write staged datastore.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/remote.cfg", []byte("remote: r1\n    host 10.0.0.10\n"), 0o640); err != nil {
		t.Fatalf("write staged remote.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/sync.cfg", []byte("sync: job1\n    remote r1\n"), 0o640); err != nil {
		t.Fatalf("write staged sync.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/verification.cfg", []byte("verification: v1\n    datastore DS1\n"), 0o640); err != nil {
		t.Fatalf("write staged verification.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/prune.cfg", []byte("prune: p1\n    keep-last 1\n"), 0o640); err != nil {
		t.Fatalf("write staged prune.cfg: %v", err)
	}

	planClean := &RestorePlan{
		SystemType:         SystemTypePBS,
		PBSRestoreBehavior: PBSRestoreBehaviorClean,
		NormalCategories: []Category{
			{ID: "pbs_host"},
			{ID: "datastore_pbs"},
			{ID: "pbs_remotes"},
			{ID: "pbs_jobs"},
		},
	}
	// The datastore API error is written from the backup file by the datastore
	// block, whose ⚠ outcome is already logged: the restore ends with warnings.
	cleanErr := maybeApplyPBSConfigsFromStage(context.Background(), newTestLogger(), planClean, stageRoot, false)
	if _, ok := errors.AsType[*stageApplyOutcomeLoggedError](cleanErr); !ok {
		t.Fatalf("maybeApplyPBSConfigsFromStage clean: %v; want the datastore block outcome already logged", cleanErr)
	}

	for _, path := range []string{
		"/etc/proxmox-backup/traffic-control.cfg",
		"/etc/proxmox-backup/node.cfg",
		"/etc/proxmox-backup/s3.cfg",
		"/etc/proxmox-backup/datastore.cfg",
		"/etc/proxmox-backup/remote.cfg",
		"/etc/proxmox-backup/sync.cfg",
	} {
		if _, err := fakeFS.Stat(path); err != nil {
			t.Fatalf("expected %s to exist in clean fallback mode: %v", path, err)
		}
	}

	if len(strictArgsClean) == 0 {
		t.Fatalf("expected strict API calls in clean mode")
	}
	for _, strict := range strictArgsClean {
		if !strict {
			t.Fatalf("expected strict=true in clean mode, got false")
		}
	}

	// In merge mode, the same API failures must not trigger file-based fallbacks.
	strictSink = func(strict bool) {
		strictArgsMerge = append(strictArgsMerge, strict)
	}
	fakeFS2 := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS2.Root) })
	restoreFS = fakeFS2
	if err := fakeFS2.WriteFile(stageRoot+"/etc/proxmox-backup/acme/accounts/le", []byte(`{"account":{"status":"valid"}}`), 0o600); err != nil {
		t.Fatalf("write staged ACME account (merge): %v", err)
	}
	if err := fakeFS2.WriteFile(stageRoot+"/etc/proxmox-backup/node.cfg", []byte("node: n1\n    description test\n"), 0o640); err != nil {
		t.Fatalf("write staged node.cfg (merge): %v", err)
	}
	if err := fakeFS2.WriteFile(stageRoot+"/etc/proxmox-backup/s3.cfg", []byte("s3: r1\n    bucket test\n"), 0o640); err != nil {
		t.Fatalf("write staged s3.cfg (merge): %v", err)
	}
	if err := fakeFS2.WriteFile(stageRoot+"/etc/proxmox-backup/remote.cfg", []byte("remote: r1\n    host 10.0.0.10\n"), 0o640); err != nil {
		t.Fatalf("write staged remote.cfg (merge): %v", err)
	}
	if err := fakeFS2.WriteFile(stageRoot+"/etc/proxmox-backup/sync.cfg", []byte("sync: job1\n    remote r1\n"), 0o640); err != nil {
		t.Fatalf("write staged sync.cfg (merge): %v", err)
	}

	planMerge := &RestorePlan{
		SystemType:         SystemTypePBS,
		PBSRestoreBehavior: PBSRestoreBehaviorMerge,
		NormalCategories: []Category{
			{ID: "pbs_host"},
			{ID: "datastore_pbs"},
			{ID: "pbs_remotes"},
			{ID: "pbs_jobs"},
		},
	}
	// BH-003: in merge mode every API apply failed and there is no file fallback,
	// so nothing was applied; the wrapper must surface this as an error (which the
	// workflow turns into "restore completed with warnings") instead of nil.
	if err := maybeApplyPBSConfigsFromStage(context.Background(), newTestLogger(), planMerge, stageRoot, false); err == nil {
		t.Fatalf("maybeApplyPBSConfigsFromStage merge: expected an error when all API applies fail with no fallback")
	}
	for _, path := range []string{
		"/etc/proxmox-backup/node.cfg",
		"/etc/proxmox-backup/s3.cfg",
		"/etc/proxmox-backup/remote.cfg",
		"/etc/proxmox-backup/sync.cfg",
	} {
		if _, err := fakeFS2.Stat(path); err == nil {
			t.Fatalf("did not expect %s to be file-applied in merge mode", path)
		}
	}

	if len(strictArgsMerge) == 0 {
		t.Fatalf("expected strict API calls in merge mode")
	}
	for _, strict := range strictArgsMerge {
		if strict {
			t.Fatalf("expected strict=false in merge mode, got true")
		}
	}
}

func TestMaybeApplyPBSConfigsFromStage_CleanRemoveRefusedThenCreateFails_NoFallback(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	origIsReal := pbsStagedApplyIsRealRestoreFSFn
	origGeteuid := pbsStagedApplyGeteuidFn
	origEnsure := pbsStagedApplyEnsurePBSServicesForAPIFn
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
		pbsStagedApplyIsRealRestoreFSFn = origIsReal
		pbsStagedApplyGeteuidFn = origGeteuid
		pbsStagedApplyEnsurePBSServicesForAPIFn = origEnsure
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
	pbsStagedApplyGeteuidFn = func() int { return 0 }
	pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error { return nil }

	const (
		listCmd   = "proxmox-backup-manager remote list --output-format=json"
		removeCmd = "proxmox-backup-manager remote remove probe-inuse"
		createCmd = "proxmox-backup-manager remote create probe-bad --foo-bar baz"
		updateCmd = "proxmox-backup-manager remote update probe-bad --foo-bar baz"
	)
	runner := &FakeCommandRunner{
		Outputs: map[string][]byte{
			listCmd: []byte(`[{"id":"probe-inuse","name":"probe-inuse"}]`),
		},
		Errors: map[string]error{
			removeCmd: errors.New("remote is in use by sync job"),
			createCmd: errors.New("schema does not allow additional properties"),
			updateCmd: errors.New("schema does not allow additional properties"),
		},
	}
	restoreCmd = runner

	stageRoot := "/stage"
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/remote.cfg", []byte("remote: probe-bad\n\tfoo-bar baz\n"), 0o640); err != nil {
		t.Fatalf("write staged remote.cfg: %v", err)
	}

	var logBuf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&logBuf)

	plan := &RestorePlan{
		SystemType:         SystemTypePBS,
		PBSRestoreBehavior: PBSRestoreBehaviorClean,
		NormalCategories:   []Category{{ID: "pbs_remotes"}},
	}

	err := maybeApplyPBSConfigsFromStage(context.Background(), logger, plan, stageRoot, false)

	// The scenario is only meaningful if the apply really reached the failed update.
	for _, want := range []string{listCmd, removeCmd, createCmd, updateCmd} {
		if !slices.Contains(runner.CallsList(), want) {
			t.Fatalf("expected command %q, calls: %v", want, runner.CallsList())
		}
	}
	// The remote block logged the outcome of every remote and its own: the step
	// returns it as already reported, still naming the stale remote it did not
	// remove, so the restore reports 'with warnings' without a second line.
	var logged *stageApplyOutcomeLoggedError
	var incomplete *pbsSectionRestoreIncompleteError
	if !errors.As(err, &logged) || !errors.As(err, &incomplete) || len(incomplete.notRemoved) == 0 {
		t.Fatalf("expected an outcome-logged error with a stale remote not removed, got: %T %v", err, err)
	}
	if strings.Contains(logBuf.String(), "PBS API apply") || strings.Contains(logBuf.String(), "failed to apply") {
		t.Fatalf("the caller must not repeat the block outcome, log:\n%s", logBuf.String())
	}
	if strings.Contains(logBuf.String(), "falling back to file-based") {
		t.Fatalf("file fallback must NOT run on a refused clean remove, log:\n%s", logBuf.String())
	}
	if _, statErr := fakeFS.Stat("/etc/proxmox-backup/remote.cfg"); statErr == nil {
		t.Fatalf("file fallback must NOT rewrite /etc/proxmox-backup/remote.cfg on a refused clean remove")
	}
}

// TestMaybeApplyPBSConfigsFromStage_CleanDatastoreRemoveRefusedThenApplyFails_NoFallback
// runs the real datastore.cfg API apply in Clean (1:1) mode: PBS refuses to remove a
// stale datastore, then the staged datastore fails on each of the paths that leave it
// not applied. Both reach the caller as outcomes the datastore block already logged,
// so the restore ends with warnings, and the file-based fallback must NOT run (it
// would drop the datastore PBS refused to remove).
func TestMaybeApplyPBSConfigsFromStage_CleanDatastoreRemoveRefusedThenApplyFails_NoFallback(t *testing.T) {
	const (
		listCmd       = "proxmox-backup-manager datastore list --output-format=json"
		removeStale   = "proxmox-backup-manager datastore remove stale-ds"
		updateCmd     = "proxmox-backup-manager datastore update ds1"
		removeDS1     = "proxmox-backup-manager datastore remove ds1"
		rollbackCmd   = "proxmox-backup-manager datastore create ds1 /old-ds1"
		staleRow      = `{"name":"stale-ds","path":"/stale-ds"}`
		ds1OldPathRow = `{"name":"ds1","path":"/old-ds1"}`
	)
	createCmd := func(p string) string { return "proxmox-backup-manager datastore create ds1 " + p }
	errRefused := errors.New("datastore is in use")
	errCreate := errors.New("create refused")
	errUpdate := errors.New("update refused")
	errRemoveDS1 := errors.New("remove refused")
	errRollback := errors.New("rollback refused")

	cases := []struct {
		name  string
		rows  func(newPath string) string
		errs  func(newPath string) map[string]error
		calls func(newPath string) []string
	}{
		{
			name: "create-and-update-fail",
			rows: func(string) string { return "[" + staleRow + "]" },
			errs: func(p string) map[string]error {
				return map[string]error{createCmd(p): errCreate, updateCmd: errUpdate}
			},
			calls: func(p string) []string { return []string{createCmd(p), updateCmd} },
		},
		{
			name:  "update-fails",
			rows:  func(p string) string { return "[" + staleRow + `,{"name":"ds1","path":"` + p + `"}]` },
			errs:  func(string) map[string]error { return map[string]error{updateCmd: errUpdate} },
			calls: func(string) []string { return []string{updateCmd} },
		},
		{
			name:  "path-mismatch-remove-fails",
			rows:  func(string) string { return "[" + staleRow + "," + ds1OldPathRow + "]" },
			errs:  func(string) map[string]error { return map[string]error{removeDS1: errRemoveDS1} },
			calls: func(string) []string { return []string{removeDS1} },
		},
		{
			name:  "path-change-create-fails-rollback-ok",
			rows:  func(string) string { return "[" + staleRow + "," + ds1OldPathRow + "]" },
			errs:  func(p string) map[string]error { return map[string]error{createCmd(p): errCreate} },
			calls: func(p string) []string { return []string{removeDS1, createCmd(p), rollbackCmd} },
		},
		{
			name: "path-change-create-and-rollback-fail",
			rows: func(string) string { return "[" + staleRow + "," + ds1OldPathRow + "]" },
			errs: func(p string) map[string]error {
				return map[string]error{createCmd(p): errCreate, rollbackCmd: errRollback}
			},
			calls: func(p string) []string { return []string{removeDS1, createCmd(p), rollbackCmd} },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origFS := restoreFS
			origCmd := restoreCmd
			origIsReal := pbsStagedApplyIsRealRestoreFSFn
			origGeteuid := pbsStagedApplyGeteuidFn
			origEnsure := pbsStagedApplyEnsurePBSServicesForAPIFn
			t.Cleanup(func() {
				restoreFS = origFS
				restoreCmd = origCmd
				pbsStagedApplyIsRealRestoreFSFn = origIsReal
				pbsStagedApplyGeteuidFn = origGeteuid
				pbsStagedApplyEnsurePBSServicesForAPIFn = origEnsure
			})

			fakeFS := NewFakeFS()
			t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
			restoreFS = fakeFS

			pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
			pbsStagedApplyGeteuidFn = func() int { return 0 }
			pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error { return nil }

			// A SAFE, empty datastore path: the file fallback, IF wrongly invoked, passes
			// shouldApplyPBSDatastoreBlock and actually writes the live datastore.cfg.
			newPath := t.TempDir()
			errs := tc.errs(newPath)
			errs[removeStale] = errRefused
			runner := &FakeCommandRunner{
				Outputs: map[string][]byte{listCmd: []byte(tc.rows(newPath))},
				Errors:  errs,
			}
			restoreCmd = runner

			stageRoot := "/stage"
			if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/datastore.cfg", []byte("datastore: ds1\n\tpath "+newPath+"\n"), 0o640); err != nil {
				t.Fatalf("write staged datastore.cfg: %v", err)
			}

			var logBuf bytes.Buffer
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(&logBuf)

			plan := &RestorePlan{
				SystemType:         SystemTypePBS,
				PBSRestoreBehavior: PBSRestoreBehaviorClean,
				NormalCategories:   []Category{{ID: "datastore_pbs"}},
			}

			err := maybeApplyPBSConfigsFromStage(context.Background(), logger, plan, stageRoot, false)

			// The case is only meaningful if the apply really reached the failing path.
			for _, want := range append([]string{listCmd, removeStale}, tc.calls(newPath)...) {
				if !slices.Contains(runner.CallsList(), want) {
					t.Fatalf("expected command %q, calls: %v", want, runner.CallsList())
				}
			}
			if _, ok := errors.AsType[*stageApplyOutcomeLoggedError](err); !ok {
				t.Fatalf("expected an outcome-logged error so the restore reports 'with warnings', got: %v", err)
			}
			var incomplete *pbsDatastoreRestoreIncompleteError
			if !errors.As(err, &incomplete) || !slices.Equal(incomplete.notApplied, []string{"ds1"}) || !slices.Equal(incomplete.notRemoved, []string{"stale-ds"}) {
				t.Fatalf("expected ds1 not applied and stale-ds not removed, got: %v", err)
			}
			for _, unwanted := range []string{"PBS API apply: datastore", "failed to apply"} {
				if strings.Contains(logBuf.String(), unwanted) {
					t.Fatalf("no line may follow the datastore block (%q), log:\n%s", unwanted, logBuf.String())
				}
			}
			if strings.Contains(logBuf.String(), "falling back to file-based") {
				t.Fatalf("file fallback must NOT run on a refused clean remove, log:\n%s", logBuf.String())
			}
			if _, statErr := fakeFS.Stat("/etc/proxmox-backup/datastore.cfg"); statErr == nil {
				t.Fatalf("file fallback must NOT rewrite /etc/proxmox-backup/datastore.cfg on a refused clean remove")
			}
		})
	}
}

// A datastore that cannot be applied must not stop the datastores after it: dsa and
// dsc are applied, dsb is reported by its own outcome line, and nothing follows the
// datastore block. Neither mode runs the file fallback over the block's outcomes.
func TestMaybeApplyPBSConfigsFromStage_DatastoreFailureKeepsApplyingTheRest(t *testing.T) {
	const (
		listCmd     = "proxmox-backup-manager datastore list --output-format=json"
		removeStale = "proxmox-backup-manager datastore remove stale-ds"
		removeDSB   = "proxmox-backup-manager datastore remove dsb"
		updateDSB   = "proxmox-backup-manager datastore update dsb"
	)
	create := func(name, p string) string { return "proxmox-backup-manager datastore create " + name + " " + p }

	cases := []struct {
		name     string
		behavior PBSRestoreBehavior
		rows     string
		errs     func(pb string) map[string]error
	}{
		{
			name:     "merge-create-and-update-fail",
			behavior: PBSRestoreBehaviorMerge,
			rows:     "[]",
			errs: func(pb string) map[string]error {
				return map[string]error{create("dsb", pb): errors.New("create refused"), updateDSB: errors.New("update refused")}
			},
		},
		{
			name:     "clean-path-mismatch-remove-fails",
			behavior: PBSRestoreBehaviorClean,
			rows:     `[{"name":"stale-ds","path":"/stale-ds"},{"name":"dsb","path":"/old-dsb"}]`,
			errs: func(string) map[string]error {
				return map[string]error{removeStale: errors.New("datastore is in use"), removeDSB: errors.New("remove refused")}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origFS := restoreFS
			origCmd := restoreCmd
			origIsReal := pbsStagedApplyIsRealRestoreFSFn
			origGeteuid := pbsStagedApplyGeteuidFn
			origEnsure := pbsStagedApplyEnsurePBSServicesForAPIFn
			origDS := pbsStagedApplyDatastoreCfgViaAPIFn
			t.Cleanup(func() {
				restoreFS = origFS
				restoreCmd = origCmd
				pbsStagedApplyIsRealRestoreFSFn = origIsReal
				pbsStagedApplyGeteuidFn = origGeteuid
				pbsStagedApplyEnsurePBSServicesForAPIFn = origEnsure
				pbsStagedApplyDatastoreCfgViaAPIFn = origDS
			})

			fakeFS := NewFakeFS()
			t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
			restoreFS = fakeFS

			pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
			pbsStagedApplyGeteuidFn = func() int { return 0 }
			pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error { return nil }
			pbsStagedApplyDatastoreCfgViaAPIFn = applyPBSDatastoreCfgViaAPI

			base := t.TempDir()
			pa, pb, pc := base+"/pa", base+"/pb", base+"/pc"
			runner := &FakeCommandRunner{
				Outputs: map[string][]byte{listCmd: []byte(tc.rows)},
				Errors:  tc.errs(pb),
			}
			restoreCmd = runner

			stageRoot := "/stage"
			staged := "datastore: dsa\n\tpath " + pa + "\n\ndatastore: dsb\n\tpath " + pb + "\n\ndatastore: dsc\n\tpath " + pc + "\n"
			if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/datastore.cfg", []byte(staged), 0o640); err != nil {
				t.Fatalf("write staged datastore.cfg: %v", err)
			}

			var logBuf bytes.Buffer
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(&logBuf)

			plan := &RestorePlan{
				SystemType:         SystemTypePBS,
				PBSRestoreBehavior: tc.behavior,
				NormalCategories:   []Category{{ID: "datastore_pbs"}},
			}
			err := maybeApplyPBSConfigsFromStage(context.Background(), logger, plan, stageRoot, false)

			for _, want := range []string{create("dsa", pa), create("dsc", pc)} {
				if !slices.Contains(runner.CallsList(), want) {
					t.Fatalf("expected command %q, calls: %v", want, runner.CallsList())
				}
			}
			if !strings.Contains(logBuf.String(), "WARNING  ⚠ PBS datastore dsb: not applied\n") {
				t.Fatalf("expected the dsb outcome line, log:\n%s", logBuf.String())
			}
			for _, unwanted := range []string{"PBS API apply: datastore", "failed to apply", "falling back to file-based"} {
				if strings.Contains(logBuf.String(), unwanted) {
					t.Fatalf("no line may follow the datastore block (%q), log:\n%s", unwanted, logBuf.String())
				}
			}
			if _, ok := errors.AsType[*stageApplyOutcomeLoggedError](err); !ok {
				t.Fatalf("expected an outcome-logged error so the restore reports 'with warnings', got: %v", err)
			}
		})
	}
}

// In Merge mode there is no file fallback, so an object of remote.cfg whose create and
// update both fail must not stop the objects after it: alpha and charlie are applied,
// bravo is reported by the remote block, and the step returns the block's outcome as
// already reported.
func TestMaybeApplyPBSConfigsFromStage_MergeMode_RemoteObjectFailureKeepsApplyingTheRest(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	origIsReal := pbsStagedApplyIsRealRestoreFSFn
	origGeteuid := pbsStagedApplyGeteuidFn
	origEnsure := pbsStagedApplyEnsurePBSServicesForAPIFn
	origRemote := pbsStagedApplyRemoteCfgViaAPIFn
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
		pbsStagedApplyIsRealRestoreFSFn = origIsReal
		pbsStagedApplyGeteuidFn = origGeteuid
		pbsStagedApplyEnsurePBSServicesForAPIFn = origEnsure
		pbsStagedApplyRemoteCfgViaAPIFn = origRemote
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
	pbsStagedApplyGeteuidFn = func() int { return 0 }
	pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error { return nil }
	pbsStagedApplyRemoteCfgViaAPIFn = applyPBSRemoteCfgViaAPI

	runner := &pbsSectionCharRunner{rules: []pbsSectionCharRule{
		{verb: "create", id: "bravo", err: errPBSSectionCharCreate},
		{verb: "update", id: "bravo", err: errPBSSectionCharUpdate},
	}}
	restoreCmd = runner

	stageRoot := "/stage"
	var staged strings.Builder
	for _, id := range []string{"alpha", "bravo", "charlie"} {
		staged.WriteString("remote: " + id + "\n\thost " + id + ".pbs.example\n\tauth-id sync@pbs\n\tpassword " + pbsSectionCharRemotePasswordStaged + "\n\n")
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/remote.cfg", []byte(staged.String()), 0o640); err != nil {
		t.Fatalf("write staged remote.cfg: %v", err)
	}

	var logBuf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&logBuf)

	plan := &RestorePlan{
		SystemType:         SystemTypePBS,
		PBSRestoreBehavior: PBSRestoreBehaviorMerge,
		NormalCategories:   []Category{{ID: "pbs_remotes"}},
	}
	err := maybeApplyPBSConfigsFromStage(context.Background(), logger, plan, stageRoot, false)

	if got := strings.Join(pbsSectionCharObjects(runner.calls, "create"), ","); got != "alpha,bravo,charlie" {
		t.Fatalf("create objects = %s, want alpha,bravo,charlie", got)
	}
	if got := strings.Join(pbsSectionCharObjects(runner.calls, "update"), ","); got != "bravo" {
		t.Fatalf("update objects = %s, want bravo", got)
	}

	for _, want := range []string{
		"INFO     ✓ PBS remote alpha: applied\n",
		`DEBUG    PBS remote restore: remote=bravo create failed error="injected create failure" action=update` + "\n",
		`DEBUG    PBS remote restore: remote=bravo update failed error="injected update failure"` + "\n",
		`INFO     Remote bravo was rejected by PBS: "injected create failure"` + "\n",
		"WARNING  ⚠ PBS remote bravo: not applied\n",
		"INFO     ✓ PBS remote charlie: applied\n",
		"WARNING  ⚠ PBS remote configuration: partially applied\n",
	} {
		if !strings.Contains(logBuf.String(), want) {
			t.Fatalf("expected log line %q, log:\n%s", want, logBuf.String())
		}
	}
	for _, secret := range []string{pbsSectionCharRemotePassword, pbsSectionCharRemotePasswordStaged} {
		if strings.Contains(logBuf.String(), secret) || (err != nil && strings.Contains(err.Error(), secret)) {
			t.Fatalf("secret %q leaked, err: %v, log:\n%s", secret, err, logBuf.String())
		}
	}
	if strings.Contains(logBuf.String(), "falling back to file-based") {
		t.Fatalf("merge mode has no file fallback, log:\n%s", logBuf.String())
	}
	if strings.Contains(logBuf.String(), "PBS API apply") || strings.Contains(logBuf.String(), "failed to apply") {
		t.Fatalf("the caller must not repeat the block outcome, log:\n%s", logBuf.String())
	}
	var logged *stageApplyOutcomeLoggedError
	if !errors.As(err, &logged) || err.Error() != "PBS remote configuration incomplete: 1 remote(s) not applied [bravo]" {
		t.Fatalf("err = %T %v, want the remote block outcome as already reported", err, err)
	}
}
