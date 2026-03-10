package input

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type blockingLineReader struct {
	release  chan struct{}
	finish   chan struct{}
	returned chan struct{}
	payload  string
	calls    atomic.Int32
}

func (r *blockingLineReader) Read(p []byte) (int, error) {
	r.calls.Add(1)
	<-r.release
	if r.finish != nil {
		<-r.finish
	}
	if r.payload == "" {
		signalNonBlocking(r.returned)
		return 0, io.EOF
	}
	n := copy(p, r.payload)
	r.payload = r.payload[n:]
	signalNonBlocking(r.returned)
	return n, nil
}

type lineCallResult struct {
	line string
	err  error
}

type passwordCallResult struct {
	b   []byte
	err error
}

func signalNonBlocking(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func waitForSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitForCondition(t *testing.T, name string, cond func() bool) {
	t.Helper()
	deadline := time.After(500 * time.Millisecond)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", name)
		case <-ticker.C:
		}
	}
}

func currentLineInflight(t *testing.T, reader *bufio.Reader) *lineInflight {
	t.Helper()
	state := getLineState(reader)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.inflight == nil {
		t.Fatalf("expected line inflight state")
	}
	return state.inflight
}

func currentPasswordInflight(t *testing.T, fd int) *passwordInflight {
	t.Helper()
	state := getPasswordState(fd)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.inflight == nil {
		t.Fatalf("expected password inflight state")
	}
	return state.inflight
}

func assertSameLineInflight(t *testing.T, reader *bufio.Reader, want *lineInflight) {
	t.Helper()
	state := getLineState(reader)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.inflight != want {
		t.Fatalf("line inflight=%p; want %p", state.inflight, want)
	}
}

func assertSamePasswordInflight(t *testing.T, fd int, want *passwordInflight) {
	t.Helper()
	state := getPasswordState(fd)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.inflight != want {
		t.Fatalf("password inflight=%p; want %p", state.inflight, want)
	}
}

func assertLineInflightCleared(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	state := getLineState(reader)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.inflight != nil {
		t.Fatalf("line inflight=%p; want nil", state.inflight)
	}
}

func assertPasswordInflightCleared(t *testing.T, fd int) {
	t.Helper()
	state := getPasswordState(fd)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.inflight != nil {
		t.Fatalf("password inflight=%p; want nil", state.inflight)
	}
}

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

func TestReadLineWithContext_ReusesInflightReadWhilePendingAfterTimeout(t *testing.T) {
	src := &blockingLineReader{
		release:  make(chan struct{}),
		finish:   make(chan struct{}),
		returned: make(chan struct{}, 1),
		payload:  "hello\n",
	}
	reader := bufio.NewReader(src)

	ctx1, cancel1 := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel1()
	_, err := ReadLineWithContext(ctx1, reader)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first err=%v; want %v", err, context.DeadlineExceeded)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel2()
	_, err = ReadLineWithContext(ctx2, reader)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second err=%v; want %v", err, context.DeadlineExceeded)
	}

	if got := src.calls.Load(); got != 1 {
		t.Fatalf("underlying Read calls=%d; want 1", got)
	}

	resultCh := make(chan lineCallResult, 1)
	go func() {
		line, err := ReadLineWithContext(context.Background(), reader)
		resultCh <- lineCallResult{line: line, err: err}
	}()

	state := getLineState(reader)
	waitForCondition(t, "line retry to block on inflight read", func() bool {
		if state.mu.TryLock() {
			state.mu.Unlock()
			return false
		}
		return true
	})

	close(src.release)
	close(src.finish)
	waitForSignal(t, src.returned, "underlying line read completion")

	res := <-resultCh
	if res.err != nil {
		t.Fatalf("retry ReadLineWithContext error: %v", res.err)
	}
	if res.line != "hello\n" {
		t.Fatalf("line=%q; want %q", res.line, "hello\n")
	}
	if got := src.calls.Load(); got != 1 {
		t.Fatalf("underlying Read calls after pending retry=%d; want 1", got)
	}
}

func TestReadLineWithContext_PreservesCompletedReadForNextRetryAfterTimeout(t *testing.T) {
	src := &blockingLineReader{
		release:  make(chan struct{}),
		returned: make(chan struct{}, 1),
		payload:  "hello\n",
	}
	reader := bufio.NewReader(src)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := ReadLineWithContext(ctx, reader)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first err=%v; want %v", err, context.DeadlineExceeded)
	}

	inflight := currentLineInflight(t, reader)
	close(src.release)
	waitForSignal(t, src.returned, "underlying line read return")
	waitForSignal(t, inflight.completed, "line inflight completion")
	assertSameLineInflight(t, reader, inflight)

	line, err := ReadLineWithContext(context.Background(), reader)
	if err != nil {
		t.Fatalf("retry ReadLineWithContext error: %v", err)
	}
	if line != "hello\n" {
		t.Fatalf("line=%q; want %q", line, "hello\n")
	}
	if got := src.calls.Load(); got != 1 {
		t.Fatalf("underlying Read calls after completed retry=%d; want 1", got)
	}
}

func TestReadPasswordWithContext_ReusesInflightReadWhilePendingAfterTimeout(t *testing.T) {
	release := make(chan struct{})
	finish := make(chan struct{})
	returned := make(chan struct{}, 1)
	var calls atomic.Int32
	readPassword := func(fd int) ([]byte, error) {
		calls.Add(1)
		<-release
		<-finish
		signalNonBlocking(returned)
		return []byte("secret"), nil
	}

	ctx1, cancel1 := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel1()
	got, err := ReadPasswordWithContext(ctx1, readPassword, 42)
	if got != nil {
		t.Fatalf("expected nil bytes on first deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first err=%v; want %v", err, context.DeadlineExceeded)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel2()
	got, err = ReadPasswordWithContext(ctx2, readPassword, 42)
	if got != nil {
		t.Fatalf("expected nil bytes on second deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second err=%v; want %v", err, context.DeadlineExceeded)
	}

	if gotCalls := calls.Load(); gotCalls != 1 {
		t.Fatalf("readPassword calls=%d; want 1", gotCalls)
	}

	resultCh := make(chan passwordCallResult, 1)
	go func() {
		got, err := ReadPasswordWithContext(context.Background(), readPassword, 42)
		resultCh <- passwordCallResult{b: got, err: err}
	}()

	state := getPasswordState(42)
	waitForCondition(t, "password retry to block on inflight read", func() bool {
		if state.mu.TryLock() {
			state.mu.Unlock()
			return false
		}
		return true
	})

	close(release)
	close(finish)
	waitForSignal(t, returned, "underlying password read completion")

	res := <-resultCh
	if res.err != nil {
		t.Fatalf("retry ReadPasswordWithContext error: %v", res.err)
	}
	if string(res.b) != "secret" {
		t.Fatalf("got=%q; want %q", string(res.b), "secret")
	}
	if gotCalls := calls.Load(); gotCalls != 1 {
		t.Fatalf("readPassword calls after pending retry=%d; want 1", gotCalls)
	}
}

func TestReadPasswordWithContext_PreservesCompletedReadForNextRetryAfterTimeout(t *testing.T) {
	release := make(chan struct{})
	returned := make(chan struct{}, 1)
	var calls atomic.Int32
	readPassword := func(fd int) ([]byte, error) {
		calls.Add(1)
		<-release
		signalNonBlocking(returned)
		return []byte("secret"), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	got, err := ReadPasswordWithContext(ctx, readPassword, 42)
	if got != nil {
		t.Fatalf("expected nil bytes on first deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first err=%v; want %v", err, context.DeadlineExceeded)
	}

	inflight := currentPasswordInflight(t, 42)
	close(release)
	waitForSignal(t, returned, "underlying password read return")
	waitForSignal(t, inflight.completed, "password inflight completion")
	assertSamePasswordInflight(t, 42, inflight)

	got, err = ReadPasswordWithContext(context.Background(), readPassword, 42)
	if err != nil {
		t.Fatalf("retry ReadPasswordWithContext error: %v", err)
	}
	if string(got) != "secret" {
		t.Fatalf("got=%q; want %q", string(got), "secret")
	}
	if gotCalls := calls.Load(); gotCalls != 1 {
		t.Fatalf("readPassword calls after completed retry=%d; want 1", gotCalls)
	}
}

func TestReadLineWithContext_ClearsInflightAfterCompletedRetryConsumesResult(t *testing.T) {
	src := &blockingLineReader{
		release:  make(chan struct{}),
		returned: make(chan struct{}, 1),
		payload:  "hello\n",
	}
	reader := bufio.NewReader(src)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := ReadLineWithContext(ctx, reader)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first err=%v; want %v", err, context.DeadlineExceeded)
	}

	inflight := currentLineInflight(t, reader)
	close(src.release)
	waitForSignal(t, src.returned, "underlying line read return")
	waitForSignal(t, inflight.completed, "line inflight completion")
	assertSameLineInflight(t, reader, inflight)

	line, err := ReadLineWithContext(context.Background(), reader)
	if err != nil {
		t.Fatalf("retry ReadLineWithContext error: %v", err)
	}
	if line != "hello\n" {
		t.Fatalf("line=%q; want %q", line, "hello\n")
	}
	assertLineInflightCleared(t, reader)
}

func TestReadPasswordWithContext_ClearsInflightAfterCompletedRetryConsumesResult(t *testing.T) {
	release := make(chan struct{})
	returned := make(chan struct{}, 1)
	var calls atomic.Int32
	readPassword := func(fd int) ([]byte, error) {
		calls.Add(1)
		<-release
		signalNonBlocking(returned)
		return []byte("secret"), nil
	}

	const fd = 43

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	got, err := ReadPasswordWithContext(ctx, readPassword, fd)
	if got != nil {
		t.Fatalf("expected nil bytes on first deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first err=%v; want %v", err, context.DeadlineExceeded)
	}

	inflight := currentPasswordInflight(t, fd)
	close(release)
	waitForSignal(t, returned, "underlying password read return")
	waitForSignal(t, inflight.completed, "password inflight completion")
	assertSamePasswordInflight(t, fd, inflight)

	got, err = ReadPasswordWithContext(context.Background(), readPassword, fd)
	if err != nil {
		t.Fatalf("retry ReadPasswordWithContext error: %v", err)
	}
	if string(got) != "secret" {
		t.Fatalf("got=%q; want %q", string(got), "secret")
	}
	assertPasswordInflightCleared(t, fd)
}
