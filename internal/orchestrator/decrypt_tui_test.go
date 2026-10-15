package orchestrator

import (
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
		{"unknown", "unknown"},
		{"Unknown", "unknown"},
		// Only the literal value detection records is special; anything else is a version.
		{"unknown-1", "vunknown-1"},
	}

	for _, tt := range cases {
		if got := normalizeProxmoxVersion(tt.in); got != tt.want {
			t.Fatalf("normalizeProxmoxVersion(%q)=%q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFilterEncryptedCandidates(t *testing.T) {
	now := time.Now()
	encrypted := &backupCandidate{Manifest: &backup.Manifest{EncryptionMode: "age", CreatedAt: now}}
	plain := &backupCandidate{Manifest: &backup.Manifest{EncryptionMode: "none", CreatedAt: now}}

	filtered := filterEncryptedCandidates([]*backupCandidate{nil, encrypted, plain, {}})
	if len(filtered) != 1 || filtered[0] != encrypted {
		t.Fatalf("filterEncryptedCandidates returned %+v, want only encrypted candidate", filtered)
	}
}
