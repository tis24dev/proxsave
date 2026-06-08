package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/input"
	"golang.org/x/term"
)

var (
	errInteractiveAborted = input.ErrInputAborted
)

func ensureInteractiveStdin() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("install wizard requires an interactive terminal (stdin is not a TTY)")
	}
	return nil
}

func promptYesNo(ctx context.Context, reader *bufio.Reader, question string, defaultYes bool) (bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return false, errInteractiveAborted
		}
		fmt.Print(question)
		resp, err := input.ReadLineWithContext(ctx, reader)
		if err != nil {
			return false, err
		}
		resp = strings.TrimSpace(strings.ToLower(resp))
		if resp == "" {
			return defaultYes, nil
		}
		switch resp {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Println("Please answer with 'y' or 'n'.")
		}
	}
}

// confirmDefault asks a yes/no question, rendering the [Y/n]/[y/N] hint from the
// default so the prompt truthfully reflects what pressing Enter does, then defers
// to promptYesNo. The install wizard uses it so re-running on an existing config
// defaults each toggle to its stored value instead of always to "no".
func confirmDefault(ctx context.Context, reader *bufio.Reader, label string, defaultYes bool) (bool, error) {
	hint := "[y/N]"
	if defaultYes {
		hint = "[Y/n]"
	}
	return promptYesNo(ctx, reader, fmt.Sprintf("%s %s: ", label, hint), defaultYes)
}

// promptNonEmptyWithDefault behaves like promptNonEmpty when def is empty, and
// otherwise shows def as the current value and returns it when the user presses
// Enter — so a stored path/remote is kept without retyping while a fresh value
// is still required when there is nothing to keep.
func promptNonEmptyWithDefault(ctx context.Context, reader *bufio.Reader, question, def string) (string, error) {
	if strings.TrimSpace(def) == "" {
		return promptNonEmpty(ctx, reader, question)
	}
	resp, err := promptOptional(ctx, reader, fmt.Sprintf("%s[%s] ", question, def))
	if err != nil {
		return "", err
	}
	if resp == "" {
		return def, nil
	}
	return resp, nil
}

// promptOptionalWithDefault behaves like promptOptional but returns def when the
// user presses Enter (and surfaces def in the prompt), so an existing optional
// value is preserved on a no-op edit.
func promptOptionalWithDefault(ctx context.Context, reader *bufio.Reader, question, def string) (string, error) {
	if strings.TrimSpace(def) != "" {
		question = fmt.Sprintf("%s[%s] ", question, def)
	}
	resp, err := promptOptional(ctx, reader, question)
	if err != nil {
		return "", err
	}
	if resp == "" {
		return def, nil
	}
	return resp, nil
}

func promptNonEmpty(ctx context.Context, reader *bufio.Reader, question string) (string, error) {
	for {
		resp, err := promptOptional(ctx, reader, question)
		if err != nil {
			return "", err
		}
		if resp != "" {
			return resp, nil
		}
		fmt.Println("Value cannot be empty.")
	}
}

func promptOptional(ctx context.Context, reader *bufio.Reader, question string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", errInteractiveAborted
	}
	fmt.Print(question)
	resp, err := input.ReadLineWithContext(ctx, reader)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp), nil
}
