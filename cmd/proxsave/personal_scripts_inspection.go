package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/safeexec"
)

type personalScriptState string

const (
	personalScriptNotConfigured    personalScriptState = "not-configured"
	personalScriptReady            personalScriptState = "ready"
	personalScriptReadyWithWarning personalScriptState = "ready-with-warning"
	personalScriptRefused          personalScriptState = "refused"
	// personalScriptUnknown is the CURRENT side only: there is no configuration to
	// inspect, so no verdict can be reached. A daemon never publishes it (see
	// personalScriptDiagnosticFromRuntime, which rejects it as an invalid record),
	// and it is not "not-configured", which asserts the settings were empty.
	personalScriptUnknown personalScriptState = "unknown"
)

// personalScriptConfigUnreadable is the one reason text for the unknown state, so
// the CLI and the dashboard cannot drift apart on it. personalScriptUnreadableReason
// appends the loader's own words when the caller has them: "could not be read" tells
// the operator which screen is guessing, the cause tells them which file to fix.
const personalScriptConfigUnreadable = "current configuration could not be read"

func personalScriptUnreadableReason(cause error) string {
	if cause == nil {
		return personalScriptConfigUnreadable
	}
	return personalScriptConfigUnreadable + ": " + cause.Error()
}

type personalScriptPathComponent struct {
	Path string
	UID  uint32
	Mode os.FileMode
}

type personalScriptDiagnostic struct {
	Key       string
	Path      string
	State     personalScriptState
	Reason    string
	DaemonUID int
	// Assignment explains a NOT CONFIGURED verdict by naming how the variable is
	// written in the file being read RIGHT NOW: absent, empty, or overwritten by a
	// later line. It belongs to the current-configuration side only, because the
	// running daemon read its own copy of the file when it started.
	//
	// It is deliberately NOT part of the Reason field: comparePersonalScript compares
	// Reason between the two sides, so a note that only one side can ever carry would
	// turn every IN SYNC into PATH STATE CHANGED.
	Assignment string
	// HardlinkAdvisory states whether the kernel setting the accepted foreign-owned
	// ancestor rests on is in force. It is read from /proc/sys RIGHT NOW, so it is a
	// fact about the KERNEL and not about the path, and it lives outside Reason for
	// the same rule Assignment does: comparePersonalScript compares Reason between
	// the two sides, so a clause that can change without the path moving turns a
	// sysctl edit into PATH STATE CHANGED SINCE STARTUP.
	HardlinkAdvisory string
	// HardlinkProtectionInForce is the same reading as a fact rather than a sentence,
	// so a renderer decides the LEVEL without matching on the advisory's words. The
	// two halves are written together by personalScriptHardlinkAdvisory and must never
	// disagree; TestTheAdvisoryTextAndItsFlagAgree pins that they cannot.
	//
	// It exists because the advisory says two opposite things. Off or unreadable is a
	// warning: the trust decision has nothing behind it. In force is the reassurance
	// that it does, and raising that as a WARNING told the operator to act on
	// something already right.
	HardlinkProtectionInForce bool
	Components                []personalScriptPathComponent
}

type personalScriptsDiagnostics struct {
	Pre  personalScriptDiagnostic
	Post personalScriptDiagnostic
}

var (
	personalScriptEvalSymlinks       = filepath.EvalSymlinks
	personalScriptStat               = os.Stat
	personalScriptValidateExecutable = safeexec.ValidateTrustedExecutablePath
	personalScriptHardlinkProtection = readProtectedHardlinks
)

const protectedHardlinksPath = "/proc/sys/fs/protected_hardlinks"

func readProtectedHardlinks() (int, error) {
	data, err := os.ReadFile(protectedHardlinksPath)
	if err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", protectedHardlinksPath, err)
	}
	return value, nil
}

// personalScriptHardlinkAdvisory states whether the kernel setting the accepted
// foreign-owned ancestor RESTS ON is actually in force.
//
// The ancestor's owner can unlink the script and put another file in its place;
// what stops them is the execution-time gate refusing anything not owned by root
// or the daemon, and they cannot chown a file to root. The one way around that is
// a hard link to an existing root-owned executable, which fs.protected_hardlinks=1
// forbids for a file the linker neither owns nor can write. With the setting off,
// the ownership check stops nothing and the trust decision has no mitigation left
// behind it. Reporting it is not a policy change: the path stays enabled either
// way, as the maintainer decided; the operator is told what the decision rests on.
// It returns the sentence AND whether the protection is actually in force, because
// the two readings need opposite levels and matching on the sentence to tell them
// apart would put a renderer's verdict at the mercy of an edit to this text.
func personalScriptHardlinkAdvisory() (string, bool) {
	value, err := personalScriptHardlinkProtection()
	if err != nil {
		return fmt.Sprintf("fs.protected_hardlinks unreadable: %v", err), false
	}
	if value == 0 {
		return "fs.protected_hardlinks=0 allows hard-linking root-owned executables; set it to 1", false
	}
	return fmt.Sprintf("fs.protected_hardlinks=%d blocks hard-linking root-owned executables", value), true
}

// inspectPersonalScripts returns both configured-script verdicts without
// logging, mutating the configuration, or executing either script.
//
// A nil cfg is "no configuration to read", NOT "both settings are empty". Reading
// it as the empty string made every consumer state two falsehoods at once: the
// current side rendered NOT CONFIGURED, and the daemon-status comparison then saw
// a config path of "" against the running daemon's real one and told the operator
// to restart the daemon, when the daemon was fine and the config file was not.
func inspectPersonalScripts(cfg *config.Config, daemonUID int) personalScriptsDiagnostics {
	if cfg == nil {
		// No cause to name from here: this entry point is handed a config, not the
		// attempt that failed to produce one. A caller that HAS the loader's error
		// calls unknownPersonalScripts directly.
		return unknownPersonalScripts(daemonUID, nil)
	}
	return personalScriptsDiagnostics{
		Pre:  inspectPersonalScript(personalScriptPreRunKey, cfg.PersonalScriptPreRun, daemonUID),
		Post: inspectPersonalScript(personalScriptPostRunKey, cfg.PersonalScriptPostRun, daemonUID),
	}
}

// unknownPersonalScripts is the single builder of the unknown verdict, so the two
// ways in (a nil config, and a caller holding the load error) cannot word it
// differently.
func unknownPersonalScripts(daemonUID int, cause error) personalScriptsDiagnostics {
	return personalScriptsDiagnostics{
		Pre:  unknownPersonalScript(personalScriptPreRunKey, daemonUID, cause),
		Post: unknownPersonalScript(personalScriptPostRunKey, daemonUID, cause),
	}
}

func unknownPersonalScript(key string, daemonUID int, cause error) personalScriptDiagnostic {
	return personalScriptDiagnostic{
		Key:       key,
		State:     personalScriptUnknown,
		Reason:    personalScriptUnreadableReason(cause),
		DaemonUID: daemonUID,
	}
}

// inspectPersonalScript applies the daemon's trusted-path policy and records
// the ownership/mode evidence used to reach its presentation-neutral verdict.
func inspectPersonalScript(key, path string, daemonUID int) personalScriptDiagnostic {
	path = strings.TrimSpace(path)
	diagnostic := personalScriptDiagnostic{
		Key:       key,
		Path:      path,
		State:     personalScriptNotConfigured,
		DaemonUID: daemonUID,
	}
	if path == "" {
		return diagnostic
	}

	refuse := func(err error) personalScriptDiagnostic {
		diagnostic.State = personalScriptRefused
		diagnostic.Reason = err.Error()
		return diagnostic
	}

	clean := filepath.Clean(path)
	diagnostic.Path = clean
	resolved, err := personalScriptEvalSymlinks(clean)
	if err != nil {
		return refuse(fmt.Errorf("resolve %s: %w", path, err))
	}
	if resolved != clean {
		return refuse(fmt.Errorf("%s traverses a symlink (resolves to %s)", path, resolved))
	}
	if err := personalScriptValidateExecutable(clean); err != nil {
		return refuse(err)
	}

	info, err := personalScriptStat(clean)
	if err != nil {
		return refuse(fmt.Errorf("stat %s: %w", path, err))
	}
	if err := appendPersonalScriptComponent(&diagnostic, clean, info); err != nil {
		return refuse(err)
	}
	if err := personalScriptOwnerError(clean, info, daemonUID); err != nil {
		return refuse(err)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return refuse(fmt.Errorf("%s is writable by group or others (mode %04o)", clean, info.Mode().Perm()))
	}

	var foreign []personalScriptForeignAncestor
	for dir := filepath.Dir(clean); ; dir = filepath.Dir(dir) {
		dirInfo, err := personalScriptStat(dir)
		if err != nil {
			return refuse(fmt.Errorf("stat %s: %w", dir, err))
		}
		if err := appendPersonalScriptComponent(&diagnostic, dir, dirInfo); err != nil {
			return refuse(err)
		}
		uid, err := personalScriptOwnerUID(dir, dirInfo)
		if err != nil {
			return refuse(err)
		}
		if uid != 0 && int(uid) != daemonUID {
			foreign = append(foreign, personalScriptForeignAncestor{Path: dir, UID: int(uid)})
		}
		if dirInfo.Mode().Perm()&0o022 != 0 && dirInfo.Mode()&os.ModeSticky == 0 {
			return refuse(fmt.Errorf("directory %s is writable by group or others without the sticky bit (mode %04o)", dir, dirInfo.Mode().Perm()))
		}
		if dir == "/" {
			diagnostic.Path = clean
			if len(foreign) > 0 {
				// The mitigation the accepted ancestor rests on is reported with it,
				// never separately: an operator reading "owner can replace
				// descendants" needs to know in the same breath whether anything is
				// stopping them. It rides its own field rather than the Reason text
				// because it is a live kernel reading, not a fact about the path -
				// see HardlinkAdvisory.
				diagnostic.State = personalScriptReadyWithWarning
				diagnostic.Reason = personalScriptForeignAncestorReason(foreign, daemonUID)
				diagnostic.HardlinkAdvisory, diagnostic.HardlinkProtectionInForce = personalScriptHardlinkAdvisory()
			} else {
				diagnostic.State = personalScriptReady
			}
			return diagnostic
		}
	}
}

func appendPersonalScriptComponent(diagnostic *personalScriptDiagnostic, path string, info os.FileInfo) error {
	uid, err := personalScriptOwnerUID(path, info)
	if err != nil {
		return err
	}
	diagnostic.Components = append(diagnostic.Components, personalScriptPathComponent{
		Path: path,
		UID:  uid,
		Mode: info.Mode(),
	})
	return nil
}

func personalScriptOwnerUID(path string, info os.FileInfo) (uint32, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("owner of %s cannot be determined", path)
	}
	return st.Uid, nil
}

func personalScriptOwnerError(path string, info os.FileInfo, daemonUID int) error {
	uid, err := personalScriptOwnerUID(path, info)
	if err != nil {
		return err
	}
	if uid != 0 && int(uid) != daemonUID {
		return fmt.Errorf("%s is owned by uid %d; accepted owners are root or daemon uid %d. Keep the user home ownership unchanged and move the script to a root-owned path such as /usr/local/bin", path, uid, daemonUID)
	}
	return nil
}

// personalScriptForeignAncestor is one directory on the path that belongs to neither
// root nor the daemon, so its owner can replace what sits below it.
type personalScriptForeignAncestor struct {
	Path string
	UID  int
}

// personalScriptForeignAncestorReason states the trust decision once per owner rather
// than once per directory. A script under /home/<user>/<dir> has two foreign ancestors
// with the same uid, and repeating the same clause twice made the line longer without
// telling the operator anything the first clause had not.
//
// The walk collects deepest-first; the sentence reads shallowest-first, the order the
// path itself is written in.
func personalScriptForeignAncestorReason(foreign []personalScriptForeignAncestor, daemonUID int) string {
	order := make([]int, 0, len(foreign))
	paths := make(map[int][]string, len(foreign))
	for i := len(foreign) - 1; i >= 0; i-- {
		entry := foreign[i]
		if _, seen := paths[entry.UID]; !seen {
			order = append(order, entry.UID)
		}
		paths[entry.UID] = append(paths[entry.UID], entry.Path)
	}
	clauses := make([]string, 0, len(order))
	for _, uid := range order {
		clauses = append(clauses, fmt.Sprintf("%s: UID %d-owned; owner can replace descendants run as UID %d",
			strings.Join(paths[uid], ", "), uid, daemonUID))
	}
	return strings.Join(clauses, "; ")
}
