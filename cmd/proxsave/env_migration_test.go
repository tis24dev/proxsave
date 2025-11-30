package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

func captureStdout(t *testing.T, fn func()) string {
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

func TestPrintMigratedKeys(t *testing.T) {
	summary := &config.EnvMigrationSummary{
		MigratedKeys: map[string]string{
			"NEW":   "legacy_new",
			"ALPHA": "legacy_alpha",
		},
	}
	logger := logging.NewBootstrapLogger()

	output := captureStdout(t, func() {
		printMigratedKeys(summary, logger)
	})

	if !strings.Contains(output, "Mapped legacy keys") {
		t.Fatalf("expected heading in output, got %q", output)
	}
	if strings.Index(output, "  ALPHA <- legacy_alpha") > strings.Index(output, "  NEW <- legacy_new") {
		t.Fatalf("expected entries sorted alphabetically, got %q", output)
	}
}

func TestPrintMigratedKeysEmpty(t *testing.T) {
	logger := logging.NewBootstrapLogger()
	output := captureStdout(t, func() {
		printMigratedKeys(&config.EnvMigrationSummary{}, logger)
	})
	if !strings.Contains(output, "No legacy keys matched") {
		t.Fatalf("expected fallback message, got %q", output)
	}
}

func TestPrintUnmappedKeys(t *testing.T) {
	summary := &config.EnvMigrationSummary{
		UnmappedLegacyKeys: []string{"BETA", "ALPHA"},
	}
	logger := logging.NewBootstrapLogger()
	output := captureStdout(t, func() {
		printUnmappedKeys(summary, logger)
	})
	if !strings.Contains(output, "Legacy keys requiring manual review (2):") {
		t.Fatalf("unexpected output: %q", output)
	}
	if strings.Index(output, "  ALPHA") > strings.Index(output, "  BETA") {
		t.Fatalf("expected alphabetical order, got %q", output)
	}
}

func TestEnsureLegacyFile(t *testing.T) {
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "backup.env")
	if err := os.WriteFile(file, []byte("FOO=bar"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	if err := ensureLegacyFile(file); err != nil {
		t.Fatalf("ensureLegacyFile valid file: %v", err)
	}
	if err := ensureLegacyFile(filepath.Join(tmpDir, "missing.env")); err == nil {
		t.Fatalf("expected error for missing file")
	}
	if err := ensureLegacyFile(tmpDir); err == nil {
		t.Fatalf("expected error for directory path")
	}
}

func TestResolveLegacyEnvPathWithArg(t *testing.T) {
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "legacy.env")
	if err := os.WriteFile(file, []byte("KEY=value"), 0o600); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}

	logger := logging.NewBootstrapLogger()
	args := &cli.Args{LegacyEnvPath: file}
	path, err := resolveLegacyEnvPath(context.Background(), args, logger)
	if err != nil {
		t.Fatalf("resolveLegacyEnvPath returned error: %v", err)
	}
	if path != file {
		t.Fatalf("expected %s, got %s", file, path)
	}
}
