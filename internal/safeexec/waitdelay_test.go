package safeexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// fakeOnPath installs an executable ahead of everything else on PATH. It PREPENDS
// rather than replaces, because the scripts below call "sleep": on a stripped PATH
// they would spawn no grandchild at all and every test here would pass while proving
// nothing, which is a mistake this repository already had to fix once
// (cmd/proxsave/run_hostname_timeout_test.go). 0o755 and not 0o777, because
// ValidateTrustedExecutablePath refuses a world-writable file.
func fakeOnPath(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil { //nolint:gosec // deliberately executable
		t.Fatalf("write fake %s: %v", name, err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

// TestTrustedCommandContextBoundsAnInheritedPipe is the defect itself, driven rather
// than described: the child exits immediately and successfully, so the context
// deadline never fires and there is nothing for a timeout to bound, while a
// backgrounded grandchild holds stdout open.
func TestTrustedCommandContextBoundsAnInheritedPipe(t *testing.T) {
	// The shipped value is used deliberately, not a shrunken one. A test that sets its
	// own budget pins the mechanism and leaves the number free, and the number is load
	// bearing in both directions: too small kills healthy commands, too large is the
	// hang this exists to stop. This case guards the UPPER end: with a budget longer
	// than the grandchild's 60 second sleep the drain is never cut, so the select below
	// falls through to its timeout and the test fails on the deadline rather than on
	// the error. TestAGrandchildThatLetsGoInsideTheBudgetIsNotKilled guards the lower
	// end. The cost is that this case takes about CommandWaitDelay to run.
	path := fakeOnPath(t, "orphaner", "sleep 60 &\necho done\nexit 0")

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		cmd, err := TrustedCommandContext(context.Background(), path)
		if err != nil {
			done <- err
			return
		}
		_, runErr := cmd.Output()
		done <- runErr
	}()

	select {
	case err := <-done:
		if elapsed := time.Since(start); elapsed > CommandWaitDelay+5*time.Second {
			t.Fatalf("the call took %s with a budget of %s; the drain was not bounded", elapsed, CommandWaitDelay)
		}
		if !errors.Is(err, exec.ErrWaitDelay) {
			t.Fatalf("err = %v, want exec.ErrWaitDelay. The error IS the fix: it is the only thing separating a partial read from a complete one, so a caller that gets nil here is back in the silent state the bound exists to leave", err)
		}
	case <-time.After(CommandWaitDelay + 15*time.Second):
		t.Fatal("the call never returned: the context deadline bounds the process, not the pipe, so a grandchild holding stdout blocks it for that grandchild's whole lifetime. This runs before the run log file exists, so a stall here produces no output at all on any run")
	}
}

// TestAGrandchildThatLetsGoInsideTheBudgetIsNotKilled pins the VALUE, not merely its
// presence. Without this, setting CommandWaitDelay to one millisecond leaves every
// other test here green while killing healthy commands in the field.
func TestAGrandchildThatLetsGoInsideTheBudgetIsNotKilled(t *testing.T) {
	// The shipped value again, for the same reason: overriding it here was what let a
	// 1ms default pass this test while killing every healthy command in the field.
	// 300ms is comfortably inside a 3s budget and comfortably outside a 1ms one.
	path := fakeOnPath(t, "briefholder", "sleep 0.3 &\necho complete-output\nexit 0")

	cmd, err := TrustedCommandContext(context.Background(), path)
	if err != nil {
		t.Fatalf("TrustedCommandContext: %v", err)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("a grandchild that let go of the pipe well inside the budget still produced %v. The budget is a DRAIN allowance, and one too small turns healthy commands into failures", err)
	}
	if got := string(out); got != "complete-output\n" {
		t.Fatalf("output = %q, want the whole of it. A bound that costs output is not a bound, it is a truncation", got)
	}
}

// TestCommandContextCarriesNoWaitDelay is the boundary, and it is load bearing rather
// than tidy. internal/backup/archiver.go builds the compression pipeline through
// CommandContext with cmd.Stdout as the encrypting writer that produces the archive.
// A drain budget there turns a stalled destination into a SHORT ARCHIVE. Moving the
// default onto this constructor is the one edit that would reach it silently, and it
// is a one line change somebody will be tempted to make for symmetry.
func TestCommandContextCarriesNoWaitDelay(t *testing.T) {
	cmd, err := CommandContext(context.Background(), "echo", "x")
	if err != nil {
		t.Skipf("echo is not on the allowlist here: %v", err)
	}
	if cmd.WaitDelay != 0 {
		t.Fatalf("CommandContext stamped WaitDelay=%s. It builds the compression pipeline, where the archive itself is the thing being drained, so a budget here truncates backups. Use safeexec.ApplyWaitDelay at the call sites that want one", cmd.WaitDelay)
	}
}

// TestApplyWaitDelayMutatesTheCallersCommand pins that the helper works on the command
// the caller is about to run. A version returning a modified COPY satisfies every
// structural assertion above and bounds nothing.
func TestApplyWaitDelayMutatesTheCallersCommand(t *testing.T) {
	old := CommandWaitDelay
	CommandWaitDelay = 1234 * time.Millisecond
	t.Cleanup(func() { CommandWaitDelay = old })

	// An ARG-BEARING construction, because a constructor that stamps only when
	// len(args)==0 passed every other test here: they all build with no arguments.
	withArgs, err := TrustedCommandContext(context.Background(), "/bin/sh", "-c", "true")
	if err != nil {
		t.Fatalf("TrustedCommandContext with arguments: %v", err)
	}
	if withArgs.WaitDelay != CommandWaitDelay {
		t.Errorf("a command built WITH arguments carries WaitDelay=%s, want %s. Nearly every real call site passes arguments, so a constructor that bounds only the bare form bounds almost nothing", withArgs.WaitDelay, CommandWaitDelay)
	}

	cmd := exec.Command("true")
	returned := ApplyWaitDelay(cmd)
	if cmd.WaitDelay != CommandWaitDelay {
		t.Errorf("the caller's own command still has WaitDelay=%s; ApplyWaitDelay worked on a copy, so nothing it is called on is actually bounded", cmd.WaitDelay)
	}
	if returned != cmd {
		t.Error("ApplyWaitDelay returned a different pointer from the one it was given")
	}
	if ApplyWaitDelay(nil) != nil {
		t.Error("ApplyWaitDelay(nil) must return nil rather than panic: callers chain it onto a constructor that can fail")
	}
}
