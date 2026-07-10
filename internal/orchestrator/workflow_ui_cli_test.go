package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func captureCLIStdout(t *testing.T, fn func()) (captured string) {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	defer func() {
		os.Stdout = oldStdout
		_ = w.Close()
		<-done
		_ = r.Close()
		captured = buf.String()
	}()

	fn()
	return
}

func captureCLIStderr(t *testing.T, fn func()) (captured string) {
	t.Helper()

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	defer func() {
		os.Stderr = oldStderr
		_ = w.Close()
		<-done
		_ = r.Close()
		captured = buf.String()
	}()

	fn()
	return
}

func TestCLIWorkflowUIResolveExistingPath_RejectsEquivalentNormalizedPath(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("2\n/tmp/out/\n2\n /tmp/out/../alt \n"))
	ui := newCLIWorkflowUI(reader, nil)

	var (
		decision ExistingPathDecision
		newPath  string
		err      error
	)
	stderrOutput := captureCLIStderr(t, func() {
		decision, newPath, err = ui.ResolveExistingPath(context.Background(), "/tmp/out", "archive", "")
	})
	if err != nil {
		t.Fatalf("ResolveExistingPath error: %v", err)
	}
	if decision != PathDecisionNewPath {
		t.Fatalf("decision=%v, want %v", decision, PathDecisionNewPath)
	}
	if newPath != "/tmp/alt" {
		t.Fatalf("newPath=%q, want %q", newPath, "/tmp/alt")
	}
	if !strings.Contains(stderrOutput, "path must be different from existing path") {
		t.Fatalf("expected validation message in stderr, got %q", stderrOutput)
	}
}

func TestCLIWorkflowUIResolveExistingPath_EmptyPathRetriesUntilValid(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("2\n   \n2\n/tmp/next\n"))
	ui := newCLIWorkflowUI(reader, nil)

	decision, newPath, err := ui.ResolveExistingPath(context.Background(), "/tmp/out", "archive", "")
	if err != nil {
		t.Fatalf("ResolveExistingPath error: %v", err)
	}
	if decision != PathDecisionNewPath {
		t.Fatalf("decision=%v, want %v", decision, PathDecisionNewPath)
	}
	if newPath != "/tmp/next" {
		t.Fatalf("newPath=%q, want %q", newPath, "/tmp/next")
	}
}

// TestCLIWorkflowUIShowStatusResultSanitizesInjection: in non-TUI mode the same
// external error text (e.g. rclone output in a scan error) is printed to stdout;
// raw escape/control bytes must be stripped before reaching the terminal.
func TestCLIWorkflowUIShowStatusResultSanitizesInjection(t *testing.T) {
	ui := newCLIWorkflowUI(bufio.NewReader(strings.NewReader("")), nil)
	out := captureCLIStdout(t, func() {
		_ = ui.ShowStatusResult(context.Background(), "Decrypt", HealthcheckSetupLevelWarn,
			"SCAN FAILED", "failed: \x1b[2J\x07oops\x1b]0;evil\x07")
	})
	for _, bad := range []string{"\x1b]0;", "\x07", "\x1b[2J", "evil"} {
		if strings.Contains(out, bad) {
			t.Fatalf("CLI ShowStatusResult leaked injected sequence %q: %q", bad, out)
		}
	}
	for _, want := range []string{"SCAN FAILED", "oops"} {
		if !strings.Contains(out, want) {
			t.Fatalf("CLI ShowStatusResult dropped legitimate text %q: %q", want, out)
		}
	}
}

// TestCLIWorkflowUIConfirmApplyVMConfigsSanitizesNode: sourceNode is an export
// node directory name read from inside the backup archive; it must be scrubbed
// before the CLI cluster-apply confirmation prints it.
func TestCLIWorkflowUIConfirmApplyVMConfigsSanitizesNode(t *testing.T) {
	ui := newCLIWorkflowUI(bufio.NewReader(strings.NewReader("n\n")), nil)
	out := captureCLIStdout(t, func() {
		_, _ = ui.ConfirmApplyVMConfigs(context.Background(), "node-A\x1b]0;pwned\x07", "pve1", 3)
	})
	for _, bad := range []string{"\x1b]0;", "\x07", "\x9b", "\x1b[2J", "pwned"} {
		if strings.Contains(out, bad) {
			t.Fatalf("ConfirmApplyVMConfigs leaked injected sequence %q: %q", bad, out)
		}
	}
	for _, want := range []string{"node-A", "pve1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ConfirmApplyVMConfigs dropped legitimate text %q: %q", want, out)
		}
	}
}

// TestCLIWorkflowUIRunTaskSanitizesProgress: RunTask progress messages can embed
// remote/archive filenames (e.g. rclone lsf entries); they must be scrubbed
// before printing to stderr.
func TestCLIWorkflowUIRunTaskSanitizesProgress(t *testing.T) {
	ui := newCLIWorkflowUI(bufio.NewReader(strings.NewReader("")), nil)
	out := captureCLIStderr(t, func() {
		_ = ui.RunTask(context.Background(), "Scan", "", func(_ context.Context, report ProgressReporter) error {
			report("Inspecting 1/2: file\x1b]0;pwned\x07name\x1b[2J.tar")
			return nil
		})
	})
	for _, bad := range []string{"\x1b]0;", "\x07", "\x1b[2J", "pwned"} {
		if strings.Contains(out, bad) {
			t.Fatalf("RunTask progress leaked injected sequence %q: %q", bad, out)
		}
	}
	if !strings.Contains(out, "Inspecting 1/2:") || !strings.Contains(out, "filename.tar") {
		t.Fatalf("RunTask dropped legitimate progress text: %q", out)
	}
}

// TestCLIWorkflowUIPromptDecryptSecretSanitizesDisplayName: displayName is the
// manifest archive filename; it must be scrubbed before the prompt prints it.
func TestCLIWorkflowUIPromptDecryptSecretSanitizesDisplayName(t *testing.T) {
	orig := readPassword
	readPassword = func(int) ([]byte, error) { return []byte("0"), nil } // 0 = exit, no terminal read
	defer func() { readPassword = orig }()

	ui := newCLIWorkflowUI(bufio.NewReader(strings.NewReader("")), nil)
	out := captureCLIStdout(t, func() {
		_, _ = ui.PromptDecryptSecret(context.Background(), "arch\x1b]0;pwned\x07ive.tar.age", "")
	})
	for _, bad := range []string{"\x1b]0;", "\x07", "pwned"} {
		if strings.Contains(out, bad) {
			t.Fatalf("PromptDecryptSecret leaked injected sequence %q: %q", bad, out)
		}
	}
	if !strings.Contains(out, "archive.tar.age") {
		t.Fatalf("PromptDecryptSecret dropped legitimate filename: %q", out)
	}
}

// TestCLIWorkflowUIConfirmActionSanitizesMessage: the confirm message embeds
// backup-derived data (NIC names, PVE pool IDs); it must be scrubbed before the
// CLI confirmation prints it. timeout=0 skips the countdown for a fast read.
func TestCLIWorkflowUIConfirmActionSanitizesMessage(t *testing.T) {
	ui := newCLIWorkflowUI(bufio.NewReader(strings.NewReader("no\n")), nil)
	out := captureCLIStdout(t, func() {
		_, _ = ui.ConfirmAction(context.Background(), "Apply network config?",
			"NIC map: eth0\x1b]0;pwned\x07 -> \x1b[2Jeth1", "Yes", "No", 0, false)
	})
	for _, bad := range []string{"\x1b]0;", "\x07", "\x1b[2J", "pwned"} {
		if strings.Contains(out, bad) {
			t.Fatalf("ConfirmAction leaked injected sequence %q: %q", bad, out)
		}
	}
	if !strings.Contains(out, "NIC map: eth0") || !strings.Contains(out, "eth1") {
		t.Fatalf("ConfirmAction dropped legitimate message text: %q", out)
	}
}
