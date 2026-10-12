package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Rejecting a database in SQLite must not let the raw-copy or dry-run path
// publish a directory/symlink as a successfully collected recovery database.
func TestPVEInvalidDatabaseNeverBecomesCollected(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		for _, dry := range []bool{false, true} {
			t.Run(fmt.Sprintf("symlink=%v/dry=%v", symlink, dry), func(t *testing.T) {
				c, clusterDir, output := configDBCollector(t, CollectorDeps{})
				c.config.BackupClusterConfig, c.dryRun = false, dry
				src := filepath.Join(clusterDir, "config.db")
				if err := os.Remove(src); err != nil {
					t.Fatal(err)
				}
				if symlink {
					if err := os.Symlink(t.TempDir(), src); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Mkdir(src, 0700); err != nil {
					t.Fatal(err)
				}
				if err := c.collectPVEClusterSnapshot(context.Background(), false); err != nil {
					t.Fatal(err)
				}
				assertPVEDiagnosticEvents(t, output.String(), "PVE configuration database:", []string{
					"WARNING|PVE configuration database: not a regular file " + src,
					"INFO|PVE configuration database: falling back to a raw copy, which can tear mid-write and misses WAL data not yet checkpointed",
					"WARNING|PVE configuration database: sqlite snapshot failed",
				})
				if _, err := os.Lstat(c.targetPathFor(src)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("invalid database published a canonical artifact: %v", err)
				}
				c.populatePVEManifest()
				if got := c.pveManifest["var/lib/pve-cluster/config.db"]; got.Status != StatusFailed {
					t.Errorf("invalid database reported as recoverable: %+v", got)
				}
				if got := c.GetStats().FilesProcessed; got != 0 {
					t.Errorf("invalid database counted as processed: %d", got)
				}
			})
		}
	}
}
