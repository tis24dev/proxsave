package orchestrator

import (
	"context"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestTUIRestoreWorkflowUISelectExportNode_UsesBuildPage(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyEnter})

	ui := newTUIRestoreWorkflowUI("/tmp/config.env", "sig", nil)
	builderCalls := 0
	ui.buildPage = func(title, configPath, buildSig string, content tview.Primitive) tview.Primitive {
		builderCalls++
		return buildRestoreWizardPage(title, configPath, buildSig, content)
	}

	got, err := ui.SelectExportNode(context.Background(), t.TempDir(), "node0", []string{"node1"})
	if err != nil {
		t.Fatalf("SelectExportNode error: %v", err)
	}
	if got != "node1" {
		t.Fatalf("node=%q, want %q", got, "node1")
	}
	if builderCalls != 1 {
		t.Fatalf("builderCalls=%d, want 1", builderCalls)
	}
}
