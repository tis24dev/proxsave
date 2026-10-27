package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A file listed by its directory and gone before the walk's lstat (on /etc/pve, HA
// and vzdump write through temp siblings that live for milliseconds) stopped the whole
// copy, and for /etc/pve the backup with no archive. Files created and removed while
// the copy runs are skipped; the copy completes every time.
func TestSafeCopyDirSkipsEntriesThatVanishDuringTheWalk(t *testing.T) {
	src := t.TempDir()
	for i := range 200 {
		if err := os.WriteFile(filepath.Join(src, fmt.Sprintf("keep-%03d.cfg", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			p := filepath.Join(src, fmt.Sprintf(".tmp-%d", i%50))
			_ = os.WriteFile(p, []byte("y"), 0o644)
			_ = os.Remove(p)
		}
	}()
	failures := 0
	var lastErr error
	for range 20 {
		staging := t.TempDir()
		c := NewCollectorWithDeps(logging.New(types.LogLevelError, false), GetDefaultCollectorConfig(), staging, types.ProxmoxVE, false, CollectorDeps{})
		if err := c.safeCopyDir(context.Background(), src, filepath.Join(staging, "etc", "pve"), "PVE configuration"); err != nil {
			failures++
			lastErr = err
		}
	}
	close(stop)
	wg.Wait()
	if failures > 0 {
		t.Fatalf("%d of 20 copies failed on an entry that vanished, last: %v", failures, lastErr)
	}
}
