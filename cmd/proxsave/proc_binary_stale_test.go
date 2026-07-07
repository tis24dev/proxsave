package main

import (
	"os"
	"testing"
)

// TestLinkTargetStaleDeleted: a /proc/<pid>/exe target ending in " (deleted)" is definitively stale
// (the running executable was unlinked by an in-place upgrade) -- this is the whole, hash-free signal.
func TestLinkTargetStaleDeleted(t *testing.T) {
	if !linkTargetStale("/opt/proxsave/build/proxsave (deleted)") {
		t.Fatalf("a deleted running image must read as stale")
	}
}

// TestLinkTargetStaleIntact: a plain target (still on disk, not unlinked) is NOT stale.
func TestLinkTargetStaleIntact(t *testing.T) {
	if linkTargetStale("/opt/proxsave/build/proxsave") {
		t.Fatalf("an intact on-disk binary must NOT read as stale")
	}
	// A path that merely CONTAINS "deleted" (not the trailing suffix) is not stale either.
	if linkTargetStale("/opt/deleted-backups/proxsave") {
		t.Fatalf("only the trailing ' (deleted)' suffix marks staleness")
	}
}

// TestProcBinaryStaleGuardsPid: a non-positive pid cannot be probed -> (false, false), never a false
// stale verdict.
func TestProcBinaryStaleGuardsPid(t *testing.T) {
	if stale, checked := procBinaryStale(0); stale || checked {
		t.Fatalf("pid 0 must yield (false,false), got (%v,%v)", stale, checked)
	}
	if stale, checked := procBinaryStale(-1); stale || checked {
		t.Fatalf("pid -1 must yield (false,false), got (%v,%v)", stale, checked)
	}
}

// TestProcBinaryStaleSelfNotStale: probing our OWN pid reads /proc/self/exe (the running test binary),
// which is a live, non-deleted file on disk -> (stale=false, checked=true). This exercises the real
// readlink path end-to-end on Linux; where /proc is unavailable the readlink fails and we get
// checked=false, which we skip.
func TestProcBinaryStaleSelfNotStale(t *testing.T) {
	stale, checked := procBinaryStale(os.Getpid())
	if !checked {
		t.Skipf("self /proc/exe not readable in this environment; skipping")
	}
	if stale {
		t.Fatalf("the running test binary is on disk (not deleted); must NOT read as stale")
	}
}
