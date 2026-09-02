package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// errors.As matches a typed-nil *storage.StorageError and leaves the target nil:
// the sidecar branch then read se.PrimarySaved off a nil pointer and panicked the
// whole Sync, turning a non-critical store failure into a crash. The typed nil is
// not hypothetical - any helper that declares `var se *storage.StorageError`,
// never assigns it, and returns it through an error wrap produces exactly this.
func TestStoreErrorCarryingATypedNilStorageErrorDoesNotPanic(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)

	backend := &fakeStorageBackend{
		name:     "secondary",
		location: storage.LocationSecondary,
		enabled:  true,
		critical: false,
		storeFn: func(context.Context, string, *types.BackupMetadata) error {
			return fmt.Errorf("secondary store: %w", (*storage.StorageError)(nil))
		},
	}

	adapter := NewStorageAdapter(backend, logger, &config.Config{})
	stats := sampleAdapterStats()

	if err := adapter.Sync(context.Background(), stats); err != nil {
		t.Fatalf("Sync returned error: %v; want nil for a non-critical backend", err)
	}
	if out := buf.String(); !strings.Contains(out, "Backup was not saved to secondary") {
		t.Fatalf("the nil StorageError must fall into the generic not-saved arm:\n%s", out)
	}
}
