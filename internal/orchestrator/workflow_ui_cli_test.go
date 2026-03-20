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
