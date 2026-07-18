package backup

import (
	"context"
	"errors"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// A failed PBS datastore enumeration (command error or unparseable output) must
// increment FilesFailed so the backup reports DEGRADED and is distinguishable
// from a genuine zero-datastore host; it must NOT return an error (non-fatal:
// the raw datastore.cfg is still collected).
func TestGetDatastoreListEnumerationFailureCountsFailed(t *testing.T) {
	cases := []struct {
		name string
		run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	}{
		{"command error", func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("proxmox-backup-manager boom")
		}},
		{"invalid json", func(context.Context, string, ...string) ([]byte, error) {
			return []byte("not json"), nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := GetDefaultCollectorConfig()
			cfg.PBSDatastorePaths = []string{"/mnt/datastore/Fallback"}
			deps := CollectorDeps{
				LookPath:   func(string) (string, error) { return "/bin/true", nil },
				RunCommand: tc.run,
			}
			c := NewCollectorWithDeps(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false, deps)

			got, err := c.getDatastoreList(context.Background())
			if err != nil {
				t.Fatalf("enumeration failure must be non-fatal, got err: %v", err)
			}
			if c.stats.FilesFailed != 1 {
				t.Fatalf("FilesFailed=%d; want 1 (enumeration failure recorded)", c.stats.FilesFailed)
			}
			// Fallback still applied: the configured path is present.
			foundFallback := false
			for _, ds := range got {
				if ds.Source == pbsDatastoreSourceOverride {
					foundFallback = true
				}
			}
			if !foundFallback {
				t.Fatalf("PBSDatastorePaths fallback must still be applied after enumeration failure")
			}
		})
	}
}

// A successful enumeration must NOT touch FilesFailed.
func TestGetDatastoreListSuccessNoFailedCount(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/true", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`[{"name":"Data1","path":"/mnt/datastore/Data1"}]`), nil
		},
	}
	c := NewCollectorWithDeps(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false, deps)
	if _, err := c.getDatastoreList(context.Background()); err != nil {
		t.Fatalf("getDatastoreList: %v", err)
	}
	if c.stats.FilesFailed != 0 {
		t.Fatalf("FilesFailed=%d; want 0 on success", c.stats.FilesFailed)
	}
}
