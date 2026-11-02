package orchestrator

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A Clean restore removed the stale remotes before the stale sync jobs, so a remote
// still used by a job that was about to go was refused ("PBS remote rX: not removed").
// The jobs are applied first, in Clean and Merge alike. PBS accepts a sync job whose
// remote does not exist yet (measured on pve-test, PBS 4.2.8, pull and push).
func TestPBSJobsAreAppliedBeforeRemotes(t *testing.T) {
	origFS := restoreFS
	origIsReal, origGeteuid := pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn
	origEnsure := pbsStagedApplyEnsurePBSServicesForAPIFn
	origRemote, origSync := pbsStagedApplyRemoteCfgViaAPIFn, pbsStagedApplySyncCfgViaAPIFn
	origVerify, origPrune := pbsStagedApplyVerificationCfgViaAPIFn, pbsStagedApplyPruneCfgViaAPIFn
	t.Cleanup(func() {
		restoreFS = origFS
		pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn = origIsReal, origGeteuid
		pbsStagedApplyEnsurePBSServicesForAPIFn = origEnsure
		pbsStagedApplyRemoteCfgViaAPIFn, pbsStagedApplySyncCfgViaAPIFn = origRemote, origSync
		pbsStagedApplyVerificationCfgViaAPIFn, pbsStagedApplyPruneCfgViaAPIFn = origVerify, origPrune
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
	pbsStagedApplyGeteuidFn = func() int { return 0 }
	pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error { return nil }

	var order []string
	record := func(name string) func(context.Context, *logging.Logger, string, bool) error {
		return func(context.Context, *logging.Logger, string, bool) error {
			order = append(order, name)
			return nil
		}
	}
	pbsStagedApplyRemoteCfgViaAPIFn = record("remote")
	pbsStagedApplySyncCfgViaAPIFn = record("sync")
	pbsStagedApplyVerificationCfgViaAPIFn = record("verification")
	pbsStagedApplyPruneCfgViaAPIFn = record("prune")

	for _, behavior := range []PBSRestoreBehavior{PBSRestoreBehaviorClean, PBSRestoreBehaviorMerge} {
		order = nil
		logger := logging.New(types.LogLevelError, false)
		plan := &RestorePlan{SystemType: SystemTypePBS, PBSRestoreBehavior: behavior,
			NormalCategories: []Category{{ID: "pbs_remotes"}, {ID: "pbs_jobs"}}}
		if err := maybeApplyPBSConfigsFromStage(context.Background(), logger, plan, "/stage", false); err != nil {
			t.Fatalf("%v: apply: %v", behavior, err)
		}
		if want := []string{"sync", "verification", "prune", "remote"}; !slices.Equal(order, want) {
			t.Fatalf("%v: order %v, want %v", behavior, order, want)
		}
	}
}
