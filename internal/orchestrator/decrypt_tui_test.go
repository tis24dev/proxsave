package orchestrator

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
)

func TestNormalizeProxmoxVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"8.1", "v8.1"},
		{"v7.4", "v7.4"},
		{"V9", "V9"},
	}

	for _, tt := range cases {
		if got := normalizeProxmoxVersion(tt.in); got != tt.want {
			t.Fatalf("normalizeProxmoxVersion(%q)=%q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuildTargetInfo(t *testing.T) {
	manifest := &backup.Manifest{
		ProxmoxTargets: []string{"pbs", "node1"},
		ProxmoxVersion: "8.0",
		ClusterMode:    "cluster",
		CreatedAt:      time.Now(),
	}

	got := buildTargetInfo(manifest)
	want := "Targets: PBS+NODE1 v8.0 (cluster)"
	if got != want {
		t.Fatalf("buildTargetInfo()=%q, want %q", got, want)
	}

	manifest = &backup.Manifest{
		ProxmoxType: "pbs",
	}
	if got := buildTargetInfo(manifest); got != "Targets: PBS" {
		t.Fatalf("buildTargetInfo fallback=%q, want %q", got, "Targets: PBS")
	}
}

func TestFilterEncryptedCandidates(t *testing.T) {
	now := time.Now()
	encrypted := &decryptCandidate{Manifest: &backup.Manifest{EncryptionMode: "age", CreatedAt: now}}
	plain := &decryptCandidate{Manifest: &backup.Manifest{EncryptionMode: "none", CreatedAt: now}}

	filtered := filterEncryptedCandidates([]*decryptCandidate{nil, encrypted, plain, &decryptCandidate{}})
	if len(filtered) != 1 || filtered[0] != encrypted {
		t.Fatalf("filterEncryptedCandidates returned %+v, want only encrypted candidate", filtered)
	}
}

func TestEnsureWritablePathTUI_ReturnsCleanMissingPath(t *testing.T) {
	originalFS := restoreFS
	restoreFS = osFS{}
	defer func() { restoreFS = originalFS }()

	tmp := t.TempDir()
	target := filepath.Join(tmp, "subdir", "file.txt")
	dirty := target + string(filepath.Separator) + ".." + string(filepath.Separator) + "file.txt"

	path, err := ensureWritablePathTUI(dirty, "test file", "cfg", "sig")
	if err != nil {
		t.Fatalf("ensureWritablePathTUI returned error: %v", err)
	}
	if path != target {
		t.Fatalf("ensureWritablePathTUI path=%q, want %q", path, target)
	}
}
