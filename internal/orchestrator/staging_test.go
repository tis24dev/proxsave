package orchestrator

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestCreateRestoreStageDir_Creates0700Directory(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	origSeq := restoreStageSequence
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		restoreStageSequence = origSeq
	})

	fake := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fake.Root) })
	restoreFS = fake
	restoreTime = &FakeTime{Current: time.Unix(1700000000, 0)}
	restoreStageSequence = 0

	stageRoot, err := createRestoreStageDir()
	if err != nil {
		t.Fatalf("createRestoreStageDir error: %v", err)
	}
	if !strings.HasPrefix(stageRoot, "/tmp/proxsave/restore-stage-") {
		t.Fatalf("stageRoot=%q; want under /tmp/proxsave/restore-stage-*", stageRoot)
	}

	info, err := fake.Stat(stageRoot)
	if err != nil {
		t.Fatalf("Stat(%q): %v", stageRoot, err)
	}
	if info == nil || !info.IsDir() {
		t.Fatalf("Stat(%q): isDir=%v; want dir", stageRoot, info != nil && info.IsDir())
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("stageRoot perm=%#o; want %#o", perm, 0o700)
	}
}

func TestCleanupOldRestoreStageDirs_RemovesOnlyOldDirs(t *testing.T) {
	fake := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fake.Root) })

	base := "/tmp/proxsave"
	oldDir := base + "/restore-stage-old"
	newDir := base + "/restore-stage-new"

	if err := fake.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", base, err)
	}
	if err := fake.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q): %v", oldDir, err)
	}
	if err := fake.MkdirAll(newDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q): %v", newDir, err)
	}
	if err := fake.WriteFile(base+"/restore-stage-file", []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile restore-stage-file: %v", err)
	}

	now := time.Unix(1700000000, 0).UTC()
	oldTime := now.Add(-48 * time.Hour)
	newTime := now.Add(-1 * time.Hour)

	if err := os.Chtimes(fake.onDisk(oldDir), oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes(oldDir): %v", err)
	}
	if err := os.Chtimes(fake.onDisk(newDir), newTime, newTime); err != nil {
		t.Fatalf("Chtimes(newDir): %v", err)
	}

	removed, failed := cleanupOldRestoreStageDirs(fake, nil, now, 24*time.Hour)
	if failed != 0 {
		t.Fatalf("failed=%d; want 0", failed)
	}
	if removed != 1 {
		t.Fatalf("removed=%d; want 1", removed)
	}

	if _, err := fake.Stat(oldDir); err == nil || !os.IsNotExist(err) {
		t.Fatalf("oldDir still exists (err=%v); want removed", err)
	}
	if _, err := fake.Stat(newDir); err != nil {
		t.Fatalf("newDir missing (err=%v); want kept", err)
	}
	if _, err := fake.Stat(base + "/restore-stage-file"); err != nil {
		t.Fatalf("restore-stage-file missing (err=%v); want kept", err)
	}
}
