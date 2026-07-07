// daemon_pid.go records the resident daemon's PID so a STANDALONE backup run can find the daemon
// to wake (SIGUSR1) for the manual-outcome handoff. The daemon writes it at startup and removes
// it on shutdown; a standalone run reads it, verifies the pid is a LIVE proxsave --daemon process
// (see the run side), and only then signals. A plain-text pid keeps this trivial. It is a sibling
// of the status/handoff files in the identity dir, written with the same atomic rename idiom, and
// stays logging-free + stdlib-only like its siblings.

package health

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DaemonPIDPath returns the daemon-pid file path, a sibling of the status file in the identity dir
// (same same-uid, non-immutable rationale as StatusPath).
func DaemonPIDPath(baseDir string) string {
	return filepath.Join(baseDir, "identity", ".daemon.pid")
}

// WriteDaemonPID writes pid as plain text atomically (daemon side): MkdirAll the identity dir,
// WriteFile a ".tmp" sibling at 0o600, then Rename over the final path so a concurrent reader sees
// either the old or the new file, never a partial one.
func WriteDaemonPID(baseDir string, pid int) error {
	path := DaemonPIDPath(baseDir)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write daemon pid: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup so a failed rename leaves no stray ".tmp"
		return fmt.Errorf("rename daemon pid: %w", err)
	}
	return nil
}

// ReadDaemonPID reads the recorded daemon pid: a missing file yields (0, nil) ("no daemon
// recorded"), the normal "no live daemon to signal" state. An empty or non-numeric file is an
// error (a present-but-garbage pid must not be mistaken for a safe zero).
func ReadDaemonPID(baseDir string) (int, error) {
	data, err := os.ReadFile(DaemonPIDPath(baseDir))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read daemon pid: %w", err)
	}
	s := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("parse daemon pid %q: %w", s, err)
	}
	return pid, nil
}

// RemoveDaemonPID deletes the daemon-pid file (daemon side, on shutdown). A missing file is not an
// error.
func RemoveDaemonPID(baseDir string) error {
	if err := os.Remove(DaemonPIDPath(baseDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove daemon pid: %w", err)
	}
	return nil
}
