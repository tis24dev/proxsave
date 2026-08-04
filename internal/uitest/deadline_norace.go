//go:build !race

package uitest

// raceScale is the headroom a normal run gets. It is not 1: these deadlines bound how
// promptly the bubbletea event loop is scheduled, and a test that renders in a second
// on its own can miss a 15s budget while the rest of the package competes for the CPU.
// See Deadline -- the extra headroom costs nothing when the run is green.
const raceScale = 4
