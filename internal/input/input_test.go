package input

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMapInputError(t *testing.T) {
	if MapInputError(nil) != nil {
		t.Fatalf("expected nil")
	}
	if !errors.Is(MapInputError(io.EOF), ErrInputAborted) {
		t.Fatalf("expected ErrInputAborted for EOF")
	}
	if !errors.Is(MapInputError(os.ErrClosed), ErrInputAborted) {
		t.Fatalf("expected ErrInputAborted for ErrClosed")
	}

	for _, msg := range []string{
		"use of closed file",
		"bad file descriptor",
		"file already closed",
		"Use Of Closed File", // case-insensitive
	} {
		if !errors.Is(MapInputError(errors.New(msg)), ErrInputAborted) {
			t.Fatalf("expected ErrInputAborted for %q", msg)
		}
	}

	sentinel := errors.New("some other error")
	if MapInputError(sentinel) != sentinel {
		t.Fatalf("expected passthrough for non-mapped errors")
	}
}

func TestIsAborted(t *testing.T) {
	if IsAborted(nil) {
		t.Fatalf("expected false for nil")
	}
	if !IsAborted(ErrInputAborted) {
		t.Fatalf("expected true for ErrInputAborted")
	}
	if !IsAborted(context.Canceled) {
		t.Fatalf("expected true for context.Canceled")
	}
	if IsAborted(errors.New("other")) {
		t.Fatalf("expected false for non-abort errors")
	}
}

func TestReadLineWithContext_ReturnsLine(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("hello\n"))
	got, err := ReadLineWithContext(context.Background(), reader)
	if err != nil {
		t.Fatalf("ReadLineWithContext error: %v", err)
	}
	if got != "hello\n" {
		t.Fatalf("got=%q; want %q", got, "hello\n")
	}
}

func TestReadLineWithContext_NilContextWorks(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("hello\n"))
	got, err := ReadLineWithContext(nil, reader)
	if err != nil {
		t.Fatalf("ReadLineWithContext error: %v", err)
	}
	if got != "hello\n" {
		t.Fatalf("got=%q; want %q", got, "hello\n")
	}
}

func TestReadLineWithContext_CancelledReturnsAborted(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	reader := bufio.NewReader(pr)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		_, err = ReadLineWithContext(ctx, reader)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("ReadLineWithContext did not return after cancellation")
	}
	if !errors.Is(err, ErrInputAborted) {
		t.Fatalf("err=%v; want %v", err, ErrInputAborted)
	}

	// Ensure the read goroutine unblocks and exits.
	_ = pw.Close()
}

func TestReadLineWithContext_DeadlineReturnsDeadlineExceeded(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	reader := bufio.NewReader(pr)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		_, err = ReadLineWithContext(ctx, reader)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("ReadLineWithContext did not return after deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v; want %v", err, context.DeadlineExceeded)
	}

	_ = pw.Close()
}

func TestReadPasswordWithContext_NilReadPasswordErrors(t *testing.T) {
	_, err := ReadPasswordWithContext(context.Background(), nil, 0)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestReadPasswordWithContext_ReturnsBytes(t *testing.T) {
	readPassword := func(fd int) ([]byte, error) {
		if fd != 123 {
			t.Fatalf("fd=%d; want 123", fd)
		}
		return []byte("secret"), nil
	}
	got, err := ReadPasswordWithContext(context.Background(), readPassword, 123)
	if err != nil {
		t.Fatalf("ReadPasswordWithContext error: %v", err)
	}
	if string(got) != "secret" {
		t.Fatalf("got=%q; want %q", string(got), "secret")
	}
}

func TestReadPasswordWithContext_NilContextWorks(t *testing.T) {
	readPassword := func(fd int) ([]byte, error) {
		return []byte("secret"), nil
	}
	got, err := ReadPasswordWithContext(nil, readPassword, 0)
	if err != nil {
		t.Fatalf("ReadPasswordWithContext error: %v", err)
	}
	if string(got) != "secret" {
		t.Fatalf("got=%q; want %q", string(got), "secret")
	}
}

func TestReadPasswordWithContext_CancelledReturnsAborted(t *testing.T) {
	unblock := make(chan struct{})
	readPassword := func(fd int) ([]byte, error) {
		<-unblock
		return []byte("secret"), nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := ReadPasswordWithContext(ctx, readPassword, 0)
	close(unblock) // ensure goroutine can exit
	if got != nil {
		t.Fatalf("expected nil bytes on cancel")
	}
	if !errors.Is(err, ErrInputAborted) {
		t.Fatalf("err=%v; want %v", err, ErrInputAborted)
	}
}

func TestReadPasswordWithContext_DeadlineReturnsDeadlineExceeded(t *testing.T) {
	unblock := make(chan struct{})
	readPassword := func(fd int) ([]byte, error) {
		<-unblock
		return []byte("secret"), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got, err := ReadPasswordWithContext(ctx, readPassword, 0)
	close(unblock)
	if got != nil {
		t.Fatalf("expected nil bytes on deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v; want %v", err, context.DeadlineExceeded)
	}
}
