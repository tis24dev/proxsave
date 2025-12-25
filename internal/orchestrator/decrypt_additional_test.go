package orchestrator

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestPromptDestinationDir(t *testing.T) {
	ctx := context.Background()

	t.Run("defaults to ./decrypt when config missing", func(t *testing.T) {
		reader := bufio.NewReader(strings.NewReader("\n"))
		got, err := promptDestinationDir(ctx, reader, nil)
		if err != nil {
			t.Fatalf("promptDestinationDir error: %v", err)
		}
		if got != "decrypt" {
			t.Fatalf("promptDestinationDir() = %q; want %q", got, "decrypt")
		}
	})

	t.Run("defaults under config base dir", func(t *testing.T) {
		reader := bufio.NewReader(strings.NewReader("\n"))
		cfg := &config.Config{BaseDir: "/opt/proxsave"}
		got, err := promptDestinationDir(ctx, reader, cfg)
		if err != nil {
			t.Fatalf("promptDestinationDir error: %v", err)
		}
		if got != "/opt/proxsave/decrypt" {
			t.Fatalf("promptDestinationDir() = %q; want %q", got, "/opt/proxsave/decrypt")
		}
	})

	t.Run("returns cleaned user input", func(t *testing.T) {
		reader := bufio.NewReader(strings.NewReader(" ../out \n"))
		got, err := promptDestinationDir(ctx, reader, nil)
		if err != nil {
			t.Fatalf("promptDestinationDir error: %v", err)
		}
		if got != "../out" {
			t.Fatalf("promptDestinationDir() = %q; want %q", got, "../out")
		}
	})
}

func TestDownloadRcloneBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires sh/exec semantics")
	}

	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	remoteDir := t.TempDir()
	remotePath := filepath.Join(remoteDir, "backup.bundle.tar")
	payload := []byte("bundle-data")
	if err := os.WriteFile(remotePath, payload, 0o640); err != nil {
		t.Fatalf("write remote file: %v", err)
	}

	// Fake rclone binary that supports `copyto SRC DST`.
	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "rclone")
	script := "#!/bin/sh\nset -e\nif [ \"$1\" != \"copyto\" ]; then exit 2; fi\ncp \"$2\" \"$3\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}

	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)

	tmpPath, cleanup, err := downloadRcloneBackup(context.Background(), remotePath, logger)
	if err != nil {
		t.Fatalf("downloadRcloneBackup error: %v", err)
	}
	if cleanup == nil {
		t.Fatalf("expected cleanup func")
	}
	if !strings.Contains(tmpPath, filepath.Join(string(os.PathSeparator), "tmp", "proxsave")) {
		t.Fatalf("tmpPath = %q; want under /tmp/proxsave", tmpPath)
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatalf("read tmpPath: %v", err)
	}
	if string(data) != string(payload) {
		t.Fatalf("downloaded content = %q; want %q", string(data), string(payload))
	}

	cleanup()
	if _, err := os.Stat(tmpPath); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected downloaded temp file to be removed, stat err=%v", err)
	}
}
