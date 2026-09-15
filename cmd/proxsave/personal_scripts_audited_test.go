package main

import (
	"bytes"
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// writePersonalScript drops an executable script in dir and returns its absolute path. 0o700, not
// 0o755: a world-writable or group-writable fixture is what safeexec's trusted-path check
// refuses, and a fixture that only passes because nothing checks stops meaning anything the
// day something does.
func writePersonalScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// TestPersonalScriptCmdLeavesEveryDescriptorNil pins the silence and the hang immunity in the
// one place they are both decided. io.Discard is NOT an equivalent tidy-up of a nil writer:
// nil is wired to os.DevNull with no copy goroutine at all, while io.Discard makes os/exec
// build a pipe plus a goroutine, and Wait then blocks until that pipe reaches EOF, which a
// backgrounded grandchild holding the descriptor withholds for its own whole lifetime.
func TestPersonalScriptCmdLeavesEveryDescriptorNil(t *testing.T) {
	// Under t.TempDir, the way the sibling test below already does it: a fixed
	// /usr/local/bin name is a name the host may own. On the PVE test node that
	// directory holds a dozen operator-installed entries, and one of them landing on
	// this name, root-owned and 0755, would pass the gate and fail the assertion below
	// for a reason that has nothing to do with what this test pins.
	cmd, refusal := personalScriptCmd(context.Background(), filepath.Join(t.TempDir(), "missing.sh"))
	if refusal == nil {
		t.Fatal("a path that does not exist must be refused by the per-run gate")
	}

	if cmd.Stdout != nil || cmd.Stderr != nil || cmd.Stdin != nil {
		t.Fatalf("stdin/stdout/stderr must all stay nil (os.DevNull, no copy goroutine); got stdin=%v stdout=%v stderr=%v", cmd.Stdin, cmd.Stdout, cmd.Stderr)
	}
	if cmd.Env == nil {
		t.Fatal("Env must be the daemon's environment minus LOG_FILE and BASE_DIR, not nil: nil inherits both")
	}
	for _, kv := range cmd.Env {
		if key, _, _ := strings.Cut(kv, "="); key == "LOG_FILE" || key == "BASE_DIR" {
			t.Errorf("the script is handed %s: those two are the only way it could learn about the run", key)
		}
	}
	if cmd.Dir != "" {
		t.Errorf("Dir must stay empty; got %q", cmd.Dir)
	}
	if len(cmd.Args) != 1 {
		t.Errorf("the script gets no arguments; got %v", cmd.Args)
	}
	if cmd.WaitDelay != 0 {
		t.Errorf("WaitDelay must stay 0: there are no pipes to drain; got %s", cmd.WaitDelay)
	}
}

func TestPersonalScriptCommandsExecuteTheOpenedFileAfterPathReplacement(t *testing.T) {
	builders := map[string]func(string) (*exec.Cmd, error){
		"waited": func(path string) (*exec.Cmd, error) {
			return personalScriptCmd(context.Background(), path)
		},
		"detached": personalScriptCmdDetached,
	}

	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			originalMarker := filepath.Join(dir, "original-ran")
			replacementMarker := filepath.Join(dir, "replacement-ran")
			path := writePersonalScript(t, dir, "configured.sh", "touch "+originalMarker)
			replacement := writePersonalScript(t, dir, "replacement.sh", "touch "+replacementMarker)

			cmd, refusal := build(path)
			if refusal != nil {
				t.Fatalf("the per-run gate refused a script it must accept: %v", refusal)
			}
			for _, file := range cmd.ExtraFiles {
				file := file
				t.Cleanup(func() { _ = file.Close() })
			}
			if err := os.Rename(replacement, path); err != nil {
				t.Fatalf("replace configured path: %v", err)
			}
			if err := cmd.Run(); err != nil {
				t.Fatalf("run pinned command: %v", err)
			}

			if _, err := os.Stat(originalMarker); err != nil {
				t.Fatalf("opened original did not run: %v", err)
			}
			if _, err := os.Stat(replacementMarker); !os.IsNotExist(err) {
				t.Fatalf("pathname replacement ran with daemon privileges: %v", err)
			}
		})
	}
}

func TestPersonalScriptCommandFailsClosedWithInheritedFD3(t *testing.T) {
	const probeEnv = "PROXSAVE_TEST_PERSONAL_SCRIPT_INHERITED_FD3"
	if os.Getenv(probeEnv) == "1" {
		cmd, _ := personalScriptCmd(context.Background(), filepath.Join(t.TempDir(), "missing.sh"))
		if err := startPersonalScriptCmd(cmd); err == nil {
			_ = cmd.Wait()
		}
		return
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, "inherited-fd-ran")
	malicious := writePersonalScript(t, dir, "inherited.sh", "touch "+marker)
	file, err := os.Open(malicious)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	probe := exec.Command(os.Args[0], "-test.run=^TestPersonalScriptCommandFailsClosedWithInheritedFD3$")
	probe.Env = append(os.Environ(), probeEnv+"=1")
	probe.ExtraFiles = []*os.File{file}
	if output, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("run inherited-fd probe: %v\n%s", err, output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unvalidated inherited descriptor executed: %v", err)
	}
}

// TestPersonalScriptBudgetsAreTheShippedOnes pins the two frozen numbers themselves. Every
// other timing test in this file shrinks personalScriptTimeout before it asserts anything, so
// without this one the shipped budget is unpinned: raising it to 90 minutes passes the whole
// package.
func TestPersonalScriptBudgetsAreTheShippedOnes(t *testing.T) {
	if personalScriptTimeout != 10*time.Minute {
		t.Errorf("personalScriptTimeout = %s, want the frozen 10m", personalScriptTimeout)
	}
	if personalScriptReapSlack != 15*time.Second {
		t.Errorf("personalScriptReapSlack = %s, want 15s (daemonReapSlack's own margin)", personalScriptReapSlack)
	}
	if personalScriptOpenTimeout != 5*time.Second {
		t.Errorf("personalScriptOpenTimeout = %s, want 5s (cronProbeTimeout's value and reasoning)", personalScriptOpenTimeout)
	}
}

// TestPersonalScriptSurvivesAGrandchildHoldingItsOutput is the behavioural half of the test
// above: it fails the moment somebody sets a writer on either stream.
func TestPersonalScriptSurvivesAGrandchildHoldingItsOutput(t *testing.T) {
	dir := t.TempDir()
	script := writePersonalScript(t, dir, "grandchild.sh", "sleep 5 &\necho out\necho err 1>&2\nexit 0")

	done := make(chan struct{})
	go func() { defer close(done); runPersonalScript(script, nil) }()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runPersonalScript blocked on a backgrounded grandchild: a writer was set on stdout or stderr")
	}
}

// TestPersonalScriptIsKilledAtItsTimeout pins that the kill is a SIGKILL no trap can catch,
// and that the reap slack is not spent on a script the kernel can actually kill.
func TestPersonalScriptIsKilledAtItsTimeout(t *testing.T) {
	origTimeout := personalScriptTimeout
	t.Cleanup(func() { personalScriptTimeout = origTimeout })
	personalScriptTimeout = 300 * time.Millisecond

	dir := t.TempDir()
	script := writePersonalScript(t, dir, "sigterm-proof.sh", `trap "" TERM`+"\nsleep 5")

	start := time.Now()
	runPersonalScript(script, nil)
	elapsed := time.Since(start)

	if elapsed >= personalScriptReapSlack {
		t.Fatalf("the script outlived its timeout by the whole reap slack (%s): the cancellation is not a SIGKILL", elapsed)
	}
	if elapsed < personalScriptTimeout {
		t.Fatalf("returned in %s, before the %s timeout could fire", elapsed, personalScriptTimeout)
	}
}

// TestPersonalScriptDropsEveryUnusablePath walks every way a configured path can be useless.
// Each row must return quickly and do nothing at all: no panic, no log, no error surfaced.
func TestPersonalScriptDropsEveryUnusablePath(t *testing.T) {
	dir := t.TempDir()

	notExecutable := filepath.Join(dir, "not-executable.sh")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	noShebang := filepath.Join(dir, "no-shebang")
	if err := os.WriteFile(noShebang, []byte("this is not a program\n"), 0o700); err != nil {
		t.Fatalf("write: %v", err)
	}
	worldWritable := filepath.Join(dir, "world-writable.sh")
	if err := os.WriteFile(worldWritable, []byte("#!/bin/sh\nexit 0\n"), 0o777); err != nil {
		t.Fatalf("write: %v", err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"missing path", filepath.Join(dir, "nope.sh")},
		{"a directory", dir},
		{"no execute bit", notExecutable},
		{"no shebang", noShebang},
		{"bare name not on PATH", "proxsave-no-such-command-anywhere"},
		{"exits non-zero", writePersonalScript(t, dir, "exit3.sh", "exit 3")},
		// The world-writable row pins the execution-time backstop: the starter stays
		// silent, but refuses an unsafe opened inode if the path changed after the LOUD
		// startup gate accepted it.
		{"world writable but runnable", worldWritable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan struct{})
			go func() { defer close(done); runPersonalScript(tc.path, nil) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("runPersonalScript(%q) did not return", tc.path)
			}
		})
	}
}

// TestOnlyTheDaemonStartsThePersonalScripts is the guard for the daemon-only rule. A
// behavioural test of the negative would have to run the real binary for a manual backup and
// then prove a negative from its absence; this instead pins the call sites, which is what a
// future wiring mistake would change. The scan is textual on purpose: a mention in a comment
// in a third file is also worth stopping at.
//
// There are two rings, because the starters gained a reporting wrapper. The MUTE starters may be
// named only by the file that defines them and by the one file allowed to give them a voice; the
// REPORTING wrappers may be named only by that file and by the daemon. What neither ring permits
// is a third caller anywhere, which is the whole rule: a manual backup, a cron-mode run and the
// dashboard must reach none of them.
//
// The match is word-bounded so runPersonalScript does not silently match
// runPersonalScriptReporting. Without it the wrapper would satisfy the mute starter's own
// daemon-must-still-call-it half, and deleting the real call site would pass.
func TestOnlyTheDaemonStartsThePersonalScripts(t *testing.T) {
	const (
		defining  = "cmd/proxsave/personal_scripts.go"
		reporting = "cmd/proxsave/personal_scripts_gate.go"
		daemonGo  = "cmd/proxsave/daemon.go"
	)
	rings := []struct {
		names         []string
		allowed       []string
		mustBeNamedBy string
	}{
		{
			names:         []string{"runPersonalScript", "startPersonalScriptDetached"},
			allowed:       []string{defining, reporting},
			mustBeNamedBy: reporting,
		},
		{
			names:         []string{"runPersonalScriptReporting", "startPersonalScriptDetachedReporting"},
			allowed:       []string{reporting, daemonGo},
			mustBeNamedBy: daemonGo,
		},
	}
	root := filepath.Join("..", "..")

	for _, ring := range rings {
		allowed := map[string]bool{}
		for _, file := range ring.allowed {
			allowed[filepath.FromSlash(file)] = true
		}
		required := filepath.FromSlash(ring.mustBeNamedBy)
		for _, name := range ring.names {
			mention := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
			var seenInRequired bool
			err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				// Skip dot directories and anything holding its own .git entry. Without this the
				// walk descends into the agent worktrees this repo keeps under .claude/, finds the
				// same two files in a nested checkout, and fails on a tree where nothing is wrong.
				if d.IsDir() {
					if path != root && strings.HasPrefix(d.Name(), ".") {
						return fs.SkipDir
					}
					if path != root {
						if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr == nil {
							return fs.SkipDir
						}
					}
					return nil
				}
				if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
					return nil
				}
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				if !mention.Match(data) {
					return nil
				}
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				if rel == required {
					seenInRequired = true
				}
				if !allowed[rel] {
					t.Errorf("%s names %s: these scripts run for the daemon's own scheduled run and nothing else", rel, name)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("walk: %v", err)
			}
			// The other half of the rule, and the half a deleted call site would break: the
			// chain must still be wired. Without this the whole test passes on a tree where the
			// feature was removed.
			if !seenInRequired {
				t.Errorf("%s no longer names %s: the daemon is the only thing that starts these scripts", ring.mustBeNamedBy, name)
			}
		}
	}
}

// TestStartedScriptsGetNoShellNoArgumentsAndAnUnchangedEnvironment asserts the execution shape
// through the STARTERS, not through personalScriptCmd. Every command-shape assertion above
// inspects the builder's return value, so a mutation one line later, inside runPersonalScript
// or inside startPersonalScriptDetached, is invisible to it.
func TestStartedScriptsGetNoShellNoArgumentsAndAnUnchangedEnvironment(t *testing.T) {
	dumpScript := func(t *testing.T, dir, name, dump string) string {
		t.Helper()
		return writePersonalScript(t, dir, name, "env > "+dump+".env\nprintf '%s\\n' \"$#\" > "+dump+".argc")
	}
	// os/exec and the shell both touch these; nothing else may differ.
	tolerated := map[string]bool{"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true}
	// The two the daemon deliberately withholds.
	stripped := map[string]bool{"LOG_FILE": true, "BASE_DIR": true}

	assertShape := func(t *testing.T, dump string) {
		t.Helper()
		argc, err := os.ReadFile(dump + ".argc")
		if err != nil {
			t.Fatalf("the script did not run: %v", err)
		}
		if strings.TrimSpace(string(argc)) != "0" {
			t.Errorf("the script was passed %s arguments, want none", strings.TrimSpace(string(argc)))
		}
		raw, err := os.ReadFile(dump + ".env")
		if err != nil {
			t.Fatalf("read env dump: %v", err)
		}
		parent := map[string]bool{}
		for _, kv := range os.Environ() {
			parent[kv] = true
		}
		seen := map[string]bool{}
		for _, kv := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
			if kv == "" {
				continue
			}
			key, _, _ := strings.Cut(kv, "=")
			seen[key] = true
			if stripped[key] {
				t.Errorf("the script was handed %s: the daemon withholds it so a script cannot learn about the run, or write into its log", key)
				continue
			}
			if parent[kv] || tolerated[key] {
				continue
			}
			t.Errorf("the script was handed %q, which the daemon's own environment does not carry: nothing is injected", kv)
		}
		// A negative that only means something when the parent really had them.
		for key := range stripped {
			if os.Getenv(key) == "" {
				t.Fatalf("%s was not set in the parent, so this test proves nothing", key)
			}
			if seen[key] {
				t.Errorf("%s survived into the script", key)
			}
		}
	}

	t.Setenv("LOG_FILE", "/var/log/proxsave/backup-probe.log")
	t.Setenv("BASE_DIR", "/opt/proxsave")

	t.Run("the waited starter", func(t *testing.T) {
		dir := t.TempDir()
		dump := filepath.Join(dir, "dump")
		runPersonalScript(dumpScript(t, dir, "dump.sh", dump), nil)
		assertShape(t, dump)
	})

	t.Run("the detached starter", func(t *testing.T) {
		dir := t.TempDir()
		dump := filepath.Join(dir, "dump")
		startPersonalScriptDetached(dumpScript(t, dir, "dump.sh", dump))
		// Wait on .argc, not .env. The script writes ".env" first and ".argc" second,
		// so waiting on the first one let the assertions run in the gap between the
		// two redirections and read a .argc that did not exist yet. Waiting on its
		// CONTENT rather than its existence also covers the gap between the shell
		// creating the file and printf filling it.
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if b, err := os.ReadFile(dump + ".argc"); err == nil && strings.TrimSpace(string(b)) != "" {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		assertShape(t, dump)
	})
}

// TestNoShellIsInvolved is the behavioural half of the direct FD-backed exec. The
// argument-count assertions above cannot see a shell wrapper applied inside the starter.
func TestNoShellIsInvolved(t *testing.T) {
	t.Run("a file with no shebang is not interpreted", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "marker")
		script := filepath.Join(dir, "no-shebang")
		if err := os.WriteFile(script, []byte("touch "+marker+"\n"), 0o700); err != nil {
			t.Fatalf("write: %v", err)
		}

		runPersonalScript(script, nil)

		if _, err := os.Stat(marker); err == nil {
			t.Fatal("the file was interpreted by a shell: execve must fail on a script with no shebang")
		}
	})

	t.Run("the value is never a command line", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "marker")

		runPersonalScript("/bin/true; touch "+marker, nil)

		if _, err := os.Stat(marker); err == nil {
			t.Fatal("the value was parsed as a command line: no shell may be involved")
		}
	})
}

// TestAbandonedWaitGoroutineDoesNotLeak pins the buffered waitCh. When the two-phase wait gives
// up, the goroutine it walks away from must still be able to send and exit; on an unbuffered
// channel it blocks forever and every timed-out script costs the daemon a goroutine.
func TestAbandonedWaitGoroutineDoesNotLeak(t *testing.T) {
	origTimeout, origSlack := personalScriptTimeout, personalScriptReapSlack
	t.Cleanup(func() { personalScriptTimeout, personalScriptReapSlack = origTimeout, origSlack })
	personalScriptTimeout = 100 * time.Millisecond
	personalScriptReapSlack = time.Nanosecond

	dir := t.TempDir()
	script := writePersonalScript(t, dir, "slow.sh", `trap "" TERM`+"\nsleep 2")

	before := runtime.NumGoroutine()
	for i := 0; i < 10; i++ {
		runPersonalScript(script, nil)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("goroutines went %d -> %d and stayed: the abandoned waiter cannot send, so waitCh lost its buffer", before, runtime.NumGoroutine())
}

// TestPersonalScriptsFileImportsNoReportingPackage is the only test that catches a well-meant
// Debug line added a year from now. The silence rule is a property of the import list, so
// that is what is asserted; logging.Debug reaches the daemon's on-disk log file, not only
// journald, so even the quietest level is a breach.
func TestPersonalScriptsFileImportsNoReportingPackage(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "personal_scripts.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse personal_scripts.go: %v", err)
	}
	banned := []string{"internal/logging", "internal/notify", "internal/health", "internal/metrics"}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		for _, b := range banned {
			if strings.Contains(path, b) {
				t.Errorf("personal_scripts.go imports %q: these scripts report nothing, at any level, on any outcome", path)
			}
		}
	}
}

// TestAShutdownStopsTheWaitButNotTheScript pins the two halves of the stop contract at
// once. A shutdown that lands mid-script must not keep the scheduler goroutine parked
// for up to personalScriptTimeout+personalScriptReapSlack - the daemon's whole teardown
// budget is a stock TimeoutStopSec of 90 seconds, and daemon.go spells out that a wait
// held through it SIGKILLs the daemon with the pid and info files still on disk. And the
// stop must abandon only the WAIT: the script keeps its whole budget (the kill stays
// with os/exec's context watcher and the unit's cgroup), because a shutdown must not
// silently truncate what the feature grants.
func TestAShutdownStopsTheWaitButNotTheScript(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "survived")
	script := writePersonalScript(t, dir, "slow.sh", "sleep 1\ntouch "+marker)

	stop := make(chan struct{})
	time.AfterFunc(50*time.Millisecond, func() { close(stop) })

	start := time.Now()
	runPersonalScript(script, stop)
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("the wait outlived the stop signal by %s: shutdown is blocked on the script", elapsed)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the script was killed when the wait stopped: the stop abandons the wait, never the script")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The trusted-path gate, added on the release-PR review (#303). The diagnostic gate
// lives at daemon startup, where a refusal can be LOUD; the starters independently
// bind and revalidate the opened inode while staying silent. Each startup refusal
// names the variable, the path and the reason, and blanks the setting so the run
// behaves as if it were never configured.
func capturePersonalScriptValidation(t *testing.T, pre, post string) (preOut, postOut, logged string) {
	t.Helper()
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	prev := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(prev) })
	logging.SetDefaultLogger(logger)
	cfg := &config.Config{PersonalScriptPreRun: pre, PersonalScriptPostRun: post}
	validatePersonalScripts(cfg)
	return cfg.PersonalScriptPreRun, cfg.PersonalScriptPostRun, buf.String()
}

type personalScriptOwnerFixture struct {
	uid uint32
}

func (personalScriptOwnerFixture) Name() string       { return "owner-fixture" }
func (personalScriptOwnerFixture) Size() int64        { return 0 }
func (personalScriptOwnerFixture) Mode() os.FileMode  { return 0o700 }
func (personalScriptOwnerFixture) ModTime() time.Time { return time.Time{} }
func (personalScriptOwnerFixture) IsDir() bool        { return false }
func (f personalScriptOwnerFixture) Sys() any         { return &syscall.Stat_t{Uid: f.uid} }

func TestPersonalScriptOwnerErrorAllowsOnlyRootOrDaemonUIDAndGuidesRecovery(t *testing.T) {
	daemonUID := os.Geteuid()
	for name, uid := range map[string]uint32{
		"root":       0,
		"daemon uid": uint32(daemonUID),
	} {
		t.Run(name, func(t *testing.T) {
			if err := personalScriptOwnerError("/trusted/script", personalScriptOwnerFixture{uid: uid}, daemonUID); err != nil {
				t.Fatalf("trusted uid %d was refused: %v", uid, err)
			}
		})
	}

	foreignUID := uint32(4242)
	if int(foreignUID) == daemonUID {
		foreignUID++
	}
	err := personalScriptOwnerError("/home/operator", personalScriptOwnerFixture{uid: foreignUID}, daemonUID)
	if err == nil {
		t.Fatal("a path component owned by a foreign uid was accepted")
	}
	for _, want := range []string{
		"/home/operator",
		fmt.Sprintf("uid %d", foreignUID),
		fmt.Sprintf("daemon uid %d", daemonUID),
		"Keep the user home ownership unchanged",
		"/usr/local/bin",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ownership refusal is missing %q: %v", want, err)
		}
	}
}

func TestPersonalScriptValidationRefusesAWorldWritableScript(t *testing.T) {
	dir := t.TempDir()
	script := writePersonalScript(t, dir, "loose.sh", "exit 0")
	if err := os.Chmod(script, 0o777); err != nil {
		t.Fatal(err)
	}

	pre, _, logged := capturePersonalScriptValidation(t, script, "")
	if pre != "" {
		t.Fatalf("a world-writable script survived validation: %q", pre)
	}
	if !strings.Contains(logged, "PERSONAL_SCRIPT_PRE_RUN") || !strings.Contains(logged, "world-writable") {
		t.Fatalf("the refusal does not name the variable and the reason:\n%s", logged)
	}
	if !strings.Contains(logged, "WARNING") {
		t.Fatalf("the refusal is not loud:\n%s", logged)
	}
}

func TestPersonalScriptValidationRefusesAGroupWritableScript(t *testing.T) {
	dir := t.TempDir()
	script := writePersonalScript(t, dir, "group.sh", "exit 0")
	if err := os.Chmod(script, 0o775); err != nil {
		t.Fatal(err)
	}

	_, post, logged := capturePersonalScriptValidation(t, "", script)
	if post != "" {
		t.Fatalf("a group-writable script survived validation: %q", post)
	}
	if !strings.Contains(logged, "PERSONAL_SCRIPT_POST_RUN") {
		t.Fatalf("the refusal does not name the variable:\n%s", logged)
	}
}

func TestPersonalScriptValidationRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := writePersonalScript(t, dir, "real.sh", "exit 0")
	link := filepath.Join(dir, "link.sh")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	pre, _, logged := capturePersonalScriptValidation(t, link, "")
	if pre != "" {
		t.Fatalf("a symlinked script survived validation: %q", pre)
	}
	if !strings.Contains(logged, "symlink") {
		t.Fatalf("the refusal does not name the symlink:\n%s", logged)
	}
}

func TestPersonalScriptValidationKeepsATrustedPathAndStaysSilent(t *testing.T) {
	dir := t.TempDir()
	script := writePersonalScript(t, dir, "good.sh", "exit 0")
	configured := filepath.Dir(script) + string(os.PathSeparator) + "." + string(os.PathSeparator) + filepath.Base(script)

	pre, post, logged := capturePersonalScriptValidation(t, configured, configured)
	if pre != script || post != script {
		t.Fatalf("trusted paths were not normalized: pre=%q post=%q want=%q\n%s", pre, post, script, logged)
	}
	if strings.Contains(logged, "WARNING") {
		t.Fatalf("a trusted path produced a warning:\n%s", logged)
	}
}

func TestPersonalScriptValidationLeavesEmptySettingsAlone(t *testing.T) {
	pre, post, logged := capturePersonalScriptValidation(t, "", "   ")
	if pre != "" || post != "" {
		t.Fatalf("empty settings changed: pre=%q post=%q", pre, post)
	}
	if strings.Contains(logged, "WARNING") {
		t.Fatalf("empty settings produced a warning:\n%s", logged)
	}
}

// TestTheDaemonRunsTheTrustedPathGate pins the wiring the way the call-site scan above pins
// the starters: textually. Every behavioural gate test drives validatePersonalScripts
// directly, so without this line a deleted call in run() would leave the whole gate green
// and dead.
func TestTheDaemonRunsTheTrustedPathGate(t *testing.T) {
	data, err := os.ReadFile("daemon.go")
	if err != nil {
		t.Fatalf("read daemon.go: %v", err)
	}
	if !strings.Contains(string(data), "validatePersonalScripts(d.cfg)") {
		t.Fatal("daemon.go no longer runs validatePersonalScripts at startup: the gate is dead code")
	}
}

// fifoParentScript returns a path whose PARENT DIRECTORY component is a FIFO. Opening it is
// what os.OpenRoot does first, and open(2) on a FIFO with no writer blocks in the kernel, so
// this is the cheapest faithful stand-in for the ancestor the execution-time gate exists to
// defend against - and for the dead NFS/CIFS mount personalScriptReapSlack's comment names.
func fifoParentScript(t *testing.T) string {
	t.Helper()
	orig := personalScriptOpenTimeout
	t.Cleanup(func() { personalScriptOpenTimeout = orig })
	personalScriptOpenTimeout = 200 * time.Millisecond
	dir := t.TempDir()
	fifo := filepath.Join(dir, "dd")
	if err := syscall.Mkfifo(fifo, 0o755); err != nil {
		t.Skipf("mkfifo is unavailable here: %v", err)
	}
	return filepath.Join(fifo, "pre.sh")
}

// The gate that opens the script runs on the caller's goroutine BEFORE the timeout context and
// the stop channel are ever consulted, so an unbounded open there is an unbounded wait on the
// daemon's scheduler goroutine: scheduleLoop never returns, run() never reaches wg.Wait(), and
// SIGTERM cannot stop the daemon while the heartbeat keeps reporting the host green. That is
// the harm superviseChild's own comment describes, and this path sits outside it.
//
// The stop channel is closed BEFORE the call, so nothing but a bound on the open can make this
// return.
func TestOpeningTheScriptCannotParkTheSchedulerGoroutine(t *testing.T) {
	script := fifoParentScript(t)
	stop := make(chan struct{})
	close(stop)

	done := make(chan struct{})
	go func() { runPersonalScript(script, stop); close(done) }()

	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("runPersonalScript never returned: the open of the parent directory is unbounded")
	}
}

// The detached starter carries the same open, and it runs from a defer on the shutdown path,
// so a park there wedges the way OUT of the daemon rather than the schedule.
func TestStartingTheDetachedScriptCannotParkTheShutdown(t *testing.T) {
	script := fifoParentScript(t)

	done := make(chan struct{})
	go func() { startPersonalScriptDetached(script); close(done) }()

	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("startPersonalScriptDetached never returned: the open of the parent directory is unbounded")
	}
}

// parkedParentOpens counts the goroutines currently sitting in the parent-directory open that
// os.OpenRoot performs. NumGoroutine would count every straggler the rest of the package left
// behind; this counts only the ones this file is about, so the assertions below are exact
// rather than tolerant.
func parkedParentOpens(t *testing.T) int {
	t.Helper()
	buf := make([]byte, 1<<20)
	stacks := string(buf[:runtime.Stack(buf, true)])
	parked := 0
	for _, goroutine := range strings.Split(stacks, "\n\n") {
		if strings.Contains(goroutine, "os.openRootNolog") || strings.Contains(goroutine, "os.OpenRoot(") {
			parked++
		}
	}
	return parked
}

// waitForParkedParentOpens polls until the count settles on want, so a probe that has been
// launched but has not yet reached the syscall does not decide the outcome. Every assertion
// below is a DELTA against a baseline taken at the start of the test: a parked open never
// returns on its own, so the tests in this file hand each other their stragglers and an
// absolute count would only ever measure the order they ran in.
func waitForParkedParentOpens(t *testing.T, want int, why string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := parkedParentOpens(t)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d goroutines parked in the parent open, want %d", why, got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// personalScriptOpenTimeout bounds the WAIT, not the open: the goroutine it gives up on is
// abandoned, and a parent that is a FIFO with no writer never lets that open return. One
// abandoned goroutine is the price of not wedging the scheduler and is paid once; one per
// invocation is a leak, because a goroutine parked in a syscall holds an OS thread and the Go
// runtime kills the process at 10000 of them.
//
// The daemon calls this twice per backup run, pre and post, for as long as the condition
// lasts, so nothing else bounds the count. This asserts the bound: while a probe for a path is
// still outstanding, another call for that same path must refuse without launching a second.
func TestATimedOutOpenLeavesOneProbeNoMatterHowManyRuns(t *testing.T) {
	script := fifoParentScript(t)
	base := parkedParentOpens(t)

	for i := 0; i < 8; i++ {
		if file, refusal := openPersonalScriptForExecution(script); refusal == nil {
			_ = file.Close()
			t.Fatalf("run %d opened a script whose parent is a FIFO", i)
		}
	}

	waitForParkedParentOpens(t, base+1, "8 runs against one stuck path")
}

// The bound is per path, not global: a second script on a healthy path must still be opened
// while the first one is stuck, and a second stuck path gets its own single probe rather than
// being silenced by the first one's.
func TestTheProbeBoundIsPerPathNotGlobal(t *testing.T) {
	stuck := fifoParentScript(t)
	base := parkedParentOpens(t)
	if file, refusal := openPersonalScriptForExecution(stuck); refusal == nil {
		_ = file.Close()
		t.Fatal("opened a script whose parent is a FIFO")
	}
	waitForParkedParentOpens(t, base+1, "the first stuck path")

	otherDir := t.TempDir()
	otherFifo := filepath.Join(otherDir, "dd")
	if err := syscall.Mkfifo(otherFifo, 0o755); err != nil {
		t.Skipf("mkfifo is unavailable here: %v", err)
	}
	if file, refusal := openPersonalScriptForExecution(filepath.Join(otherFifo, "post.sh")); refusal == nil {
		_ = file.Close()
		t.Fatal("opened a second script whose parent is a FIFO")
	}
	waitForParkedParentOpens(t, base+2, "a second stuck path must get its own probe")

	healthy := writePersonalScript(t, t.TempDir(), "ok.sh", "true")
	file, refusal := openPersonalScriptForExecution(healthy)
	if refusal != nil {
		t.Fatalf("a healthy script was refused while an unrelated path was stuck: the bound is global, not per path: %v", refusal)
	}
	_ = file.Close()
}

// The bound must lift itself. A dead NFS or CIFS mount is the reachable way into this state and
// it comes back, so once the abandoned open finally returns, the next run has to probe again
// instead of refusing the script forever. Opening the FIFO for writing is what releases the
// parked open here: it returns a descriptor, os.OpenRoot fstats it, sees it is not a directory
// and fails - the goroutine ends, exactly as a recovered mount would end it.
func TestTheProbeBoundLiftsWhenTheOpenFinallyReturns(t *testing.T) {
	script := fifoParentScript(t)
	base := parkedParentOpens(t)
	if file, refusal := openPersonalScriptForExecution(script); refusal == nil {
		_ = file.Close()
		t.Fatal("opened a script whose parent is a FIFO")
	}
	waitForParkedParentOpens(t, base+1, "the stuck path")

	writer, err := os.OpenFile(filepath.Dir(script), os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("opening the FIFO for writing is what unblocks the parked open: %v", err)
	}
	waitForParkedParentOpens(t, base, "the parked open never returned after a writer arrived")
	if err := writer.Close(); err != nil {
		t.Fatalf("close the writer so the path blocks again: %v", err)
	}

	if file, refusal := openPersonalScriptForExecution(script); refusal == nil {
		_ = file.Close()
		t.Fatal("opened a script whose parent is a FIFO")
	}
	waitForParkedParentOpens(t, base+1, "the path blocked again and the bound never lifted: the script is refused for the daemon's lifetime")
}

// capturePersonalScriptStart runs one script through the REPORTING starter with the default
// logger redirected, so a test can assert on what the daemon would have written. The logger is
// handed in AND installed as the default because the two halves of the line come from different
// places: the debug bracket takes the logger it is given, the warning goes to the package-level
// default.
func capturePersonalScriptStart(t *testing.T, key, path string) string {
	t.Helper()
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	prev := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(prev) })
	logging.SetDefaultLogger(logger)
	runPersonalScriptReporting(logger, key, path, nil)
	return buf.String()
}

// TestAPerRunGateRefusalIsReported is the fix for the hole the startup gate never covered.
//
// The startup gate speaks once, about the path as it was when the daemon started. The per-run
// gate re-validates the OPENED INODE before every invocation, which is the only thing standing
// between an accepted foreign-owned ancestor and its owner swapping the file, and until this it
// refused in total silence: the daemon knew the script had not run, the operator saw an ordinary
// green backup and had nothing anywhere to tell the two apart.
//
// Each row is a way the file can stop being the one the daemon was told to trust, and the point
// of asserting the reason and not just the presence of a line is that "it is gone" and "it is no
// longer safe to run" send an operator to two different places.
func TestAPerRunGateRefusalIsReported(t *testing.T) {
	dir := t.TempDir()

	notExecutable := filepath.Join(dir, "not-executable.sh")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	groupWritable := filepath.Join(dir, "group-writable.sh")
	if err := os.WriteFile(groupWritable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Chmod, not the WriteFile mode: the process umask clears the group bits this row is
	// entirely about, and the file then passes the check the row exists to fail.
	if err := os.Chmod(groupWritable, 0o770); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	cases := []struct {
		name       string
		key        string
		path       string
		wantReason string
	}{
		{"the file is gone", personalScriptPreRunKey, filepath.Join(dir, "nope.sh"), "could not be opened"},
		{"the path is a directory", personalScriptPostRunKey, dir, "is not a regular file"},
		{"the execute bit is gone", personalScriptPreRunKey, notExecutable, "is not executable (mode 0600)"},
		{"the group can write it", personalScriptPostRunKey, groupWritable, "is writable by group or others (mode 0770)"},
		{"the path is relative", personalScriptPreRunKey, "personal-script.sh", "is not an absolute path"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logged := capturePersonalScriptStart(t, tc.key, tc.path)

			if !strings.Contains(logged, "WARNING") {
				t.Fatalf("a refused script must produce one WARNING; got %q", logged)
			}
			if !strings.Contains(logged, tc.key) {
				t.Errorf("the warning must name the variable to edit (%s); got %q", tc.key, logged)
			}
			if !strings.Contains(logged, "was not started for this run") {
				t.Errorf("the warning must say the script did not run; got %q", logged)
			}
			if !strings.Contains(logged, tc.wantReason) {
				t.Errorf("the warning must carry the reason %q; got %q", tc.wantReason, logged)
			}
			if !strings.Contains(logged, tc.path) {
				t.Errorf("the warning must carry the path %q; got %q", tc.path, logged)
			}
		})
	}
}

// TestTheScriptsOwnFailuresAreStillSilent is the other half, and the one a well-meant widening
// of the fix above would break. What gained a voice is ProxSave's decision NOT TO START a
// script. What the script itself does when it does start is still the operator's business:
// DAEMON.md promises nothing it prints or fails at reaches any surface, and an exit code or a
// missing shebang is exactly that.
func TestTheScriptsOwnFailuresAreStillSilent(t *testing.T) {
	dir := t.TempDir()

	noShebang := filepath.Join(dir, "no-shebang")
	if err := os.WriteFile(noShebang, []byte("this is not a program\n"), 0o700); err != nil {
		t.Fatalf("write: %v", err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"exits non-zero", writePersonalScript(t, dir, "exit3.sh", "exit 3")},
		{"has no shebang and cannot be forked", noShebang},
		{"is not configured at all", ""},
		{"is whitespace only", "   "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logged := capturePersonalScriptStart(t, personalScriptPreRunKey, tc.path)
			if strings.Contains(logged, "WARNING") {
				t.Errorf("the script's own outcome must reach no surface at any level; got %q", logged)
			}
		})
	}
}

// TestAnUnconfiguredScriptIsNotEvenTraced pins the quiet end of the bracket. A daemon with no
// personal scripts configured is the shipped state, and it must not pay two debug lines per run
// telling a reader that nothing was configured - the startup diagnostic already said so once.
func TestAnUnconfiguredScriptIsNotEvenTraced(t *testing.T) {
	if logged := capturePersonalScriptStart(t, personalScriptPreRunKey, ""); logged != "" {
		t.Errorf("an empty setting must produce no output at all, not even a debug bracket; got %q", logged)
	}
}

// TestTheDebugBracketFramesEveryStart asserts the bracket itself, on both outcomes. It is what
// gives an operator running at debug level the ordering they need to read a slow run: the pre
// script's End line precedes the backup's launch line, and its duration is the delay DAEMON.md
// warns a slow pre script adds to the monitor's start signal.
func TestTheDebugBracketFramesEveryStart(t *testing.T) {
	dir := t.TempDir()

	t.Run("a script that runs", func(t *testing.T) {
		logged := capturePersonalScriptStart(t, personalScriptPreRunKey, writePersonalScript(t, dir, "ok.sh", "exit 0"))
		if !strings.Contains(logged, "Start personal script") || !strings.Contains(logged, "End personal script") {
			t.Fatalf("both ends of the bracket must be written; got %q", logged)
		}
		if strings.Contains(logged, "error=") {
			t.Errorf("a script that started must not close its bracket with an error; got %q", logged)
		}
	})

	t.Run("a script that is refused", func(t *testing.T) {
		logged := capturePersonalScriptStart(t, personalScriptPreRunKey, filepath.Join(dir, "gone.sh"))
		if !strings.Contains(logged, "Start personal script") || !strings.Contains(logged, "End personal script") {
			t.Fatalf("both ends of the bracket must be written; got %q", logged)
		}
		if !strings.Contains(logged, "error=") {
			t.Errorf("a refused script must close its bracket with the refusal; got %q", logged)
		}
	})
}

// TestTheDetachedStarterReportsItsRefusalToo covers the two paths that start the post script and
// walk away - the abandoned-child unwind and any shutdown. The daemon is leaving on both, which
// is when an operator is least able to reconstruct what happened afterwards, so the one line is
// worth more there rather than less.
func TestTheDetachedStarterReportsItsRefusalToo(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	prev := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(prev) })
	logging.SetDefaultLogger(logger)

	startPersonalScriptDetachedReporting(logger, personalScriptPostRunKey, filepath.Join(t.TempDir(), "gone.sh"))

	logged := buf.String()
	if !strings.Contains(logged, personalScriptPostRunKey) || !strings.Contains(logged, "was not started for this run") {
		t.Errorf("the detached starter must report a refusal exactly as the waited one does; got %q", logged)
	}
}
