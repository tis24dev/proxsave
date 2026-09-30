// Package main contains the proxsave command entrypoint.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// captureDefaultLog routes the global logger into a buffer at DEBUG, the sink the switch
// uses on the --daemon-setup path, where bootstrap is nil.
func captureDefaultLog(t *testing.T) (*logging.Logger, *bytes.Buffer) {
	t.Helper()
	origLog := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(origLog) })
	var buf bytes.Buffer
	def := logging.New(types.LogLevelDebug, false)
	def.SetOutput(&buf)
	logging.SetDefaultLogger(def)
	return def, &buf
}

// Switching from cron to the daemon must not move the backup. In cron mode the crontab IS the
// schedule and the SCHEDULER_* variables are leftovers nothing keeps in step: an operator who
// edits the cron line changes the schedule, and the variables still say whatever they said at
// install. The daemon then reads them, so without this the host silently starts running on a
// different schedule the moment it is retrofitted.
//
// It OVERWRITES, unlike the install-time seeding, which fills the variables only when they are
// absent (schedule_helpers.go: an explicit operator value is never overridden). That gate is
// right at install time, where the variables and the crontab are two independent statements of
// intent. Here they are not: the host is on cron, so the crontab is the one that has been in force.
func TestAdoptSchedulerTimeForDaemon(t *testing.T) {
	const bin = " /usr/local/bin/proxsave --backup"
	for _, tc := range []struct {
		name      string
		stored    string
		lines     []string
		wantKeys  []string
		wantBlock bool
	}{
		{
			name:      "the cron line wins over the stale variable",
			stored:    "SCHEDULER_TIME=03:15\n",
			lines:     []string{"0 21 * * *" + bin},
			wantKeys:  []string{"SCHEDULER_TIME=21:00", "SCHEDULER_FREQUENCY=daily"},
			wantBlock: true,
		},
		{
			name:      "a weekly line carries the whole cadence",
			stored:    "SCHEDULER_TIME=02:00\nSCHEDULER_FREQUENCY=daily\n",
			lines:     []string{"0 3 * * 1" + bin},
			wantKeys:  []string{"SCHEDULER_FREQUENCY=weekly", "SCHEDULER_WEEKDAY=mon", "SCHEDULER_TIME=03:00"},
			wantBlock: true,
		},
		{
			name:     "already in step: nothing written, nothing said",
			stored:   "SCHEDULER_TIME=21:00\n",
			lines:    []string{"0 21 * * *" + bin},
			wantKeys: []string{"SCHEDULER_TIME=21:00"},
		},
		{
			name:     "no proxsave cron line: the variables are left alone",
			stored:   "SCHEDULER_TIME=03:15\n",
			lines:    []string{"0 6 * * * /usr/bin/rsync /a /b"},
			wantKeys: []string{"SCHEDULER_TIME=03:15"},
		},
		{
			name:     "unreadable crontab: the variables are left alone",
			stored:   "SCHEDULER_TIME=03:15\n",
			lines:    nil,
			wantKeys: []string{"SCHEDULER_TIME=03:15"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "backup.env")
			if err := os.WriteFile(configPath, []byte("BACKUP_PATH=/data\n"+tc.stored), 0o600); err != nil {
				t.Fatal(err)
			}
			def, buf := captureDefaultLog(t)

			adoptSchedulerTimeForDaemon(configPath, tc.lines, nil)

			data, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range append(tc.wantKeys, "BACKUP_PATH=/data") {
				if !strings.Contains(string(data), key) {
					t.Errorf("want %q in the config, got:\n%s", key, data)
				}
			}
			out := buf.String()
			said := strings.Contains(out, scheduleAdoptHeader)
			if said != tc.wantBlock {
				t.Errorf("block printed = %v, want %v, out=%q", said, tc.wantBlock, out)
			}
			if tc.wantBlock {
				assertInOrder(t, out, scheduleAdoptHeader, "schedule adopt: cron line=", "schedule adopt: before ", "schedule adopt: wrote ", "  Frequency: ", "  Time: ", "✓ Backup schedule: adopted from cron entry")
			}
			if def.WarningCount() != 0 {
				t.Errorf("no warning expected, out=%q", out)
			}
		})
	}
}

// A monthly line on day 29-31 is not a cadence. The switch keeps what backup.env says and
// warns, naming the entry and the reason.
func TestAdoptSchedulerTimeForDaemonReportsDay31(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(configPath, []byte("SCHEDULER_TIME=02:00\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	def, buf := captureDefaultLog(t)

	adoptSchedulerTimeForDaemon(configPath, []string{"0 3 31 * * /usr/local/bin/proxsave --backup"}, nil)

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "SCHEDULER_TIME=02:00\n" {
		t.Errorf("nothing may be written, got:\n%s", data)
	}
	out := buf.String()
	assertInOrder(t, out,
		scheduleAdoptHeader,
		"schedule adopt: cron line=", "not adoptable reason=monthday_out_of_range",
		"schedule adopt: kept frequency=daily time=02:00",
		"  Frequency: daily", "  Time: 02:00",
		"Day 31 is outside 1-28",
		`⚠ Backup schedule: cron entry "0 3 31 * *" not adopted`)
	if got := logLineLevel(out, "⚠ Backup schedule"); got != "WARNING" {
		t.Errorf("outcome level = %q, want WARNING", got)
	}
	if def.WarningCount() != 1 {
		t.Errorf("warnings = %d, want 1", def.WarningCount())
	}
}

// The write is made to fail the way it fails in production - the directory is not writable, so
// WriteConfigFileAtomic cannot create its temp file - rather than by a seam, because the config
// stays perfectly readable and only the write half breaks. The removal that follows goes ahead
// either way, so the host keeps the schedule backup.env already had; that is the one outcome the
// operator cannot infer from anything else on screen, so it is a WARNING with the schedule in
// effect as its details.
func TestAFailedSchedulerTimeAdoptionIsSaidOutLoud(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "backup.env")
	if err := os.WriteFile(configPath, []byte("BACKUP_PATH=/data\nSCHEDULER_TIME=03:15\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	def, buf := captureDefaultLog(t)

	adoptSchedulerTimeForDaemon(configPath, []string{"0 21 * * * /usr/local/bin/proxsave --backup"}, nil)

	out := buf.String()
	assertInOrder(t, out,
		scheduleAdoptHeader,
		"schedule adopt: cron line=", "time=21:00",
		"schedule adopt: write failed file="+configPath+" error=",
		"schedule adopt: kept frequency=daily time=03:15",
		"  Frequency: daily", "  Time: 03:15",
		"backup.env could not be written",
		"⚠ Backup schedule: not adopted from cron entry")
	if def.WarningCount() != 1 {
		t.Errorf("a failed adoption must warn once, not whisper at debug; out=%q", out)
	}
	if strings.Contains(out, "✓ Backup schedule") {
		t.Errorf("no success may be claimed, out=%q", out)
	}
}
