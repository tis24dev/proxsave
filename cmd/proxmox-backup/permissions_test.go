package main

import (
	"bytes"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/config"
	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

func TestApplyBackupPermissionsSkipsMissingDirectories(t *testing.T) {
	currentUser, err := user.Current()
	if err != nil {
		t.Fatalf("failed to lookup current user: %v", err)
	}
	group, err := user.LookupGroupId(currentUser.Gid)
	if err != nil {
		t.Fatalf("failed to lookup current group: %v", err)
	}

	existingDir := t.TempDir()
	missingDir := filepath.Join(t.TempDir(), "does-not-exist")

	cfg := &config.Config{
		BackupUser:       currentUser.Username,
		BackupGroup:      group.Name,
		BackupPath:       existingDir,
		LogPath:          missingDir,
		SecondaryPath:    "",
		SecondaryLogPath: "",
	}

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)

	if err := applyBackupPermissions(cfg, logger); err != nil {
		t.Fatalf("applyBackupPermissions returned error: %v", err)
	}

	output := buf.String()
	expected := fmt.Sprintf("Permissions: directory does not exist: %s", missingDir)
	if !strings.Contains(output, expected) {
		t.Fatalf("expected skip log for missing directory; log output:\n%s", output)
	}
	if !strings.Contains(output, fmt.Sprintf("Applying permissions on path: %s", existingDir)) {
		t.Fatalf("expected permissions applied to existing directory; log output:\n%s", output)
	}
}

func TestApplyBackupPermissionsSkipsNonDirectoryPaths(t *testing.T) {
	currentUser, err := user.Current()
	if err != nil {
		t.Fatalf("failed to lookup current user: %v", err)
	}
	group, err := user.LookupGroupId(currentUser.Gid)
	if err != nil {
		t.Fatalf("failed to lookup current group: %v", err)
	}

	existingDir := t.TempDir()
	tmpFile, err := os.CreateTemp(t.TempDir(), "not-a-dir")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	_ = tmpFile.Close()

	cfg := &config.Config{
		BackupUser:       currentUser.Username,
		BackupGroup:      group.Name,
		BackupPath:       existingDir,
		LogPath:          tmpFile.Name(),
		SecondaryPath:    "",
		SecondaryLogPath: "",
	}

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)

	if err := applyBackupPermissions(cfg, logger); err != nil {
		t.Fatalf("applyBackupPermissions returned error: %v", err)
	}

	output := buf.String()
	expected := fmt.Sprintf("Permissions: path is not a directory, skipping: %s", tmpFile.Name())
	if !strings.Contains(output, expected) {
		t.Fatalf("expected skip log for non-directory path; log output:\n%s", output)
	}
}
