// Package whatsnew owns the "what's new" screen's state model: the atomic
// seen-flag persisted under {base}/identity/.whatsnew_seen.json, the semver
// "unseen" gate, and the version-keyed notes registry. It is deliberately
// stdlib-plus-semver only and logger-free (mirroring internal/health/status.go's
// rationale) so both the daemon and the CLI can import it without dragging in a
// logger. A side-channel, if ever needed, is a settable hook var, not a logger
// dependency.
//
// The whole package fails toward SILENCE: a missing, empty, or malformed flag,
// or a malformed version string, resolves to "do not show" rather than
// "show everything", so a corrupt file can never nag the user forever.
package whatsnew

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// State is the persisted seen-flag. One field: the newest notes version the user
// has acknowledged. omitempty keeps a fresh file minimal, matching the Status tag
// style in internal/health/status.go.
type State struct {
	LastSeenNotesVersion string `json:"last_seen_notes_version,omitempty"`
}

// ErrStateParse marks LoadState's malformed-JSON failure (as opposed to a genuine
// read/permission error). A writer treats a parse error as a corrupt file to
// quarantine and overwrite via loadStateForWrite; every other LoadState error is
// propagated. Matched with errors.Is, mirroring health.ErrStatusParse.
var ErrStateParse = errors.New("whatsnew state parse")

// corruptStateHook, if set, is invoked with the quarantine path whenever
// loadStateForWrite discards an unparseable flag file. It lets a caller that owns
// a logger emit a self-heal debug line WITHOUT this package importing a logger, so
// whatsnew stays logger-free and stdlib-only (the SetCorruptStatusHook pattern from
// internal/health/status.go). Nil by default.
var corruptStateHook func(quarantinedPath string)

// SetCorruptStateHook registers the corrupt-file self-heal callback. Not safe to
// call concurrently with the writers; call once at startup. Tests leave it nil (the
// self-heal still happens, just silently).
func SetCorruptStateHook(fn func(quarantinedPath string)) { corruptStateHook = fn }

// StatePath returns the shared seen-flag path under the identity dir, resolved with
// a plain Join (no TrimSpace) so the installer seed and the dashboard read always
// agree on the byte-identical path, exactly like health.StatusPath.
func StatePath(baseDir string) string {
	return filepath.Join(baseDir, "identity", ".whatsnew_seen.json")
}

// LoadState reads the flag tolerantly, mirroring health.LoadStatus. A missing OR
// empty file is a normal "nothing acknowledged yet" state and yields the zero State
// with present=false and a nil error. present is true ONLY when a non-empty file
// parsed cleanly. Malformed JSON returns the zero State, present=false, and an error
// wrapping ErrStateParse so the gate fails toward silence and a writer can self-heal.
func LoadState(baseDir string) (State, bool, error) {
	var st State
	data, err := os.ReadFile(StatePath(baseDir))
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, false, nil
		}
		return State{}, false, fmt.Errorf("read whatsnew state: %w", err)
	}
	if len(data) == 0 {
		return State{}, false, nil
	}
	if err := json.Unmarshal(data, &st); err != nil {
		// Return the zero value (not a half-parsed struct) so a tolerant caller treats
		// it as unreadable rather than trusting garbage. Wrap ErrStateParse so a writer
		// can tell recoverable corruption from a genuine read/permission error.
		return State{}, false, fmt.Errorf("%w: %w", ErrStateParse, err)
	}
	return st, true, nil
}

// loadStateForWrite loads the flag for a read-modify-write. A parse error means the
// on-disk file is corrupt: the next write must overwrite it, so quarantine the bytes
// once (fixed .corrupt name, best effort, so an operator can recover them) and
// continue from a zero State. Any other LoadState error (a real IO/permission fault;
// missing/empty already return nil) is propagated so the write is not attempted on a
// half-known file. Mirrors health.loadStatusForWrite.
func loadStateForWrite(baseDir string) (State, error) {
	st, _, err := LoadState(baseDir)
	if errors.Is(err, ErrStateParse) {
		quarantine := StatePath(baseDir) + ".corrupt"
		_ = os.Rename(StatePath(baseDir), quarantine) // best-effort; a lost race just self-heals without a sidecar
		if corruptStateHook != nil {
			corruptStateHook(quarantine)
		}
		return State{}, nil
	}
	return st, err
}

// MarkSeen atomically persists last_seen = version, creating identity/ on demand. It
// first self-heals a corrupt file (quarantine to .corrupt, continue from zero) so a
// garbage flag never blocks acknowledgement. The write is the writeJSONAtomic idiom
// from internal/health/status.go byte-for-byte (MkdirAll 0o750 -> MarshalIndent ->
// WriteFile ".tmp" 0o600 -> Rename, remove tmp on rename failure).
func MarkSeen(baseDir, version string) error {
	st, err := loadStateForWrite(baseDir)
	if err != nil {
		return err
	}
	st.LastSeenNotesVersion = version
	return writeJSONAtomic(StatePath(baseDir), st)
}

// writeJSONAtomic writes v as indented JSON to path atomically, byte-for-byte the same
// idiom as internal/health/status.go writeJSONAtomic: MkdirAll(dir, 0o750) so the
// parent dir exists, MarshalIndent for a human-readable file, WriteFile to a ".tmp"
// sibling at 0o600, then Rename over the final path so a concurrent reader sees either
// the old or the new file, never a partial one. The identity dir must NOT set the
// immutable +i attribute (which would block every rewrite).
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
