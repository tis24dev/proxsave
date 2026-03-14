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
