package main

import (
	"context"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if cmd.Env != nil {
		t.Errorf("Env must stay nil so the script inherits the daemon's environment and is given no run context; got %v", cmd.Env)
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
	script := writePersonalScript(t, dir, "grandchild.sh", "sleep 60 &\necho out\necho err 1>&2\nexit 0")

	done := make(chan struct{})
	go func() { defer close(done); runPersonalScript(script) }()

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
	script := writePersonalScript(t, dir, "sigterm-proof.sh", `trap "" TERM`+"\nsleep 30")

	start := time.Now()
	runPersonalScript(script)
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
		// The world-writable row is what documents why safeexec.TrustedCommandContext was not
		// used: ValidateTrustedExecutablePath refuses this file, and under the silence rule
		// that refusal would be indistinguishable from a script that ran and did nothing.
		{"world writable but runnable", worldWritable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan struct{})
			go func() { defer close(done); runPersonalScript(tc.path) }()
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
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
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
			if !allowed[rel] {
				t.Errorf("%s names %s: these scripts run for the daemon's own scheduled run and nothing else", rel, name)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
	}
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
