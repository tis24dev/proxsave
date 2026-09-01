package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// ledgerScript writes an executable script that appends one word to a shared ledger file, so
// the ORDER of the pre script, the backup child and the post script is a single byte-exact
// assertion instead of three racing timestamps. The ledger path is baked into the script text
// because the scripts get no arguments and no injected environment: that is the feature.
func ledgerScript(t *testing.T, dir, name, word, ledger string, body ...string) string {
	t.Helper()
	return writePersonalScript(t, dir, name, "echo "+word+" >> "+ledger+"\n"+strings.Join(body, "\n"))
}

// ledgerCmd is shCmd's ledger-aware sibling: the stand-in backup child appends its own word
// to the SAME file, so one read of that file proves the bracketing.
func ledgerCmd(ledger, word, tail string) func(ctx context.Context) *exec.Cmd {
	return shCmd("echo " + word + " >> " + ledger + "\n" + tail)
}

func readLedger(t *testing.T, ledger string) string {
	t.Helper()
	data, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	return string(data)
}

// waitForLedger polls until the ledger holds want, for the one path that does NOT wait for
// the post script: the abandoned-child unwind starts it and walks away on purpose.
func waitForLedger(t *testing.T, ledger, want string, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(ledger); err == nil && strings.Contains(string(data), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("ledger never contained %q within %s; got %q", want, limit, readLedger(t, ledger))
}

// TestPersonalScriptsBracketTheBackupChild is the whole feature in one assertion. Byte-exact
// rather than three Contains checks, so it also proves neither script ran twice.
func TestPersonalScriptsBracketTheBackupChild(t *testing.T) {
	dir := t.TempDir()
	ledger := filepath.Join(dir, "ledger")
	rep := &fakeReporter{}
	d := newTestDaemon(t, rep, ledgerCmd(ledger, "child", "exit 0"), time.Minute)
	d.cfg.PersonalScriptPreRun = ledgerScript(t, dir, "pre.sh", "pre", ledger)
	d.cfg.PersonalScriptPostRun = ledgerScript(t, dir, "post.sh", "post", ledger)

	if d.runOnce(context.Background()) {
		t.Fatal("runOnce reported an abandoned child")
	}

	if got := readLedger(t, ledger); got != "pre\nchild\npost\n" {
		t.Fatalf("ledger = %q, want %q", got, "pre\nchild\npost\n")
	}
}

// TestPostRunStartsAfterEveryOutcome walks the returns below the call site. Each one is a run
// that happened, so each one must reach the post script.
func TestPostRunStartsAfterEveryOutcome(t *testing.T) {
	cases := []struct {
		name   string
		child  string
		maxRun time.Duration
		check  func(t *testing.T, snap *fakeReporter)
	}{
		{
			name:   "a failing run",
			child:  "exit 4",
			maxRun: time.Minute,
			check: func(t *testing.T, snap *fakeReporter) {
				if snap.lastCode != 4 {
					t.Errorf("lastCode = %d, want 4", snap.lastCode)
				}
			},
		},
		{
			name:   "a skipped child",
			child:  "exit " + strconv.Itoa(types.ExitBackupSkipped.Int()),
			maxRun: time.Minute,
			check: func(t *testing.T, snap *fakeReporter) {
				if snap.finished != 0 || snap.hung != 0 {
					t.Errorf("a skip must ping no outcome; finished=%d hung=%d", snap.finished, snap.hung)
				}
			},
		},
		{
			name:   "a hang",
			child:  "sleep 5",
			maxRun: 150 * time.Millisecond,
			check: func(t *testing.T, snap *fakeReporter) {
				if snap.hung != 1 {
					t.Errorf("hung = %d, want 1", snap.hung)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ledger := filepath.Join(dir, "ledger")
			rep := &fakeReporter{}
			d := newTestDaemon(t, rep, ledgerCmd(ledger, "child", tc.child), tc.maxRun)
			d.cfg.PersonalScriptPreRun = ledgerScript(t, dir, "pre.sh", "pre", ledger)
			d.cfg.PersonalScriptPostRun = ledgerScript(t, dir, "post.sh", "post", ledger)

			d.runOnce(context.Background())

			if got := readLedger(t, ledger); !strings.HasSuffix(got, "post\n") {
				t.Fatalf("ledger = %q, want it to end in post", got)
			}
			snap := rep.snapshot()
			tc.check(t, &snap)
		})
	}
}

// TestPostRunStartsAfterAShutdownInterruptedRun covers the return that fires when the parent
// context is cancelled while the child is running: still a run that happened.
func TestPostRunStartsAfterAShutdownInterruptedRun(t *testing.T) {
	dir := t.TempDir()
	ledger := filepath.Join(dir, "ledger")
	rep := &fakeReporter{}
	d := newTestDaemon(t, rep, ledgerCmd(ledger, "child", "sleep 5"), time.Minute)
	d.cfg.PersonalScriptPostRun = ledgerScript(t, dir, "post.sh", "post", ledger)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	d.runOnce(ctx)

	if got := readLedger(t, ledger); !strings.HasSuffix(got, "post\n") {
		t.Fatalf("ledger = %q, want it to end in post", got)
	}
}

// TestPostRunIsStartedButNotWaitedForOnTheAbandonPath pins the maintainer's call: on the one
// unwind whose whole point is a fast systemd restart, the script is started and left to the
// cgroup instead of putting up to 10 minutes in front of that restart.
func TestPostRunIsStartedButNotWaitedForOnTheAbandonPath(t *testing.T) {
	dir := t.TempDir()
	ledger := filepath.Join(dir, "ledger")
	rep := &fakeReporter{}
	d := newAbandonDaemon(t, rep, 150*time.Millisecond)
	d.cfg.PersonalScriptPostRun = ledgerScript(t, dir, "post.sh", "post", ledger, "sleep 30")

	start := time.Now()
	if !d.runOnce(context.Background()) {
		t.Fatal("runOnce must report the abandoned child so the caller unwinds")
	}
	elapsed := time.Since(start)

	if elapsed > 20*time.Second {
		t.Fatalf("runOnce waited %s on the abandon path: the restart must not queue behind the post script", elapsed)
	}
	waitForLedger(t, ledger, "post", 10*time.Second)
}

// TestNeitherScriptRunsWhenNoRunHappens covers the two returns ABOVE the call site. Both
// script files exist on disk, so the test cannot pass merely because the fixtures are missing.
func TestNeitherScriptRunsWhenNoRunHappens(t *testing.T) {
	t.Run("backups administratively off", func(t *testing.T) {
		dir := t.TempDir()
		ledger := filepath.Join(dir, "ledger")
		d := newTestDaemon(t, &fakeReporter{}, ledgerCmd(ledger, "child", "exit 0"), time.Minute)
		d.cfg.BackupEnabled = false
		d.cfg.PersonalScriptPreRun = ledgerScript(t, dir, "pre.sh", "pre", ledger)
		d.cfg.PersonalScriptPostRun = ledgerScript(t, dir, "post.sh", "post", ledger)

		d.runOnce(context.Background())

		if _, err := os.Stat(ledger); !os.IsNotExist(err) {
			t.Fatalf("a tick that runs no backup must start no script; ledger = %q", readLedger(t, ledger))
		}
	})

	t.Run("a tick during shutdown", func(t *testing.T) {
		dir := t.TempDir()
		ledger := filepath.Join(dir, "ledger")
		d := newTestDaemon(t, &fakeReporter{}, ledgerCmd(ledger, "child", "exit 0"), time.Minute)
		d.cfg.PersonalScriptPreRun = ledgerScript(t, dir, "pre.sh", "pre", ledger)
		d.cfg.PersonalScriptPostRun = ledgerScript(t, dir, "post.sh", "post", ledger)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		d.runOnce(ctx)

		if _, err := os.Stat(ledger); !os.IsNotExist(err) {
			t.Fatalf("a tick that runs no backup must start no script; ledger = %q", readLedger(t, ledger))
		}
	})
}

// TestEmptyPersonalScriptVariablesStartNothing is the byte-identity control: with both keys
// blank the run is what it was before the feature existed. The fixtures are written to disk
// anyway, so the test cannot pass because they are absent.
func TestEmptyPersonalScriptVariablesStartNothing(t *testing.T) {
	for _, value := range []string{"", "   "} {
		t.Run("value "+strconv.Quote(value), func(t *testing.T) {
			dir := t.TempDir()
			ledger := filepath.Join(dir, "ledger")
			ledgerScript(t, dir, "pre.sh", "pre", ledger)
			ledgerScript(t, dir, "post.sh", "post", ledger)
			rep := &fakeReporter{}
			d := newTestDaemon(t, rep, ledgerCmd(ledger, "child", "exit 0"), time.Minute)
			d.cfg.PersonalScriptPreRun = value
			d.cfg.PersonalScriptPostRun = value

			d.runOnce(context.Background())

			if got := readLedger(t, ledger); got != "child\n" {
				t.Fatalf("ledger = %q, want only the child", got)
			}
			snap := rep.snapshot()
			if snap.started != 1 || snap.finished != 1 || snap.hung != 0 || snap.lastCode != 0 {
				t.Fatalf("the run changed shape: started=%d finished=%d hung=%d code=%d", snap.started, snap.finished, snap.hung, snap.lastCode)
			}
		})
	}
}

// TestPersonalScriptExitCodesAreIgnored: the scripts are the operator's, so their fate is
// theirs. Nothing they do reaches the run's outcome, its exit code or its warning counters.
func TestPersonalScriptExitCodesAreIgnored(t *testing.T) {
	dir := t.TempDir()
	ledger := filepath.Join(dir, "ledger")

	origLog := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(origLog) })
	var buf bytes.Buffer
	def := logging.New(types.LogLevelDebug, false)
	def.SetOutput(&buf)
	logging.SetDefaultLogger(def)

	rep := &fakeReporter{}
	d := newTestDaemon(t, rep, ledgerCmd(ledger, "child", "exit 0"), time.Minute)
	d.cfg.PersonalScriptPreRun = ledgerScript(t, dir, "pre.sh", "pre", ledger, "exit 3")
	d.cfg.PersonalScriptPostRun = ledgerScript(t, dir, "post.sh", "post", ledger, "exit 9")

	if d.runOnce(context.Background()) {
		t.Fatal("a failing operator script must not unwind the daemon")
	}

	snap := rep.snapshot()
	if snap.lastCode != 0 || snap.finished != 1 {
		t.Errorf("the run's own outcome changed: code=%d finished=%d", snap.lastCode, snap.finished)
	}
	if def.WarningCount() != 0 || def.ErrorCount() != 0 || def.HasWarnings() || len(def.IssueLines()) != 0 {
		t.Errorf("a failing operator script raised an issue: warnings=%d errors=%d lines=%v", def.WarningCount(), def.ErrorCount(), def.IssueLines())
	}
}

// TestPersonalScriptsAreInvisibleToEveryReportingSurface is the silence contract. Both scripts
// print a unique marker on stdout AND stderr, and the post script exits non-zero.
func TestPersonalScriptsAreInvisibleToEveryReportingSurface(t *testing.T) {
	const preMarker = "PRE_MARKER_9f2a"
	const postMarker = "POST_MARKER_9f2a"
	const childMarker = "CHILD_MARKER_9f2a"

	dir := t.TempDir()
	ledger := filepath.Join(dir, "ledger")

	origLog := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(origLog) })
	var buf bytes.Buffer
	// Debug, not Info: a Debug line is still a line, and it still reaches the daemon's
	// on-disk log file (initializeRunLogFile, main_runtime.go:308).
	def := logging.New(types.LogLevelDebug, false)
	def.SetOutput(&buf)
	logging.SetDefaultLogger(def)

	rep := &fakeReporter{}
	d := newTestDaemon(t, rep, ledgerCmd(ledger, "child", "echo "+childMarker+"\nexit 0"), time.Minute)
	d.cfg.HealthcheckSendLog = true
	d.cfg.PersonalScriptPreRun = ledgerScript(t, dir, "pre.sh", "pre", ledger, "echo "+preMarker, "echo "+preMarker+" 1>&2")
	d.cfg.PersonalScriptPostRun = ledgerScript(t, dir, "post.sh", "post", ledger, "echo "+postMarker, "echo "+postMarker+" 1>&2", "exit 9")

	d.runOnce(context.Background())

	t.Run("log", func(t *testing.T) {
		logged := buf.String()
		for _, forbidden := range []string{preMarker, postMarker, d.cfg.PersonalScriptPreRun, d.cfg.PersonalScriptPostRun, "PERSONAL_SCRIPT"} {
			if strings.Contains(logged, forbidden) {
				t.Errorf("the run log names %q; these scripts are never logged, at any level", forbidden)
			}
		}
	})

	t.Run("healthchecks tail", func(t *testing.T) {
		tail := rep.snapshot().lastTail
		if !strings.Contains(tail, childMarker) {
			t.Fatalf("the child's own marker is missing from the tail, so this test proves nothing: %q", tail)
		}
		for _, forbidden := range []string{preMarker, postMarker} {
			if strings.Contains(tail, forbidden) {
				t.Errorf("the healthchecks payload carries %q", forbidden)
			}
		}
	})

	t.Run("status file", func(t *testing.T) {
		status, err := health.LoadStatus(d.cfg.BaseDir)
		if err != nil {
			t.Fatalf("load status: %v", err)
		}
		raw, err := json.Marshal(status)
		if err != nil {
			t.Fatalf("marshal status: %v", err)
		}
		blob := string(raw)
		for _, forbidden := range []string{preMarker, postMarker, "PERSONAL_SCRIPT", d.cfg.PersonalScriptPreRun, d.cfg.PersonalScriptPostRun} {
			if strings.Contains(blob, forbidden) {
				t.Errorf("the daemon status records %q", forbidden)
			}
		}
	})
}
