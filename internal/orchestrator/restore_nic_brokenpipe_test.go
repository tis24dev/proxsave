package orchestrator

import (
	"archive/tar"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// LIVE-NIC-BROKENPIPE: an xz archive whose network_inventory.json is NOT the
// last entry must load successfully. Before the fix, the piped xz dies on
// SIGPIPE when the reader stops early and its close error is returned in place
// of the valid inventory bytes.
func TestReadArchiveEntry_XZ_EntryNotLast_NoBrokenPipe(t *testing.T) {
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz not installed")
	}
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	dir := t.TempDir()
	plain := filepath.Join(dir, "b.tar")
	f, err := os.Create(plain)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	inv := archivedNetworkInventory{Hostname: "repro"}
	invBytes, _ := json.Marshal(inv)
	write := func(name string, content []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: "./" + name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	write("var/lib/proxsave-info/commands/system/network_inventory.json", invBytes)
	pad := []byte(strings.Repeat("x", 65536))
	for i := 0; i < 200; i++ {
		write("padding/f"+strings.Repeat("0", i%5)+string(rune('a'+i%26))+".dat", pad)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	xzPath := filepath.Join(dir, "backup.tar.xz")
	out, err := os.Create(xzPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("xz", "-z", "-c", plain)
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		t.Fatalf("xz: %v", err)
	}
	_ = out.Close()

	got, source, err := loadBackupNetworkInventoryFromArchive(context.Background(), xzPath)
	if err != nil {
		t.Fatalf("load must succeed, got err=%v", err)
	}
	if got == nil || got.Hostname != "repro" {
		t.Fatalf("inventory not loaded: %+v (source=%q)", got, source)
	}
}
