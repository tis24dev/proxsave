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

func promptNonEmpty(ctx context.Context, reader *bufio.Reader, question string) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", errInteractiveAborted
		}
		fmt.Print(question)
		resp, err := input.ReadLineWithContext(ctx, reader)
		if err != nil {
			return "", err
		}
		resp = strings.TrimSpace(resp)
		if resp != "" {
			return resp, nil
		}
		fmt.Println("Value cannot be empty.")
	}
}
