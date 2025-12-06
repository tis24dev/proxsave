package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
			"systemctl stop --no-block proxmox-backup-proxy":                          fmt.Errorf("fail-proxy"),
			"systemctl stop proxmox-backup-proxy":                                     fmt.Errorf("fail-blocking"),
			"systemctl kill --signal=SIGTERM --kill-who=all proxmox-backup-proxy":     fmt.Errorf("kill-term"),
			"systemctl kill --signal=SIGKILL --kill-who=all proxmox-backup-proxy":     fmt.Errorf("kill-9"),
			"systemctl is-active proxmox-backup":                                      fmt.Errorf("inactive"),
			"systemctl is-active proxmox-backup-proxy":                                fmt.Errorf("inactive"),
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
