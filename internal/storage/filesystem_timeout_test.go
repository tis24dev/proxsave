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
