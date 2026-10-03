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

	if !strings.Contains(buf.String(), "INFO       Copy failed: timed out after 1s\n") ||
		!strings.Contains(buf.String(), "WARNING  ⚠ Log not copied to secondary\n") {
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

	if !strings.Contains(buf.String(), "INFO       Directory not created: timed out after 1s\n") ||
		!strings.Contains(buf.String(), "WARNING  ⚠ Log not copied to secondary\n") {
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

	if !strings.Contains(buf.String(), "INFO       Source log not accessible: timed out after 1s\n") ||
		!strings.Contains(buf.String(), "WARNING  ⚠ Log not copied to cloud\n") {
		t.Fatalf("expected a cloud source-probe timeout warning, got:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "Copy failed") {
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
	if !strings.Contains(buf.String(), "INFO     ✓ Log copied to cloud\n") {
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
	if strings.Contains(buf.String(), "timed out") || strings.Contains(buf.String(), "failed") ||
		strings.Contains(buf.String(), "Log not copied") || !strings.Contains(buf.String(), "✓ Log copied to secondary\n") {
		t.Fatalf("healthy bounded copy must not warn:\n%s", buf.String())
	}
}

// Step [8] layout: each copy opens with its destination at the left, the facts sit
// under it, and the outcome closes it. A log that reached the cloud verified by size
// only closes with its own warning, not a failure.
func TestDispatchLogFileCopyBlocksAndChecksumOutcome(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	secondaryDir := t.TempDir()
	cfg := &config.Config{SecondaryEnabled: true, SecondaryLogPath: secondaryDir, CloudEnabled: true, CloudLogPath: "/logs", CloudRemote: "remote", FsIoTimeoutSeconds: 30}
	o := &Orchestrator{logger: logger, cfg: cfg}
	o.copyLogToCloudFn = func(context.Context, string, string) error { return errLogChecksumNotVerified }

	src := writeSrcLog(t)
	if err := o.dispatchLogFile(context.Background(), src); err != nil {
		t.Fatalf("dispatchLogFile: %v", err)
	}
	out := buf.String()
	name := filepath.Base(src)
	for _, want := range []string{
		"INFO     Secondary: " + filepath.Join(secondaryDir, name) + "\n",
		"INFO     ✓ Log copied to secondary\n",
		"INFO     Cloud: remote:/logs/" + name + "\n",
		"WARNING  ⚠ Log copied to cloud, checksum not verified\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Log not copied") || strings.Contains(out, "✓ Log copied to cloud") {
		t.Fatalf("a size-only verified log is neither a failure nor a clean copy:\n%s", out)
	}
}

type logStatDeniedFS struct{ osFS }

func (logStatDeniedFS) Stat(p string) (os.FileInfo, error) {
	return nil, &os.PathError{Op: "stat", Path: p, Err: os.ErrPermission}
}

func TestDispatchLogFileCloudSourceStatError(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	cfg := &config.Config{CloudEnabled: true, CloudLogPath: "/logs", CloudRemote: "remote", FsIoTimeoutSeconds: 30}
	o := &Orchestrator{logger: logger, cfg: cfg, fs: logStatDeniedFS{}}
	o.copyLogToCloudFn = func(context.Context, string, string) error {
		t.Fatal("the upload must not be attempted when the source log cannot be read")
		return nil
	}
	if err := o.dispatchLogFile(context.Background(), writeSrcLog(t)); err != nil {
		t.Fatalf("dispatchLogFile: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"INFO       Source log not accessible: permission denied\n", "WARNING  ⚠ Log not copied to cloud\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}
