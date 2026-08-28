package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/safeexec"
)

// TestTheCollectorRunnerIsBoundedWhenAToolLeavesAChildHoldingStdout pins the drain
// budget on the busiest command runner in the product.
//
// runCommandCapturedWithEnv executes every collected artifact's command, more per run
// than every other call site combined, and it captures into two in-memory buffers.
// Without a budget, ONE collected tool that leaves a background child holding stdout
// stalls the entire collection phase for that child's lifetime: the tool exits 0, so
// the context deadline never fires and there is nothing for a timeout to bound.
//
// The budget cannot cost output here, and the assertion below proves it rather than
// asserting it: a buffer never blocks, so everything the tool wrote is already in it
// when the timer starts, and the payload is deliberately far past the 64 KiB pipe
// buffer so a lucky short read cannot pass for a complete one.
func TestTheCollectorRunnerIsBoundedWhenAToolLeavesAChildHoldingStdout(t *testing.T) {
	old := safeexec.CommandWaitDelay
	safeexec.CommandWaitDelay = 300 * time.Millisecond
	t.Cleanup(func() { safeexec.CommandWaitDelay = old })

	const payload = 200000
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "say"), []byte(strings.Repeat("A", payload-1)+"Z"), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	// "which" is on the allowlist safeexec.CommandContext validates against.
	if err := os.WriteFile(filepath.Join(dir, "which"), []byte("#!/bin/sh\nsleep 60 &\ncat "+filepath.Join(dir, "say")+"\nexit 0\n"), 0o755); err != nil { //nolint:gosec // deliberately executable
		t.Fatalf("write fake which: %v", err)
	}
	// Prepended, not replaced: the script itself runs "sleep" and "cat", and a PATH
	// holding only dir would spawn no grandchild and prove nothing.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		out, _, err := runCommandCapturedWithEnv(context.Background(), nil, "which", "thing")
		done <- result{out, err}
	}()

	select {
	case got := <-done:
		if elapsed := time.Since(start); elapsed > 20*time.Second {
			t.Fatalf("the collector runner took %s; the drain was not bounded", elapsed)
		}
		if len(got.out) != payload || got.out[len(got.out)-1] != 'Z' {
			t.Fatalf("the runner captured %d of %d bytes. A budget that costs a collected artifact its content is worse than the stall it replaced", len(got.out), payload)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the collector runner never returned: one tool leaving a child on stdout stalls the whole collection phase, and this runner executes more commands per run than every other call site combined")
	}
}
