package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// df statfs's every mount: on a dead NFS/CIFS mount it never returns. The storage
// mounts brick bounds it by FS_IO_TIMEOUT, so the run goes on.
//
// It runs the production command runner against a df that never returns: the bound
// kills it, and the error is the process's "signal: killed", not the context's. That
// is a non-critical failure, so the brick goes on to the mount probe. A fake runner
// that returned ctx.Err() instead made it look as if the brick stopped there.
func TestStorageMountsDFIsBoundedByTheFSIOTimeout(t *testing.T) {
	bin := t.TempDir()
	// exec, so the kill reaches the sleeper itself and no child holds the pipes open.
	if err := os.WriteFile(filepath.Join(bin, "df"), []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "mount"), []byte("#!/bin/sh\necho mounted\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := GetDefaultCollectorConfig()
	cfg.FsIoTimeoutSeconds = 1
	staging := t.TempDir()
	c := NewCollectorWithDeps(logging.New(types.LogLevelError, false), cfg, staging, types.ProxmoxVE, false, CollectorDeps{
		DetectUnprivilegedContainer: func() (bool, string) { return false, "" },
	})
	dir := filepath.Join(staging, "commands", "system")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.collectSystemStorageMountsRuntime(context.Background(), dir) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("collectSystemStorageMountsRuntime = %v, want nil: a df past FS_IO_TIMEOUT is not critical", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("df is still running 10s after a 1s FS_IO_TIMEOUT")
	}
	if _, err := os.Stat(filepath.Join(dir, "df.txt")); err == nil {
		t.Fatal("df.txt written although df never answered")
	}
	data, err := os.ReadFile(filepath.Join(dir, "mount.txt"))
	if err != nil || strings.TrimSpace(string(data)) != "mounted" {
		t.Fatalf("mount.txt = %q, %v: the mount probe must run after the df timeout", data, err)
	}
}
