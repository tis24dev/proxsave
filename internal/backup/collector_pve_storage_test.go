package backup

import (
	"context"
	"strings"
	"testing"
)

// TestCollectPVEStorageRuntimeWarnsOnParseFailure asserts that a failed parse
// of the storage-status JSON is visible in the run log at Warning, not swallowed
// to Debug.
//
// The RunCommand stub returns a successful (nil error) result for every command,
// so the sibling query-failure Warning at collector_pve.go:647 cannot fire (that
// branch only runs when the storage capture returns an error). The disks-list and
// pvesm-status steps also succeed, so execution reaches the storage-status parse
// branch, where the non-empty but malformed JSON makes parseNodeStorageList error.
// The only Warning that can be emitted on this path is therefore the parse-failure
// branch under test.
func TestCollectPVEStorageRuntimeWarnsOnParseFailure(t *testing.T) {
	collector := newPVECollectorWithDeps(t, CollectorDeps{
		LookPath: func(name string) (string, error) {
			return "/bin/true", nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			joined := commandSpec(name, args...).String()
			if strings.Contains(joined, "/storage") {
				// Non-empty but unparseable, so parseNodeStorageList rejects it.
				return []byte("{ this is not valid storage json "), nil
			}
			// Disks list and pvesm status succeed so execution proceeds past
			// them to the storage-status parse branch.
			return []byte("[]"), nil
		},
	})

	// commandsDir must live under the collector staging root (c.tempDir) so the
	// disks-list writeReportFile succeeds; a path outside the root is rejected by
	// os.OpenRoot confinement and would return early before the storage capture.
	commandsDir := collector.proxsaveCommandsDir("pve")

	var info pveRuntimeInfo
	if err := collector.collectPVEStorageRuntime(context.Background(), commandsDir, &info); err != nil {
		t.Fatalf("collectPVEStorageRuntime returned an unexpected error: %v", err)
	}

	// The malformed JSON must have failed to parse, so no storages are recorded.
	// This confirms the parse-failure branch was reached.
	if len(info.Storages) != 0 {
		t.Fatalf("expected no storages recorded from malformed JSON, got %d", len(info.Storages))
	}

	if collector.logger.WarningCount() == 0 {
		t.Fatal("a storage-status parse failure must be logged at Warning")
	}
}
