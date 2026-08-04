//go:build !race

package uitest

// raceScale leaves driver-test render-poll deadlines unchanged for normal runs.
const raceScale = 1
