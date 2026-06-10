package orchestrator

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestPVEStorageMountGuardItems_BuildsExpectedTargets(t *testing.T) {
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
		"/":                 {},
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

// pveshWhichOKApplyFailRunner makes "which pvesh" succeed (so pvesh is considered
// available) but fails every other command, so applyStorageCfg records failed
// entries.
type pveshWhichOKApplyFailRunner struct{}

func (pveshWhichOKApplyFailRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "which" {
		return nil, nil
	}
	return nil, fmt.Errorf("pvesh failed")
}

func (pveshWhichOKApplyFailRunner) RunStream(ctx context.Context, name string, stdin io.Reader, args ...string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("pvesh failed")
}

// TestApplyPVEStorageCfgFromStage_ReturnsErrorOnApplyFailure locks in part of the
// BH-003 fix: when staged storage.cfg entries fail to apply (failed > 0), the
// step must return an error so the staged-apply wrapper reports the restore "with
// warnings" instead of swallowing the failure and reporting success.
func TestApplyPVEStorageCfgFromStage_ReturnsErrorOnApplyFailure(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	fakeFS := NewFakeFS()
	restoreFS = fakeFS
	restoreCmd = pveshWhichOKApplyFailRunner{}

	stageRoot := "/stage"
	cfg := "storage: local\n    type dir\n    path /var/lib/vz\n"
	if err := fakeFS.AddFile(stageRoot+"/etc/pve/storage.cfg", []byte(cfg)); err != nil {
		t.Fatalf("add storage.cfg: %v", err)
	}

	if err := applyPVEStorageCfgFromStage(context.Background(), newTestLogger(), stageRoot); err == nil {
		t.Fatalf("expected an error when storage.cfg entries fail to apply")
	}
}
