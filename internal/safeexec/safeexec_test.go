package safeexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCommandContextAllowlist(t *testing.T) {
	if _, err := CommandContext(context.Background(), "rclone", "lsf", "remote:"); err != nil {
		t.Fatalf("CommandContext allowed command error: %v", err)
	}
	if _, err := CommandContext(context.Background(), "not-a-proxsave-command"); !errors.Is(err, ErrCommandNotAllowed) {
		t.Fatalf("CommandContext unknown command error = %v, want ErrCommandNotAllowed", err)
	}
	if _, err := CommandContext(context.Background(), "/bin/sh"); !errors.Is(err, ErrCommandNotAllowed) {
		t.Fatalf("CommandContext path command error = %v, want ErrCommandNotAllowed", err)
	}
}

func TestValidateTrustedExecutablePath(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "proxsave")
	if err := os.WriteFile(execPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTrustedExecutablePath(execPath); err != nil {
		t.Fatalf("ValidateTrustedExecutablePath valid error: %v", err)
	}

	if err := ValidateTrustedExecutablePath("relative"); err == nil {
		t.Fatalf("expected relative path to be rejected")
	}

	worldWritable := filepath.Join(dir, "ww")
	if err := os.WriteFile(worldWritable, []byte("#!/bin/sh\nexit 0\n"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(worldWritable, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTrustedExecutablePath(worldWritable); err == nil {
		t.Fatalf("expected world-writable executable to be rejected")
	}
}

func TestValidateRcloneRemoteName(t *testing.T) {
	valid := []string{"remote", "s3backup_01", "gdrive-prod"}
	for _, name := range valid {
		if err := ValidateRcloneRemoteName(name); err != nil {
			t.Fatalf("ValidateRcloneRemoteName(%q) error: %v", name, err)
		}
	}

	invalid := []string{"", "-remote", "bad remote", "bad/remote", "bad:remote", "bad\nremote"}
	for _, name := range invalid {
		if err := ValidateRcloneRemoteName(name); err == nil {
			t.Fatalf("ValidateRcloneRemoteName(%q) expected error", name)
		}
	}
}

func TestValidateRemoteRelativePath(t *testing.T) {
	valid := []string{"", "tenant/a", "/tenant/a/", "tenant with spaces/a"}
	for _, value := range valid {
		if err := ValidateRemoteRelativePath(value, "path"); err != nil {
			t.Fatalf("ValidateRemoteRelativePath(%q) error: %v", value, err)
		}
	}

	invalid := []string{"../escape", "tenant/../../escape", "bad\npath"}
	for _, value := range invalid {
		if err := ValidateRemoteRelativePath(value, "path"); err == nil {
			t.Fatalf("ValidateRemoteRelativePath(%q) expected error", value)
		}
	}
}

func TestProcPath(t *testing.T) {
	got, err := ProcPath(123, "status")
	if err != nil {
		t.Fatalf("ProcPath valid error: %v", err)
	}
	if got != "/proc/123/status" {
		t.Fatalf("ProcPath = %q", got)
	}
	if _, err := ProcPath(0, "status"); err == nil {
		t.Fatalf("expected pid 0 to be rejected")
	}
	if _, err := ProcPath(123, "../status"); err == nil {
		t.Fatalf("expected unsupported leaf to be rejected")
	}
}
