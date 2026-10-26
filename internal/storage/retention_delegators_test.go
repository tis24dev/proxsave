package storage

import "context"

// deleteAssociatedLog attempts to remove the local log file corresponding to a backup.
// It is best-effort and never returns an error to the caller.
func (l *LocalStorage) deleteAssociatedLog(ctx context.Context, backupFile string) bool {
	if l == nil || l.config == nil {
		return false
	}
	return l.retention().deleteAssociatedLog(ctx, backupFile)
}

func (l *LocalStorage) countLogFiles(ctx context.Context) int {
	if l == nil || l.config == nil {
		return -1
	}
	return l.retention().countLogFiles(ctx, nil)
}

// deleteAssociatedLog attempts to remove the secondary log file corresponding to a backup.
// It is best-effort and never returns an error to the caller.
func (s *SecondaryStorage) deleteAssociatedLog(ctx context.Context, backupFile string) bool {
	if s == nil || s.config == nil {
		return false
	}
	return s.retention().deleteAssociatedLog(ctx, backupFile)
}

func (s *SecondaryStorage) countLogFiles(ctx context.Context) int {
	if s == nil || s.config == nil {
		return -1
	}
	return s.retention().countLogFiles(ctx, nil)
}
