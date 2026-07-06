// status.go persists the daemon's last ping outcomes to a small JSON file that
// the run-phase Healthchecks section reads back. The daemon and the run are
// separate processes, so a shared on-disk record is the only honest way for the
// section to report REAL transmission (heartbeat / backup outcome) instead of a
// cosmetic "active" derived from a secret merely being present on disk.
//
// The write is an atomic read-modify-write cloned from
// internal/orchestrator/temp_registry.go (MarshalIndent -> WriteFile ".tmp" 0o600
// -> Rename) so a reader never observes a torn file, and it deliberately does NOT
// reuse identity.PersistNotifySecret (that path sets the immutable +i attribute,
// which would block every rewrite). This file stays logging-free and stdlib-only
// so both the daemon and the orchestrator can import it without a logger.

package health

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Ping kind constants. These are the values RecordPing accepts for the kind
// argument and they match the daemon's own ping labels so the two sides agree
// without a second mapping table. KindRunStarted/Finished/Hang mirror the
// reporter labels ("start"/"finish"/"hang"); KindHeartbeat names the liveness
// ping ("heartbeat") the daemon sends on its fixed interval.
const (
	KindHeartbeat   = "heartbeat"
	KindRunStarted  = "start"
	KindRunFinished = "finish"
	KindRunHang     = "hang"
	KindUpdates     = "updates"
)

// ReasonNoURL is the PingRecord.Reason set when a ping did NOT transmit because no
// ping URL was resolved (centralized pairing still pending, or the server was
// unreachable when the daemon tried to resolve the URLs). It is a distinct machine
// code -- NOT a raw error string -- so the run-side section can phrase "daemon up but
// not provisioned yet" separately and clearly from a genuine transmit failure. A beat
// carrying this reason still PROVES the daemon is alive (it ran and tried).
const ReasonNoURL = "no_url"

// PingRecord is the outcome of a single ping attempt.
type PingRecord struct {
	// TS is the unix time in SECONDS when the ping was attempted (the caller
	// passes it; this package never reads the clock, which keeps it trivially
	// testable and deterministic).
	TS int64 `json:"ts"`
	// OK is true iff the ping actually transmitted (a 2xx from the monitor).
	OK bool `json:"ok"`
	// Reason is a machine code classifying WHY OK is false, when the cause is a
	// well-known non-transmit condition rather than a monitor-side error. Currently
	// only ReasonNoURL ("no url resolved"); empty otherwise. It lets the section
	// distinguish "not provisioned yet" from a real transmit failure without string
	// matching. Omitted when empty.
	Reason string `json:"reason,omitempty"`
	// Err is the redacted error text when OK is false AND Reason is empty (a genuine
	// transmit failure). The Reporter already strips the ping URL / check UUID from its
	// errors (redactURLErr), so what lands here is safe to persist; omitted when empty.
	Err string `json:"err,omitempty"`
}

// Status is the last-known outcome of each ping kind the daemon sends. Every
// field is a pointer so "never attempted" (nil) is distinguishable from
// "attempted, result recorded". Fields are omitempty so a fresh file carries
// only what has actually happened.
type Status struct {
	// Mode records the healthcheck mode in effect at the last write ("centralized"
	// / "self"); the section uses it to phrase its output.
	Mode        string      `json:"mode,omitempty"`
	Heartbeat   *PingRecord `json:"heartbeat,omitempty"`
	RunStarted  *PingRecord `json:"run_started,omitempty"`
	RunFinished *PingRecord `json:"run_finished,omitempty"`
	RunHang     *PingRecord `json:"run_hang,omitempty"`
	// Update is the last update-check + report-ping outcome. It is a dedicated record
	// (not a bare PingRecord) because the /0-vs-/1 SIGNAL (Available) is orthogonal to
	// whether the ping transmitted (Ping.OK). Nil until the first update check runs;
	// omitempty so an old status file that predates it round-trips unchanged.
	Update *UpdateRecord `json:"update,omitempty"`
}

// UpdateRecord is the outcome of one update check plus the report ping that announced it.
// Ping is the TRANSMISSION outcome (did the /0 or /1 reach the monitor); Available is the
// orthogonal SEMANTIC that chose /0 (up to date) vs /1 (update available, which makes the
// monitor's check go DOWN so alerts fire); Latest is the newest version seen (for display).
// A perfectly transmitted /1 is Ping.OK==true AND Available==true.
type UpdateRecord struct {
	Ping      PingRecord `json:"ping"`
	Available bool       `json:"available"`
	Latest    string     `json:"latest,omitempty"`
}

// StatusPath returns the shared status-file path under the identity directory,
// which both the daemon process and the run process already agree on (same
// convention as identity.NotifySecretPath). Kept a plain Join with no TrimSpace
// so the daemon and the section always resolve the byte-identical path.
func StatusPath(baseDir string) string {
	return filepath.Join(baseDir, "identity", ".healthcheck_status.json")
}

// LoadStatus reads the status file tolerantly. A missing OR empty file is a
// normal "nothing recorded yet" state and yields the zero Status with a nil
// error (mirroring temp_registry.loadEntries' os.IsNotExist / len==0 handling).
// Only malformed JSON is an error, and even then the returned Status is the zero
// value so a caller that ignores the error still renders a safe "no data" state.
func LoadStatus(baseDir string) (Status, error) {
	var st Status
	data, err := os.ReadFile(StatusPath(baseDir))
	if err != nil {
		if os.IsNotExist(err) {
			return Status{}, nil
		}
		return Status{}, fmt.Errorf("read healthcheck status: %w", err)
	}
	if len(data) == 0 {
		return Status{}, nil
	}
	if err := json.Unmarshal(data, &st); err != nil {
		// Return the zero value (not a half-parsed struct) so a tolerant caller
		// treats it as unreadable (a distinct state) rather than trusting garbage.
		return Status{}, fmt.Errorf("parse healthcheck status: %w", err)
	}
	return st, nil
}

// RecordPing performs an atomic read-modify-write of the status file: it loads
// the current Status, replaces the record for kind with the given outcome, sets
// the mode, and writes the result back atomically. ts is the unix SECONDS of the
// attempt; ok is whether it transmitted; pingErr is the (already redacted) error
// on failure. An unknown kind is a programming error and is returned WITHOUT
// touching the file, never silently dropped.
func RecordPing(baseDir, mode, kind string, ts int64, ok bool, pingErr error) error {
	rec := &PingRecord{TS: ts, OK: ok}
	if pingErr != nil {
		// A "no ping URL resolved" error means the daemon was alive and tried but has
		// no endpoint yet (pairing pending / server unreachable). Persist it as a
		// distinct Reason code instead of a raw error string, so the section renders a
		// clear "not provisioned yet" line separate from a real transmit failure.
		if errors.Is(pingErr, ErrNoAliveURL) || errors.Is(pingErr, ErrNoBackupURL) {
			rec.Reason = ReasonNoURL
		} else {
			rec.Err = pingErr.Error()
		}
	}

	// Reject an unknown kind before the write so a caller typo cannot leave the
	// file mutated (mode changed) without the record it asked for.
	st, err := LoadStatus(baseDir)
	if err != nil {
		return err
	}
	switch kind {
	case KindHeartbeat:
		st.Heartbeat = rec
	case KindRunStarted:
		st.RunStarted = rec
	case KindRunFinished:
		st.RunFinished = rec
	case KindRunHang:
		st.RunHang = rec
	default:
		return fmt.Errorf("healthcheck status: unknown ping kind %q", kind)
	}
	st.Mode = mode

	return writeStatus(baseDir, st)
}

// RecordUpdate persists the outcome of one update check + its report ping, atomically,
// next to RecordPing and sharing LoadStatus/writeStatus. ts is the unix SECONDS of the
// attempt; available is whether a newer release was found (the /0-vs-/1 signal); latest
// is the newest version string (for display); ok is whether the report ping transmitted;
// pingErr is the (already redacted) error on failure. A "no updates url resolved" error is
// stored as the distinct ReasonNoURL code (like RecordPing), never a raw string, so the
// section renders "not provisioned yet" instead of a transmit failure.
func RecordUpdate(baseDir, mode string, ts int64, available bool, latest string, ok bool, pingErr error) error {
	rec := &UpdateRecord{
		Ping:      PingRecord{TS: ts, OK: ok},
		Available: available,
		Latest:    latest,
	}
	if pingErr != nil {
		if errors.Is(pingErr, ErrNoUpdatesURL) || errors.Is(pingErr, ErrNoAliveURL) || errors.Is(pingErr, ErrNoBackupURL) {
			rec.Ping.Reason = ReasonNoURL
		} else {
			rec.Ping.Err = pingErr.Error()
		}
	}

	st, err := LoadStatus(baseDir)
	if err != nil {
		return err
	}
	st.Update = rec
	st.Mode = mode

	return writeStatus(baseDir, st)
}

// writeStatus writes st atomically, byte-for-byte the same idiom as
// temp_registry.saveEntries: MkdirAll(dir, 0o750) so the identity dir exists,
// MarshalIndent for a human-readable file, WriteFile to a ".tmp" sibling at 0o600,
// then Rename over the final path so a concurrent reader sees either the old or
// the new file, never a partial one.
func writeStatus(baseDir string, st Status) error {
	path := StatusPath(baseDir)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create healthcheck status dir: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal healthcheck status: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write healthcheck status: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Best-effort cleanup so a failed rename does not leave a stray ".tmp".
		_ = os.Remove(tmp)
		return fmt.Errorf("rename healthcheck status: %w", err)
	}
	return nil
}
