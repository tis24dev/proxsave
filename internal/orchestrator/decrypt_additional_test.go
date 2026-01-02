package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"filippo.io/age"

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

func TestPrepareDecryptedBackup_SuccessPlain(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	dir := t.TempDir()
	writeRawBackup(t, dir, "backup.tar")

	cfg := &config.Config{
		BackupPath: dir,
	}

	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	reader := bufio.NewReader(strings.NewReader("1\n1\n"))
	cand, prepared, err := prepareDecryptedBackup(context.Background(), reader, cfg, logger, "1.2.3", false)
	if err != nil {
		t.Fatalf("prepareDecryptedBackup error: %v", err)
	}
	if cand == nil || prepared == nil {
		t.Fatalf("expected candidate and prepared bundle to be non-nil")
	}
	t.Cleanup(prepared.Cleanup)

	if cand.Source != sourceRaw {
		t.Fatalf("candidate Source=%q; want %q", cand.Source, sourceRaw)
	}
	if prepared.Manifest.EncryptionMode != "none" {
		t.Fatalf("prepared manifest EncryptionMode=%q; want %q", prepared.Manifest.EncryptionMode, "none")
	}
	if prepared.Manifest.ScriptVersion != "1.2.3" {
		t.Fatalf("prepared manifest ScriptVersion=%q; want %q", prepared.Manifest.ScriptVersion, "1.2.3")
	}
	if prepared.ArchivePath == "" {
		t.Fatalf("prepared bundle ArchivePath is empty")
	}
	if _, err := os.Stat(prepared.ArchivePath); err != nil {
		t.Fatalf("expected staged archive to exist, stat error: %v", err)
	}

	prepared.Cleanup()
	if _, err := os.Stat(prepared.ArchivePath); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected staged archive to be removed by Cleanup, stat err=%v", err)
	}
}

func TestDecryptArchiveWithPrompts_UsesIdentityInputs(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	dir := t.TempDir()
	encryptedPath := filepath.Join(dir, "archive.age")
	outputPath := filepath.Join(dir, "archive.tar")
	plaintext := []byte("secret payload")

	correctID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}
	wrongID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	outFile, err := os.Create(encryptedPath)
	if err != nil {
		t.Fatalf("Create encrypted file: %v", err)
	}
	enc, err := age.Encrypt(outFile, correctID.Recipient())
	if err != nil {
		_ = outFile.Close()
		t.Fatalf("age.Encrypt: %v", err)
	}
	if _, err := enc.Write(plaintext); err != nil {
		_ = enc.Close()
		_ = outFile.Close()
		t.Fatalf("write encrypted payload: %v", err)
	}
	if err := enc.Close(); err != nil {
		_ = outFile.Close()
		t.Fatalf("close age writer: %v", err)
	}
	if err := outFile.Close(); err != nil {
		t.Fatalf("close encrypted file: %v", err)
	}

	origReadPassword := readPassword
	t.Cleanup(func() { readPassword = origReadPassword })

	var mu sync.Mutex
	inputs := [][]byte{
		[]byte(wrongID.String()),
		[]byte(correctID.String()),
	}
	readPassword = func(fd int) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(inputs) == 0 {
			return nil, io.EOF
		}
		next := append([]byte(nil), inputs[0]...)
		inputs = inputs[1:]
		return next, nil
	}

	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	err = decryptArchiveWithPrompts(context.Background(), bufio.NewReader(strings.NewReader("")), encryptedPath, outputPath, logger)
	if err != nil {
		t.Fatalf("decryptArchiveWithPrompts error: %v", err)
	}

	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile output: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("decrypted payload=%q; want %q", string(got), string(plaintext))
	}

	// Ensure we did retry at least once (wrong identity first).
	mu.Lock()
	remaining := len(inputs)
	mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected all identity inputs to be consumed, remaining=%d", remaining)
	}

	// Cleanup should remove any created output if desired by callers; ensure it exists here.
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("expected output file to exist, stat error: %v", err)
	}

	// Best-effort extra assertion: output should be non-empty.
	if len(got) == 0 {
		t.Fatalf("expected non-empty decrypted output")
	}
}
