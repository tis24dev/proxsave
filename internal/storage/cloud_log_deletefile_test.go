package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// deleteAssociatedLog removes ONE file, so it must use rclone's single-object verb.
// The directory-oriented `delete` it used before answered a merely-absent log with
// "directory not found" on directory-listing backends, which the error branch read
// as the whole CLOUD_LOG_PATH being gone: a false WARNING naming the base, plus
// logPathMissing poisoning log cleanup for every remaining backup of the pass. On
// prefix backends the same miss exited 0 and falsely counted a deletion. Both
// behaviors were reproduced live with rclone v1.75.0.
func TestDeleteAssociatedLogUsesDeletefile(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:  true,
		CloudRemote:   "remote",
		CloudLogPath:  "remote:logs",
		RcloneRetries: 1,
	}
	cs := newCloudStorageForTest(cfg)

	var calls [][]string
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return nil, nil
	}

	if !cs.deleteAssociatedLog(context.Background(), "host-backup-20250101-010101.tar.xz") {
		t.Fatal("deleteAssociatedLog() = false on a successful delete, want true")
	}
	if len(calls) != 1 {
		t.Fatalf("expected exactly one rclone call, got %d: %v", len(calls), calls)
	}
	if calls[0][0] != "deletefile" {
		t.Fatalf("verb = %q, want deletefile (single-object delete): %v", calls[0][0], calls[0])
	}
	if last := calls[0][len(calls[0])-1]; !strings.HasSuffix(last, "backup-host-20250101-010101.log") {
		t.Fatalf("expected the derived log path as the final argument, got %v", calls[0])
	}
}

// A missing log is the benign branch whatever wording the backend picks:
// "directory not found" from a directory-listing backend must NOT mark the whole
// log path unavailable and must NOT warn - the next backup's log delete still runs.
func TestDeleteAssociatedLogMissingLogDoesNotPoisonTheLogPath(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:  true,
		CloudRemote:   "remote",
		CloudLogPath:  "remote:logs",
		RcloneRetries: 1,
	}
	cs := newCloudStorageForTest(cfg)

	var calls int
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		return []byte("2025/11/16 22:11:47 ERROR : remote:logs/backup-host-20250101-010101.log: directory not found"), errors.New("exit status 3")
	}

	if cs.deleteAssociatedLog(context.Background(), "host-backup-20250101-010101.tar.xz") {
		t.Fatal("deleteAssociatedLog() = true for a missing log, want false")
	}
	if cs.isCloudLogPathUnavailable() {
		t.Fatal("a single missing log marked the whole CLOUD_LOG_PATH unavailable, poisoning cleanup for every remaining backup")
	}
	if cs.deleteAssociatedLog(context.Background(), "host-backup-20250102-010101.tar.xz"); calls != 2 {
		t.Fatalf("the second backup's log delete was skipped (calls=%d), want it to still run", calls)
	}
}
