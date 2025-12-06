package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/pkg/utils"
)

func newBufferedTestLogger() (*logging.Logger, *bytes.Buffer) {
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	return logger, buf
}

func TestEnsureDirectoryExistsAlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	logger, buf := newBufferedTestLogger()

	ensureDirectoryExists(logger, "Backup directory", dir)

	output := buf.String()
	expected := "Backup directory exists: " + dir
	if !strings.Contains(output, expected) {
		t.Fatalf("expected log to contain %q, got %s", expected, output)
	}
	if strings.Contains(output, "created:") {
		t.Fatalf("did not expect creation log for existing directory; output=%s", output)
	}
}

func TestEnsureDirectoryExistsCreatesMissingDir(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "cloud", "logs")
	logger, buf := newBufferedTestLogger()

	ensureDirectoryExists(logger, "Cloud log directory", target)

	if !utils.DirExists(target) {
		t.Fatalf("expected directory %s to be created", target)
	}

	output := buf.String()
	if !strings.Contains(output, "Cloud log directory created: "+target) {
		t.Fatalf("expected creation log for %s, got %s", target, output)
	}
	if !strings.Contains(output, "Cloud log directory not found: "+target) {
		t.Fatalf("expected missing warning for %s, got %s", target, output)
	}
}

func TestEnsureDirectoryExistsLogsFailure(t *testing.T) {
	root := t.TempDir()
	conflict := filepath.Join(root, "conflict")
	if err := os.WriteFile(conflict, []byte("x"), 0o600); err != nil {
		t.Fatalf("failed to prepare conflict file: %v", err)
	}

	logger, buf := newBufferedTestLogger()
	ensureDirectoryExists(logger, "Cloud log directory", conflict)

	output := buf.String()
	if !strings.Contains(output, "Cloud log directory not found: "+conflict) {
		t.Fatalf("expected missing warning for %s, got %s", conflict, output)
	}
	if !strings.Contains(output, "Failed to create "+conflict) {
		t.Fatalf("expected failure log for %s, got %s", conflict, output)
	}
	if utils.DirExists(conflict) {
		t.Fatalf("conflict path %s unexpectedly became a directory", conflict)
	}
}
