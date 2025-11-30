package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

var (
	errInteractiveAborted = errors.New("interactive input aborted")
	errPromptInputClosed  = errors.New("stdin closed")
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
		resp, err := readLineWithContext(ctx, reader)
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
		resp, err := readLineWithContext(ctx, reader)
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

func readLineWithContext(ctx context.Context, reader *bufio.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := reader.ReadString('\n')
		ch <- result{line: line, err: mapPromptInputError(err)}
	}()
	select {
	case <-ctx.Done():
		return "", errInteractiveAborted
	case res := <-ch:
		if res.err != nil {
			if errors.Is(res.err, errPromptInputClosed) {
				return "", errInteractiveAborted
			}
			return "", res.err
		}
		return res.line, nil
	}
}

func mapPromptInputError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) {
		return errPromptInputClosed
	}
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "use of closed file") ||
		strings.Contains(errStr, "bad file descriptor") ||
		strings.Contains(errStr, "file already closed") {
		return errPromptInputClosed
	}
	return err
}
