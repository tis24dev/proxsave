package storage

import (
	"context"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// Regression for zero-retries-skips-upload (2026-06-09 audit): uploadWithRetry's
// loop `for attempt := 1; attempt <= RcloneRetries` never ran when a user set
// RCLONE_RETRIES<=0 (never clamped), so rclone was never called, lastErr stayed
// nil, and the function returned a bogus "upload failed after 0 attempts: <nil>"
// while silently uploading nothing. Written after the clamp-to-1 fix.
func TestCloudStorageUploadWithRetry_ZeroRetriesStillUploadsOnce(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:           true,
		CloudRemote:            "remote",
		RcloneRetries:          0, // misconfigured: must NOT skip the upload entirely
		RcloneTimeoutOperation: 5,
	}
	cs := newCloudStorageForTest(cfg)

	queue := &commandQueue{
		t:     t,
		queue: []queuedResponse{{name: "rclone", out: "ok"}},
	}
	cs.execCommand = queue.exec
	cs.waitForRetry = func(context.Context, time.Duration) error { return nil }

	if err := cs.uploadWithRetry(context.Background(), "/tmp/local.tar", "remote:local.tar"); err != nil {
		t.Fatalf("uploadWithRetry with RcloneRetries=0 should still attempt the upload, got: %v", err)
	}
	if len(queue.calls) != 1 {
		t.Fatalf("expected exactly 1 upload attempt despite RcloneRetries=0, got %d", len(queue.calls))
	}
}

func TestCloudStorageUploadWithRetry_NegativeRetriesStillUploadsOnce(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:           true,
		CloudRemote:            "remote",
		RcloneRetries:          -5,
		RcloneTimeoutOperation: 5,
	}
	cs := newCloudStorageForTest(cfg)

	queue := &commandQueue{
		t:     t,
		queue: []queuedResponse{{name: "rclone", out: "ok"}},
	}
	cs.execCommand = queue.exec
	cs.waitForRetry = func(context.Context, time.Duration) error { return nil }

	if err := cs.uploadWithRetry(context.Background(), "/tmp/local.tar", "remote:local.tar"); err != nil {
		t.Fatalf("uploadWithRetry with negative RcloneRetries should still attempt the upload, got: %v", err)
	}
	if len(queue.calls) != 1 {
		t.Fatalf("expected exactly 1 upload attempt with negative RcloneRetries, got %d", len(queue.calls))
	}
}
