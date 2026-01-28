package input

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
)

// ErrInputAborted signals that interactive input was interrupted (typically via Ctrl+C
// causing context cancellation and/or stdin closure).
//
// Callers should translate this into the appropriate workflow-level abort error.
var ErrInputAborted = errors.New("input aborted")

// IsAborted reports whether an operation was aborted by the user (typically via Ctrl+C),
// by checking for ErrInputAborted and context cancellation.
func IsAborted(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrInputAborted) || errors.Is(err, context.Canceled)
}

// MapInputError normalizes common stdin errors (EOF/closed fd) into ErrInputAborted.
func MapInputError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
		return ErrInputAborted
	}
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "use of closed file") ||
		strings.Contains(errStr, "bad file descriptor") ||
		strings.Contains(errStr, "file already closed") {
		return ErrInputAborted
	}
	return err
}

// ReadLineWithContext reads a single line and supports cancellation. On ctx cancellation
// or stdin closure it returns ErrInputAborted. On ctx deadline it returns context.DeadlineExceeded.
func ReadLineWithContext(ctx context.Context, reader *bufio.Reader) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := reader.ReadString('\n')
		ch <- result{line: line, err: MapInputError(err)}
	}()
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", context.DeadlineExceeded
		}
		return "", ErrInputAborted
	case res := <-ch:
		return res.line, res.err
	}
}

// ReadPasswordWithContext reads a password (no echo) and supports cancellation. On ctx
// cancellation or stdin closure it returns ErrInputAborted. On ctx deadline it returns
// context.DeadlineExceeded.
func ReadPasswordWithContext(ctx context.Context, readPassword func(int) ([]byte, error), fd int) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if readPassword == nil {
		return nil, errors.New("readPassword function is nil")
	}
	type result struct {
		b   []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := readPassword(fd)
		ch <- result{b: b, err: MapInputError(err)}
	}()
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, ErrInputAborted
	case res := <-ch:
		return res.b, res.err
	}
}
