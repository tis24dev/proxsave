package backup

import (
	"context"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// df statfs's every mount: on a dead NFS/CIFS mount it never returns. The storage
// mounts brick bounds it by FS_IO_TIMEOUT, so the run goes on.
func TestStorageMountsDFIsBoundedByTheFSIOTimeout(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.FsIoTimeoutSeconds = 1
	blocking := func(ctx context.Context, name string, _ ...string) ([]byte, error) {
		if name == "df" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, nil
	}
	c := NewCollectorWithDeps(logging.New(types.LogLevelError, false), cfg, t.TempDir(), types.ProxmoxVE, false, CollectorDeps{
		LookPath:   func(string) (string, error) { return "/usr/bin/x", nil },
		RunCommand: blocking,
		RunCommandCaptured: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			out, err := blocking(ctx, name, args...)
			return out, nil, err
		},
	})
	done := make(chan struct{})
	go func() { _ = c.collectSystemStorageMountsRuntime(context.Background(), t.TempDir()); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("df is still running 10s after a 1s FS_IO_TIMEOUT")
	}
}
