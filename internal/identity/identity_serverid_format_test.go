package identity

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestDetectedServerIDIsAcceptedByTheOwnershipValidator holds two definitions of the
// same format together that the compiler cannot.
//
// This package MINTS the server identity; internal/types decides which values
// retention may compare, and it deliberately refuses everything that is not exactly
// sixteen ASCII decimal digits so a corrupt or future-format value degrades to the
// hostname rule instead of creating a false match. internal/types is a leaf package
// and cannot import this one, so nothing but this test stops the two drifting apart.
//
// A drift is silent and total: widen or narrow what this package mints and every
// archive written afterwards records an identity retention discards, so the whole
// adoption mechanism stops working with no error anywhere and discussion #292 returns
// on every backend at once.
func TestDetectedServerIDIsAcceptedByTheOwnershipValidator(t *testing.T) {
	baseDir := t.TempDir()

	logger := logging.New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	info, err := Detect(baseDir, logger)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	t.Cleanup(func() {
		_ = setImmutableAttributeWithContext(context.Background(), filepath.Join(baseDir, identityDirName, identityFileName), false, nil)
	})
	if info == nil {
		t.Fatal("Detect() returned nil info")
	}

	// Without this, the comparison below holds vacuously: NormalizeServerID("") returns
	// "", so an empty minted identity would report success in the very test that exists
	// to catch it.
	if info.ServerID == "" {
		t.Fatal("Detect() minted an empty ServerID, so there is no format for this guard to compare")
	}
	if got := types.NormalizeServerID(info.ServerID); got != info.ServerID {
		t.Fatalf("types.NormalizeServerID(%q) = %q: the value this package mints is not one retention may compare, so every archive written with it would be classified by hostname alone", info.ServerID, got)
	}
}
