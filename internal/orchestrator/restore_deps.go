package orchestrator

import (
	"context"
	"time"
)

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

// runCommandStdout runs a command and returns ONLY its stdout, for callers that
// parse the output instead of logging it. It mirrors runRestoreCommandStream's
// shape: an optional capability on the injected runner, with the ordinary Run as
// the fallback so no fake has to grow a method it does not need.
//
// The fallback is not equivalent - Run merges stderr - and that is deliberate: a
// test fake decides its own bytes, while on a real host stderr is what breaks the
// parse. See osCommandRunner.RunStdout for the measurement.
func runCommandStdout(ctx context.Context, name string, args ...string) ([]byte, error) {
	type stdoutRunner interface {
		RunStdout(ctx context.Context, name string, args ...string) ([]byte, error)
	}
	if sr, ok := restoreCmd.(stdoutRunner); ok && sr != nil {
		return sr.RunStdout(ctx, name, args...)
	}
	return restoreCmd.Run(ctx, name, args...)
}

func nowRestore() time.Time {
	if restoreTime != nil {
		return restoreTime.Now()
	}
	return time.Now()
}
