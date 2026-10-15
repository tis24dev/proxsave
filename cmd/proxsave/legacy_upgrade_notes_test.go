package main

import (
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

func legacyChangesFixture() []config.LegacyChange {
	return []config.LegacyChange{
		{Name: "LOCAL_LOG_PATH", Current: "LOG_PATH", Lines: []int{90}, Renamed: true},
		{Name: "EMAIL_ENABLE", Current: "EMAIL_ENABLED", Lines: []int{12, 40}},
		{Name: "RCLONE_REMOTE", Current: "CLOUD_REMOTE", Lines: []int{7}, Renamed: true},
	}
}

// The DEBUG evidence of every variable comes first, then one INFO line for the renamed
// ones and one for the ones dropped.
func TestLegacyUpgradeNotes(t *testing.T) {
	notes := legacyUpgradeNotes(legacyChangesFixture())
	want := []config.UpgradeNote{
		{Level: config.UpgradeNoteDebug, Text: "config upgrade: legacy LOCAL_LOG_PATH current=LOG_PATH lines=90 action=renamed"},
		{Level: config.UpgradeNoteDebug, Text: "config upgrade: legacy EMAIL_ENABLE current=EMAIL_ENABLED lines=12, 40 action=removed"},
		{Level: config.UpgradeNoteDebug, Text: "config upgrade: legacy RCLONE_REMOTE current=CLOUD_REMOTE lines=7 action=renamed"},
		{Level: config.UpgradeNoteInfo, Text: "Renamed 2 legacy variable(s): LOCAL_LOG_PATH to LOG_PATH, RCLONE_REMOTE to CLOUD_REMOTE"},
		{Level: config.UpgradeNoteInfo, Text: "Removed 1 legacy variable(s) with no effect: EMAIL_ENABLE"},
	}
	if len(notes) != len(want) {
		t.Fatalf("notes = %+v", notes)
	}
	for i := range want {
		if notes[i] != want[i] {
			t.Fatalf("note %d = %+v, want %+v", i, notes[i], want[i])
		}
	}
	if got := legacyUpgradeNotes(nil); got != nil {
		t.Fatalf("no change, notes %+v", got)
	}
}

func TestLegacyPlanLinesInTheDashboardCheck(t *testing.T) {
	text := describeConfigPlan(&config.UpgradeResult{Changed: true, Legacy: legacyChangesFixture()})
	for _, want := range []string{
		"Legacy variables that would be renamed (2): LOCAL_LOG_PATH to LOG_PATH, RCLONE_REMOTE to CLOUD_REMOTE",
		"Legacy variables that would be removed (1): EMAIL_ENABLE",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}
