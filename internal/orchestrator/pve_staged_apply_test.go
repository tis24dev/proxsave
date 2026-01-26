package orchestrator

import (
	"context"
	"strings"
	"testing"
)

func TestPVEGuardTargetsForStoragePaths_UsesFstabMountpoints(t *testing.T) {
	t.Parallel()

	storagePaths := []string{
		"/mnt/datastore/Data1",
		"/mnt/Synology_NFS/PBS_Backup",
		"/var/lib/vz",
	}
	mountCandidates := []string{
		"/mnt/datastore",
		"/mnt/Synology_NFS",
	}

	got := pveGuardTargetsForStoragePaths(storagePaths, mountCandidates)
	want := []string{"/mnt/Synology_NFS", "/mnt/datastore"}
	if len(got) != len(want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got=%v want=%v", got, want)
		}
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
