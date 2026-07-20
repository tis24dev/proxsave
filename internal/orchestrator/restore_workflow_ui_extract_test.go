package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// TestRunStageApplyStep_InconsistentStateHalts pins F06-08: a step returning
// ErrRestoreInconsistentState (an auth-DB left half-written) must PROPAGATE (halt the restore),
// not be swallowed into restoreHadWarnings like an ordinary staged-apply error.
func TestRunStageApplyStep_InconsistentStateHalts(t *testing.T) {
	w := &restoreUIWorkflowRun{ctx: context.Background(), logger: newTestLogger()}

	inconsistent := restoreStageApplyStep{name: "System accounts staged apply", run: func() error {
		return fmt.Errorf("commit failed and rollback also failed: %w", ErrRestoreInconsistentState)
	}}
	if err := w.runStageApplyStep(inconsistent); !errors.Is(err, ErrRestoreInconsistentState) {
		t.Fatalf("inconsistent-state step must halt (return the error), got: %v", err)
	}
	if w.restoreHadWarnings {
		t.Fatalf("inconsistent state must NOT be downgraded to a warning")
	}

	// An ordinary step error is still swallowed to a warning (returns nil), unchanged.
	w2 := &restoreUIWorkflowRun{ctx: context.Background(), logger: newTestLogger()}
	ordinary := restoreStageApplyStep{name: "PVE staged config apply", run: func() error {
		return errors.New("some non-fatal apply glitch")
	}}
	if err := w2.runStageApplyStep(ordinary); err != nil {
		t.Fatalf("ordinary step error must be downgraded (return nil), got: %v", err)
	}
	if !w2.restoreHadWarnings {
		t.Fatalf("ordinary step error must set restoreHadWarnings")
	}
}

func TestRunClusterSafeApplySkipsWhenExportExtractionIncomplete(t *testing.T) {
	logger := newTestLogger()
	logger.SetLevel(types.LogLevelWarning)
	w := &restoreUIWorkflowRun{
		ctx:           context.Background(),
		logger:        logger,
		ui:            nil,
		exportRoot:    filepath.Join(t.TempDir(), "export"),
		exportLogPath: "",
		plan:          &RestorePlan{ClusterSafeMode: true},
		prepared:      &preparedBundle{ArchivePath: missingArchivePath(t)},
	}

	if err := w.runClusterSafeApply(); err != nil {
		t.Fatalf("runClusterSafeApply error: %v", err)
	}
	if logger.WarningCount() != 1 {
		t.Fatalf("expected skip warning for incomplete export extraction, got %d", logger.WarningCount())
	}
}

func TestExtractStagedCategoriesReportsIncompleteOnNonAbortError(t *testing.T) {
	origRestoreFS := restoreFS
	fakeFS := NewFakeFS()
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		_ = fakeFS.Cleanup()
	})
	restoreFS = fakeFS

	w := &restoreUIWorkflowRun{
		ctx:    context.Background(),
		logger: newTestLogger(),
		plan: &RestorePlan{
			SystemType:       SystemTypePBS,
			StagedCategories: []Category{{ID: "pbs_notifications"}},
		},
		prepared: &preparedBundle{ArchivePath: missingArchivePath(t)},
	}

	success, err := w.extractStagedCategories()
	if err != nil {
		t.Fatalf("extractStagedCategories error: %v", err)
	}
	if success {
		t.Fatalf("success=true; want false")
	}
	if !w.restoreHadWarnings {
		t.Fatalf("restoreHadWarnings=false; want true")
	}
	if w.stageLogPath != "" {
		t.Fatalf("stageLogPath=%q; want empty on incomplete staging", w.stageLogPath)
	}
}

func TestStageAndApplySensitiveCategoriesSkipsApplyWhenStagingIncomplete(t *testing.T) {
	origRestoreFS := restoreFS
	fakeFS := NewFakeFS()
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		_ = fakeFS.Cleanup()
	})
	restoreFS = fakeFS

	w := &restoreUIWorkflowRun{
		ctx:      context.Background(),
		logger:   newTestLogger(),
		destRoot: "/",
		plan: &RestorePlan{
			SystemType:       SystemTypePBS,
			StagedCategories: []Category{{ID: "pbs_notifications"}},
		},
		prepared: &preparedBundle{ArchivePath: missingArchivePath(t)},
	}

	if err := w.stageAndApplySensitiveCategories(); err != nil {
		t.Fatalf("stageAndApplySensitiveCategories error: %v", err)
	}
	if !w.restoreHadWarnings {
		t.Fatalf("restoreHadWarnings=false; want true")
	}
	if w.stageLogPath != "" {
		t.Fatalf("stageLogPath=%q; want empty on incomplete staging", w.stageLogPath)
	}
}

func missingArchivePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "missing.tar")
}
