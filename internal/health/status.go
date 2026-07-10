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
	// Down encodes the /1-vs-/0 SIGNAL for checks whose ping suffix carries a semantic
	// (a per-notification-channel check pings /1 when the send failed, so the monitor goes
	// DOWN/red, exactly like the updates sensor's /1). It is orthogonal to OK (which is only
	// whether the ping transmitted): a perfectly transmitted /1 is OK==true AND Down==true.
	// Unused (false) for the fixed alive/backup kinds, whose severity is the monitor's job.
	// Omitted when false so old readers see byte-identical records on a downgrade.
	Down bool `json:"down,omitempty"`
}

// Status is the last-known outcome of each ping the daemon sends, keyed by KIND in a
// dynamic map so a variable set of per-notification-channel checks (notify-<ch>) fits
// alongside the fixed heartbeat/start/finish/hang kinds. A missing key means "never
// attempted"; omitempty keeps a fresh file minimal.
type Status struct {
	// Mode records the healthcheck mode in effect at the last write ("centralized"
	// / "self"); the section uses it to phrase its output.
	Mode string `json:"mode,omitempty"`
	// Records maps a ping KIND (KindHeartbeat/KindRunStarted="start"/KindRunFinished=
	// "finish"/KindRunHang="hang", or "notify-<ch>") to its last outcome. See the custom
	// UnmarshalJSON below for the legacy-format migration.
	Records map[string]*PingRecord `json:"records,omitempty"`
	// Update is the last update-check + report-ping outcome. It is a dedicated record
	// (not a bare PingRecord) because the /0-vs-/1 SIGNAL (Available) is orthogonal to
	// whether the ping transmitted (Ping.OK). Nil until the first update check runs;
	// omitempty so an old status file that predates it round-trips unchanged.
	Update *UpdateRecord `json:"update,omitempty"`
}

// Record returns the last PingRecord for kind, or nil if never attempted. Nil-safe on a
// zero Status (nil map read yields nil).
func (s Status) Record(kind string) *PingRecord { return s.Records[kind] }

// UnmarshalJSON migrates the pre-Fase-2 status file in place: that format stored the
// fixed kinds as top-level keys "heartbeat"/"run_started"/"run_finished"/"run_hang"
// (note the json tag "run_started" mapped to Kind "start", etc.). We decode BOTH the new
// "records" map AND those legacy keys, then fold each legacy record into Records under its
// KIND, but only if the new map does not already carry it (new format wins). This is
// load-bearing: an in-place daemon upgrade whose FIRST write is a heartbeat read-modify-
// write would otherwise drop the last backup outcome + update verdict permanently.
func (s *Status) UnmarshalJSON(data []byte) error {
	type statusAlias Status // shed the custom UnmarshalJSON to avoid infinite recursion
	var aux struct {
		statusAlias
		LegacyHeartbeat   *PingRecord `json:"heartbeat"`
		LegacyRunStarted  *PingRecord `json:"run_started"`
		LegacyRunFinished *PingRecord `json:"run_finished"`
		LegacyRunHang     *PingRecord `json:"run_hang"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*s = Status(aux.statusAlias)
	if s.Records == nil {
		s.Records = make(map[string]*PingRecord)
	}
	fold := func(kind string, rec *PingRecord) {
		if rec == nil {
			return
		}
		if _, ok := s.Records[kind]; !ok {
			s.Records[kind] = rec
		}
	}
	fold(KindHeartbeat, aux.LegacyHeartbeat)
	fold(KindRunStarted, aux.LegacyRunStarted)
	fold(KindRunFinished, aux.LegacyRunFinished)
	fold(KindRunHang, aux.LegacyRunHang)
	if len(s.Records) == 0 { // keep a fresh/empty Status marshaling clean (omitempty)
		s.Records = nil
	}
	return nil
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
		// Wrap ErrStatusParse so a writer can tell a recoverable corruption (which
		// the next write must overwrite) from a genuine read/permission error.
		return Status{}, fmt.Errorf("%w: %w", ErrStatusParse, err)
	}
	return st, nil
}

// ErrStatusParse marks LoadStatus's malformed-JSON failure (as opposed to a
// genuine read/permission error). A writer treats a parse error as a corrupt file
// to overwrite via loadStatusForWrite; every other LoadStatus error is propagated.
var ErrStatusParse = errors.New("parse healthcheck status")

// corruptStatusHook, if set, is invoked with the quarantine path whenever
// loadStatusForWrite discards an unparseable status file. It lets the daemon (the
// sole writer, which owns a logger) emit a debug line for the self-heal WITHOUT
// this package importing a logger, so health stays logger-free and stdlib-only.
// Nil by default; set once at startup (SetCorruptStatusHook).
var corruptStatusHook func(quarantinedPath string)

// SetCorruptStatusHook registers the corrupt-file self-heal callback. The daemon
// wires it to a debug log line at startup; tests and the orchestrator leave it nil
// (the self-heal still happens, just silently). Not safe to call concurrently with
// the writers; call once before the daemon loops start.
func SetCorruptStatusHook(fn func(quarantinedPath string)) { corruptStatusHook = fn }

// loadStatusForWrite loads the status for a read-modify-write. A parse error means
// the on-disk file is corrupt: the next write must overwrite it, so quarantine the
// bytes once (fixed name, best effort, so an operator/support bundle can recover
// them) and continue from a zero Status. Any other LoadStatus error (a real IO or
// permission fault; missing/empty already return nil) is propagated so the write is
// not attempted on a half-known file and the reader-side "status file unreadable"
// signal is preserved.
func loadStatusForWrite(baseDir string) (Status, error) {
	st, err := LoadStatus(baseDir)
	if errors.Is(err, ErrStatusParse) {
		quarantine := StatusPath(baseDir) + ".corrupt"
		_ = os.Rename(StatusPath(baseDir), quarantine) // best-effort; a lost race just self-heals without a sidecar
		if corruptStatusHook != nil {
			corruptStatusHook(quarantine)
		}
		return Status{}, nil
	}
	return st, err
}

// RecordPing performs an atomic read-modify-write of the status file: it loads
// the current Status, replaces the record for kind with the given outcome, sets
// the mode, and writes the result back atomically. ts is the unix SECONDS of the
// attempt; ok is whether it transmitted; pingErr is the (already redacted) error
// on failure. An unknown kind is a programming error and is returned WITHOUT
// touching the file, never silently dropped.
func RecordPing(baseDir, mode, kind string, ts int64, ok bool, pingErr error) error {
	// Reject an empty kind (a caller bug) before the write so it cannot leave the file
	// mutated (mode changed) without a record. Any non-empty kind is now valid: the map
	// stores the fixed heartbeat/start/finish/hang AND dynamic notify-<ch> kinds.
	if kind == "" {
		return fmt.Errorf("healthcheck status: empty ping kind")
	}
	rec := &PingRecord{TS: ts, OK: ok}
	if pingErr != nil {
		// A "no ping URL resolved" error means the daemon was alive and tried but has
		// no endpoint yet (pairing pending / server unreachable). Persist it as a
		// distinct Reason code instead of a raw error string, so the section renders a
		// clear "not provisioned yet" line separate from a real transmit failure.
		if IsNoURLErr(pingErr) {
			rec.Reason = ReasonNoURL
		} else {
			rec.Err = pingErr.Error()
		}
	}

	st, err := loadStatusForWrite(baseDir)
	if err != nil {
		return err
	}
	if st.Records == nil {
		st.Records = make(map[string]*PingRecord)
	}
	st.Records[kind] = rec
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
		if IsNoURLErr(pingErr) {
			rec.Ping.Reason = ReasonNoURL
		} else {
			rec.Ping.Err = pingErr.Error()
		}
	}

	st, err := loadStatusForWrite(baseDir)
	if err != nil {
		return err
	}
	st.Update = rec
	st.Mode = mode

	return writeStatus(baseDir, st)
}

// RecordNotifyPing persists one per-notification-channel ping outcome into Records[kind]
// (kind = "notify-<ch>"), atomically, next to RecordPing and sharing LoadStatus/writeStatus.
// down is the /1 SIGNAL (the channel's send failed/degraded, so the monitor check goes DOWN);
// ok is whether the ping transmitted. A no-url error is stored as the distinct ReasonNoURL
// code. It is a separate writer from RecordPing so the fixed-kind callers keep their signature
// and only the notify path carries Down.
func RecordNotifyPing(baseDir, mode, kind string, ts int64, ok, down bool, pingErr error) error {
	if kind == "" {
		return fmt.Errorf("healthcheck status: empty notify kind")
	}
	rec := &PingRecord{TS: ts, OK: ok, Down: down}
	if pingErr != nil {
		if IsNoURLErr(pingErr) {
			rec.Reason = ReasonNoURL
		} else {
			rec.Err = pingErr.Error()
		}
	}
	st, err := loadStatusForWrite(baseDir)
	if err != nil {
		return err
	}
	if st.Records == nil {
		st.Records = make(map[string]*PingRecord)
	}
	st.Records[kind] = rec
	st.Mode = mode
	return writeStatus(baseDir, st)
}

// writeStatus writes st atomically to the shared status file.
func writeStatus(baseDir string, st Status) error {
	return writeJSONAtomic(StatusPath(baseDir), st)
}

// writeJSONAtomic writes v as indented JSON to path atomically, byte-for-byte the same
// idiom as temp_registry.saveEntries: MkdirAll(dir, 0o750) so the parent dir exists,
// MarshalIndent for a human-readable file, WriteFile to a ".tmp" sibling at 0o600, then
// Rename over the final path so a concurrent reader sees either the old or the new file,
// never a partial one. Shared by the status file and the per-run notify-results file, both
// of which live in the identity dir and must NOT set the immutable +i attribute (which
// would block every rewrite).
func writeJSONAtomic(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filepath.Base(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup so a failed rename leaves no stray ".tmp"
		return fmt.Errorf("rename %s: %w", filepath.Base(path), err)
	}
	return nil
}
