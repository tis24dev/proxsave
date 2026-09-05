package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestInspectPersonalScriptRetainsNormalizedConfiguredPath(t *testing.T) {
	dir := t.TempDir()
	script := writePersonalScript(t, dir, "script.sh", "exit 0")
	link := filepath.Join(dir, "link.sh")
	if err := os.Symlink(script, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		path      string
		mode      os.FileMode
		wantPath  string
		wantState personalScriptState
	}{
		{"empty", " \t ", 0o700, "", personalScriptNotConfigured},
		{"ready", " \t" + dir + "/./script.sh  ", 0o700, script, personalScriptReady},
		{"missing", " " + dir + "/./missing.sh ", 0o700, filepath.Join(dir, "missing.sh"), personalScriptRefused},
		{"symlink", " " + dir + "/./link.sh ", 0o700, link, personalScriptRefused},
		{"directory", " " + dir + "/. ", 0o700, dir, personalScriptRefused},
		{"not executable", " " + dir + "/./script.sh ", 0o600, script, personalScriptRefused},
		{"group writable", " " + dir + "/./script.sh ", 0o720, script, personalScriptRefused},
		{"world writable", " " + dir + "/./script.sh ", 0o702, script, personalScriptRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.Chmod(script, tc.mode); err != nil {
				t.Fatal(err)
			}
			got := inspectPersonalScript("PERSONAL_SCRIPT_PRE_RUN", tc.path, os.Geteuid())
			stateMatches := got.State == tc.wantState || (tc.wantState == personalScriptReady && got.State == personalScriptReadyWithWarning)
			if !stateMatches || got.Path != tc.wantPath {
				t.Errorf("inspection = %+v, want state %q and path %q", got, tc.wantState, tc.wantPath)
			}
			if got.State == personalScriptRefused {
				if got.Reason == "" {
					t.Error("refusal lost its reason")
				}
				if enabled := applyPersonalScriptDiagnostic(got); enabled != "" {
					t.Errorf("refused path survived execution gate: %q", enabled)
				}
			}
		})
	}
}

type personalScriptInspectionFileInfo struct {
	name string
	mode os.FileMode
	uid  uint32
	dir  bool
}

func (f personalScriptInspectionFileInfo) Name() string      { return f.name }
func (personalScriptInspectionFileInfo) Size() int64         { return 0 }
func (f personalScriptInspectionFileInfo) Mode() os.FileMode { return f.mode }
func (personalScriptInspectionFileInfo) ModTime() time.Time  { return time.Time{} }
func (f personalScriptInspectionFileInfo) IsDir() bool       { return f.dir }
func (f personalScriptInspectionFileInfo) Sys() any          { return &syscall.Stat_t{Uid: f.uid} }

func installPersonalScriptInspectionOps(
	t *testing.T,
	eval func(string) (string, error),
	stat func(string) (os.FileInfo, error),
	validate func(string) error,
) {
	t.Helper()
	origEval := personalScriptEvalSymlinks
	origStat := personalScriptStat
	origValidate := personalScriptValidateExecutable
	t.Cleanup(func() {
		personalScriptEvalSymlinks = origEval
		personalScriptStat = origStat
		personalScriptValidateExecutable = origValidate
	})
	personalScriptEvalSymlinks = eval
	personalScriptStat = stat
	personalScriptValidateExecutable = validate
}

func TestInspectPersonalScriptStatesAndReasons(t *testing.T) {
	const daemonUID = 1001
	trustedFile := personalScriptInspectionFileInfo{name: "script.sh", mode: 0o700, uid: daemonUID}
	trustedDir := personalScriptInspectionFileInfo{name: "scripts", mode: os.ModeDir | 0o700, uid: daemonUID, dir: true}
	rootDir := personalScriptInspectionFileInfo{name: "/", mode: os.ModeDir | 0o755, uid: 0, dir: true}

	tests := []struct {
		name       string
		path       string
		eval       func(string) (string, error)
		stat       func(string) (os.FileInfo, error)
		validate   func(string) error
		wantState  personalScriptState
		wantReason string
		wantParts  int
	}{
		{
			name:      "not configured",
			path:      "   ",
			wantState: personalScriptNotConfigured,
		},
		{
			name: "ready",
			path: "/srv/scripts/script.sh",
			eval: func(path string) (string, error) { return path, nil },
			stat: func(path string) (os.FileInfo, error) {
				switch path {
				case "/srv/scripts/script.sh":
					return trustedFile, nil
				case "/srv/scripts", "/srv":
					return trustedDir, nil
				case "/":
					return rootDir, nil
				default:
					return nil, fmt.Errorf("unexpected stat %s", path)
				}
			},
			validate:  func(string) error { return nil },
			wantState: personalScriptReady,
			wantParts: 4,
		},
		{
			name:       "missing",
			path:       "/missing/script.sh",
			eval:       func(string) (string, error) { return "", os.ErrNotExist },
			wantState:  personalScriptRefused,
			wantReason: "resolve /missing/script.sh",
		},
		{
			name:       "relative",
			path:       "script.sh",
			eval:       func(path string) (string, error) { return path, nil },
			validate:   func(string) error { return errors.New("executable path must be absolute: script.sh") },
			wantState:  personalScriptRefused,
			wantReason: "must be absolute",
		},
		{
			name:       "symlinked",
			path:       "/srv/scripts/link.sh",
			eval:       func(string) (string, error) { return "/srv/scripts/script.sh", nil },
			wantState:  personalScriptRefused,
			wantReason: "traverses a symlink",
		},
		{
			name:       "directory",
			path:       "/srv/scripts",
			eval:       func(path string) (string, error) { return path, nil },
			validate:   func(string) error { return errors.New("executable path is not a regular file: /srv/scripts") },
			wantState:  personalScriptRefused,
			wantReason: "not a regular file",
		},
		{
			name:       "not executable",
			path:       "/srv/scripts/script.sh",
			eval:       func(path string) (string, error) { return path, nil },
			validate:   func(string) error { return errors.New("executable path is not executable: /srv/scripts/script.sh") },
			wantState:  personalScriptRefused,
			wantReason: "not executable",
		},
		{
			name:       "world writable",
			path:       "/srv/scripts/script.sh",
			eval:       func(path string) (string, error) { return path, nil },
			validate:   func(string) error { return errors.New("executable path is world-writable: /srv/scripts/script.sh") },
			wantState:  personalScriptRefused,
			wantReason: "world-writable",
		},
		{
			name: "group writable",
			path: "/srv/scripts/script.sh",
			eval: func(path string) (string, error) { return path, nil },
			stat: func(string) (os.FileInfo, error) {
				return personalScriptInspectionFileInfo{name: "script.sh", mode: 0o720, uid: daemonUID}, nil
			},
			validate:   func(string) error { return nil },
			wantState:  personalScriptRefused,
			wantReason: "writable by group or others",
			wantParts:  1,
		},
		{
			name: "foreign owned target stays refused",
			path: "/srv/scripts/script.sh",
			eval: func(path string) (string, error) { return path, nil },
			stat: func(string) (os.FileInfo, error) {
				return personalScriptInspectionFileInfo{name: "script.sh", mode: 0o700, uid: 4242}, nil
			},
			validate:   func(string) error { return nil },
			wantState:  personalScriptRefused,
			wantReason: "owned by uid 4242",
			wantParts:  1,
		},
		{
			name: "foreign owned parent is advisory",
			path: "/home/operator/script.sh",
			eval: func(path string) (string, error) { return path, nil },
			stat: func(path string) (os.FileInfo, error) {
				switch path {
				case "/home/operator":
					return personalScriptInspectionFileInfo{name: "operator", mode: os.ModeDir | 0o700, uid: 4242, dir: true}, nil
				case "/":
					return rootDir, nil
				default:
					return trustedFile, nil
				}
			},
			validate:   func(string) error { return nil },
			wantState:  personalScriptReadyWithWarning,
			wantReason: "/home/operator is owned by uid 4242",
			wantParts:  4,
		},
		{
			name: "writable non sticky parent",
			path: "/srv/scripts/script.sh",
			eval: func(path string) (string, error) { return path, nil },
			stat: func(path string) (os.FileInfo, error) {
				if path == "/srv/scripts" {
					return personalScriptInspectionFileInfo{name: "scripts", mode: os.ModeDir | 0o777, uid: daemonUID, dir: true}, nil
				}
				return trustedFile, nil
			},
			validate:   func(string) error { return nil },
			wantState:  personalScriptRefused,
			wantReason: "without the sticky bit",
			wantParts:  2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eval := tc.eval
			if eval == nil {
				eval = func(path string) (string, error) { return path, nil }
			}
			stat := tc.stat
			if stat == nil {
				stat = func(string) (os.FileInfo, error) { return trustedFile, nil }
			}
			validate := tc.validate
			if validate == nil {
				validate = func(string) error { return nil }
			}
			installPersonalScriptInspectionOps(t, eval, stat, validate)

			got := inspectPersonalScript("PERSONAL_SCRIPT_PRE_RUN", tc.path, daemonUID)
			if got.State != tc.wantState {
				t.Fatalf("state = %q, want %q; diagnostic=%+v", got.State, tc.wantState, got)
			}
			if tc.wantReason != "" && !strings.Contains(got.Reason, tc.wantReason) {
				t.Errorf("reason %q does not contain %q", got.Reason, tc.wantReason)
			}
			if len(got.Components) != tc.wantParts {
				t.Errorf("components = %d, want %d: %+v", len(got.Components), tc.wantParts, got.Components)
			}
			if got.Key != "PERSONAL_SCRIPT_PRE_RUN" || got.DaemonUID != daemonUID {
				t.Errorf("identity fields missing: %+v", got)
			}
		})
	}
}

func TestInspectPersonalScriptsIsPureAndDoesNotExecute(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "executed")
	script := writePersonalScript(t, dir, "pure.sh", "touch "+marker)
	cfg := &config.Config{PersonalScriptPreRun: script, PersonalScriptPostRun: ""}

	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	previous := logging.GetDefaultLogger()
	logging.SetDefaultLogger(logger)
	t.Cleanup(func() { logging.SetDefaultLogger(previous) })

	beforePre, beforePost := cfg.PersonalScriptPreRun, cfg.PersonalScriptPostRun
	got := inspectPersonalScripts(cfg, os.Geteuid())
	if got.Pre.State != personalScriptReady || got.Post.State != personalScriptNotConfigured {
		t.Fatalf("unexpected diagnostics: %+v", got)
	}
	if cfg.PersonalScriptPreRun != beforePre || cfg.PersonalScriptPostRun != beforePost {
		t.Fatalf("inspection mutated config: before=(%q,%q) after=(%q,%q)", beforePre, beforePost, cfg.PersonalScriptPreRun, cfg.PersonalScriptPostRun)
	}
	if buf.Len() != 0 {
		t.Fatalf("inspection logged output: %s", buf.String())
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection executed the script; marker stat error = %v", err)
	}
}

func TestPersonalScriptValidationEmitsOneWarningPerRefusedSetting(t *testing.T) {
	dir := t.TempDir()
	script := writePersonalScript(t, dir, "loose.sh", "exit 0")
	if err := os.Chmod(script, 0o777); err != nil {
		t.Fatal(err)
	}

	pre, post, logged := capturePersonalScriptValidation(t, script, script)
	if pre != "" || post != "" {
		t.Fatalf("refused settings survived: pre=%q post=%q", pre, post)
	}
	for _, key := range []string{"PERSONAL_SCRIPT_PRE_RUN", "PERSONAL_SCRIPT_POST_RUN"} {
		if got := strings.Count(logged, key+" disabled for this daemon:"); got != 1 {
			t.Errorf("%s warning count = %d, want 1\n%s", key, got, logged)
		}
	}
}

func TestApplyPersonalScriptDiagnosticKeepsAdvisoryPathAndWarnsOnce(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	previous := logging.GetDefaultLogger()
	logging.SetDefaultLogger(logger)
	t.Cleanup(func() { logging.SetDefaultLogger(previous) })

	diagnostic := personalScriptDiagnostic{
		Key:    "PERSONAL_SCRIPT_PRE_RUN",
		Path:   "/home/operator/script.sh",
		State:  personalScriptReadyWithWarning,
		Reason: "/home/operator is owned by uid 4242; that owner can replace descendants executed as daemon uid 0",
	}
	if got := applyPersonalScriptDiagnostic(diagnostic); got != diagnostic.Path {
		t.Fatalf("advisory path = %q, want %q", got, diagnostic.Path)
	}
	if got := strings.Count(buf.String(), "PERSONAL_SCRIPT_PRE_RUN enabled with administrator trust warning:"); got != 1 {
		t.Fatalf("warning count = %d, want 1\n%s", got, buf.String())
	}
}

// The accepted foreign-owned ancestor rests on one kernel setting: its owner can
// unlink the script and put another file in its place, and what stops them is the
// execution-time gate refusing anything not owned by root - which only holds while
// fs.protected_hardlinks forbids linking a root-owned executable they neither own
// nor can write. docs/SECURITY.md used to describe the trust decision without ever
// naming that dependency; the advisory now carries it.
func TestForeignOwnedAncestorAdvisoryNamesTheHardlinkProtection(t *testing.T) {
	tests := []struct {
		name    string
		value   int
		probeer error
		want    string
	}{
		{"enforced", 1, nil, "cannot hard-link a root-owned executable"},
		{"disabled", 0, nil, "CAN hard-link a root-owned executable"},
		{"unreadable", 0, errors.New("permission denied"), "could not be read"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := personalScriptHardlinkProtection
			t.Cleanup(func() { personalScriptHardlinkProtection = orig })
			personalScriptHardlinkProtection = func() (int, error) { return tc.value, tc.probeer }

			got := personalScriptHardlinkAdvisory()
			if !strings.Contains(got, tc.want) {
				t.Fatalf("advisory = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// The clause rides along with the ownership advisory, in the same reason string,
// and never appears on a path that raised no advisory at all.
func TestHardlinkClauseRidesWithTheOwnershipAdvisoryOnly(t *testing.T) {
	orig := personalScriptHardlinkProtection
	t.Cleanup(func() { personalScriptHardlinkProtection = orig })
	personalScriptHardlinkProtection = func() (int, error) { return 0, nil }

	origStat := personalScriptStat
	origEval := personalScriptEvalSymlinks
	origValidate := personalScriptValidateExecutable
	t.Cleanup(func() {
		personalScriptStat = origStat
		personalScriptEvalSymlinks = origEval
		personalScriptValidateExecutable = origValidate
	})
	personalScriptEvalSymlinks = func(p string) (string, error) { return p, nil }
	personalScriptValidateExecutable = func(string) error { return nil }

	// uid 1000 on the home, root everywhere else: the accepted advisory shape.
	foreign := map[string]uint32{"/home/operator": 1000}
	personalScriptStat = func(p string) (os.FileInfo, error) {
		mode := os.FileMode(0o755)
		if p == "/home/operator/pre.sh" {
			mode = 0o700
		} else {
			mode |= os.ModeDir
		}
		return personalScriptInspectionFileInfo{name: filepath.Base(p), mode: mode, uid: foreign[p], dir: mode.IsDir()}, nil
	}

	got := inspectPersonalScript("PERSONAL_SCRIPT_PRE_RUN", "/home/operator/pre.sh", 0)
	if got.State != personalScriptReadyWithWarning {
		t.Fatalf("state = %q, want ready-with-warning: %+v", got.State, got)
	}
	if !strings.Contains(got.Reason, "owned by uid 1000") || !strings.Contains(got.Reason, "fs.protected_hardlinks is 0") {
		t.Fatalf("the reason does not carry both the advisory and its mitigation: %q", got.Reason)
	}

	foreign = map[string]uint32{}
	clean := inspectPersonalScript("PERSONAL_SCRIPT_PRE_RUN", "/home/operator/pre.sh", 0)
	if clean.State != personalScriptReady {
		t.Fatalf("state = %q, want ready: %+v", clean.State, clean)
	}
	if strings.Contains(clean.Reason, "protected_hardlinks") {
		t.Fatalf("a path with no advisory got the mitigation clause anyway: %q", clean.Reason)
	}
}
