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

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"which systemctl": {},
		},
		Errors: map[string]error{
			"systemctl stop proxmox-backup-proxy": fmt.Errorf("fail-proxy"),
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	err := stopPBSServices(context.Background(), logger)
	if err == nil || !strings.Contains(err.Error(), "fail-proxy") {
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
			"which systemctl":                     {},
			"systemctl stop proxmox-backup-proxy": {},
			"systemctl stop proxmox-backup":       {},
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	if err := stopPBSServices(context.Background(), logger); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(fake.Calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(fake.Calls))
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
			"systemctl start pvedaemon": fmt.Errorf("fail daemon"),
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
