package checks

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// BackupLockFileName is the fixed basename of the backup lock file inside the lock
// directory (LockDirPath). CheckLockFile creates/removes exactly this file; a probe
// from a separate process reuses the same name so it inspects the REAL lock, not a
// look-alike.
const BackupLockFileName = ".backup.lock"

// DefaultMaxLockAge mirrors GetDefaultCheckerConfig's MaxLockAge: a backup lock older
// than this is treated as stale (a crashed backup that never released it), so an
// age-only fallback never reports a long-dead backup as "in progress".
const DefaultMaxLockAge = 2 * time.Hour

// DefaultBackupLockPath returns the backup lock path for a given base dir, matching
// config.LockPath's default (<baseDir>/lock) plus the fixed lock basename. It lets a
// SEPARATE process (an upgrade/restart flow that only has the base dir) locate the same
// lock the orchestrator's Checker acquires, without loading the full config. A custom
// LOCK_PATH override is not reflected here; on such a host the probe simply finds no
// lock at the default path and reports "not running", which only ever SKIPS the
// bounded backup-wait -- never interrupts a backup -- so it degrades safely.
func DefaultBackupLockPath(baseDir string) string {
	return filepath.Join(baseDir, "lock", BackupLockFileName)
}

// BackupInProgress reports, from a SEPARATE process and WITHOUT ever mutating the lock,
// whether a backup currently holds the lock at lockPath. It is a read-only probe (stat +
// read + signal-0) that reuses the EXACT stale/live decision CheckLockFile applies, so it
// can never block a real backup (it never O_EXCL-creates the lock) and never holds it:
//   - no lock file (or an unreadable stat)                 -> not in progress.
//   - a recorded pid on THIS host that is alive (kill 0 ->  -> in progress (a live backup).
//     nil or EPERM)
//   - a recorded pid on this host that is gone (ESRCH)      -> not in progress (stale lock).
//   - an inconclusive liveness check, an unparseable file,  -> age fallback: in progress only
//     or a foreign-host lock (liveness unverifiable)           when the file is younger than
//     maxLockAge.
//
// Note: this deliberately does NOT use flock. The backup lock is an O_EXCL lock FILE with
// pid/host metadata (see CheckLockFile), not an advisory flock, so a flock LOCK_EX|LOCK_NB
// try-acquire on the same path would succeed even while a backup holds it and would not
// interoperate. Reusing the pid-liveness detection is the faithful, non-intrusive probe.
func BackupInProgress(lockPath string, maxLockAge time.Duration) bool {
	info, err := osStat(lockPath)
	if err != nil {
		return false // missing lock (or unreadable): no backup is holding it
	}
	age := time.Since(info.ModTime())

	content, rerr := readLockFileContent(lockPath)
	if rerr != nil {
		// The file exists but its metadata is unreadable (e.g. removed between the
		// stat and the read): fall back to age. A fresh file is most likely a live
		// backup; an old one is stale.
		return age <= maxLockAge
	}
	meta := parseLockFileMetadata(content)

	hostname, _ := os.Hostname()
	if meta.PID > 0 && sameHost(meta.Host, hostname) {
		switch killErr := killFunc(meta.PID, 0); {
		case killErr == nil, errors.Is(killErr, syscall.EPERM):
			return true // pid alive on this host -> a backup is in progress
		case errors.Is(killErr, syscall.ESRCH):
			return false // pid gone -> stale lock, no backup running
		default:
			return age <= maxLockAge // inconclusive liveness -> age fallback
		}
	}
	// No parseable pid, or a lock owned by a different host (liveness unverifiable):
	// fall back to age so a fresh cross-host lock still reads as in progress.
	return age <= maxLockAge
}
