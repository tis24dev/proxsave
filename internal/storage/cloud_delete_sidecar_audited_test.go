package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// Regression for delete-partialfail-not-counted (2026-06-09 audit): deleteBackupInternal
// returned an error whenever ANY file failed - including a sidecar (.sha256/.metadata) -
// even after the backup archive itself was removed. deleteBatched then treated that as
// "not deleted" and skipped incrementing the counter, so the retention summary
// over-reported "remaining" backups while the archive was actually gone. The delete now
// distinguishes a sidecar-only failure (errBackupSidecarDeleteOnly) and counts it.
func TestCloudStorageApplyRetention_CountsBackupWhenOnlySidecarDeleteFails(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:          true,
		CloudRemote:           "remote",
		CloudBatchSize:        1,
		CloudBatchPause:       0,
		BundleAssociatedFiles: false,
	}
	cs := newCloudStorageForTest(cfg)
	cs.sleep = func(time.Duration) {}

	// Each backup carries a .sha256 completion sidecar so List marks it Verified;
	// retention only acts on verified entries.
	listOutput := strings.TrimSpace(`
100 2024-11-12 10:00:00 gamma-backup-3.tar.zst
120 2024-11-12 10:00:00 gamma-backup-3.tar.zst.sha256
100 2024-11-11 10:00:00 beta-backup-2.tar.zst
120 2024-11-11 10:00:00 beta-backup-2.tar.zst.sha256
100 2024-11-10 10:00:00 alpha-backup-1.tar.zst
120 2024-11-10 10:00:00 alpha-backup-1.tar.zst.sha256
`)
	recountOutput := strings.TrimSpace(`
100 2024-11-12 10:00:00 gamma-backup-3.tar.zst
100 2024-11-11 10:00:00 beta-backup-2.tar.zst
`)

	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{name: "rclone", args: []string{"lsl", "remote:"}, out: listOutput},
			// The data archive deletes fine...
			{name: "rclone", args: []string{"deletefile", "remote:alpha-backup-1.tar.zst"}},
			// ...but a sidecar deletion fails with a real (non-"not found") error.
			{name: "rclone", args: []string{"deletefile", "remote:alpha-backup-1.tar.zst.sha256"}, out: "permission denied", err: errors.New("exit status 1")},
			{name: "rclone", args: []string{"deletefile", "remote:alpha-backup-1.tar.zst.metadata"}},
			{name: "rclone", args: []string{"deletefile", "remote:alpha-backup-1.tar.zst.metadata.sha256"}},
			{name: "rclone", args: []string{"lsl", "remote:"}, out: recountOutput},
		},
	}
	cs.execCommand = queue.exec

	deleted, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 2})
	if err != nil {
		t.Fatalf("ApplyRetention() error = %v", err)
	}
	// The archive is gone, so the backup must be counted as deleted despite the
	// failed sidecar (previously this returned 0, over-reporting remaining backups).
	if deleted != 1 {
		t.Fatalf("ApplyRetention() deleted = %d, want 1 (sidecar-only failure must still count)", deleted)
	}
	if got := cs.lastRet.BackupsRemaining; got != 2 {
		t.Fatalf("lastRet.BackupsRemaining = %d, want 2", got)
	}
}
