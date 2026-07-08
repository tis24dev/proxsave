//go:build race

package uitest

// raceScale widens driver-test render-poll deadlines under the race detector, whose
// instrumentation slows the event loop enough that the default deadlines flake.
const raceScale = 8
