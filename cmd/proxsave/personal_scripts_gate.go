package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safeexec"
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
	cfg.PersonalScriptPreRun = validatedPersonalScriptPath("PERSONAL_SCRIPT_PRE_RUN", cfg.PersonalScriptPreRun)
	cfg.PersonalScriptPostRun = validatedPersonalScriptPath("PERSONAL_SCRIPT_POST_RUN", cfg.PersonalScriptPostRun)
}

func validatedPersonalScriptPath(key, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if err := personalScriptPathError(path); err != nil {
		logging.Warning("%s disabled for this daemon: %v", key, err)
		return ""
	}
	return path
}

func personalScriptPathError(path string) error {
	clean := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	if resolved != clean {
		return fmt.Errorf("%s traverses a symlink (resolves to %s)", path, resolved)
	}
	if err := safeexec.ValidateTrustedExecutablePath(clean); err != nil {
		return err
	}
	info, err := os.Stat(clean)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := personalScriptOwnerError(clean, info); err != nil {
		return err
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others (mode %04o)", clean, info.Mode().Perm())
	}
	for dir := filepath.Dir(clean); ; dir = filepath.Dir(dir) {
		dirInfo, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("stat %s: %w", dir, err)
		}
		if err := personalScriptOwnerError(dir, dirInfo); err != nil {
			return err
		}
		if dirInfo.Mode().Perm()&0o022 != 0 && dirInfo.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("directory %s is writable by group or others without the sticky bit (mode %04o)", dir, dirInfo.Mode().Perm())
		}
		if dir == "/" {
			return nil
		}
	}
}

func personalScriptOwnerError(path string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("owner of %s cannot be determined", path)
	}
	if st.Uid != 0 && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s is owned by uid %d; accepted owners are root or daemon uid %d. Keep the user home ownership unchanged and move the script to a root-owned path such as /usr/local/bin", path, st.Uid, os.Geteuid())
	}
	return nil
}
