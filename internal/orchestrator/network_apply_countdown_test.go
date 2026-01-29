package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestPromptNetworkCommitWithCountdown_ZeroRemaining(test *testing.T) {
	reader := bufio.NewReader(strings.NewReader(""))
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	result, err := promptNetworkCommitWithCountdown(context.Background(), reader, logger, 0)
	if result {
		test.Fatalf("expected false when remaining is zero")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		test.Fatalf("err=%v; want %v", err, context.DeadlineExceeded)
	}
}

func TestPromptNetworkCommitWithCountdown_CommitInputReturnsTrue(test *testing.T) {
	reader := bufio.NewReader(strings.NewReader("COMMIT\n"))
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	result, err := promptNetworkCommitWithCountdown(context.Background(), reader, logger, 2*time.Second)
	if err != nil {
		test.Fatalf("unexpected error: %v", err)
	}
	if !result {
		test.Fatalf("expected true for COMMIT input")
	}
}

func TestPromptNetworkCommitWithCountdown_NonCommitInputReturnsFalse(test *testing.T) {
	reader := bufio.NewReader(strings.NewReader("nope\n"))
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	result, err := promptNetworkCommitWithCountdown(context.Background(), reader, logger, 2*time.Second)
	if err != nil {
		test.Fatalf("unexpected error: %v", err)
	}
	if result {
		test.Fatalf("expected false for non-COMMIT input")
	}
}

func TestPromptNetworkCommitWithCountdown_TimeoutReturnsDeadlineExceeded(test *testing.T) {
	pipeReader, pipeWriter := io.Pipe()
	defer pipeReader.Close()
	defer pipeWriter.Close()

	reader := bufio.NewReader(pipeReader)
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	result, err := promptNetworkCommitWithCountdown(context.Background(), reader, logger, 100*time.Millisecond)
	if result {
		test.Fatalf("expected false on timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		test.Fatalf("err=%v; want %v", err, context.DeadlineExceeded)
	}
}
