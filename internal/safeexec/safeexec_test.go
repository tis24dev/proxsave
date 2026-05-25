package safeexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestCommandContextAllowlist(t *testing.T) {
	ctx := context.Background()
	allowedCommands := make([]string, 0, len(allowedCommandFactories))
	for name := range allowedCommandFactories {
		allowedCommands = append(allowedCommands, name)
	}
	sort.Strings(allowedCommands)

	for _, command := range allowedCommands {
		cmd, err := CommandContext(ctx, command, "--version")
		if err != nil {
			t.Fatalf("CommandContext(%q) allowed command error: %v", command, err)
		}
		if got, want := cmd.Args[0], command; got != want {
			t.Fatalf("CommandContext(%q) cmd.Args[0]=%q; want %q", command, got, want)
		}
	}
	if _, err := CommandContext(ctx, "not-a-proxsave-command"); !errors.Is(err, ErrCommandNotAllowed) {
		t.Fatalf("CommandContext unknown command error = %v, want ErrCommandNotAllowed", err)
	}
	if _, err := CommandContext(ctx, "/bin/sh"); !errors.Is(err, ErrCommandNotAllowed) {
		t.Fatalf("CommandContext path command error = %v, want ErrCommandNotAllowed", err)
	}
	invalidNames := []string{"", " echo", "echo ", `bad\name`}
	for _, name := range invalidNames {
		if _, err := CommandContext(ctx, name); !errors.Is(err, ErrCommandNotAllowed) {
			t.Fatalf("CommandContext(%q) malformed-name error = %v, want ErrCommandNotAllowed", name, err)
		}
	}
}

func TestCommandWrappers(t *testing.T) {
	ctx := context.Background()

	out, err := Output(ctx, "echo", "safeexec-output")
	if err != nil {
		t.Fatalf("Output valid command error: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "safeexec-output" {
		t.Fatalf("Output()=%q; want %q", got, "safeexec-output")
	}

	combined, err := CombinedOutput(ctx, "echo", "safeexec-combined")
	if err != nil {
		t.Fatalf("CombinedOutput valid command error: %v", err)
	}
	if got := strings.TrimSpace(string(combined)); got != "safeexec-combined" {
		t.Fatalf("CombinedOutput()=%q; want %q", got, "safeexec-combined")
	}

	if _, err := Output(ctx, "false"); err == nil {
		t.Fatalf("Output(false) expected exit error")
	}
	if _, err := Output(ctx, "not-allowed-command"); !errors.Is(err, ErrCommandNotAllowed) {
		t.Fatalf("Output disallowed command error = %v, want ErrCommandNotAllowed", err)
	}
	if _, err := CombinedOutput(ctx, "not-allowed-command"); !errors.Is(err, ErrCommandNotAllowed) {
		t.Fatalf("CombinedOutput disallowed command error = %v, want ErrCommandNotAllowed", err)
	}
}

func TestTrustedCommandContext(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "trusted")
	if err := os.WriteFile(execPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	cmd, err := TrustedCommandContext(context.Background(), execPath, "arg1", "arg2")
	if err != nil {
		t.Fatalf("TrustedCommandContext valid path error: %v", err)
	}
	if got, want := cmd.Path, execPath; got != want {
		t.Fatalf("TrustedCommandContext Path=%q; want %q", got, want)
	}
	if len(cmd.Args) != 3 || cmd.Args[1] != "arg1" || cmd.Args[2] != "arg2" {
		t.Fatalf("TrustedCommandContext Args=%v; want [trusted arg1 arg2]", cmd.Args)
	}

	if _, err := TrustedCommandContext(context.Background(), "relative/path"); err == nil {
		t.Fatalf("TrustedCommandContext(relative) expected error")
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

	if err := ValidateTrustedExecutablePath("   "); err == nil {
		t.Fatalf("expected empty path to be rejected")
	}

	if err := ValidateTrustedExecutablePath("relative"); err == nil {
		t.Fatalf("expected relative path to be rejected")
	}

	if err := ValidateTrustedExecutablePath(filepath.Join(dir, "missing")); err == nil {
		t.Fatalf("expected missing executable to be rejected")
	}

	if err := ValidateTrustedExecutablePath(dir); err == nil {
		t.Fatalf("expected directory path to be rejected")
	}

	notExecutable := filepath.Join(dir, "not-exec")
	if err := os.WriteFile(notExecutable, []byte("no exec"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTrustedExecutablePath(notExecutable); err == nil {
		t.Fatalf("expected non-executable file to be rejected")
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
	valid := []string{"", "/", "tenant/a", "/tenant/a/", "tenant with spaces/a"}
	for _, value := range valid {
		if err := ValidateRemoteRelativePath(value, "path"); err != nil {
			t.Fatalf("ValidateRemoteRelativePath(%q) error: %v", value, err)
		}
	}

	invalid := []string{"..", "../escape", "tenant/../../escape", "bad\npath"}
	for _, value := range invalid {
		if err := ValidateRemoteRelativePath(value, "path"); err == nil {
			t.Fatalf("ValidateRemoteRelativePath(%q) expected error", value)
		}
	}
}

func TestProcPath(t *testing.T) {
	valid := map[string]string{
		"status": "/proc/123/status",
		"comm":   "/proc/123/comm",
		"exe":    "/proc/123/exe",
	}
	for leaf, want := range valid {
		got, err := ProcPath(123, leaf)
		if err != nil {
			t.Fatalf("ProcPath(%s) valid error: %v", leaf, err)
		}
		if got != want {
			t.Fatalf("ProcPath(%s) = %q; want %q", leaf, got, want)
		}
	}
	if _, err := ProcPath(0, "status"); err == nil {
		t.Fatalf("expected pid 0 to be rejected")
	}
	if _, err := ProcPath(123, "../status"); err == nil {
		t.Fatalf("expected unsupported leaf to be rejected")
	}
}
