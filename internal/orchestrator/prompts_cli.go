package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
)

func promptYesNo(ctx context.Context, reader *bufio.Reader, prompt string) (bool, error) {
	fmt.Print(prompt)
	line, err := input.ReadLineWithContext(ctx, reader)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func promptYesNoWithDefault(ctx context.Context, reader *bufio.Reader, prompt string, defaultYes bool) (bool, error) {
	for {
		fmt.Print(prompt)
		line, err := input.ReadLineWithContext(ctx, reader)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return defaultYes, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Println("Please type yes or no.")
		}
	}
}

// promptYesNoWithCountdown prompts the user for a yes/no answer while showing a visible countdown.
// If no input is received before the timeout expires, it proceeds safely with "No".
// Pressing Enter chooses the provided defaultYes.
func promptYesNoWithCountdown(ctx context.Context, reader *bufio.Reader, logger *logging.Logger, question string, timeout time.Duration, defaultYes bool) (bool, error) {
	defStr := "[Y/n]"
	if !defaultYes {
		defStr = "[y/N]"
	}

	question = strings.TrimSpace(question)
	if question == "" {
		question = "Proceed?"
	}
	if timeout <= 0 {
		return promptYesNoWithDefault(ctx, reader, fmt.Sprintf("%s %s ", question, defStr), defaultYes)
	}

	deadline := time.Now().Add(timeout)
	logging.DebugStep(logger, "prompt yes/no", "Start: question=%q defaultYes=%v timeout=%s", question, defaultYes, timeout)

	ctxTimeout, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	inputCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		line, err := input.ReadLineWithContext(ctxTimeout, reader)
		if err != nil {
			errCh <- err
			return
		}
		inputCh <- line
	}()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			left := time.Until(deadline)
			if left < 0 {
				left = 0
			}
			fmt.Fprintf(os.Stderr, "\rAuto-skip in %ds... %s %s ", int(left.Seconds()), question, defStr)
			if left <= 0 {
				fmt.Fprintln(os.Stderr)
				logging.DebugStep(logger, "prompt yes/no", "Timeout expired: proceeding with No")
				logger.Info("No response within %ds; proceeding with No.", int(timeout.Seconds()))
				return false, nil
			}
		case line := <-inputCh:
			fmt.Fprintln(os.Stderr)
			trimmed := strings.ToLower(strings.TrimSpace(line))
			logging.DebugStep(logger, "prompt yes/no", "User input received: %q", trimmed)
			switch trimmed {
			case "":
				return defaultYes, nil
			case "y", "yes":
				return true, nil
			case "n", "no":
				return false, nil
			default:
				logger.Info("Unrecognized input %q; proceeding with No.", strings.TrimSpace(line))
				return false, nil
			}
		case err := <-errCh:
			fmt.Fprintln(os.Stderr)
			if errors.Is(err, context.DeadlineExceeded) {
				logging.DebugStep(logger, "prompt yes/no", "Input timed out: proceeding with No")
				logger.Info("No response within %ds; proceeding with No.", int(timeout.Seconds()))
				return false, nil
			}
			if errors.Is(err, context.Canceled) {
				logging.DebugStep(logger, "prompt yes/no", "Input canceled: %v", err)
				return false, err
			}
			logging.DebugStep(logger, "prompt yes/no", "Input error: %v", err)
			return false, err
		}
	}
}
