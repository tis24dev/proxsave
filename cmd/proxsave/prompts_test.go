package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestPromptYesNo(t *testing.T) {
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\n"))
	got, err := promptYesNo(ctx, reader, "Continue? ", false)
	if err != nil {
		t.Fatalf("promptYesNo returned error: %v", err)
	}
	if !got {
		t.Fatalf("expected true for 'y' response")
	}

	reader = bufio.NewReader(strings.NewReader("\n"))
	got, err = promptYesNo(ctx, reader, "Continue? ", true)
	if err != nil {
		t.Fatalf("promptYesNo default error: %v", err)
	}
	if !got {
		t.Fatalf("expected default true when response empty")
	}

	reader = bufio.NewReader(strings.NewReader("maybe\nn\n"))
	got, err = promptYesNo(ctx, reader, "Continue? ", true)
	if err != nil {
		t.Fatalf("promptYesNo invalid retry error: %v", err)
	}
	if got {
		t.Fatalf("expected false after answering 'n'")
	}
}

func TestPromptYesNoContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := promptYesNo(ctx, bufio.NewReader(strings.NewReader("y\n")), "Continue? ", false)
	if !errors.Is(err, errInteractiveAborted) {
		t.Fatalf("expected errInteractiveAborted, got %v", err)
	}
}

func TestPromptNonEmpty(t *testing.T) {
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("\nvalue\n"))
	got, err := promptNonEmpty(ctx, reader, "Enter value: ")
	if err != nil {
		t.Fatalf("promptNonEmpty error: %v", err)
	}
	if got != "value" {
		t.Fatalf("expected 'value', got %q", got)
	}
}

func TestReadLineWithContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := bufio.NewReader(strings.NewReader("ignored\n"))
	if _, err := readLineWithContext(ctx, reader); !errors.Is(err, errInteractiveAborted) {
		t.Fatalf("expected errInteractiveAborted, got %v", err)
	}
}

func TestReadLineWithContextSuccess(t *testing.T) {
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("hello\n"))
	line, err := readLineWithContext(ctx, reader)
	if err != nil {
		t.Fatalf("readLineWithContext error: %v", err)
	}
	if line != "hello\n" {
		t.Fatalf("expected full line with newline, got %q", line)
	}
}

func TestEnsureInteractiveStdinNotTTY(t *testing.T) {
	origStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdin = r
	defer func() {
		os.Stdin = origStdin
		_ = r.Close()
		_ = w.Close()
	}()

	if err := ensureInteractiveStdin(); err == nil {
		t.Fatalf("expected error when stdin is not a terminal")
	}
}
