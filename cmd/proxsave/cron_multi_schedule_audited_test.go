package main

import "testing"

// Regression for cron-multi-entry-schedule-collapse (2026-06-09 audit): when the
// crontab had two or more outdated proxsave/proxmox-backup entries with DIFFERENT
// schedules and no current entry, filterCronLines remembered only the first
// schedule, so migrateLegacyCronEntries rewrote a single entry and silently
// discarded the others. Written after changing filterCronLines to collect every
// distinct removed schedule.

func TestFilterCronLines_MultipleDistinctSchedulesPreserved(t *testing.T) {
	input := []string{
		"0 2 * * * /usr/bin/proxmox-backup",        // outdated, schedule A
		"30 5 * * 0 /usr/local/bin/proxmox-backup", // outdated, schedule B (weekly)
		"# a comment",
		"0 1 * * * /some/other/job",
	}

	lines, hasCurrent, schedules := filterCronLines(input, []string{"/usr/local/bin/proxsave"})

	if hasCurrent {
		t.Fatalf("hasCurrentEntry = true, want false")
	}
	want := []string{"0 2 * * *", "30 5 * * 0"}
	if len(schedules) != len(want) {
		t.Fatalf("schedules = %q, want %q (distinct schedules must not collapse into one)", schedules, want)
	}
	for i := range want {
		if schedules[i] != want[i] {
			t.Errorf("schedules[%d] = %q, want %q", i, schedules[i], want[i])
		}
	}

	// Both outdated lines removed; the comment and unrelated job stay.
	wantLines := []string{"# a comment", "0 1 * * * /some/other/job"}
	if len(lines) != len(wantLines) {
		t.Fatalf("lines = %q, want %q", lines, wantLines)
	}
	for i := range wantLines {
		if lines[i] != wantLines[i] {
			t.Errorf("line[%d] = %q, want %q", i, lines[i], wantLines[i])
		}
	}
}

func TestFilterCronLines_DuplicateScheduleDeduped(t *testing.T) {
	input := []string{
		"0 2 * * * /usr/bin/proxmox-backup",
		"0 2 * * * /usr/local/bin/proxmox-backup",
	}
	_, _, schedules := filterCronLines(input, []string{"/usr/local/bin/proxsave"})
	if len(schedules) != 1 || schedules[0] != "0 2 * * *" {
		t.Fatalf("schedules = %q, want exactly [\"0 2 * * *\"] (identical schedules deduped)", schedules)
	}
}
