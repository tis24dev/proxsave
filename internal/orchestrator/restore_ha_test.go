package orchestrator

import (
	"os"
	"testing"
)

func TestApplyPVEHAFromStage_AppliesAndPrunesKnownConfigFiles(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	restoreFS = fakeFS

	stageRoot := "/stage"
	if err := fakeFS.AddFile(stageRoot+"/etc/pve/ha/resources.cfg", []byte("res\n")); err != nil {
		t.Fatalf("add staged resources.cfg: %v", err)
	}
	if err := fakeFS.AddFile(stageRoot+"/etc/pve/ha/groups.cfg", []byte("grp\n")); err != nil {
		t.Fatalf("add staged groups.cfg: %v", err)
	}

	if err := fakeFS.AddFile("/etc/pve/ha/resources.cfg", []byte("old\n")); err != nil {
		t.Fatalf("add existing resources.cfg: %v", err)
	}
	if err := fakeFS.AddFile("/etc/pve/ha/rules.cfg", []byte("remove\n")); err != nil {
		t.Fatalf("add existing rules.cfg: %v", err)
	}

	applied, err := applyPVEHAFromStage(newTestLogger(), stageRoot)
	if err != nil {
		t.Fatalf("applyPVEHAFromStage error: %v", err)
	}
	if len(applied) == 0 {
		t.Fatalf("expected applied paths, got none")
	}

	data, err := fakeFS.ReadFile("/etc/pve/ha/resources.cfg")
	if err != nil {
		t.Fatalf("read resources.cfg: %v", err)
	}
	if string(data) != "res\n" {
		t.Fatalf("unexpected resources.cfg content: %q", string(data))
	}

	data, err = fakeFS.ReadFile("/etc/pve/ha/groups.cfg")
	if err != nil {
		t.Fatalf("read groups.cfg: %v", err)
	}
	if string(data) != "grp\n" {
		t.Fatalf("unexpected groups.cfg content: %q", string(data))
	}

	if _, err := fakeFS.Stat("/etc/pve/ha/rules.cfg"); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected rules.cfg to be removed; stat err=%v", err)
	}
}

func TestApplyPVEHAFromStage_DoesNotPruneWhenStageMissing(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	restoreFS = fakeFS

	if err := fakeFS.AddFile("/etc/pve/ha/resources.cfg", []byte("keep\n")); err != nil {
		t.Fatalf("add existing resources.cfg: %v", err)
	}

	stageRoot := "/stage"
	applied, err := applyPVEHAFromStage(newTestLogger(), stageRoot)
	if err != nil {
		t.Fatalf("applyPVEHAFromStage error: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("expected no applied paths, got %d", len(applied))
	}

	data, err := fakeFS.ReadFile("/etc/pve/ha/resources.cfg")
	if err != nil {
		t.Fatalf("read resources.cfg: %v", err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("unexpected resources.cfg content: %q", string(data))
	}
}

