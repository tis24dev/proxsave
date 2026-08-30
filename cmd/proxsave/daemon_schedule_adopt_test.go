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

// Switching from cron to the daemon must not move the backup. In cron mode the crontab IS the
// schedule and SCHEDULER_TIME is a leftover nothing keeps in step: an operator who edits the
// cron line changes the run time, and the key still says whatever it said at install. The
// daemon then reads that key, so without this the host silently starts running at a different
// hour the moment it is retrofitted.
//
// It OVERWRITES, unlike the install-time seeding, which fills the key only when it is absent
// (schedule_helpers.go: an explicit operator value is never overridden). That gate is right at
// install time, where the key and the crontab are two independent statements of intent. Here
// they are not: the host is on cron, so the crontab is the one that has been in force.
func TestAdoptSchedulerTimeForDaemon(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stored   string
		lines    []string
		wantKey  string
		wantNote bool
	}{
		{
			name:     "the cron line wins over the stale key",
			stored:   "SCHEDULER_TIME=03:15\n",
			lines:    []string{"0 21 * * * /usr/local/bin/proxsave --backup"},
			wantKey:  "SCHEDULER_TIME=21:00",
			wantNote: true,
		},
		{
			name:     "already in step: nothing written, nothing said",
			stored:   "SCHEDULER_TIME=21:00\n",
			lines:    []string{"0 21 * * * /usr/local/bin/proxsave --backup"},
			wantKey:  "SCHEDULER_TIME=21:00",
			wantNote: false,
		},
		{
			name:     "no proxsave cron line: the key is left alone",
			stored:   "SCHEDULER_TIME=03:15\n",
			lines:    []string{"0 6 * * * /usr/bin/rsync /a /b"},
			wantKey:  "SCHEDULER_TIME=03:15",
			wantNote: false,
		},
		{
			// Two different times, or a cadence that is not a single daily run, cannot be
			// carried over as one hour. The daemon runs once a day, so guessing which one to
			// keep would move the backup on purpose.
			name:     "two different times: no single hour to adopt",
			stored:   "SCHEDULER_TIME=03:15\n",
			lines:    []string{"0 21 * * * /usr/local/bin/proxsave --backup", "0 5 * * * /usr/local/bin/proxsave --backup"},
			wantKey:  "SCHEDULER_TIME=03:15",
			wantNote: false,
		},
		{
			name:     "not a daily cadence: no single hour to adopt",
			stored:   "SCHEDULER_TIME=03:15\n",
			lines:    []string{"*/30 * * * * /usr/local/bin/proxsave --backup"},
			wantKey:  "SCHEDULER_TIME=03:15",
			wantNote: false,
		},
		{
			name:     "unreadable crontab: the key is left alone",
			stored:   "SCHEDULER_TIME=03:15\n",
			lines:    nil,
			wantKey:  "SCHEDULER_TIME=03:15",
			wantNote: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "backup.env")
			if err := os.WriteFile(configPath, []byte("BACKUP_PATH=/data\n"+tc.stored), 0o600); err != nil {
				t.Fatal(err)
			}

			origLog := logging.GetDefaultLogger()
			t.Cleanup(func() { logging.SetDefaultLogger(origLog) })
			var buf bytes.Buffer
			def := logging.New(types.LogLevelDebug, false)
			def.SetOutput(&buf)
			logging.SetDefaultLogger(def)

			adoptSchedulerTimeForDaemon(configPath, tc.lines, nil)

			data, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), tc.wantKey) {
				t.Errorf("want %q in the config, got:\n%s", tc.wantKey, data)
			}
			if !strings.Contains(string(data), "BACKUP_PATH=/data") {
				t.Errorf("an unrelated key was lost:\n%s", data)
			}
			said := strings.Contains(buf.String(), "SCHEDULER_TIME")
			if said != tc.wantNote {
				t.Errorf("note printed = %v, want %v, out=%q", said, tc.wantNote, buf.String())
			}
		})
	}
}
