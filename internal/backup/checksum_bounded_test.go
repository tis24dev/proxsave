package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
)

// --- test doubles (chk-prefixed to avoid clashes in package backup) ---

type chkRecordingRC struct {
	r      io.Reader
	closed atomic.Bool
}

func (rc *chkRecordingRC) Read(p []byte) (int, error) { return rc.r.Read(p) }
func (rc *chkRecordingRC) Close() error               { rc.closed.Store(true); return nil }

type chkBlockingReader struct {
	prefix  []byte
	unblock <-chan struct{}
	reads   atomic.Int32
}

func (r *chkBlockingReader) Read(p []byte) (int, error) {
	if r.reads.Add(1) == 1 && len(r.prefix) > 0 {
		return copy(p, r.prefix), nil
	}
	<-r.unblock
	return 0, io.EOF
}

type chkSlowReader struct {
	data  []byte
	off   int
	step  int
	delay time.Duration
}

func (r *chkSlowReader) Read(p []byte) (int, error) {
	time.Sleep(r.delay)
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	end := r.off + r.step
	if end > len(r.data) {
		end = len(r.data)
	}
	n := copy(p, r.data[r.off:end])
	r.off += n
	return n, nil
}

type chkErrReader struct{ err error }

func (r *chkErrReader) Read(p []byte) (int, error) { return 0, r.err }

// chkBlockCloseRC reads fine but blocks forever in Close (a mount that wedges
// only at close, after the hash already succeeded).
type chkBlockCloseRC struct {
	r       io.Reader
	unblock <-chan struct{}
}

func (rc *chkBlockCloseRC) Read(p []byte) (int, error) { return rc.r.Read(p) }
func (rc *chkBlockCloseRC) Close() error               { <-rc.unblock; return nil }

// chkErrCloseRC fails the read AND the close, to prove the read error wins.
type chkErrCloseRC struct {
	readErr  error
	closeErr error
}

func (rc *chkErrCloseRC) Read(p []byte) (int, error) { return 0, rc.readErr }
func (rc *chkErrCloseRC) Close() error               { return rc.closeErr }

func withChecksumOpen(t *testing.T, fn func(context.Context, string, time.Duration) (io.ReadCloser, error)) {
	t.Helper()
	prev := checksumOpen
	t.Cleanup(func() { checksumOpen = prev })
	checksumOpen = fn
}

func newChkLogger() *logging.Logger { return logging.New(types.LogLevelDebug, false) }

// --- tests ---

// The bounded path must produce the exact crypto/sha256 digest for both the
// timeout=0 (synchronous) and timeout>0 (per-chunk worker) branches, and the
// legacy wrapper must equal the bounded(0) result.
func TestGenerateChecksumBounded_KnownVectors(t *testing.T) {
	logger := newChkLogger()
	dir := t.TempDir()
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"abc", []byte("abc")},
		{"multichunk", bytes.Repeat([]byte("proxsave-"), 300000)}, // > 1 MiB => several chunks
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(dir, c.name)
			if err := os.WriteFile(p, c.data, 0o644); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(c.data)
			want := hex.EncodeToString(sum[:])
			for _, to := range []time.Duration{0, 5 * time.Second} {
				got, err := GenerateChecksumBounded(context.Background(), logger, p, to)
				if err != nil {
					t.Fatalf("timeout=%v: %v", to, err)
				}
				if got != want {
					t.Fatalf("timeout=%v: got %s want %s", to, got, want)
				}
			}
			if leg, err := GenerateChecksum(context.Background(), logger, p); err != nil || leg != want {
				t.Fatalf("legacy wrapper = (%s, %v); want %s", leg, err, want)
			}
		})
	}
}

// A stalled read must time out (not hang) and the handle must NOT be closed,
// because the abandoned CopyBounded worker may still hold it.
func TestGenerateChecksumBounded_StallSkipsClose(t *testing.T) {
	logger := newChkLogger()
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	rc := &chkRecordingRC{r: &chkBlockingReader{prefix: []byte("partial"), unblock: unblock}}
	withChecksumOpen(t, func(context.Context, string, time.Duration) (io.ReadCloser, error) { return rc, nil })

	done := make(chan error, 1)
	go func() {
		_, err := GenerateChecksumBounded(context.Background(), logger, "/x", 50*time.Millisecond)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, safefs.ErrTimeout) || !safefs.IsAbandoned(err) {
			t.Fatalf("err = %v; want an abandoned ErrTimeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("GenerateChecksumBounded hung on a stalled read")
	}
	if rc.closed.Load() {
		t.Fatal("close must be SKIPPED on an abandoned read (the wedged worker may still hold the fd)")
	}
}

// A slow but healthy reader whose cumulative time exceeds the budget, but whose
// every chunk is under it, must still succeed (per-chunk budget, not whole-file)
// and must close the handle.
func TestGenerateChecksumBounded_SlowHealthyReaderNoFalseTimeout(t *testing.T) {
	logger := newChkLogger()
	data := bytes.Repeat([]byte("x"), 240)
	rc := &chkRecordingRC{r: &chkSlowReader{data: data, step: 8, delay: 10 * time.Millisecond}} // ~30 reads * 10ms
	withChecksumOpen(t, func(context.Context, string, time.Duration) (io.ReadCloser, error) { return rc, nil })

	got, err := GenerateChecksumBounded(context.Background(), logger, "/x", 150*time.Millisecond)
	if err != nil {
		t.Fatalf("slow healthy read err = %v; a per-chunk budget must not accumulate across chunks", err)
	}
	sum := sha256.Sum256(data)
	if got != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest mismatch")
	}
	if !rc.closed.Load() {
		t.Fatal("a healthy read must close the handle")
	}
}

// A genuine (non-timeout) read error must propagate as "failed to read file",
// must NOT be abandoned, and the handle MUST be closed (no fd leak).
func TestGenerateChecksumBounded_RealReadErrorClosesHandle(t *testing.T) {
	logger := newChkLogger()
	sentinel := errors.New("device error")
	rc := &chkRecordingRC{r: &chkErrReader{err: sentinel}}
	withChecksumOpen(t, func(context.Context, string, time.Duration) (io.ReadCloser, error) { return rc, nil })

	_, err := GenerateChecksumBounded(context.Background(), logger, "/x", time.Second)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v; want the sentinel", err)
	}
	if safefs.IsAbandoned(err) {
		t.Fatalf("a real read error must not be abandoned: %v", err)
	}
	if !strings.Contains(err.Error(), "failed to read file") {
		t.Fatalf("err = %v; want 'failed to read file'", err)
	}
	if !rc.closed.Load() {
		t.Fatal("a real read error must still close the handle")
	}
}

func TestGenerateChecksumBounded_OpenPaths(t *testing.T) {
	logger := newChkLogger()

	t.Run("open abandoned wraps ErrTimeout, no nil panic", func(t *testing.T) {
		withChecksumOpen(t, func(context.Context, string, time.Duration) (io.ReadCloser, error) {
			return nil, &safefs.TimeoutError{Op: "open"}
		})
		_, err := GenerateChecksumBounded(context.Background(), logger, "/x", time.Second)
		if !errors.Is(err, safefs.ErrTimeout) || !strings.Contains(err.Error(), "failed to open file") {
			t.Fatalf("err = %v; want a wrapped open ErrTimeout", err)
		}
	})

	t.Run("pre-cancelled ctx returns context.Canceled and never opens", func(t *testing.T) {
		var opened atomic.Bool
		withChecksumOpen(t, func(context.Context, string, time.Duration) (io.ReadCloser, error) {
			opened.Store(true)
			return nil, nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := GenerateChecksumBounded(ctx, logger, "/x", time.Second); err != context.Canceled {
			t.Fatalf("err = %v; want exactly context.Canceled", err)
		}
		if opened.Load() {
			t.Fatal("must not open on an already-cancelled context")
		}
	})

	t.Run("missing file via the real seam", func(t *testing.T) {
		_, err := GenerateChecksumBounded(context.Background(), logger, filepath.Join(t.TempDir(), "nope"), time.Second)
		if err == nil || !strings.Contains(err.Error(), "failed to open file") {
			t.Fatalf("err = %v; want open failure", err)
		}
	})
}

// A close that stalls AFTER a successful hash must not clobber the computed
// checksum: the bounded close is abandoned and its timeout is suppressed.
func TestGenerateChecksumBounded_SuccessSurvivesAbandonedClose(t *testing.T) {
	logger := newChkLogger()
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	rc := &chkBlockCloseRC{r: bytes.NewReader([]byte("abc")), unblock: unblock}
	withChecksumOpen(t, func(context.Context, string, time.Duration) (io.ReadCloser, error) { return rc, nil })

	type res struct {
		sum string
		err error
	}
	done := make(chan res, 1)
	go func() {
		s, e := GenerateChecksumBounded(context.Background(), logger, "/x", 50*time.Millisecond)
		done <- res{s, e}
	}()
	want := sha256.Sum256([]byte("abc"))
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("err = %v; a stalled close must not clobber a computed checksum", r.err)
		}
		if r.sum != hex.EncodeToString(want[:]) {
			t.Fatalf("sum = %s; want %s", r.sum, hex.EncodeToString(want[:]))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("GenerateChecksumBounded hung on a stalled close")
	}
}

// A real read error must win over a (real) close error: the close error must not
// clobber the primary failure.
func TestGenerateChecksumBounded_ReadErrorNotClobberedByCloseError(t *testing.T) {
	logger := newChkLogger()
	readErr := errors.New("read boom")
	closeErr := errors.New("close boom")
	rc := &chkErrCloseRC{readErr: readErr, closeErr: closeErr}
	withChecksumOpen(t, func(context.Context, string, time.Duration) (io.ReadCloser, error) { return rc, nil })

	_, err := GenerateChecksumBounded(context.Background(), logger, "/x", time.Second)
	if !errors.Is(err, readErr) {
		t.Fatalf("err = %v; the read error must win", err)
	}
	if errors.Is(err, closeErr) {
		t.Fatalf("err = %v; the close error must not clobber the read error", err)
	}
	if !strings.Contains(err.Error(), "failed to read file") {
		t.Fatalf("err = %v; want 'failed to read file'", err)
	}
}

// End-to-end wiring through the REAL safefs.Open: a FIFO with no writer makes
// the open block; the bound must turn that into a prompt ErrTimeout, not a hang.
func TestGenerateChecksumBounded_FifoOpenWedge(t *testing.T) {
	logger := newChkLogger()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "f.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	t.Cleanup(func() {
		// O_NONBLOCK so this never blocks: with the abandoned reader present the
		// open succeeds and the close releases it; with no reader (a regression
		// where the code under test never opened the FIFO) it returns ENXIO
		// immediately -> e != nil -> skip, so the cleanup cannot wedge CI.
		if w, e := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); e == nil {
			_ = w.Close() // release the abandoned blocked open
		}
	})

	done := make(chan error, 1)
	go func() {
		_, err := GenerateChecksumBounded(context.Background(), logger, fifo, 500*time.Millisecond)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, safefs.ErrTimeout) {
			t.Fatalf("err = %v; want ErrTimeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GenerateChecksumBounded hung on a FIFO open with no writer")
	}
}
