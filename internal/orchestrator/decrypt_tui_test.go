package orchestrator

import (
	"testing"
	"time"

	"github.com/rivo/tview"

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

	filtered := filterEncryptedCandidates([]*decryptCandidate{nil, encrypted, plain, {}})
	if len(filtered) != 1 || filtered[0] != encrypted {
		t.Fatalf("filterEncryptedCandidates returned %+v, want only encrypted candidate", filtered)
	}
}

func TestBuildWizardPageReturnsFlex(t *testing.T) {
	content := tview.NewBox()
	page := buildWizardPage("Title", "/etc/proxsave/backup.env", "sig", content)
	if page == nil {
		t.Fatalf("expected non-nil page")
	}
	if _, ok := page.(*tview.Flex); !ok {
		t.Fatalf("expected *tview.Flex, got %T", page)
	}
}
