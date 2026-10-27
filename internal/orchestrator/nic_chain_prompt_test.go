package orchestrator

import (
	"archive/tar"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

type nicChainCaptureUI struct {
	titles   []string
	messages []string
}

func (u *nicChainCaptureUI) ConfirmAction(_ context.Context, title, message, _, _ string, _ time.Duration, _ bool) (bool, error) {
	u.titles = append(u.titles, title)
	u.messages = append(u.messages, message)
	return false, nil
}

func (u *nicChainCaptureUI) ShowMessage(_ context.Context, title, message string) error {
	u.titles = append(u.titles, "SHOW:"+title)
	u.messages = append(u.messages, message)
	return nil
}

func nicChainBuildPlan(t *testing.T, current map[string]string, backup []archivedNetworkInterface) *nicRepairPlan {
	t.Helper()
	origFS := restoreFS
	origTime := restoreTime
	origSys := sysClassNetPath
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		sysClassNetPath = origSys
	})
	t.Setenv("PATH", "")

	root := t.TempDir()
	netDir := filepath.Join(root, "sys/class/net")
	sysClassNetPath = netDir
	for name, mac := range current {
		p := filepath.Join(netDir, name, "address")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(mac+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2025, 1, 1, 1, 2, 3, 0, time.UTC)}

	payload, err := json.Marshal(archivedNetworkInventory{GeneratedAt: time.Now().Format(time.RFC3339), Hostname: "backup-host", Interfaces: backup})
	if err != nil {
		t.Fatal(err)
	}
	writeTarToFakeFS(t, fakeFS, "/backup.tar", []tarEntry{
		{Name: "./commands/network_inventory.json", Typeflag: tar.TypeReg, Mode: 0o644, Data: payload},
	})
	plan, err := planNICNameRepair(context.Background(), "/backup.tar")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func nicChainRunPrompt(t *testing.T, plan *nicRepairPlan) *nicChainCaptureUI {
	t.Helper()
	origPlan := planNICNameRepairFn
	origOverride := detectNICNamingOverrideRulesFn
	t.Cleanup(func() { planNICNameRepairFn = origPlan; detectNICNamingOverrideRulesFn = origOverride })
	planNICNameRepairFn = func(context.Context, string) (*nicRepairPlan, error) { return plan, nil }
	detectNICNamingOverrideRulesFn = func(*logging.Logger) (nicNamingOverrideReport, error) {
		return nicNamingOverrideReport{}, nil
	}
	ui := &nicChainCaptureUI{}
	_ = repairNICNamesWithUI(context.Background(), ui, logging.New(types.LogLevelError, false), "/backup.tar")
	return ui
}

// A direct conflict keeps its line and the header: the old name exists here.
func TestNICConflictPromptDirectConflict(t *testing.T) {
	plan := nicChainBuildPlan(t,
		map[string]string{"eth0": "aa:bb:cc:dd:ee:01", "eno1": "aa:bb:cc:dd:ee:02", "lo": "00:00:00:00:00:00"},
		[]archivedNetworkInterface{{Name: "eno1", MAC: "aa:bb:cc:dd:ee:01"}},
	)
	ui := nicChainRunPrompt(t, plan)
	want := "Detected NIC name conflicts.\n\n" +
		"These interface names exist on the current system but map to different NICs in the backup inventory:\n\n" +
		"- eno1 -> eth0 (mac=aa:bb:cc:dd:ee:01) but current eno1 exists (mac=aa:bb:cc:dd:ee:02)\n" +
		"\nApply NIC rename mapping even when conflicting interface names exist on this system?"
	if len(ui.messages) == 0 || ui.messages[0] != want {
		t.Fatalf("prompt = %q\nwant %q", ui.messages, want)
	}
}

// A mapping held back with a conflict (28855f1c: the chain is applied whole or not at
// all) was listed as "but current ens20 exists", and ens20 does not exist here. It now
// says what holds it back, and the header no longer claims every name exists.
func TestNICConflictPromptChainedMapping(t *testing.T) {
	plan := nicChainBuildPlan(t,
		map[string]string{"eth0": "aa:bb:cc:dd:ee:01", "eno1": "aa:bb:cc:dd:ee:02", "lo": "00:00:00:00:00:00"},
		[]archivedNetworkInterface{{Name: "eno1", MAC: "aa:bb:cc:dd:ee:01"}, {Name: "ens20", MAC: "aa:bb:cc:dd:ee:02"}},
	)
	ui := nicChainRunPrompt(t, plan)
	want := "Detected NIC name conflicts.\n\n" +
		"Some of these interface names exist on the current system; the others rename onto one of those names and are applied together with it:\n\n" +
		"- eno1 -> eth0 (mac=aa:bb:cc:dd:ee:01) but current eno1 exists (mac=aa:bb:cc:dd:ee:02)\n" +
		"- ens20 -> eno1 (mac=aa:bb:cc:dd:ee:02) held back: eno1 is renamed by the conflict above\n" +
		"\nApply NIC rename mapping even when conflicting interface names exist on this system?"
	if len(ui.messages) == 0 || ui.messages[0] != want {
		t.Fatalf("prompt = %q\nwant %q", ui.messages, want)
	}
}
