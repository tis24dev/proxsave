package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/safeexec"
)

// TestThePreflightIsBoundedWhenASystemToolLeavesAChildHoldingStdout pins the drain
// budget on the checks that gate every backup.
//
// checkOpenPorts shells out to "ss", a tool this project does not control. If it
// leaves a background child holding stdout, the check blocks for that child's whole
// lifetime, and it does so BEFORE the archive is started: the operator sees a run that
// hangs having produced nothing. The tool exits 0, so the context deadline never fires
// and there is nothing for a timeout to bound.
//
// The assertion also proves the budget costs no output, rather than asserting it: the
// payload is far past the 64 KiB pipe buffer, so a lucky short read cannot pass for a
// complete one.
func TestThePreflightIsBoundedWhenASystemToolLeavesAChildHoldingStdout(t *testing.T) {
	old := safeexec.CommandWaitDelay
	safeexec.CommandWaitDelay = 300 * time.Millisecond
	t.Cleanup(func() { safeexec.CommandWaitDelay = old })

	const payload = 200000
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "say"), []byte(strings.Repeat("A", payload)), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ss"), []byte("#!/bin/sh\nsleep 60 &\ncat "+filepath.Join(dir, "say")+"\nexit 0\n"), 0o755); err != nil { //nolint:gosec // deliberately executable
		t.Fatalf("write fake ss: %v", err)
	}
	// Prepended, not replaced: the script itself runs "sleep" and "cat", and a PATH
	// holding only dir would spawn no grandchild and prove nothing.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// The package's own fixture: checkOpenPorts warns through the logger when the
	// command errors, and a bare Checker{} panics on the nil one before the assertion
	// below can run.
	c := newCheckerForTest(nil, nil)
	done := make(chan struct{})
	start := time.Now()
	go func() {
		c.checkOpenPorts(context.Background())
		close(done)
	}()

	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > 20*time.Second {
			t.Fatalf("the preflight check took %s; the drain was not bounded", elapsed)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the preflight never returned: one system tool leaving a child on stdout stalls every backup before the archive is started, with no output to show for it")
	}
}
