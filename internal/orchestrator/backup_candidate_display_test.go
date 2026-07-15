package orchestrator

import (
	"bufio"
	"context"
	"errors"
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

// rawEscapeMarkers are substrings that must never survive sanitization into a
// display field. They come from the manifest, which is fetched raw off a
// remote (rclone cat), so an attacker controls Hostname/ScriptVersion/etc.
var rawEscapeMarkers = []string{"\x1b]0;", "\x07", "\x9b", "\x1b[2J", "\x1b["}

// TestDescribeBackupCandidate_SanitizesManifestFields proves the LEAK-1 choke
// point: manifest-derived fields carrying terminal escapes are scrubbed inside
// describeBackupCandidate, so both the CLI table and Summary are clean.
func TestDescribeBackupCandidate_SanitizesManifestFields(t *testing.T) {
	created := time.Date(2026, time.March, 22, 12, 21, 22, 0, time.UTC)
	cand := &backupCandidate{
		// filepath.Base(ArchivePath) style value with an OSC injection.
		DisplayBase: "arch\x1b]0;pwned\x07ive.tar.xz",
		Manifest: &backup.Manifest{
			CreatedAt: created,
			// OSC title-set + BEL, and a C1 CSI byte (0x9b).
			Hostname:      "pve\x1b]0;pwned\x07host\x9b",
			ScriptVersion: "1.2.3\x1b[2Jevil",
			// Clear-screen CSI in a field that flows into Target.
			ProxmoxTargets:  []string{"pve\x1b[2Jevil"},
			ProxmoxVersion:  "8.0",
			CompressionType: "xz\x1b[31m",
		},
	}

	display := describeBackupCandidate(cand)

	// Every string field, and the derived Summary, must be marker-free.
	fields := map[string]string{
		"Created":     display.Created,
		"Hostname":    display.Hostname,
		"Mode":        display.Mode,
		"Tool":        display.Tool,
		"Target":      display.Target,
		"Compression": display.Compression,
		"Base":        display.Base,
		"Summary":     display.Summary,
	}
	for name, val := range fields {
		for _, marker := range rawEscapeMarkers {
			if strings.Contains(val, marker) {
				t.Fatalf("%s field retained raw escape %q: %q", name, marker, val)
			}
		}
	}

	// Legitimate text must survive; only the escapes are stripped.
	if !strings.Contains(display.Hostname, "pve") || !strings.Contains(display.Hostname, "host") {
		t.Fatalf("Hostname lost legitimate text: %q", display.Hostname)
	}
	if !strings.Contains(display.Tool, "1.2.3") {
		t.Fatalf("Tool lost legitimate text: %q", display.Tool)
	}
	if !strings.Contains(display.Base, "ive.tar.xz") {
		t.Fatalf("Base lost legitimate text: %q", display.Base)
	}
	if !strings.Contains(display.Summary, "pve") {
		t.Fatalf("Summary lost legitimate hostname text: %q", display.Summary)
	}
}

// TestConfirmRestoreAction_SanitizesDisplayBase proves the LEAK-2 fix: the CLI
// confirmation prints the remote-derived archive filename scrubbed. We drive
// the prompt to cancel (input "0") after the line is printed.
func TestConfirmRestoreAction_SanitizesDisplayBase(t *testing.T) {
	cand := &backupCandidate{
		DisplayBase: "arch\x1b]0;pwned\x07ive.tar.xz\x9b",
		Manifest:    &backup.Manifest{CreatedAt: time.Date(2026, time.March, 22, 12, 21, 22, 0, time.UTC)},
	}
	reader := bufio.NewReader(strings.NewReader("0\n"))

	var abortErr error
	stdout := captureCLIStdout(t, func() {
		abortErr = confirmRestoreAction(context.Background(), reader, cand, "/")
	})

	if !errors.Is(abortErr, ErrRestoreAborted) {
		t.Fatalf("expected ErrRestoreAborted, got: %v", abortErr)
	}
	for _, marker := range rawEscapeMarkers {
		if strings.Contains(stdout, marker) {
			t.Fatalf("confirmRestoreAction output retained raw escape %q: %q", marker, stdout)
		}
	}
	// The legitimate filename fragments must still print.
	if !strings.Contains(stdout, "arch") || !strings.Contains(stdout, "ive.tar.xz") {
		t.Fatalf("expected sanitized filename in output, got %q", stdout)
	}
}
