package main

import (
	"os"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

// This file is the LOUD half of the personal-script feature and lives apart from
// personal_scripts.go on purpose: the starters' silence rule is enforced as a property of
// that file's import list (TestPersonalScriptsFileImportsNoReportingPackage), and the gate
// exists precisely to speak - once, at daemon startup - where the starters never may.

// validatePersonalScripts is the trusted-path gate for both operator scripts, run ONCE at
// daemon startup. It lives here and not in the starters for the reason personalScriptCmd
// records: under the starters' frozen silence rule a refused path would be an undebuggable
// non-execution, so the refusal happens where it can be LOUD - one WARNING naming the
// variable, the path and the reason - and the setting is blanked, so every later tick
// behaves as if it were never configured.
//
// What it enforces, and why it is enough against the threat (a non-root user swapping the
// file a root daemon will execute): the path may not traverse a symlink, the target and
// every directory above it must belong to root or to the daemon's own user, the target must
// not be writable by group or others, and a directory writable by group or others must
// carry the sticky bit (the /tmp shape, where non-owners cannot rename or unlink entries).
// Under those rules no non-root user can replace any component between this check and any
// later execution, which is why the starters do not re-validate: the window belongs to root
// alone.
func validatePersonalScripts(cfg *config.Config) {
	if cfg == nil {
		return
	}
	diagnostics := inspectPersonalScripts(cfg, os.Geteuid())
	cfg.PersonalScriptPreRun = applyPersonalScriptDiagnostic(diagnostics.Pre)
	cfg.PersonalScriptPostRun = applyPersonalScriptDiagnostic(diagnostics.Post)
}

func applyPersonalScriptDiagnostic(diagnostic personalScriptDiagnostic) string {
	switch diagnostic.State {
	case personalScriptReady:
		return diagnostic.Path
	case personalScriptRefused:
		logging.Warning("%s disabled for this daemon: %s", diagnostic.Key, diagnostic.Reason)
		return ""
	default:
		return ""
	}
}
