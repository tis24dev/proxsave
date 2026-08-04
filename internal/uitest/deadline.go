// Package uitest holds shared helpers for the Charm/bubbletea driver TESTS. It is
// imported only from _test.go files and never linked into the production binary.
package uitest

import (
	"runtime"
	"time"
)

// Deadline scales a base driver-test timeout by a slowness-aware factor.
//
// The Charm driver tests start a REAL bubbletea event loop and then poll its render
// buffer every 10ms until a screen or a line appears. That loop has to be SCHEDULED to
// render at all, so these deadlines measure how promptly the runtime gets round to it,
// not how long the logic takes. Run on its own, such a test finishes in a second or two
// against a 15s or 60s budget. Run inside the whole package it competes with everything
// else for the CPU, and the render can arrive after the budget has expired -- with no
// bug anywhere.
//
// That is not hypothetical. TestDashboardUpgradeScreen and
// TestDashboardDiagnosticNotConfiguredShowsNotice both timed out this way: green in
// isolation, green in most full runs, red in the ones where the package took ~100s
// instead of ~38s because the machine was busy with something else. It was measured
// down to the cause -- not leaked goroutines (2 alone, 4 after every dashboard test),
// not a global left behind, not one polluting neighbour (both halves of the family
// reproduce it), and adding -v to the same command was enough to flip the outcome.
//
// Widening is FREE on the success path: every poll returns the moment its condition is
// met, so a larger budget costs nothing on a green run and only delays the report of a
// genuine hang, which `go test` still bounds with its own panic timeout. So the factor
// is deliberately generous rather than tuned to the slowdown we happened to measure.
//
// The race detector gets its own multiplier for the same reason under a different
// cause: its instrumentation slows the event loop by roughly an order of magnitude.
// Use this ONLY for UI-render polling deadlines, never for tests that assert an
// operation's own timeout behavior.
func Deadline(base time.Duration) time.Duration {
	return base * time.Duration(raceScale*cpuScale())
}

// cpuScale doubles the budget again when the event loop has few processors to be
// scheduled on -- a small CI runner, a pinned GOMAXPROCS, or a container quota. Below
// four, a driver test and the rest of the suite are effectively taking turns.
func cpuScale() int {
	if runtime.GOMAXPROCS(0) < 4 {
		return 2
	}
	return 1
}
