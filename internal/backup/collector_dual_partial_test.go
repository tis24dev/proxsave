package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// dualCollector builds a dual collector over a fake system root. Whichever of the two
// configuration directories the caller asks for is created; the other is absent, which
// is how each half is made to fail at its validate brick.
func dualCollector(t *testing.T, withPVE, withPBS bool) *Collector {
	t.Helper()
	return dualCollectorWithDryRun(t, withPVE, withPBS, true)
}

// dualCollectorWithDryRun is the same fixture with the dry-run switch exposed: the
// manifest is only written on a real run, so a test that reads it back off disk needs
// dryRun=false.
func dualCollectorWithDryRun(t *testing.T, withPVE, withPBS, dryRun bool) *Collector {
	t.Helper()
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	systemRoot := t.TempDir()
	if withPVE {
		if err := os.MkdirAll(filepath.Join(systemRoot, "etc", "pve"), 0o755); err != nil {
			t.Fatalf("mkdir /etc/pve: %v", err)
		}
	}
	if withPBS {
		if err := os.MkdirAll(filepath.Join(systemRoot, "etc", "proxmox-backup"), 0o755); err != nil {
			t.Fatalf("mkdir /etc/proxmox-backup: %v", err)
		}
	}

	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = systemRoot

	deps := defaultCollectorDeps()
	deps.LookPath = func(name string) (string, error) {
		switch name {
		case "cat", "uname", "pveversion", "proxmox-backup-manager":
			return "/bin/true", nil
		default:
			return "", errors.New("missing")
		}
	}
	deps.RunCommand = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("[]"), nil
	}

	return NewCollectorWithDeps(logger, cfg, t.TempDir(), types.ProxmoxDual, dryRun, deps)
}

// TestDualKeepsThePVEHalfWhenPBSFails is the loss issue #315 caused. The two halves
// used to be one fail-fast recipe, so an abort in the PBS half threw away the PVE
// payload that had already been collected and the host ended the night with nothing.
func TestDualKeepsThePVEHalfWhenPBSFails(t *testing.T) {
	collector := dualCollector(t, true, false)

	if err := collector.CollectAll(context.Background()); err != nil {
		t.Fatalf("CollectAll returned %v, want the run to carry on with the half it has", err)
	}

	incomplete := collector.IncompleteTargets()
	if len(incomplete) != 1 || incomplete[0] != "pbs" {
		t.Fatalf("IncompleteTargets() = %v, want exactly [pbs]", incomplete)
	}
	if len(collector.pveManifest) == 0 {
		t.Fatal("the PVE manifest is empty: the half that succeeded was not kept")
	}
}

// TestDualKeepsThePBSHalfWhenPVEFails is the same contract from the other side, so the
// behaviour is a property of the recipe pair rather than of the order they run in.
func TestDualKeepsThePBSHalfWhenPVEFails(t *testing.T) {
	collector := dualCollector(t, false, true)

	if err := collector.CollectAll(context.Background()); err != nil {
		t.Fatalf("CollectAll returned %v, want the run to carry on with the half it has", err)
	}

	incomplete := collector.IncompleteTargets()
	if len(incomplete) != 1 || incomplete[0] != "pve" {
		t.Fatalf("IncompleteTargets() = %v, want exactly [pve]", incomplete)
	}
}

// TestDualFailsWhenBothHalvesFail: with no role payload left there is nothing to keep,
// and shipping an archive of system files labelled dual would be worse than failing.
func TestDualFailsWhenBothHalvesFail(t *testing.T) {
	collector := dualCollector(t, false, false)

	err := collector.CollectAll(context.Background())
	if err == nil {
		t.Fatal("CollectAll returned nil, want an error when neither half produced anything")
	}
	if !strings.Contains(err.Error(), "both halves failed") {
		t.Fatalf("CollectAll error = %v, want it to say both halves failed", err)
	}
}

// TestIncompleteTargetTravelsInTheManifest: a log can be rotated away or never read,
// and the archive then looks whole. This writes the manifest and reads it back off
// disk, because an earlier version of this test asserted the in-memory field and
// claimed the manifest in its name without ever producing one.
func TestIncompleteTargetTravelsInTheManifest(t *testing.T) {
	collector := dualCollectorWithDryRun(t, true, false, false)
	if err := collector.CollectAll(context.Background()); err != nil {
		t.Fatalf("CollectAll: %v", err)
	}
	if err := collector.WriteManifest("host.example"); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	path := filepath.Join(collector.tempDir, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var written BackupManifest
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	if len(written.Incomplete) != 1 {
		t.Fatalf("manifest incomplete_targets = %+v, want one entry", written.Incomplete)
	}
	if written.Incomplete[0].Target != "pbs" {
		t.Fatalf("incomplete target = %q, want pbs", written.Incomplete[0].Target)
	}
	if strings.TrimSpace(written.Incomplete[0].Reason) == "" {
		t.Fatal("the manifest says a half is missing without saying why")
	}
	if !strings.Contains(string(data), "incomplete_targets") {
		t.Fatal("the JSON key is absent: a reader of the archive cannot see the gap")
	}
}

// TestSystemPayloadSurvivesAFailedRole: the common payload has nothing to do with
// which hypervisor this is, and a bare return used to skip it along with the rest.
func TestSystemPayloadSurvivesAFailedRole(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = t.TempDir()
	cfg.PVEConfigPath = filepath.Join(t.TempDir(), "missing")

	deps := defaultCollectorDeps()
	deps.LookPath = func(name string) (string, error) {
		switch name {
		case "cat", "uname":
			return "/bin/true", nil
		default:
			return "", errors.New("missing")
		}
	}

	collector := NewCollectorWithDeps(logger, cfg, t.TempDir(), types.ProxmoxVE, true, deps)
	err := collector.CollectAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "PVE collection failed:") {
		t.Fatalf("CollectAll error = %v, want the single-role failure still reported", err)
	}
	if len(collector.systemManifest) == 0 {
		t.Fatal("the system manifest is empty: the common payload was skipped because the role failed")
	}
}
