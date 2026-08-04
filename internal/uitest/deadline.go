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
//
// Do NOT widen these to chase a driver test that times out on a busy machine. That
// symptom was chased here once and the factor briefly raised; the cause was in the
// driver harness, not in the budget. waitScreen sampled its match offset when the
// TEST goroutine read a screen-push, not when the event loop emitted it, so a reader
// scheduled after the render started matching PAST the text it was waiting for and
// could never match at all. See screenPush in cmd/proxsave/newkey_charm_test.go. A
// poll that burns its ENTIRE deadline is that shape of bug; a genuinely slow machine
// fails late and irregularly instead.
func Deadline(base time.Duration) time.Duration {
	return base * time.Duration(raceScale)
}
