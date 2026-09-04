package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// The live PVE 9.1.9 node answers `pvesh set /cluster/config` with
// "No 'set' handler defined for '/cluster/config'": the endpoint the old arm
// called does not exist, so datacenter.cfg was NEVER restored on a staged
// apply. The fix writes the staged file into pmxcfs (which replicates
// cluster-wide, same as pvesh would have); pvesh is not involved at all.
func TestApplyPVEDatacenterCfgFromStageWritesThroughPmxcfs(t *testing.T) {
	origFS, origCmd := restoreFS, restoreCmd
	t.Cleanup(func() { restoreFS, restoreCmd = origFS, origCmd })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	pvesh := newSchemaAwarePvesh("local")
	restoreCmd = pvesh
	seamPmxcfs(t, "/etc/pve", true, nil)

	if err := fakeFS.AddFile("/stage/etc/pve/datacenter.cfg", []byte("keyboard: it\nmigration: secure\n")); err != nil {
		t.Fatal(err)
	}

	logger := logging.New(types.LogLevelDebug, false)
	if err := applyPVEDatacenterCfgFromStage(context.Background(), logger, "/stage"); err != nil {
		t.Fatalf("staged datacenter.cfg apply failed against the real pvesh surface: %v", err)
	}

	got, err := fakeFS.ReadFile("/etc/pve/datacenter.cfg")
	if err != nil {
		t.Fatalf("datacenter.cfg never landed in pmxcfs: %v", err)
	}
	if string(got) != "keyboard: it\nmigration: secure\n" {
		t.Fatalf("pmxcfs content differs from the staged file: %q", got)
	}
	for _, call := range pvesh.calls {
		if strings.Contains(call, "/cluster/config") {
			t.Fatalf("the arm still calls the endpoint the live node refuses: %s", call)
		}
	}
}

func TestApplyPVEDatacenterCfgFromStageRefusesWithoutPmxcfs(t *testing.T) {
	origFS, origCmd := restoreFS, restoreCmd
	t.Cleanup(func() { restoreFS, restoreCmd = origFS, origCmd })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreCmd = newSchemaAwarePvesh()
	seamPmxcfs(t, "/etc/pve", false, nil)

	if err := fakeFS.AddFile("/stage/etc/pve/datacenter.cfg", []byte("keyboard: it\n")); err != nil {
		t.Fatal(err)
	}
	logger := logging.New(types.LogLevelDebug, false)
	err := applyPVEDatacenterCfgFromStage(context.Background(), logger, "/stage")
	if err == nil || !strings.Contains(err.Error(), "not mounted") {
		t.Fatalf("an unmounted pmxcfs must refuse the write, got %v", err)
	}
	if _, readErr := fakeFS.ReadFile("/etc/pve/datacenter.cfg"); readErr == nil {
		t.Fatal("a shadow datacenter.cfg was written despite the guard")
	}
}

// The SAFE-apply UI rail called the same dead endpoint; it now shares the
// pmxcfs write, and the user's confirmation still gates it.
func TestConfirmAndApplyDatacenterCfgWritesThroughPmxcfs(t *testing.T) {
	origFS, origCmd := restoreFS, restoreCmd
	t.Cleanup(func() { restoreFS, restoreCmd = origFS, origCmd })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	pvesh := newSchemaAwarePvesh()
	restoreCmd = pvesh
	seamPmxcfs(t, "/etc/pve", true, nil)

	if err := fakeFS.AddFile("/export/etc/pve/datacenter.cfg", []byte("console: xtermjs\n")); err != nil {
		t.Fatal(err)
	}
	logger := logging.New(types.LogLevelDebug, false)
	f := &safeClusterApplyUIFlow{
		ctx:    context.Background(),
		ui:     &fakeRestoreWorkflowUI{applyDatacenterCfg: true},
		logger: logger,
	}
	if err := f.confirmAndApplyDatacenterCfg("/export/etc/pve/datacenter.cfg", 17); err != nil {
		t.Fatalf("confirmAndApplyDatacenterCfg: %v", err)
	}
	got, err := fakeFS.ReadFile("/etc/pve/datacenter.cfg")
	if err != nil {
		t.Fatalf("datacenter.cfg never landed in pmxcfs: %v", err)
	}
	if string(got) != "console: xtermjs\n" {
		t.Fatalf("pmxcfs content differs from the export file: %q", got)
	}
	for _, call := range pvesh.calls {
		if strings.Contains(call, "/cluster/config") {
			t.Fatalf("the UI rail still calls the dead endpoint: %s", call)
		}
	}
}
