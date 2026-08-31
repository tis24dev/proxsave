package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The runner rule answers "is this named after proxsave", by the file's own account, and the
// file's own rule for name evidence is that the name votes only when nothing could be read.
// The runner rule was not honouring it: the same script, read and found not to call the
// binary, was cleared when cron ran it directly and reported when a launcher sat in front of
// it. A report there refuses an unattended --upgrade, so an operator with an ordinary
// proxsave-named exporter behind an flock could not upgrade.
func TestARunnerIsNoFindingWhenTheScriptBehindItWasReadAndCleared(t *testing.T) {
	dir := t.TempDir()
	exporter := filepath.Join(dir, "proxsave-metrics-exporter")
	if err := os.WriteFile(exporter, []byte("#!/bin/sh\n/usr/bin/curl -s localhost:9100 > /var/lib/metrics.txt\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "nas-guard")
	if err := os.WriteFile(backup, []byte("#!/bin/sh\n/usr/local/bin/proxsave --backup\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		line string
		want bool
	}{
		{"read and cleared, called directly", "0 2 * * * " + exporter, false},
		{"read and cleared, behind flock", "0 2 * * * /usr/bin/flock -n /var/lock/x " + exporter, false},
		{"read and cleared, inside sh -c", "0 2 * * * /bin/sh -c '" + exporter + "'", false},

		// Guards. Every row below already answers this way today.

		{"read and it does call the binary", "0 2 * * * /usr/bin/flock -n /var/lock/x " + backup, true},
		{
			// Nothing was read here, so the name is all there is and it still votes.
			"behind a runner and could not be read",
			"0 2 * * * /usr/bin/flock -n /var/lock/x " + filepath.Join(dir, "absent", "proxsave-guard"),
			true,
		},
		{
			// A relative command resolves against the crontab owner's home, which the probe
			// refuses to guess, so nothing behind this runner was read either.
			"behind a runner and not an absolute path",
			"0 2 * * * /usr/bin/flock -n /var/lock/x proxsave-guard",
			true,
		},
		{
			// PART of the tail was read and cleared, and part of it was not read at all.
			// "Some of it" is not clearance: the second command here is relative, so the
			// walk knows nothing about it, and the name still has to vote.
			"one command behind the runner read, another skipped",
			"0 2 * * * /usr/bin/flock -n /var/lock/x " + exporter + " ; proxsave-guard",
			true,
		},
		{
			// Nothing at all sits in command position behind this runner: the lock operand
			// is consumed and the line ends. Nothing was read, so nothing is cleared.
			"a runner with no command behind it",
			"0 2 * * * /usr/bin/flock -n /var/lock/proxsave-guard",
			true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs := indirectProxsaveCronRefs([]string{tc.line}, cronProbeReadScripts)
			if got := len(refs) > 0; got != tc.want {
				t.Errorf("flagged = %v, want %v for %q (refs %+v)", got, tc.want, tc.line, refs)
			}
		})
	}
}
