package main

import "testing"

// The note counts what was FOUND under /etc, and since the run-parts directories joined
// the walk that count can include a script, which is not a line. "entry" is the word cron
// itself uses for both a crontab line and a run-parts file.
func TestTheOwnershipNoteCountsEntriesBecauseAScriptIsNotALine(t *testing.T) {
	want := "ProxSave owns the root crontab only and never edits files it did not place, 2 entry(ies) in /etc unchanged"
	if got := systemCronOwnershipNote(2); got != want {
		t.Errorf("systemCronOwnershipNote(2) = %q, want %q", got, want)
	}
}
