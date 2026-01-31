package orchestrator

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestApplyPVESDNFromStage_SyncsDirectoryAndCfg(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	if err := fakeFS.AddFile(stageRoot+"/etc/pve/sdn/zones.cfg", []byte("zone: z1\n")); err != nil {
		t.Fatalf("add zones.cfg: %v", err)
	}
	if err := fakeFS.AddFile(stageRoot+"/etc/pve/sdn/vnets.cfg", []byte("vnet: v1\n")); err != nil {
		t.Fatalf("add vnets.cfg: %v", err)
	}
	if err := fakeFS.AddFile(stageRoot+"/etc/pve/sdn.cfg", []byte("legacy\n")); err != nil {
		t.Fatalf("add sdn.cfg: %v", err)
	}
	if err := fakeFS.AddFile("/etc/pve/sdn/old.cfg", []byte("old\n")); err != nil {
		t.Fatalf("add old.cfg: %v", err)
	}

	applied, err := applyPVESDNFromStage(newTestLogger(), stageRoot)
	if err != nil {
		t.Fatalf("applyPVESDNFromStage error: %v", err)
	}

	if got, err := fakeFS.ReadFile("/etc/pve/sdn/zones.cfg"); err != nil {
		t.Fatalf("read zones.cfg: %v", err)
	} else if string(got) != "zone: z1\n" {
		t.Fatalf("unexpected zones.cfg content: %q", string(got))
	}

	if got, err := fakeFS.ReadFile("/etc/pve/sdn/vnets.cfg"); err != nil {
		t.Fatalf("read vnets.cfg: %v", err)
	} else if string(got) != "vnet: v1\n" {
		t.Fatalf("unexpected vnets.cfg content: %q", string(got))
	}

	if got, err := fakeFS.ReadFile("/etc/pve/sdn.cfg"); err != nil {
		t.Fatalf("read sdn.cfg: %v", err)
	} else if string(got) != "legacy\n" {
		t.Fatalf("unexpected sdn.cfg content: %q", string(got))
	}

	if _, err := fakeFS.Stat("/etc/pve/sdn/old.cfg"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected old.cfg to be pruned, stat err=%v", err)
	}

	joined := strings.Join(applied, "\n")
	for _, want := range []string{"/etc/pve/sdn/zones.cfg", "/etc/pve/sdn/vnets.cfg", "/etc/pve/sdn.cfg"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected applied paths to include %s; applied=%v", want, applied)
		}
	}
}

func TestApplyPVESDNFromStage_NoStageData_NoChanges(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	applied, err := applyPVESDNFromStage(newTestLogger(), "/stage")
	if err != nil {
		t.Fatalf("applyPVESDNFromStage error: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("expected no applied paths, got=%v", applied)
	}
}

