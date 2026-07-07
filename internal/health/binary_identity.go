// binary_identity.go records the identity of an on-disk executable so a caller can tell whether the
// binary a running daemon booted from still matches the file on disk (an in-place upgrade replaces
// the file without restarting the process). The stale SIGNAL is the SHA256 of the file: Size/MTime
// are informational only, because a rebuild can keep the same size or an mtime can be touched
// without a content change. It is the SINGLE sha256-of-file implementation shared by the package and
// by cmd/proxsave's executableHash (which now delegates here), and it stays logging-free +
// stdlib-only like its siblings so both the daemon and the run can import it without a logger.

package health

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// BinaryIdentity is the content-identity of an executable file on disk. SHA256 is the equality key
// (see Aligned); Size and MTime (unix seconds) are informational, carried for display/diagnostics.
type BinaryIdentity struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime"`
}

// ComputeBinaryIdentity reads the file at path and returns its identity: the SHA256 is streamed with
// io.Copy (never buffering the whole binary), and Size + MTime come from a single os.Stat. A missing
// or unreadable path is an error (an empty identity must never be mistaken for a real one). Path is
// echoed back verbatim so callers can persist the exact path the hash was computed over.
func ComputeBinaryIdentity(path string) (BinaryIdentity, error) {
	f, err := os.Open(path) // #nosec G304 -- path is the resolved proxsave binary, not user input
	if err != nil {
		return BinaryIdentity{}, fmt.Errorf("open binary %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return BinaryIdentity{}, fmt.Errorf("hash binary %s: %w", path, err)
	}

	st, err := f.Stat()
	if err != nil {
		return BinaryIdentity{}, fmt.Errorf("stat binary %s: %w", path, err)
	}

	return BinaryIdentity{
		Path:   path,
		SHA256: hex.EncodeToString(h.Sum(nil)),
		Size:   st.Size(),
		MTime:  st.ModTime().Unix(),
	}, nil
}

// Aligned reports whether a and b identify the SAME binary content. The key is SHA256-primary and
// path-agnostic: equal iff BOTH hashes are non-empty and equal. An empty hash on either side (a zero
// value, or an identity whose hash could not be computed) is never "aligned" -- an unknown identity
// must not read as a match. Size/MTime are deliberately NOT part of the comparison.
func (a BinaryIdentity) Aligned(b BinaryIdentity) bool {
	return a.SHA256 != "" && a.SHA256 == b.SHA256
}
