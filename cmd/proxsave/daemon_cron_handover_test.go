// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// handoverFixture gives the helper a STATEFUL fake crontab: what the reader returns reflects
// what the writer wrote. That is what makes the ORDER observable. With a fixed reader stub the
// adoption would find the proxsave line whether it ran before or after the removal, and the
// ordering this function exists to guarantee would be pinned by nothing.
func handoverFixture(t *testing.T, lines []string) (string, func() []string) {
	t.Helper()
	origRead, origWrite := crontabReadLinesFn, crontabWriteLinesFn
	origPaths := systemCronPaths
	t.Cleanup(func() {
		crontabReadLinesFn, crontabWriteLinesFn = origRead, origWrite
		systemCronPaths = origPaths
	})
	state := append([]string(nil), lines...)
	crontabReadLinesFn = func(context.Context) ([]string, error) { return append([]string(nil), state...), nil }
	crontabWriteLinesFn = func(_ context.Context, next []string) error {
		state = append([]string(nil), next...)
		return nil
	}
	systemCronPaths = []string{filepath.Join(t.TempDir(), "absent")}

	configPath := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(configPath, []byte("BACKUP_PATH=/data\nSCHEDULER_TIME=02:00\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, func() []string { return state }
}

// prepareCronHandoverForDaemon is the part of the cron -> daemon switch that a test can drive:
// everything around it in applyDaemonMode shells out to systemctl. Three guarantees live here
// and each one used to be unpinned, so deleting the line that provides it left the suite green.
func TestPrepareCronHandoverForDaemon(t *testing.T) {
	t.Run("the run time is adopted BEFORE the line that carries it is deleted", func(t *testing.T) {
		configPath, crontab := handoverFixture(t, []string{
			"0 21 * * * /usr/local/bin/proxsave --backup",
			"0 6 * * * /usr/bin/rsync /a /b",
		})

		outcome := prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		// 21:00 is only knowable from the cron line, and that line is gone by the time this
		// function returns. Adopting after the removal reads an emptied crontab and keeps 02:00.
		if !strings.Contains(string(data), "SCHEDULER_TIME=21:00") {
			t.Errorf("the run time must be adopted before the removal, config:\n%s", data)
		}
		if outcome.Removed != 1 || !outcome.Verified {
			t.Errorf("the proxsave line must still be removed and counted, got %+v", outcome)
		}
		remaining := crontab()
		if len(remaining) != 1 || !strings.Contains(remaining[0], "rsync") {
			t.Errorf("the operator's unrelated line must survive, got %v", remaining)
		}
	})

	t.Run("a surviving unmanaged schedule is counted into the outcome", func(t *testing.T) {
		wrapper := "30 02 * * * " + absentWrapper(t)
		configPath, _ := handoverFixture(t, []string{wrapper})

		outcome := prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

		// The count is the only thing that reaches the dashboard result screen, where the log
		// is muted for the whole operation.
		if outcome.UnmanagedSchedules != 1 {
			t.Errorf("UnmanagedSchedules = %d, want 1", outcome.UnmanagedSchedules)
		}
	})

	t.Run("an ordinary host: nothing adopted, nothing counted", func(t *testing.T) {
		configPath, _ := handoverFixture(t, []string{"0 6 * * * /usr/bin/rsync /a /b"})

		outcome := prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "SCHEDULER_TIME=02:00") {
			t.Errorf("no proxsave line means no adoption, config:\n%s", data)
		}
		if outcome.UnmanagedSchedules != 0 || outcome.Removed != 0 {
			t.Errorf("nothing to remove and nothing unmanaged, got %+v", outcome)
		}
		if !outcome.Verified {
			t.Error("the crontab was read, so the outcome must be verified")
		}
	})
}
