package storage

import (
	"slices"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// buildRcloneUploadArgsForTest builds the headless rclone upload argv through the
// real builder used by rcloneCopy (cloud.go), with the minimal inputs it needs.
func buildRcloneUploadArgsForTest(t *testing.T) []string {
	t.Helper()
	c := &CloudStorage{config: &config.Config{}}
	return c.buildRcloneUploadArgs("/tmp/local.tar", "remote:backup/local.tar")
}

// The headless rclone command must not request progress/stats output, which
// accumulates in memory over long uploads and bloats error messages.
func TestRcloneArgsHaveNoProgress(t *testing.T) {
	args := buildRcloneUploadArgsForTest(t)
	if slices.Contains(args, "--progress") {
		t.Fatalf("rclone args must not contain --progress: %v", args)
	}
	if slices.Contains(args, "--stats") {
		t.Fatalf("rclone args must not contain --stats: %v", args)
	}
}
