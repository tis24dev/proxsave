package health

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeBin writes content to a temp file and returns its path. A helper so each case gets a real
// file for ComputeBinaryIdentity to hash.
func writeBin(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// TestAlignedIdenticalBytes: two files with identical bytes hash equal -> Aligned true (even across
// different paths, since the key is content).
func TestAlignedIdenticalBytes(t *testing.T) {
	a, err := ComputeBinaryIdentity(writeBin(t, "a.bin", []byte("proxsave-binary-payload")))
	if err != nil {
		t.Fatalf("ComputeBinaryIdentity(a): %v", err)
	}
	b, err := ComputeBinaryIdentity(writeBin(t, "b.bin", []byte("proxsave-binary-payload")))
	if err != nil {
		t.Fatalf("ComputeBinaryIdentity(b): %v", err)
	}
	if !a.Aligned(b) {
		t.Fatalf("identical bytes should be Aligned; a=%q b=%q", a.SHA256, b.SHA256)
	}
}

// TestAlignedFlippedByte: a single differing byte flips the hash -> Aligned false.
func TestAlignedFlippedByte(t *testing.T) {
	a, err := ComputeBinaryIdentity(writeBin(t, "a.bin", []byte("payload-A")))
	if err != nil {
		t.Fatalf("ComputeBinaryIdentity(a): %v", err)
	}
	b, err := ComputeBinaryIdentity(writeBin(t, "b.bin", []byte("payload-B")))
	if err != nil {
		t.Fatalf("ComputeBinaryIdentity(b): %v", err)
	}
	if a.Aligned(b) {
		t.Fatalf("one flipped byte should NOT be Aligned; both=%q", a.SHA256)
	}
}

// TestAlignedMTimeIrrelevant: the same bytes touched to a different mtime stay Aligned, proving the
// equality key is SHA256, not Size/MTime.
func TestAlignedMTimeIrrelevant(t *testing.T) {
	path := writeBin(t, "a.bin", []byte("stable-content"))
	a, err := ComputeBinaryIdentity(path)
	if err != nil {
		t.Fatalf("ComputeBinaryIdentity(before touch): %v", err)
	}
	future := time.Now().Add(48 * time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	b, err := ComputeBinaryIdentity(path)
	if err != nil {
		t.Fatalf("ComputeBinaryIdentity(after touch): %v", err)
	}
	if a.MTime == b.MTime {
		t.Fatalf("mtime should have changed; both=%d", a.MTime)
	}
	if !a.Aligned(b) {
		t.Fatalf("same bytes, different mtime should stay Aligned (sha-primary)")
	}
}

// TestComputeBinaryIdentityMissing: a missing/unreadable path is an error.
func TestComputeBinaryIdentityMissing(t *testing.T) {
	if _, err := ComputeBinaryIdentity(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatalf("ComputeBinaryIdentity(missing) should error")
	}
}

// TestAlignedEmptySHA: an empty-hash identity (the zero value) is never Aligned -- unknown identity
// must not read as a match.
func TestAlignedEmptySHA(t *testing.T) {
	real, err := ComputeBinaryIdentity(writeBin(t, "a.bin", []byte("x")))
	if err != nil {
		t.Fatalf("ComputeBinaryIdentity: %v", err)
	}
	var zero BinaryIdentity
	if zero.Aligned(zero) {
		t.Fatalf("zero-vs-zero (empty sha) should NOT be Aligned")
	}
	if real.Aligned(zero) || zero.Aligned(real) {
		t.Fatalf("empty sha vs real should NOT be Aligned in either direction")
	}
}
