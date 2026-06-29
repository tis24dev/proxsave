package safefs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// blockingReader delivers prefix on its first Read, then blocks on unblock for
// every subsequent Read (a mount that hands over some bytes, then wedges).
type blockingReader struct {
	prefix  []byte
	unblock <-chan struct{}
	reads   atomic.Int32
}

func (r *blockingReader) Read(p []byte) (int, error) {
	if r.reads.Add(1) == 1 && len(r.prefix) > 0 {
		return copy(p, r.prefix), nil
	}
	<-r.unblock
	return 0, io.EOF
}

// slowReader sleeps before each Read; used to prove the per-chunk budget fires
// (small timeout) and the opt-out does not (timeout<=0).
type slowReader struct {
	data  []byte
	off   int
	delay time.Duration
}

func (r *slowReader) Read(p []byte) (int, error) {
	time.Sleep(r.delay)
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

func TestCopyBounded_StallReturnsTimeout(t *testing.T) {
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	r := &blockingReader{prefix: []byte("hello"), unblock: unblock}
	var dst bytes.Buffer

	type res struct {
		n   int64
		err error
	}
	// Run in a goroutine guarded by a deadline so an unbounded-loop regression
	// fails fast here instead of hanging until the package test timeout.
	done := make(chan res, 1)
	go func() {
		n, err := CopyBounded(context.Background(), &dst, r, 64, 25*time.Millisecond, "copy", "/x")
		done <- res{n, err}
	}()
	select {
	case got := <-done:
		if got.err == nil || !errors.Is(got.err, ErrTimeout) {
			t.Fatalf("CopyBounded err = %v; want timeout", got.err)
		}
		if got.n != int64(len("hello")) {
			t.Fatalf("written = %d; want 5 (the chunk delivered before the stall)", got.n)
		}
		if dst.String() != "hello" {
			t.Fatalf("dst = %q; want %q", dst.String(), "hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CopyBounded hung on a stalled reader")
	}
}

func TestCopyBounded_HealthyMultiChunk(t *testing.T) {
	data := make([]byte, 5*1024+123) // not a multiple of the chunk size
	for i := range data {
		data[i] = byte(i * 7)
	}
	var dst bytes.Buffer
	n, err := CopyBounded(context.Background(), &dst, bytes.NewReader(data), 1024, time.Second, "copy", "/x")
	if err != nil {
		t.Fatalf("CopyBounded err = %v", err)
	}
	if n != int64(len(data)) {
		t.Fatalf("written = %d; want %d", n, len(data))
	}
	if !bytes.Equal(dst.Bytes(), data) {
		t.Fatalf("content mismatch")
	}
}

// CopyBounded must run limiter-free: with the global pool saturated, a copy that
// (wrongly) acquired a slot would block on acquire and time out. This one must
// still succeed.
func TestCopyBounded_BypassesLimiter(t *testing.T) {
	prevStat := osStat
	prevLimiter := fsOpLimiter
	unblock := make(chan struct{})
	finished := make(chan struct{})
	registerBlockedOpCleanup(t, "saturating stat", unblock, finished, func() {
		osStat = prevStat
		fsOpLimiter = prevLimiter
	})

	fsOpLimiter = newOperationLimiter(1)
	osStat = func(string) (os.FileInfo, error) {
		<-unblock
		close(finished)
		return nil, os.ErrNotExist
	}
	if _, err := Stat(context.Background(), "/blocked", 25*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("priming Stat err = %v; want timeout", err)
	}
	if got := fsOpLimiter.inflight(); got != 1 {
		t.Fatalf("inflight after saturation = %d; want 1", got)
	}

	data := []byte("the limiter is saturated but this copy must still complete")
	var dst bytes.Buffer
	n, err := CopyBounded(context.Background(), &dst, bytes.NewReader(data), 8, time.Second, "copy", "/x")
	if err != nil {
		t.Fatalf("CopyBounded err = %v; want success despite a saturated limiter", err)
	}
	if n != int64(len(data)) || !bytes.Equal(dst.Bytes(), data) {
		t.Fatalf("copy incorrect: n=%d content=%q", n, dst.String())
	}
}

// timeout<=0 must degrade to a plain unbounded copy: a slow reader completes with
// the opt-out, but the same reader trips a tiny per-chunk budget.
func TestCopyBounded_ZeroTimeoutIsDirect(t *testing.T) {
	data := []byte("zero timeout means a plain synchronous copy with no stall budget")

	var dst bytes.Buffer
	n, err := CopyBounded(context.Background(), &dst, &slowReader{data: data, delay: 15 * time.Millisecond}, 8, 0, "copy", "/x")
	if err != nil {
		t.Fatalf("opt-out CopyBounded err = %v; want success", err)
	}
	if n != int64(len(data)) || !bytes.Equal(dst.Bytes(), data) {
		t.Fatalf("opt-out copy incorrect: n=%d", n)
	}

	_, err = CopyBounded(context.Background(), &bytes.Buffer{}, &slowReader{data: data, delay: 15 * time.Millisecond}, 8, time.Millisecond, "copy", "/x")
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("tiny-budget CopyBounded err = %v; want timeout", err)
	}
}

// blockingWriter accepts its first Write, then blocks on every subsequent one
// (a destination mount that wedges mid-stream — the realistic #242 direction).
type blockingWriter struct {
	writes  atomic.Int32
	unblock <-chan struct{}
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	if w.writes.Add(1) == 1 {
		return len(p), nil
	}
	<-w.unblock
	return 0, io.EOF
}

type errReader struct{ err error }

func (r *errReader) Read(p []byte) (int, error) { return 0, r.err }

type shortWriter struct{}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

// eofWithDataReader returns data AND io.EOF in a single Read (the nr>0 && EOF
// branch that real os.File/bytes.Reader never exercise).
type eofWithDataReader struct {
	data []byte
	done bool
}

func (r *eofWithDataReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	return copy(p, r.data), io.EOF
}

// The budget is per-chunk, not a whole-file deadline: a copy whose cumulative
// time far exceeds the budget, but whose every chunk is well under it, must
// still succeed. A WithTimeout-style whole-file mutation fails this.
func TestCopyBounded_PerChunkBudgetReArms(t *testing.T) {
	const chunks = 30
	data := make([]byte, chunks*8)
	for i := range data {
		data[i] = byte(i)
	}
	r := &slowReader{data: data, delay: 10 * time.Millisecond} // ~300ms cumulative
	var dst bytes.Buffer
	n, err := CopyBounded(context.Background(), &dst, r, 8, 150*time.Millisecond, "copy", "/x")
	if err != nil {
		t.Fatalf("CopyBounded err = %v; a per-chunk budget must not accumulate across chunks", err)
	}
	if n != int64(len(data)) || !bytes.Equal(dst.Bytes(), data) {
		t.Fatalf("copy incorrect: n=%d want %d", n, len(data))
	}
}

// A destination that wedges mid-stream must time out, not hang. Only the read
// side is covered by the FIFO storage test; this covers the write side.
func TestCopyBounded_WriteStallReturnsTimeout(t *testing.T) {
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	w := &blockingWriter{unblock: unblock}
	src := bytes.NewReader(make([]byte, 64)) // several 8-byte chunks

	done := make(chan error, 1)
	go func() {
		_, err := CopyBounded(context.Background(), w, src, 8, 25*time.Millisecond, "copy", "/x")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, ErrTimeout) {
			t.Fatalf("CopyBounded err = %v; want timeout on a stalled writer", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CopyBounded hung on a stalled writer")
	}
}

// A context cancel mid-copy must surface as context.Canceled AND be classified
// abandoned (so the caller drops, never closes, the still-held handles).
func TestCopyBounded_ContextCancelIsAbandoned(t *testing.T) {
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	r := &blockingReader{unblock: unblock} // blocks on the first Read
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := CopyBounded(ctx, &bytes.Buffer{}, r, 8, time.Second, "copy", "/x")
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v; want context.Canceled", err)
		}
		if !IsAbandoned(err) {
			t.Fatalf("IsAbandoned(%v) = false; a cancel must be treated as abandoned", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CopyBounded did not return after ctx cancel")
	}
}

// A genuine (non-timeout, non-cancel) I/O error must propagate and must NOT be
// classified abandoned, so the caller DOES close its handles afterwards.
func TestCopyBounded_RealErrorsNotAbandoned(t *testing.T) {
	sentinel := errors.New("disk exploded")
	if _, err := CopyBounded(context.Background(), &bytes.Buffer{}, &errReader{err: sentinel}, 8, time.Second, "copy", "/x"); !errors.Is(err, sentinel) || IsAbandoned(err) {
		t.Fatalf("read error: got %v (abandoned=%v); want sentinel, not abandoned", err, IsAbandoned(err))
	}

	if _, err := CopyBounded(context.Background(), &shortWriter{}, bytes.NewReader(make([]byte, 16)), 8, time.Second, "copy", "/x"); !errors.Is(err, io.ErrShortWrite) || IsAbandoned(err) {
		t.Fatalf("short write: got %v (abandoned=%v); want io.ErrShortWrite, not abandoned", err, IsAbandoned(err))
	}
}

func TestCopyBounded_DataWithEOFCountsBytes(t *testing.T) {
	data := []byte("delivered together with EOF")
	var dst bytes.Buffer
	n, err := CopyBounded(context.Background(), &dst, &eofWithDataReader{data: data}, 1024, time.Second, "copy", "/x")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if n != int64(len(data)) || !bytes.Equal(dst.Bytes(), data) {
		t.Fatalf("n=%d dst=%q; want %d bytes %q", n, dst.String(), len(data), data)
	}
}

func TestIsAbandoned(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"ErrTimeout", ErrTimeout, true},
		{"TimeoutError", &TimeoutError{Op: "x"}, true},
		{"wrapped timeout", fmt.Errorf("outer: %w", ErrTimeout), true},
		{"canceled", context.Canceled, true},
		{"wrapped canceled", fmt.Errorf("outer: %w", context.Canceled), true},
		{"short write", io.ErrShortWrite, false},
		{"permission", os.ErrPermission, false},
	}
	for _, c := range cases {
		if got := IsAbandoned(c.err); got != c.want {
			t.Errorf("IsAbandoned(%s) = %v; want %v", c.name, got, c.want)
		}
	}
}
