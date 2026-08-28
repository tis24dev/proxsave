package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/safeexec"
)

// TestRunReturnsTheCompleteAnswerWhenOnlyTheDrainWasCut pins a translation that looks
// wrong and is right, and pins WHY, because the reason is the only thing that keeps it
// from being copied to a sink where it would be wrong.
//
// osCommandRunner.Run turns exec.ErrWaitDelay into a nil error. That is safe here and
// nowhere else, because of the SINK: CombinedOutput drains into a bytes.Buffer, which
// never blocks, so every byte the child wrote is already in the buffer before the
// timer can start. What the budget interrupts is the wait for an EOF that a surviving
// descendant is withholding, not the read itself. This test proves the output is
// COMPLETE with a payload far past the 64 KiB pipe buffer, which is the only way to
// tell a complete answer from a lucky short one.
//
// Returning the error instead was tried and reverted. This []byte reaches nine bare
// availability gates ("which pvesh", "which systemctl") and the systemd-run rollback
// arming, each of which reads a non-nil error as "the tool is missing" or "arming
// failed". A complete answer turned into a failure made a restore skip applying
// datacenter.cfg while reporting success.
//
// internal/storage/cloud.go refuses the same translation for rclone and is right to:
// an interrupted OPERATION really is incomplete. The rule follows the sink, not the
// error.
func TestRunReturnsTheCompleteAnswerWhenOnlyTheDrainWasCut(t *testing.T) {
	oldSafe, oldDefault := safeexec.CommandWaitDelay, defaultCommandWaitDelay
	safeexec.CommandWaitDelay, defaultCommandWaitDelay = 300*time.Millisecond, 300*time.Millisecond
	// Both by hand: defaultCommandWaitDelay is an init-time COPY of the safeexec value,
	// not a link to it, so setting one does not move the other.
	t.Cleanup(func() { safeexec.CommandWaitDelay, defaultCommandWaitDelay = oldSafe, oldDefault })

	const payload = 200000 // far past the 64 KiB pipe buffer
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "say"), []byte(strings.Repeat("A", payload-1)+"Z"), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	// "which" is on the allowlist CommandContext validates against. The fake writes the
	// whole payload, orphans a holder on stdout, and exits 0, so the context deadline
	// never fires and the drain budget is the only thing that can produce an error.
	if err := os.WriteFile(filepath.Join(dir, "which"), []byte("#!/bin/sh\nsleep 60 &\ncat "+filepath.Join(dir, "say")+"\nexit 0\n"), 0o755); err != nil { //nolint:gosec // deliberately executable
		t.Fatalf("write fake which: %v", err)
	}
	// Prepended, not replaced: the script has to be able to run "sleep" and "cat", and
	// a PATH holding only dir would spawn no grandchild and prove nothing.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		out, err := osCommandRunner{}.Run(context.Background(), "which", "thing")
		done <- result{out, err}
	}()

	select {
	case got := <-done:
		if elapsed := time.Since(start); elapsed > 20*time.Second {
			t.Fatalf("Run took %s: the drain was not bounded at all", elapsed)
		}
		if got.err != nil {
			t.Fatalf("Run returned %v. The output was complete and only the EOF was withheld, and nine availability gates read a non-nil error as \"the tool is missing\": one of them makes a restore skip applying datacenter.cfg and report success", got.err)
		}
		if len(got.out) != payload || got.out[len(got.out)-1] != 'Z' {
			t.Fatalf("Run returned %d of %d bytes. If the buffer can come back short then the translation above it IS handing a prefix back as a complete answer, and it has to go", len(got.out), payload)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run never returned: a grandchild holding stdout blocks it for that grandchild's lifetime, which is what the drain budget exists to stop")
	}
}
