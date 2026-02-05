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

	deadlineHHMMSS := deadline.Format("15:04:05")
	timeoutSeconds := int(timeout.Seconds())
	defaultLabel := "No"
	if defaultYes {
		defaultLabel = "Yes"
	}

	// Print a single prompt line to avoid interfering with interactive input on
	// terminals that don't handle repeated carriage-return updates well (e.g. IPMI/serial).
	fmt.Fprintf(os.Stderr, "Auto-skip in %ds (at %s, default: %s)... %s %s ", timeoutSeconds, deadlineHHMMSS, defaultLabel, question, defStr)

	logging.DebugStep(logger, "prompt yes/no", "Waiting for user input (no live countdown)")
	line, err := input.ReadLineWithContext(ctxTimeout, reader)
	fmt.Fprintln(os.Stderr)
	if err != nil {
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
}
