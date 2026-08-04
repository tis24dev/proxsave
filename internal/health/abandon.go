// abandon.go records that the daemon gave up on a backup child the kernel would not let it
// reap (a task parked in TASK_UNINTERRUPTIBLE behind a dead NFS/CIFS mount or a wedged
// device). The daemon EXITS on that path and systemd restarts it seconds later, so the fact
// has to outlive the process: the restarted daemon must keep reporting the service-alive
// check DOWN instead of sending a success heartbeat that flips it green (and fires a
// "recovered" alert) while the orphan still holds the backup lock and no backup can run.
//
// It is a sibling of the pid/status files in the identity dir, written with the same atomic
// rename idiom, and stays logging-free + stdlib-only like them.

package health

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// AbandonRecord is what the abandoning daemon leaves behind for its successor. PID is the
// orphan's pid -- the one an operator has to hunt for in D state -- and RID/TS identify the
// run it belonged to, so the degrade the next daemon reports can name its cause instead of
// being an unexplained failure.
type AbandonRecord struct {
	// PID is the abandoned child's pid (0 when the process was never published).
	PID int `json:"pid"`
	// Start is the orphan's start time in clock ticks since boot (/proc/<pid>/stat field 22),
	// or 0 when it could not be read. It is the IDENTITY half of the pid, and without it the
	// pid is not an identifier at all: the kernel recycles pid numbers WITHIN a boot, so a
	// successor that only asked "is pid N alive" would keep the service-alive check DOWN
	// forever the moment any unrelated long-lived process inherited the number -- a false RED
	// that no run, no restart and no reboot could lift. The pair (pid, starttime) is unique
	// for the life of a boot, and being a tick count rather than a wall-clock stamp it is
	// immune to clock steps as well.
	Start uint64 `json:"start,omitempty"`
	// RID is the run id of the abandoned run, matching the /fail already sent on the
	// backup-outcome check.
	RID string `json:"rid,omitempty"`
	// TS is the unix time in SECONDS of the abandon (the caller passes it; this package
	// never reads the clock, like its siblings).
	TS int64 `json:"ts"`
}

// AbandonPath returns the abandoned-child marker path, a sibling of the status and pid files
// in the identity dir (same convention as StatusPath / DaemonPIDPath).
func AbandonPath(baseDir string) string {
	return filepath.Join(baseDir, "identity", ".daemon_abandoned.json")
}

// WriteAbandon persists the marker atomically: MkdirAll the identity dir, WriteFile a ".tmp"
// sibling at 0o600, then Rename over the final path so a reader sees either the old or the
// new file, never a partial one.
func WriteAbandon(baseDir string, rec AbandonRecord) error {
	path := AbandonPath(baseDir)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal abandon marker: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write abandon marker: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup so a failed rename leaves no stray ".tmp"
		return fmt.Errorf("rename abandon marker: %w", err)
	}
	return nil
}

// ReadAbandon returns the marker, or (nil, nil) when there is none -- the normal state. A
// present-but-unreadable marker is NOT an error either: its mere existence is the signal, so
// a corrupt file still yields a zero-valued record (pid 0, no rid) rather than being mistaken
// for "no abandon happened". Only a genuine read fault is returned.
func ReadAbandon(baseDir string) (*AbandonRecord, error) {
	data, err := os.ReadFile(AbandonPath(baseDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read abandon marker: %w", err)
	}
	var rec AbandonRecord
	if len(data) > 0 {
		_ = json.Unmarshal(data, &rec) // presence is the signal; unparseable contents degrade to the zero record
	}
	return &rec, nil
}

// ClearAbandon removes the marker, lifting the degrade. A missing file is not an error, so
// this is idempotent.
func ClearAbandon(baseDir string) error {
	if err := os.Remove(AbandonPath(baseDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove abandon marker: %w", err)
	}
	return nil
}
