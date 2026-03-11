package input

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
)

// ErrInputAborted signals that interactive input was interrupted (typically via Ctrl+C
// causing context cancellation and/or stdin closure).
//
// Callers should translate this into the appropriate workflow-level abort error.
var ErrInputAborted = errors.New("input aborted")

type lineResult struct {
	line string
	err  error
}

type lineState struct {
	mu       sync.Mutex
	inflight *lineInflight
}

type lineInflight struct {
	done   chan struct{}
	result lineResult
}

type passwordResult struct {
	b   []byte
	err error
}

type passwordState struct {
	mu       sync.Mutex
	inflight *passwordInflight
}

type passwordInflight struct {
	done   chan struct{}
	result passwordResult
}

var (
	lineStates     sync.Map
	passwordStates sync.Map
)

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

func mapContextInputError(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrInputAborted
}

func getLineState(reader *bufio.Reader) *lineState {
	if state, ok := lineStates.Load(reader); ok {
		return state.(*lineState)
	}
	state := &lineState{}
	actual, _ := lineStates.LoadOrStore(reader, state)
	return actual.(*lineState)
}

func getPasswordState(fd int) *passwordState {
	if state, ok := passwordStates.Load(fd); ok {
		return state.(*passwordState)
	}
	state := &passwordState{}
	actual, _ := passwordStates.LoadOrStore(fd, state)
	return actual.(*passwordState)
}

// ReadLineWithContext reads a single line and supports cancellation. On ctx cancellation
// or stdin closure it returns ErrInputAborted. On ctx deadline it returns context.DeadlineExceeded.
// Cancellation stops waiting but does not interrupt an already-started reader.ReadString call;
// at most one in-flight read is kept per reader to avoid goroutine buildup across retries.
// A completed in-flight read remains attached to the reader until a later caller consumes it.
func ReadLineWithContext(ctx context.Context, reader *bufio.Reader) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reader == nil {
		return "", errors.New("reader is nil")
	}
	state := getLineState(reader)

	for {
		if err := mapContextInputError(ctx); err != nil {
			return "", err
		}

		state.mu.Lock()
		inflight := state.inflight
		if inflight == nil {
			inflight = &lineInflight{
				done: make(chan struct{}),
			}
			state.inflight = inflight
			go func(inflight *lineInflight) {
				line, err := reader.ReadString('\n')
				inflight.result = lineResult{line: line, err: MapInputError(err)}
				close(inflight.done)
			}(inflight)
		}
		state.mu.Unlock()

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", context.DeadlineExceeded
			}
			return "", ErrInputAborted
		case <-inflight.done:
		}

		state.mu.Lock()
		if state.inflight != inflight {
			state.mu.Unlock()
			continue
		}
		state.inflight = nil
		res := inflight.result
		state.mu.Unlock()
		return res.line, res.err
	}
}

// ReadPasswordWithContext reads a password (no echo) and supports cancellation. On ctx
// cancellation or stdin closure it returns ErrInputAborted. On ctx deadline it returns
// context.DeadlineExceeded.
// Cancellation stops waiting but does not interrupt an already-started password read;
// at most one in-flight password read is kept per file descriptor to avoid goroutine buildup.
// A completed in-flight password read remains attached until a later caller consumes it.
func ReadPasswordWithContext(ctx context.Context, readPassword func(int) ([]byte, error), fd int) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if readPassword == nil {
		return nil, errors.New("readPassword function is nil")
	}
	state := getPasswordState(fd)

	for {
		if err := mapContextInputError(ctx); err != nil {
			return nil, err
		}

		state.mu.Lock()
		inflight := state.inflight
		if inflight == nil {
			inflight = &passwordInflight{
				done: make(chan struct{}),
			}
			state.inflight = inflight
			go func(inflight *passwordInflight) {
				b, err := readPassword(fd)
				inflight.result = passwordResult{b: b, err: MapInputError(err)}
				close(inflight.done)
			}(inflight)
		}
		state.mu.Unlock()

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, context.DeadlineExceeded
			}
			return nil, ErrInputAborted
		case <-inflight.done:
		}

		state.mu.Lock()
		if state.inflight != inflight {
			state.mu.Unlock()
			continue
		}
		state.inflight = nil
		res := inflight.result
		state.mu.Unlock()
		return res.b, res.err
	}
}
