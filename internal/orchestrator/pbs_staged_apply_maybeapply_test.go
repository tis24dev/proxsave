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

	if err := maybeApplyPBSConfigsFromStage(context.Background(), newTestLogger(), plan, stageRoot, false); err != nil {
		t.Fatalf("maybeApplyPBSConfigsFromStage: %v", err)
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
	pbsStagedApplyNodeCfgViaAPIFn = func(context.Context, string) error { return errors.New("forced API error") }
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
	if err := maybeApplyPBSConfigsFromStage(context.Background(), newTestLogger(), planClean, stageRoot, false); err != nil {
		t.Fatalf("maybeApplyPBSConfigsFromStage clean: %v", err)
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

// TestMaybeApplyPBSConfigsFromStage_CleanRemoveIncomplete_SurfacesWithoutFallback
// (H10) checks that when a Clean (1:1) API apply succeeds at create/update but
// could not remove a stale object, the wrapper surfaces it as a failed item (so
// the restore reports "with warnings") WITHOUT invoking the destructive
// file-based fallback that would force-rewrite the .cfg and drop the object.
func TestMaybeApplyPBSConfigsFromStage_CleanRemoveIncomplete_SurfacesWithoutFallback(t *testing.T) {
	origFS := restoreFS
	origIsReal := pbsStagedApplyIsRealRestoreFSFn
	origGeteuid := pbsStagedApplyGeteuidFn
	origEnsure := pbsStagedApplyEnsurePBSServicesForAPIFn
	origDS := pbsStagedApplyDatastoreCfgViaAPIFn
	t.Cleanup(func() {
		restoreFS = origFS
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

	// Create/update succeed but a stale datastore could not be removed in Clean mode.
	pbsStagedApplyDatastoreCfgViaAPIFn = func(context.Context, *logging.Logger, string, bool) error {
		return pbsCleanRemoveResult("datastore", []string{"stale-ds"})
	}

	stageRoot := "/stage"
	// datastore.cfg points at a SAFE, empty datastore path so the file fallback, IF
	// wrongly invoked, would pass shouldApplyPBSDatastoreBlock and actually write the
	// live file. A non-empty path like /tmp is deferred (never written), which would
	// make the "file not written" assertion below pass even on a bypass regression.
	safeDir := t.TempDir()
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/datastore.cfg", []byte("datastore: DS1\n    path "+safeDir+"\n"), 0o640); err != nil {
		t.Fatalf("write staged datastore.cfg: %v", err)
	}

	plan := &RestorePlan{
		SystemType:         SystemTypePBS,
		PBSRestoreBehavior: PBSRestoreBehaviorClean,
		NormalCategories:   []Category{{ID: "datastore_pbs"}},
	}

	err := maybeApplyPBSConfigsFromStage(context.Background(), newTestLogger(), plan, stageRoot, false)
	if err == nil {
		t.Fatalf("expected a summary error so the restore reports 'with warnings'")
	}
	if !strings.Contains(err.Error(), "datastore.cfg (clean 1:1 incomplete)") {
		t.Fatalf("expected the clean-1:1-incomplete item in the summary, got: %v", err)
	}

	// Conservative outcome: the destructive file-based fallback must NOT run, so
	// the live datastore.cfg is left untouched (the stale object stays).
	if _, statErr := fakeFS.Stat("/etc/proxmox-backup/datastore.cfg"); statErr == nil {
		t.Fatalf("file fallback must NOT rewrite /etc/proxmox-backup/datastore.cfg on a clean-remove failure")
	}
}

// TestMaybeApplyPBSConfigsFromStage_CleanRemoveRefusedThenCreateFails_NoFallback
// runs the real remote.cfg API apply in Clean (1:1) mode: PBS refuses to remove a
// stale remote, then both create and update of the staged remote fail. The
// refused remove must still reach the caller as a clean 1:1 incomplete item, and
// the file-based fallback must NOT run (it would drop the remote PBS refused to
// remove).
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
	if err == nil {
		t.Fatalf("expected a summary error so the restore reports 'with warnings'")
	}
	if !strings.Contains(err.Error(), "remote.cfg (clean 1:1 incomplete)") {
		t.Fatalf("expected the clean-1:1-incomplete item in the summary, got: %v", err)
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
// stale datastore, then the staged datastore fails on each of the paths that end the
// apply early. The refused remove must still reach the caller as a clean 1:1
// incomplete item, the error text must stay as it is, and the file-based fallback
// must NOT run (it would drop the datastore PBS refused to remove).
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
		name    string
		rows    func(newPath string) string
		errs    func(newPath string) map[string]error
		calls   func(newPath string) []string
		wantErr func(newPath string) string
	}{
		{
			name: "create-and-update-fail",
			rows: func(string) string { return "[" + staleRow + "]" },
			errs: func(p string) map[string]error {
				return map[string]error{createCmd(p): errCreate, updateCmd: errUpdate}
			},
			calls: func(p string) []string { return []string{createCmd(p), updateCmd} },
			wantErr: func(p string) string {
				return "datastore ds1: " + createCmd(p) + " failed: create refused (create) / " + updateCmd + " failed: update refused (update)"
			},
		},
		{
			name:  "update-fails",
			rows:  func(p string) string { return "[" + staleRow + `,{"name":"ds1","path":"` + p + `"}]` },
			errs:  func(string) map[string]error { return map[string]error{updateCmd: errUpdate} },
			calls: func(string) []string { return []string{updateCmd} },
			wantErr: func(string) string {
				return "datastore ds1: update failed: " + updateCmd + " failed: update refused"
			},
		},
		{
			name:  "path-mismatch-remove-fails",
			rows:  func(string) string { return "[" + staleRow + "," + ds1OldPathRow + "]" },
			errs:  func(string) map[string]error { return map[string]error{removeDS1: errRemoveDS1} },
			calls: func(string) []string { return []string{removeDS1} },
			wantErr: func(p string) string {
				return "datastore ds1: path mismatch (/old-ds1 != " + p + ") and remove failed: " + removeDS1 + " failed: remove refused"
			},
		},
		{
			name:  "path-change-create-fails-rollback-ok",
			rows:  func(string) string { return "[" + staleRow + "," + ds1OldPathRow + "]" },
			errs:  func(p string) map[string]error { return map[string]error{createCmd(p): errCreate} },
			calls: func(p string) []string { return []string{removeDS1, createCmd(p), rollbackCmd} },
			wantErr: func(p string) string {
				return "datastore ds1: path change to " + p + " failed (" + createCmd(p) + " failed: create refused); the datastore was restored to its original path /old-ds1"
			},
		},
		{
			name: "path-change-create-and-rollback-fail",
			rows: func(string) string { return "[" + staleRow + "," + ds1OldPathRow + "]" },
			errs: func(p string) map[string]error {
				return map[string]error{createCmd(p): errCreate, rollbackCmd: errRollback}
			},
			calls: func(p string) []string { return []string{removeDS1, createCmd(p), rollbackCmd} },
			wantErr: func(p string) string {
				return "datastore ds1: path change to " + p + " failed (" + createCmd(p) + " failed: create refused) and rollback re-create at original path /old-ds1 also failed (" + rollbackCmd + " failed: rollback refused); the datastore is left removed, its data is still on disk and it must be re-created manually"
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
			wantLine := "PBS API apply: datastore.cfg failed: " + tc.wantErr(newPath) + "\n"
			if !strings.Contains(logBuf.String(), wantLine) {
				t.Fatalf("expected the unchanged warning %q, log:\n%s", wantLine, logBuf.String())
			}
			if err == nil {
				t.Fatalf("expected a summary error so the restore reports 'with warnings'")
			}
			if !strings.Contains(err.Error(), "datastore.cfg (clean 1:1 incomplete)") {
				t.Fatalf("expected the clean-1:1-incomplete item in the summary, got: %v", err)
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
