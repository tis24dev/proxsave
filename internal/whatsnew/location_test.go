package whatsnew

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/safefs"
)

// writeLegacyFlag writes the flag where releases before 0.41.0 kept it.
func writeLegacyFlag(t *testing.T, baseDir, version string) string {
	t.Helper()
	path := LegacyStatePath(baseDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir identity: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"last_seen_notes_version":"`+version+`"}`), 0o600); err != nil {
		t.Fatalf("write legacy flag: %v", err)
	}
	return path
}

// The first read on an upgraded host finds no flag in LOG_PATH and the old one in identity/: it
// moves it (LOG_PATH created on demand, possibly another filesystem) and decides on it. Without
// the move the host would read "never acknowledged" and get every note since 0.0.0.
func TestLoadStateMovesTheFlagFromIdentity(t *testing.T) {
	baseDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "log")
	legacy := writeLegacyFlag(t, baseDir, "0.40.0")
	loc := Location{LogPath: logPath, BaseDir: baseDir}

	if show, ver, err := ShouldWarn(loc, "0.40.0"); show || ver != "" || err != nil {
		t.Fatalf("ShouldWarn(0.40.0) after the move = (%v, %q, %v), want silent: 0.40.0 was acknowledged in identity/", show, ver, err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("identity/.whatsnew_seen.json still there after the move (stat err = %v)", err)
	}
	st, present, err := LoadState(loc)
	if err != nil || !present || st.LastSeenNotesVersion != "0.40.0" {
		t.Fatalf("LoadState from LOG_PATH = (%+v, %v, %v), want 0.40.0 present", st, present, err)
	}
	fi, err := os.Stat(StatePath(logPath))
	if err != nil {
		t.Fatalf("flag not in LOG_PATH: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("moved flag mode = %o, want 0600", got)
	}
	if show, _, err := Decide(loc, "0.41.0"); !show || err != nil {
		t.Fatalf("Decide(0.41.0) = (%v, _, %v), want show: the move must not mark newer notes seen", show, err)
	}
}

// LOG_PATH already holding a flag wins: identity/ is not read, and its copy is removed, the
// same rule the daemon applies to its state files (identity/ holds identity only).
func TestLoadStatePrefersTheFlagInLogPath(t *testing.T) {
	baseDir := t.TempDir()
	logPath := t.TempDir()
	legacy := writeLegacyFlag(t, baseDir, "0.10.0")
	loc := Location{LogPath: logPath, BaseDir: baseDir}
	if err := MarkSeen(Location{LogPath: logPath}, "0.40.0"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}

	st, present, err := LoadState(loc)
	if err != nil || !present || st.LastSeenNotesVersion != "0.40.0" {
		t.Fatalf("LoadState = (%+v, %v, %v), want the LOG_PATH flag 0.40.0", st, present, err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("identity/ flag still there although LOG_PATH had one (stat err = %v)", err)
	}
}

// A ReadOnly Location (--dry-run) never moves the flag: a read that needs the move fails, so the
// gate stays silent, and MarkSeen writes nothing.
func TestReadOnlyLocationNeverMovesOrWrites(t *testing.T) {
	baseDir := t.TempDir()
	logPath := t.TempDir()
	legacy := writeLegacyFlag(t, baseDir, "0.10.0")
	loc := Location{LogPath: logPath, BaseDir: baseDir, ReadOnly: true}

	show, ver, err := ShouldWarn(loc, "0.40.0")
	if show || ver != "" || !errors.Is(err, ErrLegacyStatePending) {
		t.Fatalf("ShouldWarn read-only = (%v, %q, %v), want silent with ErrLegacyStatePending", show, ver, err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("read-only check moved the identity/ flag: %v", err)
	}
	if _, err := os.Stat(StatePath(logPath)); !os.IsNotExist(err) {
		t.Fatalf("read-only check wrote LOG_PATH (stat err = %v)", err)
	}
	if err := MarkSeen(loc, "0.40.0"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("MarkSeen read-only err = %v, want ErrReadOnly", err)
	}
	if _, err := os.Stat(StatePath(logPath)); !os.IsNotExist(err) {
		t.Fatalf("read-only MarkSeen wrote LOG_PATH (stat err = %v)", err)
	}
}

// No LOG_PATH (no readable configuration): no screen, no nudge, no write anywhere.
func TestNoLogPathIsSilent(t *testing.T) {
	baseDir := t.TempDir()
	loc := Location{BaseDir: baseDir}

	if show, body, err := Decide(loc, "0.40.0"); show || body != "" || !errors.Is(err, ErrNoLogPath) {
		t.Fatalf("Decide without LOG_PATH = (%v, %q, %v), want silent with ErrNoLogPath", show, body, err)
	}
	if show, ver, err := ShouldWarn(loc, "0.40.0"); show || ver != "" || !errors.Is(err, ErrNoLogPath) {
		t.Fatalf("ShouldWarn without LOG_PATH = (%v, %q, %v), want silent with ErrNoLogPath", show, ver, err)
	}
	if err := MarkSeen(loc, "0.40.0"); !errors.Is(err, ErrNoLogPath) {
		t.Fatalf("MarkSeen without LOG_PATH err = %v, want ErrNoLogPath", err)
	}
	entries, err := os.ReadDir(baseDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("BASE_DIR changed without LOG_PATH: entries=%v err=%v", entries, err)
	}
}

// hungFlag puts a FIFO where the flag is read: opening it for reading blocks until a writer
// shows up, the way a read on a dead mount never returns. Cleanup opens the write end so the
// abandoned readers finish.
func hungFlag(t *testing.T, logPath string) {
	t.Helper()
	path := StatePath(logPath)
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 20; i++ {
			f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				return // no reader left waiting
			}
			_ = f.Close()
			time.Sleep(10 * time.Millisecond)
		}
	})
}

// A read of the flag that outlives FS_IO_TIMEOUT fails toward silence: no screen, no nudge,
// no write, and the caller gets its answer within the bound instead of hanging.
func TestHungLogPathFailsTowardSilence(t *testing.T) {
	logPath := t.TempDir()
	hungFlag(t, logPath)
	loc := Location{LogPath: logPath, BaseDir: t.TempDir(), Timeout: 100 * time.Millisecond}

	start := time.Now()
	show, body, err := Decide(loc, "0.40.0")
	if show || body != "" || !errors.Is(err, safefs.ErrTimeout) {
		t.Fatalf("Decide on a hung flag = (%v, %q, %v), want silent with a timeout", show, body, err)
	}
	if show, ver, err := ShouldWarn(loc, "0.40.0"); show || ver != "" || !errors.Is(err, safefs.ErrTimeout) {
		t.Fatalf("ShouldWarn on a hung flag = (%v, %q, %v), want silent with a timeout", show, ver, err)
	}
	if err := MarkSeen(loc, "0.40.0"); !errors.Is(err, safefs.ErrTimeout) {
		t.Fatalf("MarkSeen on a hung flag err = %v, want a timeout (nothing written)", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("three bounded calls took %s, want each bounded by the 100ms timeout", elapsed)
	}
}
