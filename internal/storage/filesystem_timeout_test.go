package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/safefs"
)

// TestDetectFilesystemTimeout verifies that a detector built WithIOTimeout maps a
// blocking/expired filesystem operation to safefs.ErrTimeout instead of hanging.
func TestDetectFilesystemTimeout(t *testing.T) {
	detector := NewFilesystemDetector(newTestLogger(), WithIOTimeout(30*time.Second))

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()

	_, err := detector.DetectFilesystem(ctx, t.TempDir())
	if err == nil || !errors.Is(err, safefs.ErrTimeout) {
		t.Fatalf("DetectFilesystem err = %v; want safefs.ErrTimeout", err)
	}
}

// TestDetectFilesystemDryRunSkipsOwnershipProbe verifies that a dry-run detector
// does not run the network-FS ownership write-probe (which mutates the FS).
func TestDetectFilesystemDryRunSkipsOwnershipProbe(t *testing.T) {
	detector := NewFilesystemDetector(newTestLogger(), WithDryRun(true))
	dir := t.TempDir()
	detector.mountPointLookup = func(string) (string, error) { return "/mnt", nil }
	detector.filesystemTypeLookup = func(context.Context, string) (FilesystemType, string, error) {
		return FilesystemNFS, "server:/export", nil
	}
	probed := false
	detector.ownershipSupportTest = func(context.Context, string) bool {
		probed = true
		return false
	}

	info, err := detector.DetectFilesystem(context.Background(), dir)
	if err != nil {
		t.Fatalf("DetectFilesystem error: %v", err)
	}
	if probed {
		t.Fatal("dry-run must not run the ownership write-probe")
	}
	if info.SupportsOwnership != FilesystemNFS.SupportsUnixOwnership() {
		t.Fatalf("SupportsOwnership = %v; want type default %v", info.SupportsOwnership, FilesystemNFS.SupportsUnixOwnership())
	}
}
