package orchestrator

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
)

func TestDescribeBackupCandidate_Full(t *testing.T) {
	created := time.Date(2026, time.March, 22, 12, 21, 22, 0, time.UTC)
	cand := &backupCandidate{
		DisplayBase: "backup.tar.xz",
		Manifest: &backup.Manifest{
			CreatedAt:       created,
			Hostname:        "node1.example.com",
			EncryptionMode:  "age",
			ScriptVersion:   "1.2.3",
			ProxmoxTargets:  []string{"pve"},
			ProxmoxVersion:  "8.0",
			ClusterMode:     "cluster",
			CompressionType: "xz",
		},
	}

	display := describeBackupCandidate(cand)
	if display.Created != "2026-03-22 12:21:22" {
		t.Fatalf("Created=%q", display.Created)
	}
	if display.Hostname != "node1.example.com" {
		t.Fatalf("Hostname=%q", display.Hostname)
	}
	if display.Mode != "ENCRYPTED" {
		t.Fatalf("Mode=%q", display.Mode)
	}
	if display.Tool != "Tool v1.2.3" {
		t.Fatalf("Tool=%q", display.Tool)
	}
	if display.Target != "PVE v8.0 (cluster)" {
		t.Fatalf("Target=%q", display.Target)
	}
	if display.Compression != "XZ" {
		t.Fatalf("Compression=%q", display.Compression)
	}
	if display.Summary != "node1.example.com • backup.tar.xz (2026-03-22 12:21:22)" {
		t.Fatalf("Summary=%q", display.Summary)
	}
}

func TestDescribeBackupCandidate_Fallbacks(t *testing.T) {
	cand := &backupCandidate{
		RawArchivePath: "/tmp/archive.tar.xz",
		Manifest:       &backup.Manifest{},
	}

	display := describeBackupCandidate(cand)
	if display.Created != unknownBackupDateText {
		t.Fatalf("Created=%q, want %q", display.Created, unknownBackupDateText)
	}
	if display.Hostname != unknownBackupHostText {
		t.Fatalf("Hostname=%q, want %q", display.Hostname, unknownBackupHostText)
	}
	if display.Mode != "PLAIN" {
		t.Fatalf("Mode=%q, want %q", display.Mode, "PLAIN")
	}
	if display.Tool != "Tool unknown" {
		t.Fatalf("Tool=%q, want %q", display.Tool, "Tool unknown")
	}
	if display.Target != unknownBackupTargetText {
		t.Fatalf("Target=%q, want %q", display.Target, unknownBackupTargetText)
	}
	if display.Base != "archive.tar.xz" {
		t.Fatalf("Base=%q, want %q", display.Base, "archive.tar.xz")
	}
	if display.Summary != "archive.tar.xz" {
		t.Fatalf("Summary=%q, want %q", display.Summary, "archive.tar.xz")
	}
}

func TestBackupSummaryForUI_UsesSharedDisplayModel(t *testing.T) {
	created := time.Date(2026, time.March, 22, 12, 21, 22, 0, time.UTC)
	cand := &backupCandidate{
		DisplayBase: "backup.tar.xz",
		Manifest: &backup.Manifest{
			CreatedAt: created,
			Hostname:  "node1.example.com",
		},
	}

	if got := backupSummaryForUI(cand); got != "node1.example.com • backup.tar.xz (2026-03-22 12:21:22)" {
		t.Fatalf("backupSummaryForUI()=%q", got)
	}
}

func TestPromptCandidateSelection_PrintsHostname(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("0\n"))
	candidates := []*backupCandidate{
		{
			Manifest: &backup.Manifest{
				CreatedAt:      time.Date(2026, time.March, 22, 12, 21, 22, 0, time.UTC),
				Hostname:       "node1.example.com",
				EncryptionMode: "age",
				ScriptVersion:  "1.2.3",
				ProxmoxType:    "pve",
			},
		},
	}

	stdout := captureCLIStdout(t, func() {
		_, _ = promptCandidateSelection(context.Background(), reader, candidates)
	})

	if !strings.Contains(stdout, "Host node1.example.com") {
		t.Fatalf("expected hostname in CLI output, got %q", stdout)
	}
}
