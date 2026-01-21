package orchestrator

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestComputeNICMappingPrefersPermanentMAC(t *testing.T) {
	backup := &archivedNetworkInventory{
		Interfaces: []archivedNetworkInterface{
			{Name: "eno1", PermanentMAC: "00:11:22:33:44:55", MAC: "00:11:22:33:44:55"},
			{Name: "vmbr0", IsVirtual: true},
		},
	}
	current := &archivedNetworkInventory{
		Interfaces: []archivedNetworkInterface{
			{Name: "enp3s0", PermanentMAC: "00:11:22:33:44:55", MAC: "00:11:22:33:44:55"},
		},
	}

	got := computeNICMapping(backup, current)
	if got.IsEmpty() {
		t.Fatalf("expected mapping, got empty")
	}
	if got.Entries[0].OldName != "eno1" || got.Entries[0].NewName != "enp3s0" {
		t.Fatalf("unexpected entry: %+v", got.Entries[0])
	}
	if got.Entries[0].Method != nicMatchPermanentMAC {
		t.Fatalf("method=%s want %s", got.Entries[0].Method, nicMatchPermanentMAC)
	}
}

func TestComputeNICMappingUsesUdevIDPathWhenMACMissing(t *testing.T) {
	backup := &archivedNetworkInventory{
		Interfaces: []archivedNetworkInterface{
			{
				Name: "eno1",
				UdevProps: map[string]string{
					"ID_PATH": "pci-0000:00:1f.6",
				},
			},
		},
	}
	current := &archivedNetworkInventory{
		Interfaces: []archivedNetworkInterface{
			{
				Name: "enp3s0",
				UdevProps: map[string]string{
					"ID_PATH": "pci-0000:00:1f.6",
				},
			},
		},
	}

	got := computeNICMapping(backup, current)
	if got.IsEmpty() {
		t.Fatalf("expected mapping, got empty")
	}
	if got.Entries[0].OldName != "eno1" || got.Entries[0].NewName != "enp3s0" {
		t.Fatalf("unexpected entry: %+v", got.Entries[0])
	}
	if got.Entries[0].Method != nicMatchUdevIDPath {
		t.Fatalf("method=%s want %s", got.Entries[0].Method, nicMatchUdevIDPath)
	}
	if got.Entries[0].Identifier != "pci-0000:00:1f.6" {
		t.Fatalf("identifier=%q want %q", got.Entries[0].Identifier, "pci-0000:00:1f.6")
	}
}

func TestApplyInterfaceRenameMapReplacesTokensAndVLANs(t *testing.T) {
	original := strings.Join([]string{
		"auto lo",
		"iface lo inet loopback",
		"",
		"auto eno1",
		"iface eno1 inet manual",
		"",
		"auto vmbr0",
		"iface vmbr0 inet static",
		"    address 192.0.2.1/24",
		"    gateway 192.0.2.254",
		"    bridge_ports eno1",
		"",
		"auto eno1.100",
		"iface eno1.100 inet manual",
		"",
	}, "\n")

	updated, changed := applyInterfaceRenameMap(original, map[string]string{
		"eno1": "enp3s0",
	})
	if !changed {
		t.Fatalf("expected changed=true")
	}
	if strings.Contains(updated, " auto eno1") || strings.Contains(updated, "bridge_ports eno1") {
		t.Fatalf("expected eno1 to be replaced:\n%s", updated)
	}
	if !strings.Contains(updated, "auto enp3s0\n") {
		t.Fatalf("missing auto enp3s0:\n%s", updated)
	}
	if !strings.Contains(updated, "bridge_ports enp3s0\n") {
		t.Fatalf("missing bridge_ports enp3s0:\n%s", updated)
	}
	if !strings.Contains(updated, "auto enp3s0.100\n") || !strings.Contains(updated, "iface enp3s0.100 inet manual\n") {
		t.Fatalf("missing VLAN rename:\n%s", updated)
	}
	if !strings.Contains(updated, "auto vmbr0\n") {
		t.Fatalf("vmbr0 should be untouched:\n%s", updated)
	}
}

func TestReplaceInterfaceTokenDoesNotReplacePrefixes(t *testing.T) {
	input := "auto eno10\niface eno10 inet manual\n"
	out, changed := replaceInterfaceToken(input, "eno1", "enp3s0")
	if changed {
		t.Fatalf("expected changed=false, got true: %q", out)
	}
	if out != input {
		t.Fatalf("output differs unexpectedly: %q", out)
	}
}

func TestRewriteIfupdownConfigFilesWritesBackups(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2025, 1, 1, 1, 2, 3, 0, time.UTC)}

	if err := fakeFS.MkdirAll("/etc/network/interfaces.d", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	original := "auto eno1\niface eno1 inet manual\n"
	if err := fakeFS.WriteFile("/etc/network/interfaces", []byte(original), 0o644); err != nil {
		t.Fatalf("write interfaces: %v", err)
	}
	if err := fakeFS.WriteFile("/etc/network/interfaces.d/extra", []byte("auto vmbr0\n"), 0o644); err != nil {
		t.Fatalf("write extra: %v", err)
	}

	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)

	changed, backupDir, err := rewriteIfupdownConfigFiles(logger, map[string]string{"eno1": "enp3s0"})
	if err != nil {
		t.Fatalf("rewriteIfupdownConfigFiles error: %v", err)
	}
	if len(changed) != 1 || changed[0] != "/etc/network/interfaces" {
		t.Fatalf("changed=%v; want [/etc/network/interfaces]", changed)
	}
	if backupDir == "" {
		t.Fatalf("expected backupDir to be set")
	}

	updated, err := fakeFS.ReadFile("/etc/network/interfaces")
	if err != nil {
		t.Fatalf("read updated: %v", err)
	}
	if string(updated) != "auto enp3s0\niface enp3s0 inet manual\n" {
		t.Fatalf("updated=%q", string(updated))
	}

	backupPath := filepath.Join(backupDir, "etc/network/interfaces")
	backupContent, err := fakeFS.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backupContent) != original {
		t.Fatalf("backup content=%q; want %q", string(backupContent), original)
	}
}
