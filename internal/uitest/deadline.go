// Package uitest holds shared helpers for the Charm/bubbletea driver TESTS. It is
// imported only from _test.go files and never linked into the production binary.
package uitest

import "time"

// Deadline scales a base driver-test timeout by a race-aware factor. The Charm
// driver tests poll a render buffer until a screen/line appears; under the race
// detector the bubbletea event loop runs roughly an order of magnitude slower, so a
// fixed wall-clock deadline (e.g. 5s) can fire spuriously even though the logic is
// correct. Because those polls return as soon as the condition is met, a wider
// deadline is FREE on the success path - it only adds headroom before a genuine
// hang is finally reported. Use it ONLY for UI-render polling deadlines, never for
// tests that assert an operation's own timeout behavior.
func Deadline(base time.Duration) time.Duration {
	return base * time.Duration(raceScale)
}
