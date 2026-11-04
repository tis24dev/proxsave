package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The footers and the missing-config hint offered "proxmox-backup" as an alias, but
// install and upgrade delete that entrypoint (37e9e420) and nothing recreates it: an
// operator following the hint got "command not found".
func TestNoTextOffersTheRemovedProxmoxBackupAlias(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "alias: proxmox-backup") {
			t.Errorf("%s still offers the removed proxmox-backup alias", f)
		}
	}
}
