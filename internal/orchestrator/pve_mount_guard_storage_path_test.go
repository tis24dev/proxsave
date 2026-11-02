package orchestrator

import "testing"

// PVE mounts a network storage at its storage.cfg path, which it writes on every
// create (/mnt/pve/<id> by default, any path when given). The guard always targeted
// /mnt/pve/<id>: a storage at another path was "guarded" on a directory nothing mounts,
// while its real mountpoint stayed unguarded. A stanza without a path keeps
// /mnt/pve/<id>.
func TestPVEMountGuardTargetsTheNetworkStoragePath(t *testing.T) {
	sections := []proxmoxNotificationSection{
		{Type: "nfs", Name: "nas", Entries: []proxmoxNotificationEntry{{Key: "path", Value: "/mnt/nas/"}, {Key: "server", Value: "192.0.2.10"}}},
		{Type: "cifs", Name: "smb", Entries: []proxmoxNotificationEntry{{Key: "path", Value: "/mnt/pve/smb"}}},
		{Type: "cephfs", Name: "cfs", Entries: []proxmoxNotificationEntry{{Key: "content", Value: "backup"}}},
	}
	items := pveStorageMountGuardItems(pveStorageMountGuardCandidatesFromSections(sections), nil, nil)
	got := map[string]string{}
	for _, it := range items {
		got[it.StorageID] = it.GuardTarget
	}
	want := map[string]string{"nas": "/mnt/nas", "smb": "/mnt/pve/smb", "cfs": "/mnt/pve/cfs"}
	for id, target := range want {
		if got[id] != target {
			t.Fatalf("storage %s: guard target %q, want %q (all: %v)", id, got[id], target, got)
		}
	}
}
