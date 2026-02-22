package backup

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestSafeCmdOutput_Unprivileged_DowngradesDmidecodeToSkip(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(buf)

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/usr/sbin/dmidecode", nil },
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, "sh", "-c", "echo '/dev/mem: Permission denied'; exit 1")
			return cmd.CombinedOutput()
		},
		DetectUnprivilegedContainer: func() (bool, string) {
			return true, "uid_map=0->100000(len=65536), container=lxc"
		},
	}

	tmp := t.TempDir()
	c := NewCollectorWithDeps(logger, GetDefaultCollectorConfig(), tmp, types.ProxmoxUnknown, false, deps)
	outPath := filepath.Join(tmp, "dmidecode.txt")

	if err := c.safeCmdOutput(context.Background(), "dmidecode", outPath, "Hardware DMI information", false); err != nil {
		t.Fatalf("safeCmdOutput returned error: %v", err)
	}

	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("expected no output file to be created, stat err=%v", err)
	}

	logText := buf.String()
	if !strings.Contains(logText, "] SKIP") {
		t.Fatalf("expected SKIP log line, got: %s", logText)
	}
	if !strings.Contains(logText, "Expected in unprivileged containers") {
		t.Fatalf("expected unprivileged hint in logs, got: %s", logText)
	}
	if !strings.Contains(logText, "DMI tables not accessible") {
		t.Fatalf("expected reason in logs, got: %s", logText)
	}
}

func TestCaptureCommandOutput_Unprivileged_DowngradesBlkidToSkipWithRestoreHint(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(buf)

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/sbin/blkid", nil },
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, "sh", "-c", "exit 2")
			return cmd.CombinedOutput()
		},
		DetectUnprivilegedContainer: func() (bool, string) {
			return true, "uid_map=0->100000(len=65536), container=lxc"
		},
	}

	tmp := t.TempDir()
	c := NewCollectorWithDeps(logger, GetDefaultCollectorConfig(), tmp, types.ProxmoxUnknown, false, deps)
	outPath := filepath.Join(tmp, "blkid.txt")

	data, err := c.captureCommandOutput(context.Background(), "blkid", outPath, "Block device identifiers (blkid)", false)
	if err != nil {
		t.Fatalf("captureCommandOutput returned error: %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil data when command is skipped, got %q", string(data))
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("expected no output file to be created, stat err=%v", err)
	}

	logText := buf.String()
	if !strings.Contains(logText, "] SKIP") {
		t.Fatalf("expected SKIP log line, got: %s", logText)
	}
	if !strings.Contains(logText, "Expected in unprivileged containers") {
		t.Fatalf("expected unprivileged hint in logs, got: %s", logText)
	}
	if !strings.Contains(logText, "restore hint: automated fstab device remap") {
		t.Fatalf("expected restore hint in logs, got: %s", logText)
	}
}

func TestSafeCmdOutput_Unprivileged_DowngradesSensorsToSkip(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(buf)

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/usr/bin/sensors", nil },
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, "sh", "-c", "echo 'No sensors found'; exit 1")
			return cmd.CombinedOutput()
		},
		DetectUnprivilegedContainer: func() (bool, string) {
			return true, "uid_map=0->100000(len=65536), container=lxc"
		},
	}

	tmp := t.TempDir()
	c := NewCollectorWithDeps(logger, GetDefaultCollectorConfig(), tmp, types.ProxmoxUnknown, false, deps)
	outPath := filepath.Join(tmp, "sensors.txt")

	if err := c.safeCmdOutput(context.Background(), "sensors", outPath, "Hardware sensors", false); err != nil {
		t.Fatalf("safeCmdOutput returned error: %v", err)
	}

	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("expected no output file to be created, stat err=%v", err)
	}

	logText := buf.String()
	if !strings.Contains(logText, "] SKIP") {
		t.Fatalf("expected SKIP log line, got: %s", logText)
	}
	if !strings.Contains(logText, "Expected in unprivileged containers") {
		t.Fatalf("expected unprivileged hint in logs, got: %s", logText)
	}
	if !strings.Contains(logText, "hardware sensors not accessible") {
		t.Fatalf("expected reason in logs, got: %s", logText)
	}
}

func TestSafeCmdOutput_Unprivileged_DowngradesSmartctlToSkip(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(buf)

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/usr/sbin/smartctl", nil },
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, "sh", "-c", "echo 'Permission denied'; exit 1")
			return cmd.CombinedOutput()
		},
		DetectUnprivilegedContainer: func() (bool, string) {
			return true, "uid_map=0->100000(len=65536), container=lxc"
		},
	}

	tmp := t.TempDir()
	c := NewCollectorWithDeps(logger, GetDefaultCollectorConfig(), tmp, types.ProxmoxUnknown, false, deps)
	outPath := filepath.Join(tmp, "smartctl_scan.txt")

	if err := c.safeCmdOutput(context.Background(), "smartctl --scan", outPath, "SMART scan", false); err != nil {
		t.Fatalf("safeCmdOutput returned error: %v", err)
	}

	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("expected no output file to be created, stat err=%v", err)
	}

	logText := buf.String()
	if !strings.Contains(logText, "] SKIP") {
		t.Fatalf("expected SKIP log line, got: %s", logText)
	}
	if !strings.Contains(logText, "Expected in unprivileged containers") {
		t.Fatalf("expected unprivileged hint in logs, got: %s", logText)
	}
	if !strings.Contains(logText, "SMART devices not accessible") {
		t.Fatalf("expected reason in logs, got: %s", logText)
	}
}

func TestPrivilegeSensitiveFailureReason(t *testing.T) {
	const blkidReason = "block devices not accessible; restore hint: automated fstab device remap (UUID/PARTUUID/LABEL) may be limited"

	tests := []struct {
		name     string
		command  string
		exitCode int
		output   string
		want     string
	}{
		{"dmidecode permission denied", "dmidecode", 1, "Permission denied", "DMI tables not accessible"},
		{"dmidecode /dev/mem", "dmidecode", 1, "/dev/mem: Operation not permitted", "DMI tables not accessible"},
		{"dmidecode operation not permitted", "dmidecode", 1, "Operation not permitted", "DMI tables not accessible"},
		{"dmidecode exit non-zero empty output", "dmidecode", 1, "", "DMI tables not accessible"},
		{"dmidecode success", "dmidecode", 0, "some output", ""},

		{"blkid exit2 empty", "blkid", 2, "", blkidReason},
		{"blkid exit2 with output", "blkid", 2, "/dev/sda1: UUID=\"...\"", ""},
		{"blkid permission denied", "blkid", 1, "Permission denied", blkidReason},
		{"blkid exit0 empty", "blkid", 0, "", ""},

		{"sensors no sensors found", "sensors", 1, "No sensors found", "hardware sensors not accessible"},
		{"sensors permission denied", "sensors", 1, "Permission denied", "hardware sensors not accessible"},
		{"sensors success", "sensors", 0, "coretemp-isa-0000\nAdapter: ISA adapter\n", ""},

		{"smartctl permission denied", "smartctl", 1, "Permission denied", "SMART devices not accessible"},
		{"smartctl operation not permitted", "smartctl", 1, "Operation not permitted", "SMART devices not accessible"},
		{"smartctl no such device", "smartctl", 1, "No such device", ""},
		{"smartctl with spaces", " smartctl ", 1, "Permission denied", "SMART devices not accessible"},

		{"unknown command no match", "lspci", 1, "permission denied", ""},
		{"empty command", "", 1, "permission denied", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := privilegeSensitiveFailureReason(tc.command, tc.exitCode, tc.output)
			if got != tc.want {
				t.Fatalf("privilegeSensitiveFailureReason(%q, %d, %q) = %q, want %q",
					tc.command, tc.exitCode, tc.output, got, tc.want)
			}
		})
	}
}
