package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Notes cross a process boundary: --upgrade runs the installed binary with
// --upgrade-config-json and decodes its stdout. They must survive that round trip, and a
// result carrying them must still decode in a binary built before the field existed, which
// is the one rendering the first upgrade that brings it.
func TestUpgradeResultNotesSurviveTheJSONHandOff(t *testing.T) {
	in := UpgradeResult{
		Changed:  true,
		Warnings: []string{"w"},
		Notes: []UpgradeNote{
			{Level: UpgradeNoteInfo, Text: "Adopting backup schedule from cron entry..."},
			{Level: UpgradeNoteDebug, Text: "schedule adopt: cron line=\"0 3 * * 1 proxsave\""},
		},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}

	var out UpgradeResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(out.Notes, in.Notes) {
		t.Fatalf("Notes = %+v, want %+v", out.Notes, in.Notes)
	}

	// The shape an older binary decodes into: no Notes field at all.
	var old struct {
		Warnings []string
		Changed  bool
	}
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatalf("an older binary must still decode the result: %v", err)
	}
	if !old.Changed || len(old.Warnings) != 1 {
		t.Fatalf("older decode lost the fields it knows: %+v", old)
	}
}
