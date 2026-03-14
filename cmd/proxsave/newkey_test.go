package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func captureNewKeyStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()

	_ = w.Close()
	os.Stdout = orig
	<-done
	return buf.String()
}

func TestLogNewKeySuccessWithoutBootstrapFallsBackToStdout(t *testing.T) {
	recipientPath := filepath.Join("/tmp", "identity", "age", "recipient.txt")

	output := captureNewKeyStdout(t, func() {
		logNewKeySuccess(recipientPath, nil)
	})

	if !strings.Contains(output, "✓ New AGE recipient(s) generated and saved to "+recipientPath) {
		t.Fatalf("expected recipient success message, got %q", output)
	}
	if !strings.Contains(output, "IMPORTANT: Keep your passphrase/private key offline and secure!") {
		t.Fatalf("expected security reminder, got %q", output)
	}
}

func TestLogNewKeySuccessWithBootstrapUsesBootstrapLogger(t *testing.T) {
	recipientPath := filepath.Join("/tmp", "identity", "age", "recipient.txt")
	bootstrap := logging.NewBootstrapLogger()
	bootstrap.SetLevel(types.LogLevelInfo)

	var mirrorBuf bytes.Buffer
	mirror := logging.New(types.LogLevelDebug, false)
	mirror.SetOutput(&mirrorBuf)
	bootstrap.SetMirrorLogger(mirror)

	output := captureNewKeyStdout(t, func() {
		logNewKeySuccess(recipientPath, bootstrap)
	})

	if !strings.Contains(output, "✓ New AGE recipient(s) generated and saved to "+recipientPath) {
		t.Fatalf("expected bootstrap stdout success message, got %q", output)
	}
	if !strings.Contains(output, "IMPORTANT: Keep your passphrase/private key offline and secure!") {
		t.Fatalf("expected bootstrap stdout security reminder, got %q", output)
	}

	mirrorOutput := mirrorBuf.String()
	if !strings.Contains(mirrorOutput, "New AGE recipient(s) generated and saved to "+recipientPath) {
		t.Fatalf("expected mirror logger success message, got %q", mirrorOutput)
	}
	if !strings.Contains(mirrorOutput, "IMPORTANT: Keep your passphrase/private key offline and secure!") {
		t.Fatalf("expected mirror logger security reminder, got %q", mirrorOutput)
	}
}

func TestLoadNewKeyConfigUsesConfiguredRecipientFile(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "env", "backup.env")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(configPath), err)
	}

	customPath := filepath.Join(baseDir, "custom", "recipient.txt")
	content := "BASE_DIR=" + baseDir + "\nENCRYPT_ARCHIVE=false\nAGE_RECIPIENT_FILE=" + customPath + "\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", configPath, err)
	}

	cfg, recipientPath, err := loadNewKeyConfig(configPath, baseDir)
	if err != nil {
		t.Fatalf("loadNewKeyConfig error: %v", err)
	}
	if recipientPath != customPath {
		t.Fatalf("recipientPath=%q; want %q", recipientPath, customPath)
	}
	if cfg == nil {
		t.Fatalf("expected config")
	}
	if cfg.BaseDir != baseDir {
		t.Fatalf("BaseDir=%q; want %q", cfg.BaseDir, baseDir)
	}
	if cfg.ConfigPath != configPath {
		t.Fatalf("ConfigPath=%q; want %q", cfg.ConfigPath, configPath)
	}
	if cfg.AgeRecipientFile != customPath {
		t.Fatalf("AgeRecipientFile=%q; want %q", cfg.AgeRecipientFile, customPath)
	}
	if !cfg.EncryptArchive {
		t.Fatalf("EncryptArchive=false; want true")
	}
}

func TestLoadNewKeyConfigFailsForInvalidExistingConfig(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "env", "backup.env")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(configPath), err)
	}

	content := "BASE_DIR=" + baseDir + "\nCUSTOM_BACKUP_PATHS=\"\nunterminated\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", configPath, err)
	}

	_, _, err := loadNewKeyConfig(configPath, baseDir)
	if err == nil {
		t.Fatalf("expected loadNewKeyConfig to fail for invalid config")
	}
	if !strings.Contains(err.Error(), "load configuration for newkey") {
		t.Fatalf("expected wrapped configuration load error, got %v", err)
	}
}
