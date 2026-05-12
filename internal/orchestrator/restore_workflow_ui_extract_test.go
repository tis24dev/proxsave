package orchestrator

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

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
