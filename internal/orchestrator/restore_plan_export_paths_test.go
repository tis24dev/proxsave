package orchestrator

import (
	"context"
	"strings"
	"testing"
)

type planCapturingUI struct {
	*fakeRestoreWorkflowUI
	plan *SelectiveRestoreConfig
}

func (u *planCapturingUI) ShowRestorePlan(_ context.Context, config *SelectiveRestoreConfig) error {
	u.plan = config
	return nil
}

// Categories moved to the export directory for this run (pve_cluster in SAFE mode,
// another product's categories) keep ExportOnly false, so the plan listed their paths
// under "Files/directories that will be restored:" with the OVERWRITTEN warning. The
// plan shows them as the export they are; the plan's own categories keep the flag.
func TestRestorePlanDoesNotListExportedCategoryPaths(t *testing.T) {
	cluster := Category{ID: "pve_cluster", Name: "PVE Cluster Configuration", Paths: []string{"./var/lib/pve-cluster/"}}
	network := Category{ID: "network", Name: "Network", Paths: []string{"./etc/network/"}}
	ui := &planCapturingUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{confirmRestore: true}}
	w := &restoreUIWorkflowRun{
		ctx:       context.Background(),
		logger:    newTestLogger(),
		ui:        ui,
		candidate: &backupCandidate{},
		plan:      &RestorePlan{NormalCategories: []Category{network}, ExportCategories: []Category{cluster}},
	}
	if err := w.confirmRestorePlan(); err != nil {
		t.Fatalf("confirmRestorePlan: %v", err)
	}
	text := buildRestorePlanText(ui.plan)
	restored := text[strings.Index(text, "Files/directories that will be restored:"):]
	restored = restored[:strings.Index(restored, "⚠ WARNING:")]
	if strings.Contains(restored, "/var/lib/pve-cluster/") {
		t.Fatalf("an exported category's path is listed as restored:\n%s", restored)
	}
	if !strings.Contains(restored, "/etc/network/") {
		t.Fatalf("the restored category's path is missing:\n%s", restored)
	}
	if w.plan.ExportCategories[0].ExportOnly {
		t.Fatal("the plan's own export category was flagged; applyClusterSafety could not move it back")
	}
}
