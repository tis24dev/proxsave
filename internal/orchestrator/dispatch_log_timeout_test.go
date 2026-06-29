package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

var errBlockReleased = errors.New("blocked op released")

// blockingFS wraps osFS and blocks a chosen operation on a channel (simulating a
// dead/stale mount whose syscall never returns) until park is closed in cleanup.
type blockingFS struct {
	osFS
	blockOpen, blockMkdir, blockStat bool
	park                             chan struct{}
}

func (f *blockingFS) Open(p string) (*os.File, error) {
	if f.blockOpen {
		<-f.park
		return nil, errBlockReleased
	}
	return f.osFS.Open(p)
}

func (f *blockingFS) MkdirAll(p string, m os.FileMode) error {
	if f.blockMkdir {
		<-f.park
		return errBlockReleased
	}
	return f.osFS.MkdirAll(p, m)
}

func (f *blockingFS) Stat(p string) (os.FileInfo, error) {
	if f.blockStat {
		<-f.park
		return nil, errBlockReleased
	}
	return f.osFS.Stat(p)
}

func runDispatchWithWatchdog(t *testing.T, o *Orchestrator, src string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = o.dispatchLogFile(context.Background(), src)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatchLogFile hung on a dead mount")
	}
}

func writeSrcLog(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "backup.log")
	if err := os.WriteFile(src, []byte("logdata"), 0o640); err != nil {
		t.Fatalf("write src: %v", err)
	}
	return src
}

func TestDispatchLogFileSecondaryCopyTimeout(t *testing.T) {
	park := make(chan struct{})
	t.Cleanup(func() { close(park) })

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	secondary := t.TempDir()
	cfg := &config.Config{SecondaryEnabled: true, SecondaryLogPath: secondary, FsIoTimeoutSeconds: 1}
	o := &Orchestrator{logger: logger, cfg: cfg, fs: &blockingFS{blockOpen: true, park: park}}

	src := writeSrcLog(t)
	runDispatchWithWatchdog(t, o, src)

	if !strings.Contains(buf.String(), "timed out") {
		t.Fatalf("expected a timeout warning, got:\n%s", buf.String())
	}
	if _, err := os.Stat(filepath.Join(secondary, "backup.log")); !os.IsNotExist(err) {
		t.Fatalf("secondary copy must be skipped on timeout; stat err = %v", err)
	}
}

func TestDispatchLogFileSecondaryMkdirTimeout(t *testing.T) {
	park := make(chan struct{})
	t.Cleanup(func() { close(park) })

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	cfg := &config.Config{SecondaryEnabled: true, SecondaryLogPath: filepath.Join(t.TempDir(), "sub"), FsIoTimeoutSeconds: 1}
	o := &Orchestrator{logger: logger, cfg: cfg, fs: &blockingFS{blockMkdir: true, park: park}}

	src := writeSrcLog(t)
	runDispatchWithWatchdog(t, o, src)

	if !strings.Contains(buf.String(), "creating") || !strings.Contains(buf.String(), "timed out") {
		t.Fatalf("expected a mkdir-timeout warning, got:\n%s", buf.String())
	}
}

func TestDispatchLogFileCloudSourceProbeTimeout(t *testing.T) {
	park := make(chan struct{})
	t.Cleanup(func() { close(park) })

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	cfg := &config.Config{CloudEnabled: true, CloudLogPath: "/logs", CloudRemote: "remote", FsIoTimeoutSeconds: 1}
	o := &Orchestrator{logger: logger, cfg: cfg, fs: &blockingFS{blockStat: true, park: park}}

	src := writeSrcLog(t)
	runDispatchWithWatchdog(t, o, src)

	if !strings.Contains(buf.String(), "unreachable") {
		t.Fatalf("expected a cloud source-probe timeout warning, got:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "Failed to copy log to cloud") {
		t.Fatalf("cloud upload must not be attempted after a source-probe timeout:\n%s", buf.String())
	}
}

// The cloud log upload must be dispatched on a context DETACHED from the run
// ctx: at shutdown the log must still ship even when the run was cancelled
// (Ctrl+C). If the run ctx is threaded into the upload, a cancelled run skips
// the cloud copy with context.Canceled and the operator loses the log of the
// very run they interrupted. This pins the upload to context.Background():
// reverting extensions.go to copyLogToCloud(ctx, ...) turns this test red.
func TestDispatchLogFileCloudUploadDetachesFromCancelledRunCtx(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	cfg := &config.Config{CloudEnabled: true, CloudLogPath: "/logs", CloudRemote: "remote", FsIoTimeoutSeconds: 30}
	o := &Orchestrator{logger: logger, cfg: cfg}

	var uploaded bool
	var uploadCtxErr error
	o.copyLogToCloudFn = func(ctx context.Context, _, _ string) error {
		uploaded = true
		uploadCtxErr = ctx.Err() // the upload must NOT see a cancelled context
		return nil
	}

	src := writeSrcLog(t)
	runCtx, cancel := context.WithCancel(context.Background())
	cancel() // simulate Ctrl+C before the finalize/dispatch step

	if err := o.dispatchLogFile(runCtx, src); err != nil {
		t.Fatalf("dispatchLogFile: %v", err)
	}
	if !uploaded {
		t.Fatalf("cloud upload was not attempted; the source probe must pass for a healthy local log:\n%s", buf.String())
	}
	if uploadCtxErr != nil {
		t.Fatalf("cloud upload received a cancelled context (%v); it must run on a context detached from the run ctx so the log still ships after Ctrl+C", uploadCtxErr)
	}
	if !strings.Contains(buf.String(), "Log copied to cloud") {
		t.Fatalf("expected a success log for the dispatched cloud upload; got:\n%s", buf.String())
	}
}

func TestDispatchLogFileHealthyBoundedCopies(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	secondary := t.TempDir()
	cfg := &config.Config{SecondaryEnabled: true, SecondaryLogPath: secondary, FsIoTimeoutSeconds: 30}
	o := &Orchestrator{logger: logger, cfg: cfg}

	src := writeSrcLog(t)
	if err := o.dispatchLogFile(context.Background(), src); err != nil {
		t.Fatalf("dispatchLogFile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(secondary, "backup.log"))
	if err != nil {
		t.Fatalf("expected copied log: %v", err)
	}
	if string(data) != "logdata" {
		t.Fatalf("content mismatch: %q", string(data))
	}
	if strings.Contains(buf.String(), "timed out") || strings.Contains(buf.String(), "Failed") {
		t.Fatalf("healthy bounded copy must not warn:\n%s", buf.String())
	}
}
