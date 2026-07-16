package main

import (
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// TestCapturePanicExit_SetsExitAndStashesStack pins F02-15: on a panic, capturePanicExit
// (deferred BEFORE the summary footer in runRuntime) makes the panic authoritative for the
// exit code so the footer, which runs later in the same unwind, reads 13 and colors RED
// instead of GREEN. It also stashes the ORIGINAL stack (contains this test's frame) and
// re-panics so finishMainRun stays the single os.Exit site.
func TestCapturePanicExit_SetsExitAndStashesStack(t *testing.T) {
	state := newAppRunState()

	func() {
		// This recover stands in for finishMainRun: it must still receive the re-panic.
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("capturePanicExit must re-panic so finishMainRun stays authoritative")
			}
		}()
		defer capturePanicExit(state)
		panic("kaboom")
	}()

	if state.finalExitCode != types.ExitPanicError.Int() {
		t.Fatalf("finalExitCode = %d, want %d", state.finalExitCode, types.ExitPanicError.Int())
	}
	if len(state.panicStack) == 0 {
		t.Fatalf("panicStack not stashed")
	}
	if !strings.Contains(string(state.panicStack), "TestCapturePanicExit_SetsExitAndStashesStack") {
		t.Fatalf("stashed stack does not contain the panic origin frame:\n%s", state.panicStack)
	}
}

// TestCapturePanicExit_NoPanicNoop: on a clean return capturePanicExit is a no-op (no exit
// override, no stashed stack), so the success path is unchanged.
func TestCapturePanicExit_NoPanicNoop(t *testing.T) {
	state := newAppRunState()
	func() {
		defer capturePanicExit(state)
	}()
	if state.finalExitCode != types.ExitSuccess.Int() {
		t.Fatalf("finalExitCode = %d, want success %d", state.finalExitCode, types.ExitSuccess.Int())
	}
	if len(state.panicStack) != 0 {
		t.Fatalf("panicStack must stay empty on a clean run, got %d bytes", len(state.panicStack))
	}
}

// TestPanicExitCodeSeverityIsError: a footer painted with the panic exit code is RED (error),
// never GREEN. This is the display half of F02-15 the guard makes reachable.
func TestPanicExitCodeSeverityIsError(t *testing.T) {
	if sev := exitCodeSeverity(types.ExitPanicError.Int(), nil); sev != severityError {
		t.Fatalf("severity(panic) = %v, want severityError", sev)
	}
}
