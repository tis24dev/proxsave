package orchestrator

import (
	"context"
	"strings"
	"testing"
)

func TestPVEStorageMountGuardItems_BuildsExpectedTargets(t *testing.T) {
	t.Parallel()

	candidates := []pveStorageMountGuardCandidate{
		{StorageID: "Data1", StorageType: "dir", Path: "/mnt/datastore/Data1"},
		{StorageID: "Synology-Archive", StorageType: "dir", Path: "/mnt/Synology_NFS/PBS_Backup"},
		{StorageID: "local", StorageType: "dir", Path: "/var/lib/vz"},
		{StorageID: "nfs-backup", StorageType: "nfs"},
	}
	mountCandidates := []string{"/mnt/datastore", "/mnt/Synology_NFS", "/"}
	fstabMounts := map[string]struct{}{
		"/mnt/datastore":    {},
		"/mnt/Synology_NFS": {},
		"/":                {},
	}

	items := pveStorageMountGuardItems(candidates, mountCandidates, fstabMounts)
	got := make(map[string]pveStorageMountGuardItem, len(items))
	for _, item := range items {
		got[item.GuardTarget] = item
	}

	wantTargets := []string{"/mnt/datastore", "/mnt/Synology_NFS", "/mnt/pve/nfs-backup"}
	for _, target := range wantTargets {
		if _, ok := got[target]; !ok {
			t.Fatalf("missing guard target %s; got=%v", target, got)
		}
	}
	if len(got) != len(wantTargets) {
		t.Fatalf("unexpected number of guard targets: got=%v want=%v", got, wantTargets)
	}
}

func TestApplyPVEBackupJobsFromStage_CreatesJobsViaPvesh(t *testing.T) {
	t.Parallel()

	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	fakeFS := NewFakeFS()
	restoreFS = fakeFS

	fakeCmd := &FakeCommandRunner{}
	restoreCmd = fakeCmd

	stageRoot := "/stage"
	cfg := strings.Join([]string{
		"vzdump: job1",
		"    node pve1",
		"    storage local",
		"",
		"vzdump: job2",
		"    node pve1",
		"    storage backup",
		"",
	}, "\n")
	if err := fakeFS.AddFile(stageRoot+"/etc/pve/jobs.cfg", []byte(cfg)); err != nil {
		t.Fatalf("add jobs.cfg: %v", err)
	}

	if err := applyPVEBackupJobsFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPVEBackupJobsFromStage error: %v", err)
	}

	calls := strings.Join(fakeCmd.CallsList(), "\n")
	if !strings.Contains(calls, "which pvesh") {
		t.Fatalf("expected which pvesh call; calls=%v", fakeCmd.CallsList())
	}
	if !strings.Contains(calls, "pvesh create /cluster/backup --id job1 --node pve1 --storage local") {
		t.Fatalf("expected create job1 call; calls=%v", fakeCmd.CallsList())
	}
	if !strings.Contains(calls, "pvesh create /cluster/backup --id job2 --node pve1 --storage backup") {
		t.Fatalf("expected create job2 call; calls=%v", fakeCmd.CallsList())
	}
}
