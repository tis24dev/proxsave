package main

import (
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

// The two variable names, written once. Both the startup inspection and the per-run reporting
// below name the setting an operator has to go and edit, so a typo in either would send them
// looking for a variable that is not in the file.
const (
	personalScriptPreRunKey  = "PERSONAL_SCRIPT_PRE_RUN"
	personalScriptPostRunKey = "PERSONAL_SCRIPT_POST_RUN"
)

// This file is the LOUD half of the personal-script feature and lives apart from
// personal_scripts.go on purpose: the starters' silence rule is enforced as a property of
// that file's import list (TestPersonalScriptsFileImportsNoReportingPackage), and the gate
// exists precisely to speak - once, at daemon startup - where the starters never may.

// validatePersonalScripts is the diagnostic trusted-path gate for both operator scripts,
// run ONCE at daemon startup. It lives here because a refusal must be LOUD - one WARNING
// naming the variable, the path and the reason - while the execution-time opened-inode gate
// in personal_scripts.go must stay silent. A startup refusal blanks the setting, so every
// later tick behaves as if it were never configured. A foreign-owned ancestor is instead an
// administrator trust warning: the normalized path stays enabled after the administrator
// has chosen to configure it.
//
// What it enforces, and why: the path may not traverse a symlink, the target must belong
// to root or to the daemon's own user and must not be writable by group or others, and a
// directory writable by group or others must carry the sticky bit (the /tmp shape, where
// non-owners cannot rename or unlink entries). A foreign-owned ancestor can replace its
// descendants, so accepting that path records and logs the administrator's trust decision;
// execution remains safe because the starter pins and validates the opened target inode.
func validatePersonalScripts(cfg *config.Config) personalScriptsDiagnostics {
	diagnostics := inspectPersonalScripts(cfg, os.Geteuid())
	if cfg == nil {
		return diagnostics
	}
	cfg.PersonalScriptPreRun = applyPersonalScriptDiagnostic(diagnostics.Pre)
	cfg.PersonalScriptPostRun = applyPersonalScriptDiagnostic(diagnostics.Post)
	return diagnostics
}

func applyPersonalScriptDiagnostic(diagnostic personalScriptDiagnostic) string {
	switch diagnostic.State {
	case personalScriptReady:
		return diagnostic.Path
	case personalScriptReadyWithWarning:
		logging.Warning("%s enabled with administrator trust warning: %s", diagnostic.Key, diagnostic.Reason)
		return diagnostic.Path
	case personalScriptRefused:
		logging.Warning("%s disabled for this daemon: %s", diagnostic.Key, diagnostic.Reason)
		return ""
	default:
		return ""
	}
}

// runPersonalScriptReporting is the waited starter with the per-run gate given a voice.
//
// The startup gate already speaks once for a path it refuses or accepts under warning. What had
// no voice until here is the SECOND gate, the one that re-validates the opened inode before every
// single invocation (openPersonalScriptForExecution): it can refuse a path startup accepted,
// because between the two the file itself can change, and that is precisely the interval the
// administrator trust decision covers. Refusing in silence left the daemon knowing the script had
// not run while the operator saw an ordinary green backup.
//
// DAEMON.md's reason for the startup warnings is the same reason for this one, applied to the
// other end of the interval: without them a policy decision is indistinguishable from a script
// that ran and did nothing.
//
// What is NOT reported here is unchanged: the script's output, its exit code, its timeout kill,
// and a fork that failed on the script's own account. This is one line about ProxSave's decision,
// not a line about the operator's script. It goes to the daemon's own log, never to the run's, so
// no backup log, recap, notification, ping or metric gains a row.
func runPersonalScriptReporting(logger *logging.Logger, key, path string, stop <-chan struct{}) {
	if strings.TrimSpace(path) == "" {
		return
	}
	done := logging.DebugStart(logger, "personal script", "key=%s path=%q wait=true", key, path)
	refusal := runPersonalScript(path, stop)
	done(refusal)
	reportPersonalScriptRefusal(key, refusal)
}

// startPersonalScriptDetachedReporting is the same voice for the two paths that start the post
// script and walk away. The daemon is on its way out on both, which is exactly when an operator
// is least likely to reconstruct what happened later, so the refusal is worth its one line here
// too. The debug bracket closes before the daemon exits because nothing is waited on: what it
// times is the gate and the fork, which is all this path does.
func startPersonalScriptDetachedReporting(logger *logging.Logger, key, path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	done := logging.DebugStart(logger, "personal script", "key=%s path=%q wait=false", key, path)
	refusal := startPersonalScriptDetached(path)
	done(refusal)
	reportPersonalScriptRefusal(key, refusal)
}

// reportPersonalScriptRefusal writes the one line, and writes it the same way for both starters
// so a reader cannot tell the waited path from the detached one by its wording alone - the two
// differ in what the daemon does next, not in what the operator lost.
//
// It says "was not started for this run" rather than naming the gate: the operator's question is
// whether their script ran, and the reason that follows already carries the path and the fact.
func reportPersonalScriptRefusal(key string, refusal error) {
	if refusal == nil {
		return
	}
	logging.Warning("%s was not started for this run: %v", key, refusal)
}
