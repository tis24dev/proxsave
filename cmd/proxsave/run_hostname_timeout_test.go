package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeHostnameOnPath installs an executable named "hostname" ahead of everything
// else on PATH, so resolveHostname's exec.LookPath finds it. 0o755 matters:
// safeexec.ValidateTrustedExecutablePath refuses a world-writable file, which is
// what a lazier 0o777 would produce.
func fakeHostnameOnPath(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "hostname")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil { //nolint:gosec // deliberately executable
		t.Fatalf("write fake hostname: %v", err)
	}
	// Prepended, not replaced: exec.LookPath must find this one first, but the
	// script itself still has to be able to run "sleep", and a PATH holding only dir
	// would silently turn the grandchild case into a no-op that proves nothing.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestResolveHostnameIsBoundedWhenAGrandchildHoldsThePipe pins the reaping half of
// the timeout, which the WithTimeout above it does NOT provide.
//
// exec.CommandContext kills the direct child when the deadline fires, but
// cmd.Output waits on the stdout PIPE, and a grandchild that inherited stdout keeps
// it open after its parent is gone. Without WaitDelay the call blocks for the
// grandchild's whole lifetime. The sharpest shape is the one below: "hostname"
// exits immediately and successfully, so the 2 second context never fires at all
// and there is nothing for a timeout to bound.
//
// This is not a hypothetical hang in an unimportant place. resolveHostname is the
// first statement of initializeRunLogFile, so it runs before the run log file
// exists, and a stall here produces no output whatsoever on any run.
func TestResolveHostnameIsBoundedWhenAGrandchildHoldsThePipe(t *testing.T) {
	old := runHostnameWaitDelay
	runHostnameWaitDelay = 100 * time.Millisecond
	t.Cleanup(func() { runHostnameWaitDelay = old })

	// The child exits at once; the backgrounded sleep inherits stdout and holds the
	// pipe open. Same technique as TestDefaultExecCommandWaitDelayReturnsError.
	fakeHostnameOnPath(t, "sleep 30 &\nexit 0")

	done := make(chan string, 1)
	start := time.Now()
	go func() { done <- resolveHostname() }()

	select {
	case got := <-done:
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("resolveHostname took %s; WaitDelay did not bound the wait on the inherited pipe", elapsed)
		}
		// The probe produced nothing usable, so the kernel name is the answer. What
		// matters is that it is an answer at all, and that it arrived.
		if got == "" {
			t.Fatal("resolveHostname returned an empty name; every caller treats that as a dropped wiring rather than an unnameable machine")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("resolveHostname never returned: the 2 second timeout bounds the process, not the pipe, so a grandchild holding stdout blocks it for that grandchild's whole lifetime")
	}
}

// TestResolveHostnameStillReadsAWorkingProbe is the control: the bound must not
// break the ordinary case, where "hostname -f" answers and its value is preferred
// over the kernel short name. Losing that preference is discussion #292 itself.
func TestResolveHostnameStillReadsAWorkingProbe(t *testing.T) {
	fakeHostnameOnPath(t, `echo "pve.home.arpa"`)

	if got := resolveHostname(); got != "pve.home.arpa" {
		t.Fatalf("resolveHostname = %q, want %q: the FQDN the probe reports is what the writer stamps into archive names, and retention recognises its own work by it", got, "pve.home.arpa")
	}
}
