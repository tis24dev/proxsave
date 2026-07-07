// daemon_info.go records the running daemon's IDENTITY (pid, the binary it booted from, its version/
// commit, and start time) in a COMPANION file next to the pid file. It is deliberately separate from
// .daemon.pid, which is the SIGUSR1 handoff contract (a bare pid a standalone run reads to signal
// us); overloading that file would risk that contract. This record instead answers "is the running
// daemon still aligned with the binary on disk?" -- an in-place upgrade replaces the file without
// restarting the process, so the daemon captures the binary's identity ONCE at startup here, and a
// later reader re-hashes the on-disk file and compares (see daemon_state.go). It is a sibling of the
// status/pid files in the identity dir, written with the same atomic idiom (writeJSONAtomic) and
// read tolerantly (like LoadStatus), and stays logging-free + stdlib-only like its siblings.

package health

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// DaemonInfo is the identity record the daemon writes at startup. Binary is the identity of the
// executable the daemon is running FROM (captured once at startup); a reader compares it against a
// fresh ComputeBinaryIdentity of the same path to detect a behind/stale daemon.
type DaemonInfo struct {
	PID      int            `json:"pid"`
	ExecPath string         `json:"exec_path"`
	Binary   BinaryIdentity `json:"binary"`
	Version  string         `json:"version"`
	Commit   string         `json:"commit"`
	StartTS  int64          `json:"start_ts"`
}

// DaemonInfoPath returns the daemon-info file path, a sibling of the pid/status files in the
// identity dir (same same-uid, non-immutable rationale as DaemonPIDPath).
func DaemonInfoPath(baseDir string) string {
	return filepath.Join(baseDir, "identity", ".daemon_info.json")
}

// WriteDaemonInfo writes info as indented JSON atomically (daemon side), reusing the shared
// writeJSONAtomic idiom (MkdirAll -> WriteFile ".tmp" 0o600 -> Rename) so a concurrent reader sees
// either the old or the new file, never a partial one, and no immutable +i attribute blocks the
// rewrite.
func WriteDaemonInfo(baseDir string, info DaemonInfo) error {
	return writeJSONAtomic(DaemonInfoPath(baseDir), info)
}

// ReadDaemonInfo reads the daemon-info file tolerantly, mirroring LoadStatus: a missing OR empty
// file is the normal "no daemon recorded" state and yields (zero, false, nil). Only malformed JSON
// is an error, and even then the returned DaemonInfo is the zero value and found is false, so a
// caller that ignores the error still treats it as "no record".
func ReadDaemonInfo(baseDir string) (DaemonInfo, bool, error) {
	data, err := os.ReadFile(DaemonInfoPath(baseDir))
	if err != nil {
		if os.IsNotExist(err) {
			return DaemonInfo{}, false, nil
		}
		return DaemonInfo{}, false, fmt.Errorf("read daemon info: %w", err)
	}
	if len(data) == 0 {
		return DaemonInfo{}, false, nil
	}
	var info DaemonInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return DaemonInfo{}, false, fmt.Errorf("parse daemon info: %w", err)
	}
	return info, true, nil
}

// RemoveDaemonInfo deletes the daemon-info file (daemon side, on shutdown). A missing file is not an
// error, mirroring RemoveDaemonPID.
func RemoveDaemonInfo(baseDir string) error {
	if err := os.Remove(DaemonInfoPath(baseDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove daemon info: %w", err)
	}
	return nil
}
