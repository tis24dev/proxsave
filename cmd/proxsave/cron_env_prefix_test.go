package main

import "testing"

// cron accepts VAR=value in front of a job's command, and TZ= in front of one is common
// precisely because cron does not read /etc/timezone. Everything in this package that
// asks "does this line run proxsave" asks cronCommandToken, so a line the operator
// writes that way was invisible to the detector, to the removal and to the schedule
// adoption alike.
//
// The rows that already pass are not padding. Four of the eight callers of this function
// WRITE the crontab, so a token this parser invents is a line ProxSave deletes. Each
// guard row below is a shape where the answer must NOT change.
func TestCronCommandTokenSeesTheCommandBehindALeadingEnvironmentAssignment(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{
			"leading assignment, classic schedule",
			"0 2 * * * TZ=UTC /usr/local/bin/proxsave --backup",
			"/usr/local/bin/proxsave",
		},
		{
			"leading assignment, @shortcut schedule",
			"@daily TZ=UTC /usr/local/bin/proxsave --backup",
			"/usr/local/bin/proxsave",
		},
		{
			"two leading assignments",
			"0 2 * * * TZ=UTC LANG=C /usr/local/bin/proxsave --backup",
			"/usr/local/bin/proxsave",
		},
		{
			"quoted assignment value with no space in it",
			`0 2 * * * TZ="Europe/Rome" /usr/local/bin/proxsave --backup`,
			"/usr/local/bin/proxsave",
		},
		{
			"leading assignment in front of a command that is not ours",
			"0 2 * * * TZ=UTC /usr/bin/rsync /a /b",
			"/usr/bin/rsync",
		},

		// Guards. Every row below already answers this way today.

		{
			// A crontab ENVIRONMENT line, which is not a job at all. This is what the
			// fields[0] test exists for and it must keep winning.
			"environment line, not a job",
			`MAILTO=""`,
			"",
		},
		{
			// The value names the binary and the command runs it through the variable.
			// The line genuinely runs proxsave, so it stays ours: today's answer, kept
			// deliberately. Losing it would leave the entry in place and add a second
			// one beside it, which is #298.
			"assignment value names the binary, command expands it",
			"0 2 * * * BIN=/usr/local/bin/proxsave $BIN --backup",
			"BIN=/usr/local/bin/proxsave",
		},
		{
			// Same shape behind a runner. Reporting the runner instead would make this an
			// INDIRECT finding, and an indirect finding refuses an unattended --upgrade.
			"assignment value names the binary, command is a runner",
			"0 2 * * * BIN=/usr/local/bin/proxsave /usr/bin/flock -n /x /usr/bin/rsync /a /b",
			"BIN=/usr/local/bin/proxsave",
		},
		{
			// strings.Fields splits on the ESCAPED space, so "proxsave" here is the second
			// half of MSG's value and not a command at all. Promoting it would make
			// ProxSave delete an operator's rsync job. The line must stay unreadable to
			// this parser, exactly as it is today.
			"assignment value split by an escaped space",
			`0 2 * * * MSG=hello\ proxsave /usr/bin/rsync /a /b`,
			`MSG=hello\`,
		},
		{
			// Same, split by a quote instead of a backslash.
			"assignment value split by a quoted space",
			`0 2 * * * MSG='hello proxsave' /usr/bin/rsync /a /b`,
			"MSG='hello",
		},
		{
			"assignment with no command after it",
			"0 2 * * * a=1",
			"a=1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cronCommandToken(tc.line); got != tc.want {
				t.Errorf("cronCommandToken(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

// The writers are the reason this is not a reporting change. buildReinstallCronLines is
// the shortest path from the parser to a crontab ProxSave overwrites.
func TestAReinstallRemovesTheCommandBehindAnAssignmentAndLeavesTheOperatorsJobsAlone(t *testing.T) {
	in := []string{
		"# operator jobs",
		"0 2 * * * TZ=UTC /usr/local/bin/proxsave --backup",
		`0 5 * * * MSG=hello\ proxsave /usr/bin/rsync /a /b`,
		"0 9 * * * /usr/bin/rsync /e /f",
	}
	want := []string{
		"# operator jobs",
		`0 5 * * * MSG=hello\ proxsave /usr/bin/rsync /a /b`,
		"0 9 * * * /usr/bin/rsync /e /f",
		"0 4 * * * /usr/local/bin/proxsave --backup",
	}

	got := buildReinstallCronLines(in, "/opt/proxsave", []string{"/usr/local/bin/proxsave"}, "0 4 * * *", "/usr/local/bin/proxsave", nil)
	if len(got) != len(want) {
		t.Fatalf("buildReinstallCronLines returned %d line(s), want %d:\ngot  %q\nwant %q", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The schedule adoption reads the same token, so a host whose only cron entry carries a
// TZ prefix used to migrate to the daemon with no time to adopt.
func TestTheSchedulerTimeIsAdoptedFromALineThatCarriesAnAssignment(t *testing.T) {
	got, ok := schedulerTimeFromCronLines([]string{"30 3 * * * TZ=UTC /usr/local/bin/proxsave --backup"})
	if !ok || got != "03:30" {
		t.Errorf("schedulerTimeFromCronLines = (%q, %v), want (\"03:30\", true)", got, ok)
	}
}
