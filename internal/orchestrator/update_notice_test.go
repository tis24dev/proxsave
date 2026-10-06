package orchestrator

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/types"
)

// On a host whose daemon reports updates to healthchecks the run keeps the update notice out of
// its log, so the issue count never raises the exit code (issue #334). The notifications must
// still list it exactly as a logged WARNING would read; where the run did log it, the parsed
// log already carries it and a second entry would be a duplicate.
func TestNotificationsListAnUnloggedUpdateNotice(t *testing.T) {
	logged := notify.LogCategory{Label: "Cloud log directory not configured", Type: "WARNING", Count: 1, Example: "Cloud log directory not configured"}
	notice := UpdateNoticeMessage("0.42.0", "0.41.0")

	newStats := func(noticeLogged bool) *BackupStats {
		return &BackupStats{
			ExitCode:            types.ExitSuccess.Int(),
			WarningCount:        1,
			LogCategories:       []notify.LogCategory{logged},
			NewVersionAvailable: true,
			CurrentVersion:      "0.41.0",
			LatestVersion:       "0.42.0",
			UpdateNoticeLogged:  noticeLogged,
		}
	}
	adapter := &NotificationAdapter{logger: logging.New(types.LogLevelError, false)}

	t.Run("kept out of the log", func(t *testing.T) {
		data := adapter.convertBackupStatsToNotificationData(newStats(false))

		if data.WarningCount != 2 || data.TotalIssues != 2 {
			t.Fatalf("WarningCount=%d TotalIssues=%d, want 2 and 2: the listed notice must be counted", data.WarningCount, data.TotalIssues)
		}
		var found *notify.LogCategory
		for i := range data.LogCategories {
			if data.LogCategories[i].Label == notice {
				found = &data.LogCategories[i]
			}
		}
		if found == nil {
			t.Fatalf("update notice missing from LogCategories: %+v", data.LogCategories)
		}
		if found.Type != "WARNING" || found.Count != 1 || found.Example != notice {
			t.Fatalf("update notice entry = %+v, want a WARNING counted once that reads like the logged line", *found)
		}
		if data.ExitCode != types.ExitSuccess.Int() {
			t.Fatalf("ExitCode = %d, want the run's own exit code untouched", data.ExitCode)
		}
	})

	t.Run("already in the log", func(t *testing.T) {
		data := adapter.convertBackupStatsToNotificationData(newStats(true))

		if data.WarningCount != 1 || len(data.LogCategories) != 1 {
			t.Fatalf("WarningCount=%d categories=%d, want the parsed log untouched (no duplicate)", data.WarningCount, len(data.LogCategories))
		}
	})

	t.Run("no update", func(t *testing.T) {
		stats := newStats(false)
		stats.NewVersionAvailable = false
		data := adapter.convertBackupStatsToNotificationData(stats)

		if data.WarningCount != 1 || len(data.LogCategories) != 1 {
			t.Fatalf("WarningCount=%d categories=%d, want nothing added without an update", data.WarningCount, len(data.LogCategories))
		}
	})
}

// SetUpdateNoticeLogged carries the run's answer into the stats the notifications read.
func TestInitBackupRunCarriesUpdateNoticeLogged(t *testing.T) {
	orch := &Orchestrator{}
	orch.SetUpdateInfo(true, "0.41.0", "0.42.0")
	orch.SetUpdateNoticeLogged(true)
	if !orch.updateNoticeLogged {
		t.Fatal("SetUpdateNoticeLogged(true) did not record the answer")
	}
	var nilOrch *Orchestrator
	nilOrch.SetUpdateNoticeLogged(true) // must not panic
}
