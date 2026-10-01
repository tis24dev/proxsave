// Package whatsnew owns the "what's new" screen's state model: the atomic
// seen-flag persisted under <LOG_PATH>/.whatsnew_seen.json, the semver "unseen"
// gate, and the version-keyed notes registry. It is deliberately
// stdlib-plus-semver only (plus internal/safefs, itself stdlib-only, which bounds
// every filesystem operation on the flag) and logger-free (mirroring
// internal/health/status.go's rationale) so both the daemon and the CLI can import
// it without dragging in a logger. A side-channel, if ever needed, is a settable
// hook var, not a logger dependency.
//
// The whole package fails toward SILENCE: a missing, empty, or malformed flag,
// a malformed version string, an unknown LOG_PATH, or a filesystem operation that
// errors or outlives FS_IO_TIMEOUT resolves to "do not show" rather than "show
// everything", so a corrupt file or a dead mount can never nag the user forever or
// hang the dashboard.
package whatsnew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/tis24dev/proxsave/internal/safefs"
)

// State is the persisted seen-flag. One field: the newest notes version the user
// has acknowledged. omitempty keeps a fresh file minimal, matching the Status tag
// style in internal/health/status.go.
type State struct {
	LastSeenNotesVersion string `json:"last_seen_notes_version,omitempty"`
}

// Location says where the seen-flag lives and how long one filesystem operation on it
// may take. Callers build it from the loaded configuration.
type Location struct {
	// LogPath is the configured LOG_PATH; the flag is LogPath/.whatsnew_seen.json. Empty
	// means no configuration could be read: every read then fails (ErrNoLogPath), so the
	// gate stays silent, and nothing is written.
	LogPath string
	// BaseDir is BASE_DIR, where releases before 0.41.0 kept the flag
	// (identity/.whatsnew_seen.json). The first read moves that file to LOG_PATH.
	BaseDir string
	// Timeout bounds every filesystem operation on the flag (FS_IO_TIMEOUT), so a dead
	// LOG_PATH mount cannot hang the caller. Zero or negative means unbounded, the
	// FS_IO_TIMEOUT=0 opt-out.
	Timeout time.Duration
	// ReadOnly forbids every write: no move from identity/, no MarkSeen. A read that would
	// have to move the old flag first fails instead (ErrLegacyStatePending), so the gate
	// stays silent. Set for --dry-run, which must not touch the filesystem.
	ReadOnly bool
}

// ErrStateParse marks LoadState's UNUSABLE-content failure (as opposed to a genuine
// read/permission error): malformed JSON, OR a syntactically valid flag whose stored
// last_seen version is not valid semver (a garbage or empty string). Both are unusable by
// the semver gate and would otherwise silence the feature forever, so a writer treats them
// as a corrupt file to quarantine and overwrite via loadStateForWrite; every other
// LoadState error is propagated. Matched with errors.Is, mirroring health.ErrStatusParse.
var ErrStateParse = errors.New("whatsnew state parse")

// ErrNoLogPath is returned when the Location carries no LOG_PATH: without a readable
// configuration the flag's location is unknown, so nothing is read or written.
var ErrNoLogPath = errors.New("whatsnew state: LOG_PATH unknown")

// ErrLegacyStatePending is returned by a ReadOnly read that finds the flag only in
// identity/, where it must be moved from before anything can be decided.
var ErrLegacyStatePending = errors.New("whatsnew state: flag still in identity/, not moved on a read-only check")

// ErrReadOnly is returned by MarkSeen on a ReadOnly Location.
var ErrReadOnly = errors.New("whatsnew state: read-only, not written")

// stateFileName is the flag's name, the same in LOG_PATH and in the old identity/ location.
const stateFileName = ".whatsnew_seen.json"

// logDirPerm is the mode LOG_PATH is created with when the flag is the first thing written
// there: the same 0755 the run creates LOG_PATH with and the security check expects.
const logDirPerm os.FileMode = 0o755

// fsCtx is the context of every bounded operation: the bound is Location.Timeout, and the
// callers (dashboard, install seed, run nudge) have no cancellation to pass that the
// timeout does not already cover.
var fsCtx = context.Background()

// corruptStateHook, if set, is invoked with the quarantine path whenever
// loadStateForWrite discards an unparseable flag file. It lets a caller that owns
// a logger emit a self-heal debug line WITHOUT this package importing a logger, so
// whatsnew stays logger-free (the SetCorruptStatusHook pattern from
// internal/health/status.go). Nil by default.
var corruptStateHook func(quarantinedPath string)

// SetCorruptStateHook registers the corrupt-file self-heal callback. Not safe to
// call concurrently with the writers; call once at startup. Tests leave it nil (the
// self-heal still happens, just silently).
func SetCorruptStateHook(fn func(quarantinedPath string)) { corruptStateHook = fn }

// StatePath returns the seen-flag path under LOG_PATH, resolved with a plain Join (no
// TrimSpace) so the installer seed, the dashboard and the run nudge always agree on the
// byte-identical path, exactly like health.StatusPath.
func StatePath(logPath string) string {
	return filepath.Join(logPath, stateFileName)
}

// LegacyStatePath returns identity/.whatsnew_seen.json, where releases before 0.41.0 kept
// the flag. Only the one-time move in LoadState reads it.
func LegacyStatePath(baseDir string) string {
	return filepath.Join(baseDir, "identity", stateFileName)
}

// LoadState reads the flag tolerantly, mirroring health.LoadStatus. A missing OR
// empty file is a normal "nothing acknowledged yet" state and yields the zero State
// with present=false and a nil error. present is true ONLY when a non-empty file
// parsed cleanly AND carries a valid-semver version. Malformed JSON, or a valid flag
// whose stored version is not valid semver (garbage or empty), returns the zero State,
// present=false, and an error wrapping ErrStateParse so the gate fails toward silence and
// a writer can self-heal.
//
// When the flag is missing in LOG_PATH and identity/ still holds the one an older release
// wrote, it is moved to LOG_PATH first (adoptLegacyState), so an upgraded host keeps its
// acknowledgement. Every filesystem operation is bounded by loc.Timeout; a timeout, an
// unknown LOG_PATH or a failed move is returned as an error, which the gate turns into
// silence.
func LoadState(loc Location) (State, bool, error) {
	if strings.TrimSpace(loc.LogPath) == "" {
		return State{}, false, ErrNoLogPath
	}
	if err := adoptLegacyState(loc); err != nil {
		return State{}, false, err
	}
	var st State
	data, err := readFileBounded(StatePath(loc.LogPath), loc.Timeout)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
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
	if _, verr := semver.NewVersion(st.LastSeenNotesVersion); verr != nil {
		// A syntactically valid JSON flag whose stored version is not valid semver (garbage
		// or empty) is unusable: the semver gate can never compare it, so left as-is it would
		// silence the feature forever. Treat it as corrupt (ErrStateParse) so a writer
		// self-heals it, exactly like malformed JSON. MarkSeen only ever writes a non-empty
		// semver, so this never rejects a flag the app itself produced.
		return State{}, false, fmt.Errorf("%w: invalid last_seen version %q: %w", ErrStateParse, st.LastSeenNotesVersion, verr)
	}
	return st, true, nil
}

// adoptLegacyState moves identity/.whatsnew_seen.json to LOG_PATH when LOG_PATH has no flag
// yet. Without it every upgraded host would read "never acknowledged" and get every note
// since 0.0.0. LOG_PATH may be another filesystem, so the move is a copy, atomic for the
// destination (temporary sibling, then rename), followed by a remove of the old file.
//
// Any failure before the copy lands is returned, so the caller stays silent instead of
// deciding without the acknowledgement the old file holds. Once the copy has landed the
// decision goes ahead on it even if removing the old file fails; that file is then left in
// identity/.
func adoptLegacyState(loc Location) error {
	if strings.TrimSpace(loc.BaseDir) == "" {
		return nil
	}
	dst := StatePath(loc.LogPath)
	if _, err := safefs.Lstat(fsCtx, dst, loc.Timeout); err == nil {
		// LOG_PATH already holds the flag: it wins, and a copy left in identity/ goes, the
		// same rule the daemon applies to its own state files (identity/ holds identity only).
		if !loc.ReadOnly {
			_ = safefs.Remove(fsCtx, LegacyStatePath(loc.BaseDir), loc.Timeout) // best-effort; absent is fine
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat whatsnew state: %w", err)
	}
	legacy := LegacyStatePath(loc.BaseDir)
	data, err := readFileBounded(legacy, loc.Timeout)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read whatsnew state in identity/: %w", err)
	}
	if loc.ReadOnly {
		return ErrLegacyStatePending
	}
	if err := writeFileAtomic(dst, data, loc.Timeout); err != nil {
		return fmt.Errorf("move whatsnew state from identity/: %w", err)
	}
	_ = safefs.Remove(fsCtx, legacy, loc.Timeout) // best-effort: the copy above already decides
	return nil
}

// loadStateForWrite loads the flag for a read-modify-write. A parse error means the
// on-disk file is corrupt: the next write must overwrite it, so quarantine the bytes
// once (fixed .corrupt name, best effort, so an operator can recover them) and
// continue from a zero State. Any other LoadState error (a real IO/permission fault or
// a timeout; missing/empty already return nil) is propagated so the write is not
// attempted on a half-known file. Mirrors health.loadStatusForWrite.
func loadStateForWrite(loc Location) (State, error) {
	st, _, err := LoadState(loc)
	if errors.Is(err, ErrStateParse) {
		path := StatePath(loc.LogPath)
		quarantine := path + ".corrupt"
		_ = safefs.Rename(fsCtx, path, quarantine, loc.Timeout) // best-effort; a lost race just self-heals without a sidecar
		if corruptStateHook != nil {
			corruptStateHook(quarantine)
		}
		return State{}, nil
	}
	return st, err
}

// MarkSeen atomically persists last_seen = version under LOG_PATH, creating LOG_PATH on
// demand. It refuses a non-semver (or empty) version so the flag it writes is always
// readable by LoadState, and it writes nothing on a Location without LOG_PATH or a
// ReadOnly one. It then self-heals a corrupt file (quarantine to .corrupt, continue from
// zero) so a garbage flag never blocks acknowledgement. Every operation is bounded by
// loc.Timeout.
func MarkSeen(loc Location, version string) error {
	// Persist only a valid-semver version, so the write side agrees with LoadState's read
	// side: writing a non-semver (or empty) version would create a flag LoadState rejects as
	// corrupt, so a self-heal that re-seeds a non-semver toolVersion (a dev/make build not
	// caught by IsDevBuild) would churn on every run. All callers are best-effort (the seed
	// and the two wirings discard the error), so refusing here just skips the useless write.
	if _, err := semver.NewVersion(version); err != nil {
		return fmt.Errorf("refusing to persist non-semver whatsnew version %q: %w", version, err)
	}
	if strings.TrimSpace(loc.LogPath) == "" {
		return ErrNoLogPath
	}
	if loc.ReadOnly {
		return ErrReadOnly
	}
	st, err := loadStateForWrite(loc)
	if err != nil {
		return err
	}
	st.LastSeenNotesVersion = version
	return writeJSONAtomic(StatePath(loc.LogPath), st, loc.Timeout)
}

// readFileBounded reads path through an os.Root on its parent directory
// (safefs.ReadFileUnderRoot: a final component that is an absolute or escaping symlink is
// refused), with the read bounded by timeout.
func readFileBounded(path string, timeout time.Duration) ([]byte, error) {
	return safefs.Run(fsCtx, "read", path, timeout, func() ([]byte, error) {
		return safefs.ReadFileUnderRoot(path)
	})
}

// writeJSONAtomic marshals v as indented JSON (a human-readable file) and writes it with
// writeFileAtomic.
func writeJSONAtomic(path string, v any, timeout time.Duration) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filepath.Base(path), err)
	}
	return writeFileAtomic(path, data, timeout)
}

// writeFileAtomic writes data to path atomically, the idiom of
// internal/health/status.go: MkdirAll the parent (logDirPerm, as the run creates LOG_PATH),
// WriteFile a ".tmp" sibling at 0o600, then Rename over the final path so a concurrent
// reader sees either the old or the new file, never a partial one. The directory creation
// and the write+rename are each bounded by timeout; on a timeout the worker is abandoned and
// a stray ".tmp" may stay behind.
func writeFileAtomic(path string, data []byte, timeout time.Duration) error {
	dir := filepath.Dir(path)
	if err := safefs.MkdirAll(fsCtx, dir, logDirPerm, timeout); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	_, err := safefs.Run(fsCtx, "write", path, timeout, func() (struct{}, error) {
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return struct{}{}, fmt.Errorf("write %s: %w", filepath.Base(path), err)
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp) // best-effort cleanup so a failed rename leaves no stray ".tmp"
			return struct{}{}, fmt.Errorf("rename %s: %w", filepath.Base(path), err)
		}
		return struct{}{}, nil
	})
	return err
}
