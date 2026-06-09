package orchestrator

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/tui"
	"github.com/tis24dev/proxsave/internal/tui/components"
)

// Regression for netcommit-default-focuses-commit (2026-06-09 audit): the TUI
// network-commit gate added COMMIT first (button 0) and never called
// SetDefaultButton, so tview focused COMMIT by default. A reflexive Enter then
// committed a possibly-broken network config and disarmed the automatic rollback
// (remote lockout). The default must be the safe "Let rollback run" button, like
// the CLI (blank line = rollback) and every sibling destructive prompt. Written
// after the fix, hence the _audited suffix.
func TestConfigureNetworkCommitButtons_DefaultsToLetRollbackRun(t *testing.T) {
	form := components.NewForm(tui.NewApp())
	configureNetworkCommitButtons(form)

	if got := form.GetButtonCount(); got != 2 {
		t.Fatalf("button count = %d, want 2", got)
	}

	// Button order is COMMIT(0), "Let rollback run"(1); pin the labels so the
	// index-based default below cannot silently point at the wrong button.
	if got := form.GetButton(0).GetLabel(); got != "COMMIT" {
		t.Fatalf("button 0 label = %q, want COMMIT", got)
	}
	if got := form.GetButton(1).GetLabel(); got != "Let rollback run" {
		t.Fatalf("button 1 label = %q, want 'Let rollback run'", got)
	}

	// The safe choice must hold the default keyboard focus.
	if !form.GetButton(1).HasFocus() {
		t.Errorf("default focus should be on 'Let rollback run' (index 1) so a reflexive Enter lets the rollback run")
	}
	if form.GetButton(0).HasFocus() {
		t.Errorf("COMMIT (index 0) must NOT be the default focus (it disarms rollback)")
	}
}
