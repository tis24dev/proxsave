package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// Real-world reproduction: `proxmox-backup-manager disk list --output-format=json`
// prints smartctl failures on stderr while emitting valid JSON on stdout. Merging the
// two streams into the collected artifact leaves a file that no JSON parser accepts.
const smartctlStderrNoise = "failed to gather smart data for /dev/sde – command \"smartctl\" \"-H\" \"-A\" \"-j\" \"/dev/sde\" failed - status code: 1 - no error message\n"

const diskListStdout = "[\n  {\n    \"name\": \"sde\",\n    \"used\": \"filesystem\"\n  }\n]\n"

func newStderrNoiseCollector(t *testing.T, stdout, stderr string, runErr error) *Collector {
	t.Helper()

	deps := CollectorDeps{
		LookPath: func(name string) (string, error) {
			return "/bin/" + name, nil
		},
		RunCommandCaptured: func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, []byte, error) {
			return []byte(stdout), []byte(stderr), runErr
		},
	}

	return NewCollectorWithDeps(newTestLogger(), GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxBS, false, deps)
}

func TestSafeCmdOutputKeepsStderrOutOfArtifact(t *testing.T) {
	collector := newStderrNoiseCollector(t, diskListStdout, smartctlStderrNoise, nil)
	output := filepath.Join(collector.tempDir, "var/lib/proxsave-info", "commands", "pbs", "disk_list.json")

	err := collector.safeCmdOutput(context.Background(),
		commandSpec("proxmox-backup-manager", "disk", "list", "--output-format=json"),
		output,
		"Disk list",
		false)
	if err != nil {
		t.Fatalf("safeCmdOutput: %v", err)
	}

	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read disk_list.json: %v", err)
	}
	if string(data) != diskListStdout {
		t.Fatalf("artifact must contain stdout verbatim, got: %q", string(data))
	}
	if !json.Valid(data) {
		t.Fatalf("artifact is not valid JSON: %q", string(data))
	}
}

func TestSafeCmdOutputWithPBSAuthKeepsStderrOutOfArtifact(t *testing.T) {
	collector := newStderrNoiseCollector(t, diskListStdout, smartctlStderrNoise, nil)
	collector.config.PBSRepository = "root@pam@localhost:store"
	output := filepath.Join(collector.tempDir, "var/lib/proxsave-info", "commands", "pbs", "snapshots.json")

	err := collector.safeCmdOutputWithPBSAuth(context.Background(),
		commandSpec("proxmox-backup-client", "snapshot", "list", "--output-format=json"),
		output,
		"Snapshot list",
		false)
	if err != nil {
		t.Fatalf("safeCmdOutputWithPBSAuth: %v", err)
	}

	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read snapshots.json: %v", err)
	}
	if string(data) != diskListStdout {
		t.Fatalf("artifact must contain stdout verbatim, got: %q", string(data))
	}
}

func TestSafeCmdOutputForDatastoreKeepsStderrOutOfArtifact(t *testing.T) {
	collector := newStderrNoiseCollector(t, diskListStdout, smartctlStderrNoise, nil)
	collector.config.PBSRepository = "root@pam@localhost:store"
	output := filepath.Join(collector.tempDir, "var/lib/proxsave-info", "commands", "pbs", "namespaces.json")

	err := collector.safeCmdOutputWithPBSAuthForDatastore(context.Background(),
		commandSpec("proxmox-backup-client", "namespace", "list", "--output-format=json"),
		output,
		"Namespace list",
		"store",
		false)
	if err != nil {
		t.Fatalf("safeCmdOutputWithPBSAuthForDatastore: %v", err)
	}

	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read namespaces.json: %v", err)
	}
	if string(data) != diskListStdout {
		t.Fatalf("artifact must contain stdout verbatim, got: %q", string(data))
	}
}

// Failure diagnostics stay useful: stderr is where the command explains itself, so the
// summary logged on a non-critical failure must still carry it.
func TestFailedCommandSummaryStillIncludesStderr(t *testing.T) {
	collector := newStderrNoiseCollector(t, "", "permission denied\n", errors.New("exit status 1"))
	result, err := collector.runAndClassifyCommand(context.Background(),
		commandSpec("proxmox-backup-manager", "disk", "list", "--output-format=json"),
		commandRunOptions{
			output:      filepath.Join(collector.tempDir, "var/lib/proxsave-info", "commands", "pbs", "disk_list.json"),
			description: "Disk list",
			caller:      "test",
		})
	if err != nil {
		t.Fatalf("runAndClassifyCommand: %v", err)
	}
	if result.classification != commandRunNonCriticalFailure {
		t.Fatalf("expected non-critical failure, got %v", result.classification)
	}
	if result.outputSummary != "permission denied" {
		t.Fatalf("summary must include stderr, got %q", result.outputSummary)
	}
}

// Existing suites inject the legacy combined-output hooks; they must keep working.
func TestLegacyRunCommandDepStillFeedsArtifact(t *testing.T) {
	deps := CollectorDeps{
		LookPath: func(name string) (string, error) {
			return "/bin/" + name, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte(diskListStdout), nil
		},
	}
	collector := NewCollectorWithDeps(newTestLogger(), GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxBS, false, deps)
	output := filepath.Join(collector.tempDir, "var/lib/proxsave-info", "commands", "pbs", "disk_list.json")

	if err := collector.safeCmdOutput(context.Background(),
		commandSpec("proxmox-backup-manager", "disk", "list", "--output-format=json"),
		output,
		"Disk list",
		false); err != nil {
		t.Fatalf("safeCmdOutput: %v", err)
	}

	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read disk_list.json: %v", err)
	}
	if string(data) != diskListStdout {
		t.Fatalf("legacy dep output mismatch: %q", string(data))
	}
}
