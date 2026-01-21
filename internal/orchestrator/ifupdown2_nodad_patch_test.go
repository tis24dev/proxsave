package orchestrator

import (
	"strings"
	"testing"
	"time"
)

func TestPatchIfupdown2NlcacheNodadSignature_AppliesAndBacksUp(t *testing.T) {
	fs := NewFakeFS()

	const nlcachePath = "/usr/share/ifupdown2/lib/nlcache.py"
	orig := []byte("x\n" +
		"def addr_add_dry_run(self, ifname, addr, broadcast=None, peer=None, scope=None, preferred_lifetime=None, metric=None):\n" +
		"    pass\n")
	if err := fs.WriteFile(nlcachePath, orig, 0o644); err != nil {
		t.Fatalf("write nlcache: %v", err)
	}

	now := time.Date(2026, 1, 20, 15, 4, 58, 0, time.UTC)
	backup, applied, err := patchIfupdown2NlcacheNodadSignature(fs, nlcachePath, orig, now)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if !applied {
		t.Fatalf("expected applied=true")
	}
	if backup == "" {
		t.Fatalf("expected backup path")
	}

	updated, err := fs.ReadFile(nlcachePath)
	if err != nil {
		t.Fatalf("read patched: %v", err)
	}
	if string(updated) == string(orig) {
		t.Fatalf("expected file to change")
	}
	if !strings.Contains(string(updated), "nodad=False") {
		t.Fatalf("expected nodad=False in patched file, got:\n%s", string(updated))
	}

	backupBytes, err := fs.ReadFile(backup)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backupBytes) != string(orig) {
		t.Fatalf("backup content mismatch")
	}
}

func TestPatchIfupdown2NlcacheNodadSignature_SkipsIfAlreadyPatched(t *testing.T) {
	fs := NewFakeFS()

	const nlcachePath = "/usr/share/ifupdown2/lib/nlcache.py"
	orig := []byte("def addr_add_dry_run(self, ifname, addr, broadcast=None, peer=None, scope=None, preferred_lifetime=None, metric=None, nodad=False):\n")
	if err := fs.WriteFile(nlcachePath, orig, 0o644); err != nil {
		t.Fatalf("write nlcache: %v", err)
	}

	backup, applied, err := patchIfupdown2NlcacheNodadSignature(fs, nlcachePath, orig, time.Now())
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if applied {
		t.Fatalf("expected applied=false")
	}
	if backup != "" {
		t.Fatalf("expected no backup path")
	}
}
