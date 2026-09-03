package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/safeexec"
)

type personalScriptState string

const (
	personalScriptNotConfigured personalScriptState = "not-configured"
	personalScriptReady         personalScriptState = "ready"
	personalScriptRefused       personalScriptState = "refused"
)

type personalScriptPathComponent struct {
	Path string
	UID  uint32
	Mode os.FileMode
}

type personalScriptDiagnostic struct {
	Key        string
	Path       string
	State      personalScriptState
	Reason     string
	DaemonUID  int
	Components []personalScriptPathComponent
}

type personalScriptsDiagnostics struct {
	Pre  personalScriptDiagnostic
	Post personalScriptDiagnostic
}

var (
	personalScriptEvalSymlinks       = filepath.EvalSymlinks
	personalScriptStat               = os.Stat
	personalScriptValidateExecutable = safeexec.ValidateTrustedExecutablePath
)

// inspectPersonalScripts returns both configured-script verdicts without
// logging, mutating the configuration, or executing either script.
func inspectPersonalScripts(cfg *config.Config, daemonUID int) personalScriptsDiagnostics {
	pre, post := "", ""
	if cfg != nil {
		pre = cfg.PersonalScriptPreRun
		post = cfg.PersonalScriptPostRun
	}
	return personalScriptsDiagnostics{
		Pre:  inspectPersonalScript("PERSONAL_SCRIPT_PRE_RUN", pre, daemonUID),
		Post: inspectPersonalScript("PERSONAL_SCRIPT_POST_RUN", post, daemonUID),
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

	for dir := filepath.Dir(clean); ; dir = filepath.Dir(dir) {
		dirInfo, err := personalScriptStat(dir)
		if err != nil {
			return refuse(fmt.Errorf("stat %s: %w", dir, err))
		}
		if err := appendPersonalScriptComponent(&diagnostic, dir, dirInfo); err != nil {
			return refuse(err)
		}
		if err := personalScriptOwnerError(dir, dirInfo, daemonUID); err != nil {
			return refuse(err)
		}
		if dirInfo.Mode().Perm()&0o022 != 0 && dirInfo.Mode()&os.ModeSticky == 0 {
			return refuse(fmt.Errorf("directory %s is writable by group or others without the sticky bit (mode %04o)", dir, dirInfo.Mode().Perm()))
		}
		if dir == "/" {
			diagnostic.State = personalScriptReady
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
