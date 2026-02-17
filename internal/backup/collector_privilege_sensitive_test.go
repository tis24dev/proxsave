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

func TestParseRootIDMapShift(t *testing.T) {
	t.Run("identity mapping", func(t *testing.T) {
		shifted, host := parseRootIDMapShift("0 0 4294967295\n")
		if shifted || host != 0 {
			t.Fatalf("shifted=%v host=%d; want false,0", shifted, host)
		}
	})

	t.Run("shifted mapping", func(t *testing.T) {
		shifted, host := parseRootIDMapShift("0 100000 65536\n")
		if !shifted || host != 100000 {
			t.Fatalf("shifted=%v host=%d; want true,100000", shifted, host)
		}
	})

	t.Run("missing root range", func(t *testing.T) {
		shifted, host := parseRootIDMapShift("1 100000 65536\n")
		if shifted || host != 0 {
			t.Fatalf("shifted=%v host=%d; want false,0", shifted, host)
		}
	})
}

func TestPrivilegeSensitiveFailureReason(t *testing.T) {
	cases := []struct {
		name     string
		command  string
		exitCode int
		output   string
		want     string
	}{
		{"dmidecode perm", "dmidecode", 1, "/dev/mem: Permission denied", "DMI tables not accessible"},
		{"blkid exit2 empty", "blkid", 2, "", "block devices not accessible; restore hint: automated fstab device remap (UUID/PARTUUID/LABEL) may be limited"},
		{"blkid perm", "blkid", 2, "Permission denied", "block devices not accessible; restore hint: automated fstab device remap (UUID/PARTUUID/LABEL) may be limited"},
		{"sensors none", "sensors", 1, "No sensors found!", "no hardware sensors available"},
		{"smartctl perm", "smartctl", 1, "Permission denied", "SMART devices not accessible"},
		{"other ignored", "false", 1, "Permission denied", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := privilegeSensitiveFailureReason(tc.command, tc.exitCode, tc.output)
			if got != tc.want {
				t.Fatalf("reason=%q; want %q", got, tc.want)
			}
		})
	}
}

func TestSafeCmdOutput_DowngradesPrivilegeSensitiveFailureToSkip(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)

	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/usr/sbin/dmidecode", nil },
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.Command("sh", "-c", "echo '/dev/mem: Permission denied' >&2; exit 1")
			out, err := cmd.CombinedOutput()
			return out, err
		},
		DetectUnprivilegedContainer: func() (bool, string) {
			return true, "uid_map=0->100000 container=lxc"
		},
	}
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)

	outPath := filepath.Join(tmp, "dmidecode.txt")
	if err := c.safeCmdOutput(context.Background(), "dmidecode", outPath, "Hardware DMI information", false); err != nil {
		t.Fatalf("safeCmdOutput error: %v", err)
	}

	logText := buf.String()
	if !strings.Contains(logText, "SKIP") {
		t.Fatalf("expected SKIP in logs, got: %s", logText)
	}
	if strings.Contains(logText, "WARNING") {
		t.Fatalf("expected no WARNING in logs, got: %s", logText)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("expected no output file to be created, stat err=%v", err)
	}
}

func TestCaptureCommandOutput_DowngradesBlkidExit2ToSkipInUnprivilegedContainer(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)

	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/sbin/blkid", nil },
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.Command("sh", "-c", "exit 2")
			out, err := cmd.CombinedOutput()
			return out, err
		},
		DetectUnprivilegedContainer: func() (bool, string) {
			return true, "uid_map=0->100000 container=lxc"
		},
	}
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)

	outPath := filepath.Join(tmp, "blkid.txt")
	data, err := c.captureCommandOutput(context.Background(), "blkid", outPath, "Block device identifiers (blkid)", false)
	if err != nil {
		t.Fatalf("captureCommandOutput returned error: %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil data on non-critical failure, got %q", string(data))
	}

	logText := buf.String()
	if !strings.Contains(logText, "SKIP") {
		t.Fatalf("expected SKIP in logs, got: %s", logText)
	}
	if !strings.Contains(strings.ToLower(logText), "restore hint") {
		t.Fatalf("expected restore hint in logs, got: %s", logText)
	}
	if strings.Contains(logText, "WARNING") {
		t.Fatalf("expected no WARNING in logs, got: %s", logText)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("expected no output file to be created, stat err=%v", err)
	}
}
