package orchestrator

import "time"

// Hooks for restore-related functions to enable dependency injection.
var restoreFS FS = osFS{}
var restoreTime TimeProvider = realTimeProvider{}
var restorePrompter Prompter = consolePrompter{}
var restoreCmd CommandRunner = osCommandRunner{}
var restoreSystem SystemDetector = realSystemDetector{}

func setRestoreDeps(fs FS, tp TimeProvider, p Prompter, cmd CommandRunner, sys SystemDetector) {
	if fs != nil {
		restoreFS = fs
		compatFS = fs
	}
	if tp != nil {
		restoreTime = tp
	}
	if p != nil {
		restorePrompter = p
	}
	if cmd != nil {
		restoreCmd = cmd
	}
	if sys != nil {
		restoreSystem = sys
	}
}

func nowRestore() time.Time {
	if restoreTime != nil {
		return restoreTime.Now()
	}
	return time.Now()
}
