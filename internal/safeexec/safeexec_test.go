package safeexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"syscall"
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

// The commands the PBS datastore inventory runs, with its arguments
// (internal/backup/collector_pbs_datastore_inventory.go). Missing from the
// allowlist, each one was recorded as "command not allowed" in every backup and
// its mount, device-mapper, RAID, multipath, iSCSI or NFS data never collected.
func TestCommandContextAllowsThePBSInventoryCommands(t *testing.T) {
	for _, args := range [][]string{
		{"findmnt", "-J"},
		{"findmnt", "-J", "-T", "/mnt/datastore"},
		{"dmsetup", "ls", "--tree"},
		{"mdadm", "--detail", "--scan"},
		{"multipath", "-ll"},
		{"iscsiadm", "-m", "session"},
		{"iscsiadm", "-m", "node"},
		{"iscsiadm", "-m", "iface"},
		{"nfsstat", "-m"},
	} {
		cmd, err := CommandContext(context.Background(), args[0], args[1:]...)
		if err != nil {
			t.Fatalf("CommandContext(%q) error = %v", args, err)
		}
		if got := cmd.Args; !reflect.DeepEqual(got, args) {
			t.Fatalf("CommandContext(%q) args = %q", args, got)
		}
	}
}

// The restore validates the staged /etc/sudoers with "visudo -c -f <file>"
// (internal/orchestrator/restore_accounts.go). Missing from the allowlist, the
// validation always failed and /etc/sudoers was never restored.
func TestCommandContextAllowsVisudoCheck(t *testing.T) {
	args := []string{"visudo", "-c", "-f", "/tmp/stage/etc/sudoers"}
	cmd, err := CommandContext(context.Background(), args[0], args[1:]...)
	if err != nil {
		t.Fatalf("CommandContext(visudo) error = %v", err)
	}
	if got := cmd.Args; !reflect.DeepEqual(got, args) {
		t.Fatalf("CommandContext(visudo) args = %q", got)
	}
}

func TestCommandContextAllowsSQLiteSnapshot(t *testing.T) {
	args := []string{
		"-cmd",
		".timeout 5000",
		"/var/lib/pve-cluster/config.db",
		`.backup "/tmp/config.db"`,
	}
	cmd, err := CommandContext(context.Background(), "sqlite3", args...)
	if err != nil {
		t.Fatalf("CommandContext(sqlite3) error = %v", err)
	}
	want := append([]string{"sqlite3"}, args...)
	if got := cmd.Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("CommandContext(sqlite3) args = %q, want %q", got, want)
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

func TestOpenProcDirRejectsNonPositivePIDs(t *testing.T) {
	for _, pid := range []int{0, -1} {
		root, err := OpenProcDir(pid)
		if err == nil || root != nil {
			t.Fatalf("OpenProcDir(%d) = (%v, %v), want (nil, error)", pid, root, err)
		}
	}
}

func TestOpenProcDirReadsThisProcess(t *testing.T) {
	root, err := OpenProcDir(os.Getpid())
	if err != nil {
		t.Fatalf("OpenProcDir(self): %v", err)
	}
	defer func() { _ = root.Close() }()
	got, err := root.ReadFile("comm")
	if err != nil {
		t.Fatalf("read comm: %v", err)
	}
	want, err := os.ReadFile("/proc/self/comm")
	if err != nil {
		t.Fatalf("read /proc/self/comm: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("comm = %q, want %q", got, want)
	}
}

// The handle stays bound to the process it was opened for: after that process
// exits, reads through it fail with ESRCH.
func TestOpenProcDirStaysBoundToItsProcess(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("sleep not runnable: %v", err)
	}
	root, err := OpenProcDir(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("OpenProcDir(child): %v", err)
	}
	defer func() { _ = root.Close() }()
	if _, err := root.ReadFile("status"); err != nil {
		t.Fatalf("read status while alive: %v", err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if _, err := root.ReadFile("status"); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("read status after exit: err=%v, want ESRCH", err)
	}
}
