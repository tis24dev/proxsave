package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A cluster-wide absence observed before the loop is not permission to write
// later: another PVE actor may have claimed the VMID in the meantime. The final
// decision and write must share PVE's native guest lock.
func TestGuestCreatedAfterInventoryIsNotOverwritten(t *testing.T) {
	fakeFS, pvesh, logger, node := guestApplyFixture(t)
	stagePath := "/export/etc/pve/nodes/" + node() + "/lxc/101.conf"
	if err := fakeFS.AddFile(stagePath, []byte("hostname: restored-ct\n")); err != nil {
		t.Fatal(err)
	}

	pmxcfsIsMounted = func(string) (bool, error) {
		// This hook is reached by the old direct writer after its one-time
		// inventory decision, modelling a concurrent successful PVE create.
		pvesh.guests["101"] = true
		pvesh.guestKinds["101"] = "lxc"
		pvesh.guestNodes["101"] = node()
		return true, nil
	}

	applied, failed := applyVMConfigs(context.Background(), []vmEntry{{
		VMID: "101", Kind: "lxc", Name: "restored-ct", Path: stagePath,
	}}, logger)
	if applied != 0 || failed != 1 {
		t.Fatalf("applied=%d failed=%d, want 0/1", applied, failed)
	}
	if _, err := fakeFS.ReadFile("/etc/pve/nodes/" + node() + "/lxc/101.conf"); !os.IsNotExist(err) {
		t.Fatalf("concurrently created guest was overwritten: %v", err)
	}
}

// A stopped result from status/current can become stale before a direct write.
// Starting the guest must serialize with the final state check and prevent the
// staged bytes from replacing its persistent configuration.
func TestGuestStartedAfterStatusProbeIsNotOverwritten(t *testing.T) {
	fakeFS, pvesh, logger, node := guestApplyFixture(t)
	pvesh.guests["100"] = true
	pvesh.failSet = map[string]bool{"100": true}

	stagePath := "/export/etc/pve/nodes/" + node() + "/qemu-server/100.conf"
	if err := fakeFS.AddFile(stagePath, []byte("name: restored-vm\ncores: 4\n")); err != nil {
		t.Fatal(err)
	}

	pmxcfsIsMounted = func(string) (bool, error) {
		// The old path reaches this only after status/current said stopped.
		pvesh.running["100"] = true
		return true, nil
	}

	applied, failed := applyVMConfigs(context.Background(), []vmEntry{{
		VMID: "100", Kind: "qemu", Name: "restored-vm", Path: stagePath,
	}}, logger)
	if applied != 0 || failed != 1 {
		t.Fatalf("applied=%d failed=%d, want 0/1", applied, failed)
	}
	if _, err := fakeFS.ReadFile("/etc/pve/nodes/" + node() + "/qemu-server/100.conf"); !os.IsNotExist(err) {
		t.Fatalf("guest started after the status probe was overwritten: %v", err)
	}
	if calls := strings.Join(pvesh.calls, "\n"); !strings.Contains(calls, "/status/current") {
		t.Fatalf("test did not reach the status/write race window:\n%s", calls)
	}
}
