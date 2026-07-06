// notify_results.go carries the per-run notification outcomes from the backup CHILD (which
// sends the notifications in Phase-7) to the resident DAEMON (which is the only pinger), so
// the daemon can emit one healthchecks ping per notification channel. It is a sibling of the
// status file in the identity dir, written with the same atomic, non-immutable idiom.
//
// The daemon sets EnvRunID on the child and rejects any results file whose RID does not match
// the run it supervised (a stale file from a prior run, or a child that never reached Phase-7).

package health

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// EnvRunID is the environment variable the daemon sets on the backup child so the child's
// per-run notify-results file can be correlated (rid) with the exact run the daemon
// supervised. Defined in this leaf package so BOTH cmd/proxsave (daemon, writer of the env)
// and internal/orchestrator (child, reader of the env) reference one constant.
const EnvRunID = "PROXSAVE_RUN_ID"

// NotifyResults is the per-run handoff the backup child writes and the daemon reads. Results
// maps a notification channel's display name ("Email"/"Telegram"/"Gotify"/"Webhook") to its
// send severity ("ok"/"warning"/"error"/"disabled"); the daemon maps that to a /0 or /1 ping.
type NotifyResults struct {
	RID     string            `json:"rid"`
	TS      int64             `json:"ts"`
	Results map[string]string `json:"results"`
}

// NotifyResultsPath returns the per-run results file path, a sibling of the status file in the
// identity dir (same same-uid, non-immutable rationale as StatusPath).
func NotifyResultsPath(baseDir string) string {
	return filepath.Join(baseDir, "identity", ".notify_results.json")
}

// WriteNotifyResults writes the per-run results atomically (child side). A nil results map is
// written as an empty object so the daemon can distinguish "child ran, nothing to report" (a
// present file with the matching rid) from "child crashed" (no/stale file).
func WriteNotifyResults(baseDir, rid string, ts int64, results map[string]string) error {
	if results == nil {
		results = map[string]string{}
	}
	return writeJSONAtomic(NotifyResultsPath(baseDir), NotifyResults{RID: rid, TS: ts, Results: results})
}

// LoadNotifyResults reads the per-run results tolerantly (daemon side): a missing OR empty
// file yields the zero value with a nil error (RID=="" then fails the daemon's rid guard).
// Only malformed JSON is an error, and even then the returned value is the zero value.
func LoadNotifyResults(baseDir string) (NotifyResults, error) {
	var nr NotifyResults
	data, err := os.ReadFile(NotifyResultsPath(baseDir))
	if err != nil {
		if os.IsNotExist(err) {
			return NotifyResults{}, nil
		}
		return NotifyResults{}, fmt.Errorf("read notify results: %w", err)
	}
	if len(data) == 0 {
		return NotifyResults{}, nil
	}
	if err := json.Unmarshal(data, &nr); err != nil {
		return NotifyResults{}, fmt.Errorf("parse notify results: %w", err)
	}
	return nr, nil
}
