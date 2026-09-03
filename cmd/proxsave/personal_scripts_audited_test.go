package main

import (
	"bytes"
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
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
	cmd := personalScriptCmd(context.Background(), "/usr/local/bin/whatever")

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
		// The world-writable row pins the DIVISION of labour: the starter itself stays
		// permissive and silent (its frozen rule), because the trusted-path gate lives at
		// daemon startup - validatePersonalScripts refuses and blanks this file LOUDLY
		// before any tick, so the starter can only meet it in a test.
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
func TestOnlyTheDaemonStartsThePersonalScripts(t *testing.T) {
	allowed := map[string]bool{
		filepath.FromSlash("cmd/proxsave/personal_scripts.go"): true,
		filepath.FromSlash("cmd/proxsave/daemon.go"):           true,
	}
	root := filepath.Join("..", "..")

	for _, name := range []string{"runPersonalScript", "startPersonalScriptDetached"} {
		var seenInDaemon bool
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
			if !strings.Contains(string(data), name) {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			if rel == filepath.FromSlash("cmd/proxsave/daemon.go") {
				seenInDaemon = true
			}
			if !allowed[rel] {
				t.Errorf("%s names %s: these scripts run for the daemon's own scheduled run and nothing else", rel, name)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		// The other half of the rule, and the half a deleted call site would break: the daemon
		// must still start them. Without this the whole test passes on a tree where the
		// feature was removed.
		if !seenInDaemon {
			t.Errorf("cmd/proxsave/daemon.go no longer names %s: the daemon is the only thing that starts these scripts", name)
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

// TestNoShellIsInvolved is the behavioural half of "the path goes to execve as it stands".
// The argument-count assertions above cannot see a shell wrapper applied inside the starter.
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

// The trusted-path gate, added on the release-PR review (#303). The starters stay
// silent and permissive on purpose (their frozen rule); the gate lives at daemon
// startup instead, where a refusal can be LOUD - which is exactly what dissolves
// the recorded objection that a refused path would be an undebuggable
// non-execution. Each refusal names the variable, the path and the reason, and
// blanks the setting so the run behaves as if it were never configured.
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

	pre, post, logged := capturePersonalScriptValidation(t, script, script)
	if pre != script || post != script {
		t.Fatalf("a trusted path did not survive: pre=%q post=%q\n%s", pre, post, logged)
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
