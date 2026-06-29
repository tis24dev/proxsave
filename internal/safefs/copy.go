package safefs

import (
	"context"
	"errors"
	"io"
	"time"
)

// defaultCopyChunk is the streaming buffer size used when CopyBounded is called
// with bufSize <= 0. With the default FS_IO_TIMEOUT this only requires a few tens
// of KB/s of progress per chunk before a stall is declared, so it never penalises
// a large copy over a slow-but-healthy link.
const defaultCopyChunk = 1 << 20 // 1 MiB

// IsAbandoned reports whether err came from a bounded operation whose worker
// goroutine was abandoned (it timed out or its context was cancelled) and may
// therefore still be touching its file handles and copy buffer. A caller that
// owns those handles must NOT close them after an abandoned op: it should drop
// its references and let the os.File finalizer reclaim the fd once the stuck
// kernel call eventually returns.
func IsAbandoned(err error) bool {
	return errors.Is(err, ErrTimeout) || errors.Is(err, context.Canceled)
}

type copyChunk struct {
	written int
	eof     bool
}

// CopyBounded streams src into dst in fixed-size chunks, bounding each chunk's
// read+write round-trip with timeout as a per-chunk no-progress (stall) budget,
// NOT a whole-file deadline: every chunk that makes progress re-arms the budget,
// so an arbitrarily large copy over a slow-but-alive link still succeeds. On a
// stalled chunk the in-flight Read/Write worker is abandoned (the kernel call is
// not cancelled) and a *TimeoutError (wrapping ErrTimeout) is returned.
//
// The copy deliberately runs limiter-free (see runBounded): the loop is
// sequential, so it keeps at most one worker in flight and self-throttles its
// spawn rate; routing it through the shared fsOpLimiter would let a long-lived
// best-effort stream contend for, or on a wedge erode, the slot budget the
// critical paths depend on. timeout <= 0 degrades to a direct synchronous copy
// identical to a plain read/write loop (the FS_IO_TIMEOUT opt-out).
//
// The internal buffer is owned by CopyBounded and never escapes, so on an
// abandoned chunk the worker is its sole remaining accessor; the caller must
// still honour IsAbandoned for the src/dst handles it owns (do not close them).
func CopyBounded(ctx context.Context, dst io.Writer, src io.Reader, bufSize int, timeout time.Duration, op, path string) (int64, error) {
	if bufSize <= 0 {
		bufSize = defaultCopyChunk
	}
	buf := make([]byte, bufSize)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, normalizeContextErr(ctx, &TimeoutError{Op: op, Path: path, Timeout: timeout})
		}

		te := &TimeoutError{Op: op, Path: path, Timeout: effectiveTimeout(ctx, timeout)}
		chunk, err := runBounded(ctx, nil, timeout, te, func() (copyChunk, error) {
			nr, rerr := src.Read(buf)
			if nr > 0 {
				nw, werr := dst.Write(buf[:nr])
				if werr != nil {
					return copyChunk{written: nw}, werr
				}
				if nw != nr {
					return copyChunk{written: nw}, io.ErrShortWrite
				}
			}
			if rerr != nil {
				if rerr == io.EOF {
					return copyChunk{written: nr, eof: true}, nil
				}
				return copyChunk{written: nr}, rerr
			}
			return copyChunk{written: nr}, nil
		})

		written += int64(chunk.written)
		if err != nil {
			return written, err
		}
		if chunk.eof {
			return written, nil
		}
	}
}
