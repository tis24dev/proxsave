package orchestrator

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
)

func TestAnalyzeBackupCategories_OpenError(t *testing.T) {
	orig := restoreFS
	defer func() { restoreFS = orig }()
	restoreFS = NewFakeFS()
	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)

	_, err := AnalyzeBackupCategories("/missing/archive.tar", logger)
	if err == nil {
		t.Fatalf("expected error when archive cannot be opened")
	}
}

func TestRunRestoreCommandStream_UsesStreamingRunner(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"xz -d -c": []byte("hello"),
		},
	}
	restoreCmd = fake

	tmp, err := os.CreateTemp("", "stdin-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	reader, err := createXZReader(context.Background(), tmp)
	if err != nil {
		t.Fatalf("createXZReader: %v", err)
	}
	defer reader.(io.Closer).Close()

	buf, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("unexpected output: %q", string(buf))
	}
	if len(fake.Calls) != 1 || fake.Calls[0] != "xz -d -c" {
		t.Fatalf("unexpected calls: %#v", fake.Calls)
	}
}

func TestAnalyzeArchivePaths_Empty(t *testing.T) {
	if got := AnalyzeArchivePaths(nil, nil); got != nil {
		t.Fatalf("expected nil for empty input, got %#v", got)
	}
}

func TestStopPBSServices_CommandFails(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()
	origVerify := serviceVerifyTimeout
	serviceVerifyTimeout = 100 * time.Millisecond
	defer func() { serviceVerifyTimeout = origVerify }()

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"which systemctl":                          {},
			"systemctl is-active proxmox-backup":       []byte("inactive"),
			"systemctl is-active proxmox-backup-proxy": []byte("inactive"),
		},
		Errors: map[string]error{
			"systemctl stop --no-block proxmox-backup-proxy":                      fmt.Errorf("fail-proxy"),
			"systemctl stop proxmox-backup-proxy":                                 fmt.Errorf("fail-blocking"),
			"systemctl kill --signal=SIGTERM --kill-who=all proxmox-backup-proxy": fmt.Errorf("kill-term"),
			"systemctl kill --signal=SIGKILL --kill-who=all proxmox-backup-proxy": fmt.Errorf("kill-9"),
			"systemctl is-active proxmox-backup":                                  fmt.Errorf("inactive"),
			"systemctl is-active proxmox-backup-proxy":                            fmt.Errorf("inactive"),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := stopPBSServices(context.Background(), logger)
	if err == nil || !strings.Contains(err.Error(), "kill-9") {
		t.Fatalf("expected failure, got %v", err)
	}
	if len(fake.Calls) == 0 || fake.Calls[0] != "which systemctl" {
		t.Fatalf("expected which systemctl to be called, got %#v", fake.Calls)
	}
}

func TestStopPBSServices_Succeeds(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"which systemctl":                          {},
			"systemctl is-active proxmox-backup-proxy": []byte("inactive"),
			"systemctl is-active proxmox-backup":       []byte("inactive"),
		},
		Errors: map[string]error{
			"systemctl is-active proxmox-backup-proxy": fmt.Errorf("inactive"),
			"systemctl is-active proxmox-backup":       fmt.Errorf("inactive"),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	if err := stopPBSServices(context.Background(), logger); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(fake.Calls) != 7 {
		t.Fatalf("expected 7 calls, got %d", len(fake.Calls))
	}
}

func TestStopPBSServices_SystemctlMissing(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()

	fake := &FakeCommandRunner{
		Errors: map[string]error{
			"which systemctl": fmt.Errorf("missing"),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := stopPBSServices(context.Background(), logger)
	if err == nil || !strings.Contains(err.Error(), "systemctl not available") {
		t.Fatalf("expected systemctl missing error, got %v", err)
	}
}

func TestRunCommandWithTimeout_TimeoutError(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()

	fake := &FakeCommandRunner{
		Errors: map[string]error{
			"systemctl start proxmox-backup": context.DeadlineExceeded,
		},
	}
	restoreCmd = fake

	err := runCommandWithTimeout(context.Background(), nil, 10*time.Millisecond, "systemctl", "start", "proxmox-backup")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestStopPVEClusterServices_UsesNoBlock(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()

	outputs := map[string][]byte{}
	errors := map[string]error{}
	for _, svc := range []string{"pve-cluster", "pvedaemon", "pveproxy", "pvestatd"} {
		key := fmt.Sprintf("systemctl is-active %s", svc)
		outputs[key] = []byte("inactive")
		errors[key] = fmt.Errorf("inactive")
	}
	fake := &FakeCommandRunner{
		Outputs: outputs,
		Errors:  errors,
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	if err := stopPVEClusterServices(context.Background(), logger); err != nil {
		t.Fatalf("expected success stopping PVE services, got %v", err)
	}

	wantStops := []string{
		"systemctl stop --no-block pve-cluster",
		"systemctl stop --no-block pvedaemon",
		"systemctl stop --no-block pveproxy",
		"systemctl stop --no-block pvestatd",
	}
	for _, cmd := range wantStops {
		found := false
		for _, call := range fake.Calls {
			if call == cmd {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected %s to be called, calls: %#v", cmd, fake.Calls)
		}
	}
}

func TestStartPBSServices_CommandTimeout(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"which systemctl": {},
		},
		Errors: map[string]error{
			"systemctl start proxmox-backup":   context.DeadlineExceeded,
			"systemctl restart proxmox-backup": context.DeadlineExceeded,
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := startPBSServices(context.Background(), logger)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestStopPBSServices_AggressiveRetry(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"which systemctl":                          {},
			"systemctl is-active proxmox-backup-proxy": []byte("inactive"),
			"systemctl is-active proxmox-backup":       []byte("inactive"),
		},
		Errors: map[string]error{
			"systemctl stop --no-block proxmox-backup-proxy": fmt.Errorf("stop failed"),
			"systemctl stop proxmox-backup-proxy":            fmt.Errorf("stop failed"),
			"systemctl is-active proxmox-backup-proxy":       fmt.Errorf("inactive"),
			"systemctl is-active proxmox-backup":             fmt.Errorf("inactive"),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	if err := stopPBSServices(context.Background(), logger); err != nil {
		t.Fatalf("expected success with aggressive retry, got %v", err)
	}

	foundKill := false
	for _, call := range fake.Calls {
		if call == "systemctl kill --signal=SIGTERM --kill-who=all proxmox-backup-proxy" {
			foundKill = true
			break
		}
	}
	if !foundKill {
		t.Fatalf("expected systemctl kill --signal=SIGTERM --kill-who=all proxmox-backup-proxy to be invoked, calls: %#v", fake.Calls)
	}
}

func TestStartPBSServices_AggressiveRetry(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"which systemctl": {},
		},
		Errors: map[string]error{
			"systemctl start proxmox-backup": fmt.Errorf("start failed"),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	if err := startPBSServices(context.Background(), logger); err != nil {
		t.Fatalf("expected success with aggressive restart, got %v", err)
	}

	foundRestart := false
	for _, call := range fake.Calls {
		if call == "systemctl restart proxmox-backup" {
			foundRestart = true
			break
		}
	}
	if !foundRestart {
		t.Fatalf("expected systemctl restart proxmox-backup to be invoked, calls: %#v", fake.Calls)
	}
}

func TestStopPBSServices_VerifyFailure(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()
	origVerify := serviceVerifyTimeout
	origStatus := serviceStatusCheckTimeout
	origPoll := servicePollInterval
	serviceVerifyTimeout = 50 * time.Millisecond
	serviceStatusCheckTimeout = 10 * time.Millisecond
	servicePollInterval = 5 * time.Millisecond
	defer func() {
		serviceVerifyTimeout = origVerify
		serviceStatusCheckTimeout = origStatus
		servicePollInterval = origPoll
	}()

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"which systemctl":                          {},
			"systemctl is-active proxmox-backup-proxy": []byte("active"),
			"systemctl is-active proxmox-backup":       []byte("inactive"),
		},
		Errors: map[string]error{
			"systemctl is-active proxmox-backup-proxy": fmt.Errorf("active"),
			"systemctl is-active proxmox-backup":       fmt.Errorf("inactive"),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := stopPBSServices(context.Background(), logger)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "active") {
		t.Fatalf("expected verification error mentioning active service, got %v", err)
	}
}

func TestWaitForServiceInactive_Succeeds(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"systemctl is-active demo.service": []byte("inactive"),
		},
		Errors: map[string]error{
			"systemctl is-active demo.service": fmt.Errorf("inactive"),
		},
	}
	restoreCmd = fake

	if err := waitForServiceInactive(context.Background(), nil, "demo.service", 50*time.Millisecond); err != nil {
		t.Fatalf("expected wait to succeed, got %v", err)
	}
}

func TestWaitForServiceInactive_TimesOut(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"systemctl is-active demo.service": []byte("active"),
		},
	}
	restoreCmd = fake

	err := waitForServiceInactive(context.Background(), nil, "demo.service", 30*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "still active") {
		t.Fatalf("expected still active error, got %v", err)
	}
}

func TestIsServiceActive_Inactive(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"systemctl is-active demo.service": []byte("inactive"),
		},
		Errors: map[string]error{
			"systemctl is-active demo.service": fmt.Errorf("inactive"),
		},
	}
	restoreCmd = fake

	active, err := isServiceActive(context.Background(), "demo.service", 20*time.Millisecond)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if active {
		t.Fatalf("expected service to be inactive")
	}
}

func TestIsServiceActive_Timeout(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()

	fake := &FakeCommandRunner{
		Errors: map[string]error{
			"systemctl is-active demo.service": context.DeadlineExceeded,
		},
	}
	restoreCmd = fake

	_, err := isServiceActive(context.Background(), "demo.service", 5*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestEnsureWritablePath_Overwrite(t *testing.T) {
	tmp := t.TempDir()
	orig := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = orig })

	existing := filepath.Join(tmp, "file.txt")
	if err := os.WriteFile(existing, []byte("data"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := bufio.NewReader(strings.NewReader("1\n"))

	got, err := ensureWritablePath(ctx, reader, existing, "test")
	if err != nil {
		t.Fatalf("ensureWritablePath: %v", err)
	}
	if got != existing {
		t.Fatalf("expected same path, got %s", got)
	}
}

func TestEnsureWritablePath_EnterNewPath(t *testing.T) {
	tmp := t.TempDir()
	orig := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = orig })

	existing := filepath.Join(tmp, "file.txt")
	if err := os.WriteFile(existing, []byte("data"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := bufio.NewReader(strings.NewReader("2\n" + filepath.Join(tmp, "new.txt") + "\n"))

	got, err := ensureWritablePath(ctx, reader, existing, "test")
	if err != nil {
		t.Fatalf("ensureWritablePath: %v", err)
	}
	want := filepath.Join(tmp, "new.txt")
	if got != want {
		t.Fatalf("expected new path %s, got %s", want, got)
	}
}

func TestEnsureWritablePath_Abort(t *testing.T) {
	tmp := t.TempDir()
	orig := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = orig })

	existing := filepath.Join(tmp, "file.txt")
	if err := os.WriteFile(existing, []byte("data"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := bufio.NewReader(strings.NewReader("0\n"))

	_, err := ensureWritablePath(ctx, reader, existing, "test")
	if err == nil || !errors.Is(err, ErrDecryptAborted) {
		t.Fatalf("expected abort error, got %v", err)
	}
}

func TestStopPVEClusterServices_Failure(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"systemctl start pve-cluster": {},
		},
		Errors: map[string]error{
			"systemctl start pvedaemon":   fmt.Errorf("fail daemon"),
			"systemctl restart pvedaemon": fmt.Errorf("fail daemon restart"),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := startPVEClusterServices(context.Background(), logger)
	if err == nil || !strings.Contains(err.Error(), "fail daemon") {
		t.Fatalf("expected failure, got %v", err)
	}
}

func TestStartPBSServices_Success(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"which systemctl":                      {},
			"systemctl start proxmox-backup":       {},
			"systemctl start proxmox-backup-proxy": {},
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	if err := startPBSServices(context.Background(), logger); err != nil {
		t.Fatalf("expected PBS start success, got %v", err)
	}
}

// --------------------------------------------------------------------------
// applyStorageCfg error tests
// --------------------------------------------------------------------------

func TestApplyStorageCfg_ReadFileError(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })
	restoreFS = osFS{}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	_, _, err := applyStorageCfg(context.Background(), "/nonexistent/path/storage.cfg", logger)
	if err == nil {
		t.Fatalf("expected read error")
	}
}

func TestApplyStorageCfg_WithMultipleBlocks(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})
	restoreFS = osFS{}

	// Write storage config with multiple blocks
	cfgPath := filepath.Join(t.TempDir(), "storage.cfg")
	content := `storage: local
	path /var/lib/vz

storage: backup
	path /mnt/backup
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	restoreCmd = &FakeCommandRunner{}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	applied, failed, err := applyStorageCfg(context.Background(), cfgPath, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applied != 2 {
		t.Fatalf("expected 2 applied, got %d (failed=%d)", applied, failed)
	}
}

func TestApplyStorageCfg_PveshError(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})
	restoreFS = osFS{}

	cfgPath := filepath.Join(t.TempDir(), "storage.cfg")
	content := `storage: local
	path /var/lib/vz
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Make all pvesh calls fail
	restoreCmd = &alwaysFailCommandRunner{err: fmt.Errorf("pvesh failed")}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	applied, failed, err := applyStorageCfg(context.Background(), cfgPath, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if failed != 1 {
		t.Fatalf("expected 1 failed, got %d (applied=%d)", failed, applied)
	}
}

// --------------------------------------------------------------------------
// extractRegularFile error tests
// --------------------------------------------------------------------------

func TestExtractRegularFile_CopyError(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })
	restoreFS = osFS{}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	destRoot := t.TempDir()
	target := filepath.Join(destRoot, "test.txt")

	// Create a tar with content
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	header := &tar.Header{
		Name: "test.txt",
		Mode: 0o644,
		Size: 100, // Claim 100 bytes but provide none
	}
	_ = tw.WriteHeader(header)
	// Don't write content, causing EOF during copy
	_ = tw.Close()

	tarReader := tar.NewReader(&buf)
	_, _ = tarReader.Next()

	err := extractRegularFile(tarReader, target, header, logger)
	if err == nil {
		t.Fatalf("expected copy error")
	}
}

// --------------------------------------------------------------------------
// extractDirectory tests
// --------------------------------------------------------------------------

func TestExtractDirectory_WithTimestamps(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })
	restoreFS = osFS{}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	destRoot := t.TempDir()
	target := filepath.Join(destRoot, "testdir")

	modTime := time.Date(2023, 6, 15, 12, 0, 0, 0, time.UTC)
	header := &tar.Header{
		Name:       "testdir",
		Mode:       0o755,
		Uid:        os.Getuid(),
		Gid:        os.Getgid(),
		ModTime:    modTime,
		AccessTime: modTime,
	}

	if err := extractDirectory(target, header, logger); err != nil {
		t.Fatalf("extractDirectory failed: %v", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory")
	}
}

// --------------------------------------------------------------------------
// resetFailedService test
// --------------------------------------------------------------------------

func TestResetFailedService_IgnoresError(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Errors: map[string]error{
			"systemctl reset-failed test.service": fmt.Errorf("reset error"),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	// Should not panic even if the command fails
	resetFailedService(context.Background(), logger, "test.service")

	found := false
	for _, call := range fake.Calls {
		if strings.Contains(call, "reset-failed") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected reset-failed call")
	}
}

// --------------------------------------------------------------------------
// isServiceActive tests
// --------------------------------------------------------------------------

func TestIsServiceActive_Activating(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"systemctl is-active test.service": []byte("activating"),
		},
		Errors: map[string]error{
			"systemctl is-active test.service": fmt.Errorf("activating"),
		},
	}
	restoreCmd = fake

	// activating is considered active (transient state)
	active, _ := isServiceActive(context.Background(), "test.service", 1*time.Second)
	if !active {
		t.Fatalf("activating should be considered active")
	}
}

func TestIsServiceActive_Dead(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"systemctl is-active test.service": []byte("dead"),
		},
		Errors: map[string]error{
			"systemctl is-active test.service": fmt.Errorf("dead"),
		},
	}
	restoreCmd = fake

	active, _ := isServiceActive(context.Background(), "test.service", 1*time.Second)
	if active {
		t.Fatalf("dead should not be considered active")
	}
}

// --------------------------------------------------------------------------
// waitForServiceInactive tests
// --------------------------------------------------------------------------

func TestWaitForServiceInactive_Timeout(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"systemctl is-active test.service": []byte("active"),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := waitForServiceInactive(context.Background(), logger, "test.service", 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "still active") {
		t.Fatalf("expected 'still active' error, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// helper types
// --------------------------------------------------------------------------

type alwaysFailCommandRunner struct {
	err error
}

func (a *alwaysFailCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return nil, a.err
}

func (a *alwaysFailCommandRunner) RunStream(ctx context.Context, name string, stdin io.Reader, args ...string) (io.ReadCloser, error) {
	return nil, a.err
}

// --------------------------------------------------------------------------
// ErrorInjectingFS - FS wrapper that can inject errors
// --------------------------------------------------------------------------

type ErrorInjectingFS struct {
	base        FS
	mkdirAllErr error
	openFileErr error
	symlinkErr  error
	readlinkErr error
	linkErr     error
}

func (f *ErrorInjectingFS) Stat(path string) (os.FileInfo, error) { return f.base.Stat(path) }
func (f *ErrorInjectingFS) ReadFile(path string) ([]byte, error)  { return f.base.ReadFile(path) }
func (f *ErrorInjectingFS) Open(path string) (*os.File, error)    { return f.base.Open(path) }
func (f *ErrorInjectingFS) Create(name string) (*os.File, error)  { return f.base.Create(name) }
func (f *ErrorInjectingFS) WriteFile(path string, data []byte, perm os.FileMode) error {
	return f.base.WriteFile(path, data, perm)
}
func (f *ErrorInjectingFS) Remove(path string) error                   { return f.base.Remove(path) }
func (f *ErrorInjectingFS) RemoveAll(path string) error                { return f.base.RemoveAll(path) }
func (f *ErrorInjectingFS) ReadDir(path string) ([]os.DirEntry, error) { return f.base.ReadDir(path) }
func (f *ErrorInjectingFS) CreateTemp(dir, pattern string) (*os.File, error) {
	return f.base.CreateTemp(dir, pattern)
}
func (f *ErrorInjectingFS) MkdirTemp(dir, pattern string) (string, error) {
	return f.base.MkdirTemp(dir, pattern)
}
func (f *ErrorInjectingFS) Rename(oldpath, newpath string) error {
	return f.base.Rename(oldpath, newpath)
}

func (f *ErrorInjectingFS) MkdirAll(path string, perm os.FileMode) error {
	if f.mkdirAllErr != nil {
		return f.mkdirAllErr
	}
	return f.base.MkdirAll(path, perm)
}

func (f *ErrorInjectingFS) OpenFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	if f.openFileErr != nil {
		return nil, f.openFileErr
	}
	return f.base.OpenFile(path, flag, perm)
}

func (f *ErrorInjectingFS) Symlink(oldname, newname string) error {
	if f.symlinkErr != nil {
		return f.symlinkErr
	}
	return f.base.Symlink(oldname, newname)
}

func (f *ErrorInjectingFS) Readlink(path string) (string, error) {
	if f.readlinkErr != nil {
		return "", f.readlinkErr
	}
	return f.base.Readlink(path)
}

func (f *ErrorInjectingFS) Link(oldname, newname string) error {
	if f.linkErr != nil {
		return f.linkErr
	}
	return f.base.Link(oldname, newname)
}

// --------------------------------------------------------------------------
// extractDirectory error tests
// --------------------------------------------------------------------------

func TestExtractDirectory_MkdirAllFails(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })

	restoreFS = &ErrorInjectingFS{
		base:        fakeFS,
		mkdirAllErr: fmt.Errorf("disk full"),
	}

	header := &tar.Header{
		Name:     "testdir",
		Mode:     0o755,
		Typeflag: tar.TypeDir,
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := extractDirectory("/some/path/testdir", header, logger)
	if err == nil || !strings.Contains(err.Error(), "create directory") {
		t.Fatalf("expected MkdirAll error, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// extractRegularFile error tests
// --------------------------------------------------------------------------

func TestExtractRegularFile_OpenFileFails(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })

	restoreFS = &ErrorInjectingFS{
		base:        fakeFS,
		openFileErr: fmt.Errorf("permission denied"),
	}

	header := &tar.Header{
		Name: "testfile.txt",
		Mode: 0o644,
		Size: 5,
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := extractRegularFile(nil, "/some/path/testfile.txt", header, logger)
	if err == nil || !strings.Contains(err.Error(), "create file") {
		t.Fatalf("expected OpenFile error, got: %v", err)
	}
}

func TestExtractRegularFile_CopyFails(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	restoreFS = osFS{}

	dir := t.TempDir()
	target := filepath.Join(dir, "testfile.txt")

	header := &tar.Header{
		Name: "testfile.txt",
		Mode: 0o644,
		Size: 100, // size larger than data provided
	}

	// Create a tar reader with incomplete data
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(header)
	_, _ = tw.Write([]byte("short")) // Only 5 bytes but header says 100
	tw.Close()

	tr := tar.NewReader(&buf)
	_, _ = tr.Next()

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := extractRegularFile(tr, target, header, logger)
	if err == nil || !strings.Contains(err.Error(), "write file content") {
		t.Fatalf("expected io.Copy error, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// extractSymlink error tests
// --------------------------------------------------------------------------

func TestExtractSymlink_TargetEscapesRoot(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}

	dir := t.TempDir()
	target := filepath.Join(dir, "link")

	header := &tar.Header{
		Name:     "link",
		Linkname: "../../../etc/passwd",
		Typeflag: tar.TypeSymlink,
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := extractSymlink(target, header, dir, logger)
	if err == nil || !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("expected escapes root error, got: %v", err)
	}
}

func TestExtractSymlink_SymlinkCreationFails(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })

	restoreFS = &ErrorInjectingFS{
		base:       fakeFS,
		symlinkErr: fmt.Errorf("symlink not supported"),
	}

	dir := fakeFS.Root
	target := filepath.Join(dir, "link")

	header := &tar.Header{
		Name:     "link",
		Linkname: "target.txt",
		Typeflag: tar.TypeSymlink,
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := extractSymlink(target, header, dir, logger)
	if err == nil || !strings.Contains(err.Error(), "create symlink") {
		t.Fatalf("expected symlink creation error, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// extractHardlink error tests
// --------------------------------------------------------------------------

func TestExtractHardlink_AbsoluteTargetRejectedError(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}

	header := &tar.Header{
		Name:     "link",
		Linkname: "/etc/passwd",
		Typeflag: tar.TypeLink,
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := extractHardlink("/tmp/link", header, "/tmp", logger)
	if err == nil || !strings.Contains(err.Error(), "absolute hardlink target not allowed") {
		t.Fatalf("expected absolute target error, got: %v", err)
	}
}

func TestExtractHardlink_LinkCreationFails(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })

	// Create target file
	targetFile := filepath.Join(fakeFS.Root, "target.txt")
	if err := os.WriteFile(targetFile, []byte("data"), 0o644); err != nil {
		t.Fatalf("create target: %v", err)
	}

	restoreFS = &ErrorInjectingFS{
		base:    fakeFS,
		linkErr: fmt.Errorf("link not supported"),
	}

	header := &tar.Header{
		Name:     "link",
		Linkname: "target.txt",
		Typeflag: tar.TypeLink,
	}

	linkPath := filepath.Join(fakeFS.Root, "link")
	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := extractHardlink(linkPath, header, fakeFS.Root, logger)
	if err == nil || !strings.Contains(err.Error(), "hardlink") {
		t.Fatalf("expected link creation error, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// extractPlainArchive error tests
// --------------------------------------------------------------------------

func TestExtractPlainArchive_MkdirAllFails(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })

	restoreFS = &ErrorInjectingFS{
		base:        fakeFS,
		mkdirAllErr: fmt.Errorf("disk full"),
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := extractPlainArchive(context.Background(), "/archive.tar", "/dest", logger, nil)
	if err == nil || !strings.Contains(err.Error(), "create destination directory") {
		t.Fatalf("expected MkdirAll error, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// runSafeClusterApply error tests
// --------------------------------------------------------------------------

func TestRunSafeClusterApply_ContextCanceled(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}

	// Create a context that's already canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	reader := bufio.NewReader(strings.NewReader(""))

	// The function should check context and return early
	err := runSafeClusterApply(ctx, reader, t.TempDir(), logger)
	if err == nil {
		t.Fatalf("expected context canceled error")
	}
}

func TestRunSafeClusterApply_NoVMConfigs(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})
	restoreFS = osFS{}

	// Create empty export root - no VM configs
	exportRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(exportRoot, "etc/pve"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Mock pvesh to not exist so it skips early
	restoreCmd = &FakeCommandRunner{}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	reader := bufio.NewReader(strings.NewReader(""))

	// This should return nil (pvesh not found)
	err := runSafeClusterApply(context.Background(), reader, exportRoot, logger)
	if err != nil {
		t.Fatalf("expected nil error when pvesh missing, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// confirmRestoreAction tests
// --------------------------------------------------------------------------

func TestConfirmRestoreAction_InvalidInput(t *testing.T) {
	cand := &decryptCandidate{
		DisplayBase: "test-backup",
		Manifest:    &backup.Manifest{CreatedAt: time.Now()},
	}

	// Input: invalid, then RESTORE
	reader := bufio.NewReader(strings.NewReader("invalid\nRESTORE\n"))

	err := confirmRestoreAction(context.Background(), reader, cand, "/")
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
}

func TestConfirmRestoreAction_Cancel(t *testing.T) {
	cand := &decryptCandidate{
		DisplayBase: "test-backup",
		Manifest:    &backup.Manifest{CreatedAt: time.Now()},
	}

	reader := bufio.NewReader(strings.NewReader("0\n"))

	err := confirmRestoreAction(context.Background(), reader, cand, "/")
	if !errors.Is(err, ErrRestoreAborted) {
		t.Fatalf("expected ErrRestoreAborted, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// sleepWithContext tests
// --------------------------------------------------------------------------

func TestSleepWithContext_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	sleepWithContext(ctx, 10*time.Second)
	elapsed := time.Since(start)

	// Should return immediately due to canceled context
	if elapsed > 100*time.Millisecond {
		t.Fatalf("expected immediate return on canceled context, elapsed: %v", elapsed)
	}
}

// --------------------------------------------------------------------------
// stopPVEClusterServices tests
// --------------------------------------------------------------------------

func TestStopPVEClusterServices_ServiceStillActive(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	origVerify := serviceVerifyTimeout
	origStatus := serviceStatusCheckTimeout
	origPoll := servicePollInterval
	serviceVerifyTimeout = 50 * time.Millisecond
	serviceStatusCheckTimeout = 10 * time.Millisecond
	servicePollInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		serviceVerifyTimeout = origVerify
		serviceStatusCheckTimeout = origStatus
		servicePollInterval = origPoll
	})

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"systemctl is-active pve-cluster": []byte("active"),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := stopPVEClusterServices(context.Background(), logger)
	if err == nil {
		t.Fatalf("expected error when service stays active")
	}
}

// --------------------------------------------------------------------------
// detectConfiguredZFSPools tests
// --------------------------------------------------------------------------

func TestDetectConfiguredZFSPools_GlobError(t *testing.T) {
	origFS := restoreFS
	origGlob := restoreGlob
	t.Cleanup(func() {
		restoreFS = origFS
		restoreGlob = origGlob
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	// Make glob return error
	restoreGlob = func(pattern string) ([]string, error) {
		return nil, fmt.Errorf("glob error")
	}

	pools := detectConfiguredZFSPools()
	// Should return empty slice on error
	if len(pools) != 0 {
		t.Fatalf("expected empty pools on glob error, got: %v", pools)
	}
}

// --------------------------------------------------------------------------
// combinePoolNames tests
// --------------------------------------------------------------------------

func TestCombinePoolNames_Deduplication(t *testing.T) {
	pools := combinePoolNames(
		[]string{"tank", "backup"},
		[]string{"backup", "rpool"},
	)

	if len(pools) != 3 {
		t.Fatalf("expected 3 unique pools, got: %v", pools)
	}

	expected := map[string]bool{"tank": true, "backup": true, "rpool": true}
	for _, p := range pools {
		if !expected[p] {
			t.Fatalf("unexpected pool: %s", p)
		}
	}
}

// --------------------------------------------------------------------------
// sanitizeRestoreEntryTarget extra tests
// --------------------------------------------------------------------------

func TestSanitizeRestoreEntryTarget_InvalidNameSlashes(t *testing.T) {
	_, _, err := sanitizeRestoreEntryTarget("/dest", "///")
	if err == nil || !strings.Contains(err.Error(), "invalid archive entry name") {
		t.Fatalf("expected invalid name error, got: %v", err)
	}
}

func TestSanitizeRestoreEntryTarget_ParentDirectoryPath(t *testing.T) {
	_, _, err := sanitizeRestoreEntryTarget("/dest", "../etc/passwd")
	if err == nil || !strings.Contains(err.Error(), "illegal path") {
		t.Fatalf("expected illegal path error, got: %v", err)
	}
}

func TestSanitizeRestoreEntryTarget_DoubleDotMidPath(t *testing.T) {
	_, _, err := sanitizeRestoreEntryTarget("/dest", "foo/../../../etc/passwd")
	if err == nil || !strings.Contains(err.Error(), "illegal path") {
		t.Fatalf("expected illegal path error, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// extractSelectiveArchive tests
// --------------------------------------------------------------------------

func TestExtractSelectiveArchive_MkdirAllFails(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })

	restoreFS = &ErrorInjectingFS{
		base:        fakeFS,
		mkdirAllErr: fmt.Errorf("disk full"),
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	_, err := extractSelectiveArchive(context.Background(), "/archive.tar", "/dest", nil, RestoreModeFull, logger)
	if err == nil || !strings.Contains(err.Error(), "create destination directory") {
		t.Fatalf("expected MkdirAll error, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// runFullRestore tests
// --------------------------------------------------------------------------

func TestRunFullRestore_ExtractError(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })

	// Create an invalid archive
	archivePath := filepath.Join(fakeFS.Root, "bad.tar")
	if err := os.WriteFile(archivePath, []byte("not a tar file"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	restoreFS = fakeFS

	cand := &decryptCandidate{
		DisplayBase: "test-backup",
		Manifest:    &backup.Manifest{CreatedAt: time.Now()},
	}
	prepared := &preparedBundle{ArchivePath: archivePath}

	reader := bufio.NewReader(strings.NewReader("RESTORE\n"))
	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)

	err := runFullRestore(context.Background(), reader, cand, prepared, fakeFS.Root, logger, false)
	if err == nil {
		t.Fatalf("expected error from bad archive")
	}
}

// --------------------------------------------------------------------------
// extractSymlink edge case tests
// --------------------------------------------------------------------------

func TestExtractSymlink_RelativePathResolution(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}

	dir := t.TempDir()

	// Create subdir
	subdir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create target file
	targetFile := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(targetFile, []byte("content"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Create symlink from subdir/link -> ../target.txt
	linkPath := filepath.Join(subdir, "link")
	header := &tar.Header{
		Name:     "subdir/link",
		Linkname: "../target.txt",
		Typeflag: tar.TypeSymlink,
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := extractSymlink(linkPath, header, dir, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify symlink was created
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "../target.txt" {
		t.Fatalf("expected target ../target.txt, got: %s", target)
	}
}

// --------------------------------------------------------------------------
// createDecompressionReader tests
// --------------------------------------------------------------------------

func TestCreateDecompressionReader_UnknownExtension(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}

	// Create a file with unknown extension
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.unknown")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	file, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()

	// Should return error for unknown extension
	_, err = createDecompressionReader(context.Background(), file, filePath)
	if err == nil || !strings.Contains(err.Error(), "unsupported archive format") {
		t.Fatalf("expected unsupported archive format error, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// unmountEtcPVE tests
// --------------------------------------------------------------------------

func TestUnmountEtcPVE_NotMounted(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"findmnt -n -o TARGET /etc/pve": []byte(""),
		},
		Errors: map[string]error{
			"findmnt -n -o TARGET /etc/pve": fmt.Errorf("not found"),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := unmountEtcPVE(context.Background(), logger)
	if err != nil {
		t.Fatalf("expected nil when not mounted, got: %v", err)
	}
}

func TestUnmountEtcPVE_MountedAndUnmounts(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"findmnt -n -o TARGET /etc/pve": []byte("/etc/pve\n"),
			"umount /etc/pve":               []byte(""),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := unmountEtcPVE(context.Background(), logger)
	if err != nil {
		t.Fatalf("expected nil when unmount succeeds, got: %v", err)
	}

	// Check that umount was called
	found := false
	for _, call := range fake.Calls {
		if call == "umount /etc/pve" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected umount to be called, got: %v", fake.Calls)
	}
}

// --------------------------------------------------------------------------
// startPBSServices tests
// --------------------------------------------------------------------------

func TestStartPBSServices_SystemctlMissing(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Errors: map[string]error{
			"which systemctl": fmt.Errorf("not found"),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := startPBSServices(context.Background(), logger)
	if err == nil || !strings.Contains(err.Error(), "systemctl not available") {
		t.Fatalf("expected systemctl missing error, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// execCommand tests
// --------------------------------------------------------------------------

func TestExecCommand_CommandFails(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Errors: map[string]error{
			"nonexistent-cmd": fmt.Errorf("command not found"),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := execCommand(context.Background(), logger, 5*time.Second, "nonexistent-cmd")
	if err == nil {
		t.Fatalf("expected error when command fails")
	}
}

// --------------------------------------------------------------------------
// applyVMConfigs tests
// --------------------------------------------------------------------------

func TestApplyVMConfigs_ContextCanceled(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })
	restoreCmd = &FakeCommandRunner{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	entries := []vmEntry{
		{VMID: "100", Kind: "qemu", Path: "/tmp/100.conf"},
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	applied, failed := applyVMConfigs(ctx, entries, logger)

	// Should return early due to canceled context
	if applied != 0 || failed != 0 {
		t.Fatalf("expected (0,0) for canceled context, got (%d,%d)", applied, failed)
	}
}

func TestApplyVMConfigs_SuccessfulApply(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{},
	}
	restoreCmd = fake

	dir := t.TempDir()
	configPath := filepath.Join(dir, "100.conf")
	if err := os.WriteFile(configPath, []byte("name: test-vm"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	entries := []vmEntry{
		{VMID: "100", Kind: "qemu", Path: configPath},
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	applied, failed := applyVMConfigs(context.Background(), entries, logger)

	if applied != 1 || failed != 0 {
		t.Fatalf("expected (1,0), got (%d,%d)", applied, failed)
	}
}

// --------------------------------------------------------------------------
// extractDirectory success path test
// --------------------------------------------------------------------------

func TestExtractDirectory_SuccessWithTimestamps(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}

	dir := t.TempDir()
	target := filepath.Join(dir, "newdir")

	header := &tar.Header{
		Name:     "newdir",
		Mode:     0o755,
		Typeflag: tar.TypeDir,
		ModTime:  time.Now().Add(-time.Hour),
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := extractDirectory(target, header, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory")
	}
}

// --------------------------------------------------------------------------
// extractRegularFile success path test
// --------------------------------------------------------------------------

func TestExtractRegularFile_Success(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}

	dir := t.TempDir()
	target := filepath.Join(dir, "file.txt")

	content := []byte("hello world")
	header := &tar.Header{
		Name:    "file.txt",
		Mode:    0o644,
		Size:    int64(len(content)),
		ModTime: time.Now().Add(-time.Hour),
	}

	// Create tar with proper content
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(header); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	tw.Close()

	tr := tar.NewReader(&buf)
	if _, err := tr.Next(); err != nil {
		t.Fatalf("read header: %v", err)
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := extractRegularFile(tr, target, header, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("expected 'hello world', got: %q", string(data))
	}
}

// --------------------------------------------------------------------------
// applyStorageCfg success path test
// --------------------------------------------------------------------------

func TestApplyStorageCfg_MultipleBlocks(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})
	restoreFS = osFS{}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "storage.cfg")
	content := `storage: local
    type dir
    path /var/lib/vz

storage: backup
    type nfs
    server 10.0.0.1
    export /backup
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	fake := &FakeCommandRunner{}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	applied, failed, err := applyStorageCfg(context.Background(), cfgPath, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applied != 2 || failed != 0 {
		t.Fatalf("expected (2,0), got (%d,%d)", applied, failed)
	}
}

// --------------------------------------------------------------------------
// stopServiceWithRetries tests
// --------------------------------------------------------------------------

func TestStopServiceWithRetries_ImmediateSuccess(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"systemctl is-active test.service": []byte("inactive"),
		},
		Errors: map[string]error{
			"systemctl is-active test.service": fmt.Errorf("inactive"),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := stopServiceWithRetries(context.Background(), logger, "test.service")
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// startServiceWithRetries tests
// --------------------------------------------------------------------------

func TestStartServiceWithRetries_ImmediateSuccess(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := startServiceWithRetries(context.Background(), logger, "test.service")
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// extractArchiveNative tests
// --------------------------------------------------------------------------

func TestExtractArchiveNative_OpenError(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := extractArchiveNative(context.Background(), "/nonexistent/archive.tar", "/tmp", logger, nil, RestoreModeFull, nil, "", nil)
	if err == nil || !strings.Contains(err.Error(), "open archive") {
		t.Fatalf("expected open error, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// promptClusterRestoreMode tests
// --------------------------------------------------------------------------

func TestPromptClusterRestoreMode_Exit(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("0\n"))

	choice, err := promptClusterRestoreMode(context.Background(), reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if choice != 0 {
		t.Fatalf("expected choice 0 for exit, got: %d", choice)
	}
}

func TestPromptClusterRestoreMode_SafeMode(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("1\n"))

	choice, err := promptClusterRestoreMode(context.Background(), reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if choice != 1 {
		t.Fatalf("expected choice 1 for safe mode, got: %d", choice)
	}
}

func TestPromptClusterRestoreMode_RecoveryMode(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("2\n"))

	choice, err := promptClusterRestoreMode(context.Background(), reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if choice != 2 {
		t.Fatalf("expected choice 2 for recovery mode, got: %d", choice)
	}
}
