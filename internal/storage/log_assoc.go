package storage

import (
	"path/filepath"
	"strings"
	"time"
)

// extractLogKeyFromBackup attempts to derive the hostname and timestamp key
// from a backup filename using the Go naming scheme:
//
//	<hostname>-backup-<YYYYMMDD-HHMMSS>.<ext...>
//
// It returns hostname and timestamp string (YYYYMMDD-HHMMSS) if successful. The
// marker is the LAST "-backup-" in the name, because the hostname is written as it is
// and may carry one of its own (pbs-backup-01), and what follows it must be a real
// timestamp, ending the name or followed by an extension: anything else is not a name
// this scheme produced.
func extractLogKeyFromBackup(backupFile string) (hostname, timestamp string, ok bool) {
	base := filepath.Base(backupFile)

	const marker = "-backup-"
	idx := strings.LastIndex(base, marker)
	if idx <= 0 {
		return "", "", false
	}

	host := base[:idx]
	rest := base[idx+len(marker):]
	tsLen := len(runLogTimestampLayout)
	if host == "" || len(rest) < tsLen {
		return "", "", false
	}
	if len(rest) > tsLen && rest[tsLen] != '.' {
		return "", "", false
	}
	timestamp = rest[:tsLen]
	if _, err := time.Parse(runLogTimestampLayout, timestamp); err != nil {
		return "", "", false
	}
	return host, timestamp, true
}

func computeRemaining(initial, deleted int) (int, bool) {
	if initial < 0 {
		return 0, false
	}
	remaining := max(initial-deleted, 0)
	return remaining, true
}
