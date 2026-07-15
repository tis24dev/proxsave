package orchestrator

import (
	"context"
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
