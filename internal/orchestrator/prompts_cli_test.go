package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestPromptYesNo(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"yes-short", "y\n", true},
		{"yes-long", "yes\n", true},
		{"yes-mixed", " YeS \n", true},
		{"no-default", "\n", false},
		{"no-explicit", "no\n", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(tt.in))
			got, err := promptYesNo(context.Background(), reader, "prompt: ")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestPromptYesNo_ContextCanceledReturnsAbortError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reader := bufio.NewReader(strings.NewReader("y\n"))
	_, err := promptYesNo(ctx, reader, "prompt: ")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, input.ErrInputAborted) {
		t.Fatalf("err=%v; want %v", err, input.ErrInputAborted)
	}
}

func TestPromptYesNoWithCountdown_ZeroTimeoutUsesDefault(test *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\n"))
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	result, err := promptYesNoWithCountdown(context.Background(), reader, logger, "Proceed?", 0, true)
	if err != nil {
		test.Fatalf("unexpected error: %v", err)
	}
	if !result {
		test.Fatalf("expected true for default yes")
	}
}

func TestPromptYesNoWithCountdown_InputYes(test *testing.T) {
	reader := bufio.NewReader(strings.NewReader("yes\n"))
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	result, err := promptYesNoWithCountdown(context.Background(), reader, logger, "Proceed?", 2*time.Second, false)
	if err != nil {
		test.Fatalf("unexpected error: %v", err)
	}
	if !result {
		test.Fatalf("expected true for yes input")
	}
}

func TestPromptYesNoWithCountdown_TimeoutReturnsNo(test *testing.T) {
	pipeReader, pipeWriter := io.Pipe()
	defer pipeReader.Close()
	defer pipeWriter.Close()

	reader := bufio.NewReader(pipeReader)
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	result, err := promptYesNoWithCountdown(context.Background(), reader, logger, "Proceed?", 100*time.Millisecond, true)
	if err != nil {
		test.Fatalf("unexpected error: %v", err)
	}
	if result {
		test.Fatalf("expected false on timeout")
	}
}
