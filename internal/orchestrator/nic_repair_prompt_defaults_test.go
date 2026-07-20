package orchestrator

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

type recordedConfirm struct {
	title      string
	yesLabel   string
	noLabel    string
	defaultYes bool
}

type recordingNICUI struct {
	calls   []recordedConfirm
	answers []bool // per-call answer to return
}

func (r *recordingNICUI) ConfirmAction(_ context.Context, title, _, yesLabel, noLabel string, _ time.Duration, defaultYes bool) (bool, error) {
	r.calls = append(r.calls, recordedConfirm{title, yesLabel, noLabel, defaultYes})
	ans := false
	if len(r.calls)-1 < len(r.answers) {
		ans = r.answers[len(r.calls)-1]
	}
	return ans, nil
}

func (r *recordingNICUI) ShowMessage(_ context.Context, _, _ string) error { return nil }

// F04-01: the "NIC naming overrides" prompt must default to the SAFE branch
// ("Skip NIC repair"); the "NIC name conflicts" prompt must stay default-safe
// ("Skip conflicts", defaultYes=false).
func TestNICOverridesPromptDefaultsSafe(t *testing.T) {
	origPlan := planNICNameRepairFn
	origOverride := detectNICNamingOverrideRulesFn
	t.Cleanup(func() { planNICNameRepairFn = origPlan; detectNICNamingOverrideRulesFn = origOverride })

	planNICNameRepairFn = func(context.Context, string) (*nicRepairPlan, error) {
		return &nicRepairPlan{
			Mapping:   nicMappingResult{Entries: []nicMappingEntry{{OldName: "eth0", NewName: "nic0"}}},
			Conflicts: []nicNameConflict{{Mapping: nicMappingEntry{OldName: "eth1", NewName: "nic1"}, Existing: archivedNetworkInterface{Name: "nic1"}}},
		}, nil
	}
	detectNICNamingOverrideRulesFn = func(*logging.Logger) (nicNamingOverrideReport, error) {
		return nicNamingOverrideReport{Rules: []nicNamingOverrideRule{{Kind: nicNamingOverrideUdev, Source: "/etc/udev/rules.d/70-x.rules", Name: "nic0"}}}, nil
	}

	// answers: overrides prompt -> false (proceed, do not skip) so the conflicts
	// prompt is reached; conflicts prompt -> false (skip conflicts, no file write).
	ui := &recordingNICUI{answers: []bool{false, false}}
	logger := logging.New(types.LogLevelError, false)
	_ = repairNICNamesWithUI(context.Background(), ui, logger, "/nonexistent.tar.xz")

	var overrides, conflicts *recordedConfirm
	for i := range ui.calls {
		switch ui.calls[i].title {
		case "NIC naming overrides":
			overrides = &ui.calls[i]
		case "NIC name conflicts":
			conflicts = &ui.calls[i]
		}
	}
	if overrides == nil {
		t.Fatal("overrides prompt was not shown")
	}
	if !overrides.defaultYes {
		t.Fatalf("F04-01: overrides prompt must default to the safe branch (defaultYes=true, yes=%q)", overrides.yesLabel)
	}
	if overrides.yesLabel != "Skip NIC repair" {
		t.Fatalf("overrides safe branch must be the yes label, got yes=%q no=%q", overrides.yesLabel, overrides.noLabel)
	}
	if conflicts == nil {
		t.Fatal("conflicts prompt was not shown")
	}
	if conflicts.defaultYes {
		t.Fatal("conflicts prompt must stay default-safe (defaultYes=false, noLabel is the safe Skip)")
	}
}

// recordingApplyUI embeds the full fake RestoreWorkflowUI and records the
// promptNICRepair confirmation; returning false (skip) avoids reaching the real
// RepairNICNames.
type recordingApplyUI struct {
	*fakeRestoreWorkflowUI
	got  recordedConfirm
	seen bool
}

func (r *recordingApplyUI) ConfirmAction(_ context.Context, title, _, yesLabel, noLabel string, _ time.Duration, defaultYes bool) (bool, error) {
	r.got = recordedConfirm{title, yesLabel, noLabel, defaultYes}
	r.seen = true
	return false, nil
}

// failedNICRepairUI confirms the repair (repairNow=true) and returns a FAILED result.
type failedNICRepairUI struct {
	*fakeRestoreWorkflowUI
}

func (u *failedNICRepairUI) ConfirmAction(_ context.Context, _, _, _, _ string, _ time.Duration, _ bool) (bool, error) {
	return true, nil // repair now
}

func (u *failedNICRepairUI) RepairNICNames(_ context.Context, _ string) (*nicRepairResult, error) {
	return &nicRepairResult{Failed: true, FailedReason: "ifreload failed"}, nil
}

func (u *failedNICRepairUI) ShowMessage(_ context.Context, _, _ string) error { return nil }

// ffa32ff follow-up: a FAILED NIC repair in the forward apply flow must be surfaced at
// WARNING prominence (like the rollback and staged-auto paths), not only an info-level
// modal, so all three flows report a failed repair consistently.
func TestPromptNICRepairFailedIsWarning(t *testing.T) {
	ui := &failedNICRepairUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{}}
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	f := &networkConfigUIApplyFlow{
		ctx:         context.Background(),
		ui:          ui,
		logger:      logger,
		archivePath: "/x.tar.xz",
	}
	if err := f.promptNICRepair(); err != nil {
		t.Fatalf("promptNICRepair: %v", err)
	}
	if logger.WarningCount() < 1 {
		t.Fatalf("a failed NIC repair must be logged at warning prominence, WarningCount=%d log=%s", logger.WarningCount(), buf.String())
	}
	if !strings.Contains(buf.String(), "NIC name repair FAILED") {
		t.Fatalf("warning must carry the repair summary, log=%s", buf.String())
	}
}

// LIVE-NIC-COPY: the top-level "NIC name repair (recommended)" gate must default
// to Repair now, consistent with the recommendation.
func TestPromptNICRepairDefaultsRepair(t *testing.T) {
	ui := &recordingApplyUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{}}
	f := &networkConfigUIApplyFlow{
		ctx:         context.Background(),
		ui:          ui,
		logger:      logging.New(types.LogLevelError, false),
		archivePath: "/nonexistent.tar.xz",
	}
	if err := f.promptNICRepair(); err != nil {
		t.Fatalf("promptNICRepair: %v", err)
	}
	if !ui.seen {
		t.Fatal("promptNICRepair did not confirm")
	}
	if !ui.got.defaultYes {
		t.Fatalf("LIVE-NIC-COPY: promptNICRepair must default to Repair (defaultYes=true), got yes=%q default=%v", ui.got.yesLabel, ui.got.defaultYes)
	}
	if ui.got.yesLabel != "Repair now" {
		t.Fatalf("repair must be the yes label, got %q", ui.got.yesLabel)
	}
}
