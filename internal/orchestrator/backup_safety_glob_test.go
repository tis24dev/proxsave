package orchestrator

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"testing"
	"time"
)

func TestCreateSafetyBackup_ExpandsGlobPaths(t *testing.T) {
	origFS := safetyFS
	origNow := safetyNow
	t.Cleanup(func() {
		safetyFS = origFS
		safetyNow = origNow
	})

	fake := NewFakeFS()
	safetyFS = fake
	safetyNow = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

	if err := fake.AddFile("/etc/auto.master", []byte("a\n")); err != nil {
		t.Fatalf("add file: %v", err)
	}
	if err := fake.AddFile("/etc/auto.foo", []byte("b\n")); err != nil {
		t.Fatalf("add file: %v", err)
	}

	cats := []Category{{
		ID:    "test_glob",
		Name:  "Test glob",
		Paths: []string{"./etc/auto.*"},
	}}

	res, err := CreateSafetyBackup(newTestLogger(), cats, "/")
	if err != nil {
		t.Fatalf("CreateSafetyBackup error: %v", err)
	}
	if res == nil || res.BackupPath == "" {
		t.Fatalf("expected backup result with path")
	}

	f, err := safetyFS.Open(res.BackupPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer f.Close()

	gzReader, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gzReader.Close()

	tr := tar.NewReader(gzReader)
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		seen[h.Name] = true
	}

	if !seen["etc/auto.master"] {
		t.Fatalf("missing etc/auto.master in archive: seen=%v", seen)
	}
	if !seen["etc/auto.foo"] {
		t.Fatalf("missing etc/auto.foo in archive: seen=%v", seen)
	}
	if _, err := os.Stat(fake.onDisk(res.BackupPath)); err != nil {
		t.Fatalf("expected backup file to exist on disk: %v", err)
	}
}
