package wizard

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExtractIssueLinesFromProxsaveOutput_UsesSummaryBlock(t *testing.T) {
	output := strings.Join([]string{
		"[2026-02-24 09:12:55] \x1b[33mWARNING\x1b[0m Live warning (colored)",
		"===========================================",
		"WARNINGS/ERRORS DURING RUN (warnings=1 errors=0)",
		"",
		"[2026-02-24 09:12:55] WARNING    Corosync authkey: not configured. If unused, set BACKUP_CLUSTER_CONFIG=false to disable.",
		"===========================================",
		"",
	}, "\n")

	issues := extractIssueLinesFromProxsaveOutput(output)
	if len(issues) != 1 {
		t.Fatalf("issues len=%d, want 1: %#v", len(issues), issues)
	}
	if !strings.Contains(issues[0], "set BACKUP_CLUSTER_CONFIG=false") {
		t.Fatalf("unexpected issue line: %q", issues[0])
	}
}

func TestExtractDisableSuggestionsFromIssueLines_FiltersAllowedAndEnabled(t *testing.T) {
	allowed := map[string]struct{}{
		"BACKUP_CLUSTER_CONFIG": {},
		"BACKUP_ZFS_CONFIG":     {},
	}
	configValues := map[string]string{
		"BACKUP_CLUSTER_CONFIG": "true",
		"BACKUP_ZFS_CONFIG":     "true",
		"BACKUP_CEPH_CONFIG":    "false",
	}
	lines := []string{
		"[2026-02-24 09:12:55] WARNING    Corosync authkey: not configured. If unused, set BACKUP_CLUSTER_CONFIG=false to disable.",
		"Skipping ZFS collection: not detected. Set BACKUP_ZFS_CONFIG=false to disable.",
		"[2026-02-24 09:12:55] WARNING    Something else. Set NOT_BACKUP_VAR=false to disable.",
		"[2026-02-24 09:12:55] WARNING    Ceph not detected. If unused, set BACKUP_CEPH_CONFIG=false to disable.",
	}

	got := extractDisableSuggestionsFromIssueLines(lines, allowed, configValues)
	wantKeys := []string{"BACKUP_CLUSTER_CONFIG", "BACKUP_ZFS_CONFIG"}
	if len(got) != len(wantKeys) {
		t.Fatalf("got %d suggestions, want %d: %#v", len(got), len(wantKeys), got)
	}
	for i, key := range wantKeys {
		if got[i].Key != key {
			t.Fatalf("suggestion[%d].Key=%q, want %q", i, got[i].Key, key)
		}
	}
}

func TestCollectPostInstallDisableSuggestions_UsesRunnerAndConfigFilter(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "backup.env")
	if err := os.WriteFile(configPath, []byte("BACKUP_CLUSTER_CONFIG=true\nBACKUP_ZFS_CONFIG=false\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	origRunner := postInstallAuditRunner
	t.Cleanup(func() { postInstallAuditRunner = origRunner })

	postInstallAuditRunner = func(ctx context.Context, execPath, cfgPath string) (string, int, error) {
		return strings.Join([]string{
			"===========================================",
			"WARNINGS/ERRORS DURING RUN (warnings=1 errors=0)",
			"",
			"[2026-02-24 09:12:55] WARNING    Corosync authkey: not configured. If unused, set BACKUP_CLUSTER_CONFIG=false to disable.",
			"===========================================",
		}, "\n"), 1, nil
	}

	suggestions, err := CollectPostInstallDisableSuggestions(context.Background(), "/fake/proxsave", configPath)
	if err != nil {
		t.Fatalf("CollectPostInstallDisableSuggestions error: %v", err)
	}
	if len(suggestions) != 1 {
		t.Fatalf("got %d suggestions, want 1: %#v", len(suggestions), suggestions)
	}
	if suggestions[0].Key != "BACKUP_CLUSTER_CONFIG" {
		t.Fatalf("key=%q, want BACKUP_CLUSTER_CONFIG", suggestions[0].Key)
	}
	if len(suggestions[0].Messages) != 1 || !strings.Contains(suggestions[0].Messages[0], "Corosync authkey") {
		t.Fatalf("unexpected messages: %#v", suggestions[0].Messages)
	}
}

func TestNormalizeIssueMessage_RemovesTimestampAndLevel(t *testing.T) {
	got := normalizeIssueMessage("[2026-02-24 09:12:55] WARNING    hello world")
	want := "hello world"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStripANSI_RemovesSGRCodes(t *testing.T) {
	in := "\x1b[33mWARNING\x1b[0m hello"
	got := stripANSI(in)
	want := "WARNING hello"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSplitNormalizedLines_NormalizesCRLF(t *testing.T) {
	got := splitNormalizedLines("a\r\nb\r\n")
	want := []string{"a", "b", ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
