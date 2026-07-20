package orchestrator

import (
	"bufio"
	"context"
	"strings"
	"testing"
)

// rawInjectionMarkers are raw terminal-control byte sequences that must never
// reach the user's terminal from backup-derived data on the CLI restore path.
var rawInjectionMarkers = []string{
	"\x1b]0;", // OSC title set introducer
	"\x07",    // BEL (OSC terminator / raw C0)
	"\x9b",    // C1 CSI
	"\x1b[2J", // CSI clear screen
	"pwned",   // payload carried inside the OSC title set
}

// TestWriteProposedMountsSanitizesEscapes covers LEAK 4 (proposed side):
// mount fields parsed from the backup /etc/fstab must be scrubbed before they
// land in the fstab merge confirmation message.
func TestWriteProposedMountsSanitizesEscapes(t *testing.T) {
	p := fstabMergeUIPrompt{
		analysis: FstabAnalysisResult{
			ProposedMounts: []FstabEntry{
				{
					Device:     "/dev/sda\x1b]0;pwned\x07",
					MountPoint: "/mnt/\x1b[2Jx",
					Type:       "ext4\x07",
				},
			},
		},
	}

	var msg strings.Builder
	p.writeProposedMounts(&msg)
	out := msg.String()

	for _, marker := range rawInjectionMarkers {
		if strings.Contains(out, marker) {
			t.Errorf("writeProposedMounts leaked raw injection marker %q in: %q", marker, out)
		}
	}

	// Legit field text must survive the scrub.
	for _, want := range []string{"/dev/sda", "/mnt/", "x", "ext4"} {
		if !strings.Contains(out, want) {
			t.Errorf("writeProposedMounts dropped legit text %q from: %q", want, out)
		}
	}
}

// TestWriteSkippedMountsSanitizesEscapes covers LEAK 4 (skipped side).
func TestWriteSkippedMountsSanitizesEscapes(t *testing.T) {
	p := fstabMergeUIPrompt{
		analysis: FstabAnalysisResult{
			SkippedMounts: []FstabEntry{
				{
					Device:     "UUID=abc\x9b",
					MountPoint: "/data\x1b]0;pwned\x07",
					Type:       "nfs\x1b[2J",
				},
			},
		},
	}

	var msg strings.Builder
	p.writeSkippedMounts(&msg)
	out := msg.String()

	if out == "" {
		t.Fatal("writeSkippedMounts produced no output for a non-empty SkippedMounts")
	}
	for _, marker := range rawInjectionMarkers {
		if strings.Contains(out, marker) {
			t.Errorf("writeSkippedMounts leaked raw injection marker %q in: %q", marker, out)
		}
	}
	for _, want := range []string{"UUID=", "abc", "/data", "nfs"} {
		if !strings.Contains(out, want) {
			t.Errorf("writeSkippedMounts dropped legit text %q from: %q", want, out)
		}
	}
}

// TestPromptExportNodeSelectionSanitizesEscapes covers LEAK 3: currentNode and
// each exported node directory name (attacker-influenceable) must be scrubbed
// before the CLI menu prints them. Feeding "0" makes the prompt return after a
// single render pass. countVMConfigsForNode tolerates a missing exportRoot
// (ReadDir error -> count 0), so the node rows still print.
func TestPromptExportNodeSelectionSanitizesEscapes(t *testing.T) {
	currentNode := "pve1\x1b]0;pwned\x07"
	exportNodes := []string{"node-A\x9b", "node-\x1b[2JB"}

	reader := bufio.NewReader(strings.NewReader("0\n"))
	out := captureCLIStdout(t, func() {
		if _, err := promptExportNodeSelection(context.Background(), reader, "/nonexistent-export-root", currentNode, exportNodes); err != nil {
			t.Fatalf("promptExportNodeSelection: %v", err)
		}
	})

	for _, marker := range rawInjectionMarkers {
		if strings.Contains(out, marker) {
			t.Errorf("promptExportNodeSelection leaked raw injection marker %q in: %q", marker, out)
		}
	}
	// Legit node name text must survive.
	for _, want := range []string{"pve1", "node-", "A", "B"} {
		if !strings.Contains(out, want) {
			t.Errorf("promptExportNodeSelection dropped legit text %q from: %q", want, out)
		}
	}
}
