package orchestrator

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A recreated resource mapping kept only id and map: the export carries description,
// mdev and live-migration-capable (measured on pve-test, PVE 9.1.9, the flags as
// numbers), and the create call dropped all three.
func TestSafeClusterApplyRecreatesMappingDescriptionAndPCIFlags(t *testing.T) {
	origCmd, origFS := restoreCmd, restoreFS
	t.Cleanup(func() { restoreCmd, restoreFS = origCmd, origFS })
	restoreFS = osFS{}
	pathDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pathDir, "pvesh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runner := &recordingRunner{}
	restoreCmd = runner

	exportRoot := t.TempDir()
	dir := filepath.Join(exportRoot, "var", "lib", "proxsave-info", "commands", "pve")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	measured := `[{"description":"probe desc","digest":"e747a3a6031f1400fadfa792d11f85bbad63763d","id":"probe8","live-migration-capable":1,"map":["node=pve,path=0000:00:00.0,id=8086:7190"],"mdev":1,"type":"pci"}]`
	if err := os.WriteFile(filepath.Join(dir, "mapping_pci.json"), []byte(measured), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := runSafeClusterApply(context.Background(), bufio.NewReader(strings.NewReader("yes\n")), exportRoot, newTestLogger()); err != nil {
		t.Fatalf("runSafeClusterApply: %v", err)
	}
	want := "pvesh create /cluster/mapping/pci --id probe8 --description probe desc --mdev 1 --live-migration-capable 1 --map node=pve,path=0000:00:00.0,id=8086:7190"
	if !slices.Contains(runner.calls, want) {
		t.Fatalf("no call %q; calls=%#v", want, runner.calls)
	}
}
