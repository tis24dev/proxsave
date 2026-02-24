package orchestrator

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
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
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/acme/accounts.cfg", []byte("account: a1\n    foo bar\n"), 0o640); err != nil {
		t.Fatalf("write staged accounts.cfg: %v", err)
	}

	plan := &RestorePlan{
		SystemType:         SystemTypePBS,
		PBSRestoreBehavior: PBSRestoreBehaviorClean,
		NormalCategories:   []Category{{ID: "pbs_host"}},
	}
	if err := maybeApplyPBSConfigsFromStage(context.Background(), newTestLogger(), plan, stageRoot, false); err != nil {
		t.Fatalf("maybeApplyPBSConfigsFromStage: %v", err)
	}

	if _, err := fakeFS.Stat("/etc/proxmox-backup/acme/accounts.cfg"); err == nil {
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
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/acme/accounts.cfg", []byte("account: a1\n    foo bar\n"), 0o640); err != nil {
		t.Fatalf("write staged accounts.cfg: %v", err)
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
		"/etc/proxmox-backup/acme/accounts.cfg",
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
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/acme/accounts.cfg", []byte("account: a1\n    foo bar\n"), 0o640); err != nil {
		t.Fatalf("write staged accounts.cfg: %v", err)
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

	if _, err := fakeFS.Stat("/etc/proxmox-backup/acme/accounts.cfg"); err != nil {
		t.Fatalf("expected accounts.cfg to exist: %v", err)
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
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/acme/accounts.cfg", []byte("account: a1\n    foo bar\n"), 0o640); err != nil {
		t.Fatalf("write staged accounts.cfg: %v", err)
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
	if err := fakeFS2.WriteFile(stageRoot+"/etc/proxmox-backup/acme/accounts.cfg", []byte("account: a1\n    foo bar\n"), 0o640); err != nil {
		t.Fatalf("write staged accounts.cfg (merge): %v", err)
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
	if err := maybeApplyPBSConfigsFromStage(context.Background(), newTestLogger(), planMerge, stageRoot, false); err != nil {
		t.Fatalf("maybeApplyPBSConfigsFromStage merge: %v", err)
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
