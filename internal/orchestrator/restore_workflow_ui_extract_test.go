package orchestrator

import (
	"context"
	"testing"
)

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
		prepared: &preparedBundle{ArchivePath: "/missing.tar"},
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
		prepared: &preparedBundle{ArchivePath: "/missing.tar"},
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
