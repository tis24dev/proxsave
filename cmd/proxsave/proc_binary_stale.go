// Package main contains the proxsave command entrypoint.
package main

import (
	"fmt"
	"os"
	"strings"
)

// procBinaryStale is the RECORD-INDEPENDENT, HASH-FREE staleness probe for the resident daemon: it
// answers "was the RUNNING binary replaced on disk?" WITHOUT any recorded identity or content hash.
// The signal is /proc/<pid>/exe: Linux blocks overwriting a running executable (ETXTBSY), so an
// in-place upgrade/rebuild UNLINKS the old file and the kernel keeps the symlink pointing at the now
// deleted inode, rendering the target with a trailing " (deleted)". That suffix alone is a complete
// proof of staleness -- no hash comparison is needed or possible (the on-disk path is gone).
//
// Returns (stale, checked): checked=false means "could not determine" (a non-positive pid, or an
// unreadable link), so the caller leaves alignment UNKNOWN. Linux-only, matching the existing
// /proc/<pid>/cmdline use in probeProxsaveDaemonAlive; a non-Linux host simply gets an unreadable
// link -> checked=false.
func procBinaryStale(pid int) (stale bool, checked bool) {
	if pid <= 0 {
		return false, false
	}
	target, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return false, false
	}
	return linkTargetStale(target), true
}

// linkTargetStale is the pure decision at the heart of procBinaryStale, split out so it is
// unit-testable without a live /proc: a /proc/<pid>/exe target ending in " (deleted)" means the
// running executable was unlinked (upgraded/rebuilt in place) and is therefore stale.
func linkTargetStale(target string) bool {
	return strings.HasSuffix(target, " (deleted)")
}

// procBinaryStaleProbe is the injectable seam (production = procBinaryStale) wired into the
// daemon-state checks that observe the daemon EXTERNALLY, so tests can drive alignment without a
// live /proc.
var procBinaryStaleProbe = procBinaryStale
