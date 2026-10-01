package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// captureDefaultLogger points the package-level logger (logging.Debug and friends) at a
// buffer for the test.
func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	orig := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(orig) })
	buf := &bytes.Buffer{}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(buf)
	logging.SetDefaultLogger(logger)
	return buf
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func requireAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s should not exist (lstat err = %v)", path, err)
	}
}

// newMoveTestBase is a BASE_DIR as the daemon sees it right after acquireDaemonLock: daemon_state/
// exists (the lock created it) next to an identity/ an older release filled.
func newMoveTestBase(t *testing.T) (base, identityDir, stateDir string) {
	t.Helper()
	base = t.TempDir()
	identityDir = filepath.Join(base, "identity")
	stateDir = filepath.Join(base, "daemon_state")
	if err := os.MkdirAll(identityDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return base, identityDir, stateDir
}

// Every file an older release kept in identity/ for the daemon moves to daemon_state/ with its
// content, and the server identity stays where it is.
func TestMoveDaemonStateFromIdentityMovesEachFile(t *testing.T) {
	buf := captureDefaultLogger(t)
	base, identityDir, stateDir := newMoveTestBase(t)
	writeTestFile(t, filepath.Join(identityDir, ".server_identity"), "server")
	for _, name := range daemonStateMovedNames {
		if name == daemonLockFileName {
			continue // the lock is always in daemon_state/ too by now; see the lock test
		}
		writeTestFile(t, filepath.Join(identityDir, name), "old "+name)
	}

	if !moveDaemonStateFromIdentity(base) {
		t.Fatal("moveDaemonStateFromIdentity = false with files to move, want true")
	}

	for _, name := range daemonStateMovedNames {
		if name == daemonLockFileName {
			continue
		}
		requireAbsent(t, filepath.Join(identityDir, name))
		if got := readTestFile(t, filepath.Join(stateDir, name)); got != "old "+name {
			t.Fatalf("daemon_state/%s = %q, want the content moved from identity/", name, got)
		}
		if want := "daemon state: moved " + name + " from identity/ to daemon_state/"; !strings.Contains(buf.String(), want) {
			t.Fatalf("missing DEBUG line %q\n%s", want, buf.String())
		}
	}
	if got := readTestFile(t, filepath.Join(identityDir, ".server_identity")); got != "server" {
		t.Fatalf("identity/.server_identity = %q, want it untouched", got)
	}
}

// The names the move looks for are the ones every reader now uses in daemon_state/: a reader
// that kept another name would read nothing after the move.
func TestDaemonStateMovedNamesMatchTheReaders(t *testing.T) {
	base := "/opt/proxsave"
	stateDir := filepath.Join(base, "daemon_state")
	want := map[string]bool{
		filepath.Join(stateDir, daemonLockFileName): true,
		health.DaemonPIDPath(base):                  true,
		health.DaemonInfoPath(base):                 true,
		health.DaemonRuntimePath(base):              true,
		health.AbandonPath(base):                    true,
		health.StatusPath(base):                     true,
		health.NotifyResultsPath(base):              true,
		health.ManualOutcomePath(base):              true,
	}
	if len(daemonStateMovedNames) != len(want) {
		t.Fatalf("moved names = %d, want %d", len(daemonStateMovedNames), len(want))
	}
	for _, name := range daemonStateMovedNames {
		if !want[filepath.Join(stateDir, name)] {
			t.Fatalf("moved name %s is not where any reader looks", name)
		}
	}
}

// When daemon_state/ already has the file it is the newer one: it is kept and the identity/
// copy is deleted.
func TestMoveDaemonStateFromIdentityKeepsDaemonStateWhenBothExist(t *testing.T) {
	buf := captureDefaultLogger(t)
	base, identityDir, stateDir := newMoveTestBase(t)
	writeTestFile(t, filepath.Join(identityDir, ".healthcheck_status.json"), "old")
	writeTestFile(t, filepath.Join(stateDir, ".healthcheck_status.json"), "new")

	if !moveDaemonStateFromIdentity(base) {
		t.Fatal("moveDaemonStateFromIdentity = false after removing a file from identity/, want true")
	}
	requireAbsent(t, filepath.Join(identityDir, ".healthcheck_status.json"))
	if got := readTestFile(t, filepath.Join(stateDir, ".healthcheck_status.json")); got != "new" {
		t.Fatalf("daemon_state/.healthcheck_status.json = %q, want the daemon_state copy kept", got)
	}
	if want := "daemon state: removed .healthcheck_status.json from identity/, daemon_state/ already has it"; !strings.Contains(buf.String(), want) {
		t.Fatalf("missing DEBUG line %q\n%s", want, buf.String())
	}
}

// A quarantined ".corrupt" copy follows its file, in both cases.
func TestMoveDaemonStateFromIdentityMovesCorruptSiblings(t *testing.T) {
	base, identityDir, stateDir := newMoveTestBase(t)
	writeTestFile(t, filepath.Join(identityDir, ".healthcheck_status.json.corrupt"), "garbage")
	writeTestFile(t, filepath.Join(identityDir, ".daemon_info.json.corrupt"), "old garbage")
	writeTestFile(t, filepath.Join(stateDir, ".daemon_info.json.corrupt"), "new garbage")

	if !moveDaemonStateFromIdentity(base) {
		t.Fatal("moveDaemonStateFromIdentity = false with .corrupt files in identity/, want true")
	}
	requireAbsent(t, filepath.Join(identityDir, ".healthcheck_status.json.corrupt"))
	requireAbsent(t, filepath.Join(identityDir, ".daemon_info.json.corrupt"))
	if got := readTestFile(t, filepath.Join(stateDir, ".healthcheck_status.json.corrupt")); got != "garbage" {
		t.Fatalf("daemon_state/.healthcheck_status.json.corrupt = %q, want the moved bytes", got)
	}
	if got := readTestFile(t, filepath.Join(stateDir, ".daemon_info.json.corrupt")); got != "new garbage" {
		t.Fatalf("daemon_state/.daemon_info.json.corrupt = %q, want the daemon_state copy kept", got)
	}
}

// The old lock file: by the time the move runs this daemon already holds daemon_state/.daemon.lock,
// so the identity/ one is deleted, and that deletion alone marks the start as the first after an
// upgrade (every older daemon left one behind).
func TestMoveDaemonStateFromIdentityRemovesTheOldLock(t *testing.T) {
	base := t.TempDir()
	identityDir := filepath.Join(base, "identity")
	writeTestFile(t, filepath.Join(identityDir, daemonLockFileName), "")
	release, err := acquireDaemonLock(base)
	if err != nil {
		t.Fatalf("acquireDaemonLock: %v", err)
	}
	defer release()

	if !moveDaemonStateFromIdentity(base) {
		t.Fatal("moveDaemonStateFromIdentity = false after removing identity/.daemon.lock, want true")
	}
	requireAbsent(t, filepath.Join(identityDir, daemonLockFileName))
	if _, err := os.Stat(filepath.Join(base, "daemon_state", daemonLockFileName)); err != nil {
		t.Fatalf("daemon_state/.daemon.lock must stay: %v", err)
	}
	// The lock still works: a second owner is refused.
	if _, err := acquireDaemonLock(base); err == nil {
		t.Fatal("a second acquireDaemonLock succeeded after the move, want errDaemonLockHeld")
	}
}

// The lock lives in daemon_state/, created root-only.
func TestAcquireDaemonLockCreatesDaemonStateRootOnly(t *testing.T) {
	base := t.TempDir()
	release, err := acquireDaemonLock(base)
	if err != nil {
		t.Fatalf("acquireDaemonLock: %v", err)
	}
	defer release()
	info, err := os.Stat(filepath.Join(base, "daemon_state"))
	if err != nil {
		t.Fatalf("stat daemon_state: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("daemon_state mode = %o, want 0700", got)
	}
	if _, err := os.Stat(filepath.Join(base, "daemon_state", daemonLockFileName)); err != nil {
		t.Fatalf("lock file not in daemon_state/: %v", err)
	}
	requireAbsent(t, filepath.Join(base, "identity", daemonLockFileName))
}

// Nothing to move: identity/ holding only identity, or no identity/ at all, reports false and
// changes nothing.
func TestMoveDaemonStateFromIdentityNothingToMove(t *testing.T) {
	t.Run("identity only", func(t *testing.T) {
		buf := captureDefaultLogger(t)
		base, identityDir, stateDir := newMoveTestBase(t)
		writeTestFile(t, filepath.Join(identityDir, ".server_identity"), "server")
		writeTestFile(t, filepath.Join(identityDir, ".notify_secret"), "secret")
		writeTestFile(t, filepath.Join(identityDir, "age", "recipient.txt"), "age1")

		if moveDaemonStateFromIdentity(base) {
			t.Fatal("moveDaemonStateFromIdentity = true with nothing to move, want false")
		}
		entries, err := os.ReadDir(stateDir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("daemon_state/ changed: entries=%v err=%v", entries, err)
		}
		if strings.Contains(buf.String(), "daemon state:") {
			t.Fatalf("no DEBUG line expected when nothing moves\n%s", buf.String())
		}
	})
	t.Run("no identity dir", func(t *testing.T) {
		base := t.TempDir()
		if moveDaemonStateFromIdentity(base) {
			t.Fatal("moveDaemonStateFromIdentity = true without identity/, want false")
		}
	})
}

// A move that fails is a DEBUG line, never fatal, and the file stays where it was.
func TestMoveDaemonStateFromIdentityFailureIsDebugOnly(t *testing.T) {
	buf := captureDefaultLogger(t)
	base := t.TempDir()
	identityDir := filepath.Join(base, "identity")
	writeTestFile(t, filepath.Join(identityDir, ".healthcheck_status.json"), "old")
	// No daemon_state/: the rename has nowhere to go.

	if moveDaemonStateFromIdentity(base) {
		t.Fatal("moveDaemonStateFromIdentity = true although nothing left identity/, want false")
	}
	if got := readTestFile(t, filepath.Join(identityDir, ".healthcheck_status.json")); got != "old" {
		t.Fatalf("identity/.healthcheck_status.json = %q, want it left in place", got)
	}
	if !strings.Contains(buf.String(), "daemon state: move .healthcheck_status.json failed error=") {
		t.Fatalf("missing failure DEBUG line\n%s", buf.String())
	}
}

// startDaemonUntilPublished runs a daemon on base until it has published its pid, and returns a
// stop func that cancels it and waits for run() to return.
func startDaemonUntilPublished(t *testing.T, base string) (stop func()) {
	t.Helper()
	withProbe(t, func(pid int) bool { return false })
	d := &daemon{cfg: &config.Config{BaseDir: base, SchedulerTime: "03:00"}, now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.run(ctx)
	}()
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop")
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok, _ := health.ReadDaemonInfo(base); ok {
			if pid, err := health.ReadDaemonPID(base); err == nil && pid == os.Getpid() {
				return stop
			}
		}
		if time.Now().After(deadline) {
			stop()
			t.Fatal("daemon never published its pid and info")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The start that moved files out of identity/ also writes .daemon.pid and .daemon_info.json
// into identity/, with the content it wrote to daemon_state/: the previous release verifies its
// --upgrade by polling identity/.
func TestDaemonStartThatMovedFilesAlsoWritesIdentityCopies(t *testing.T) {
	base := t.TempDir()
	identityDir := filepath.Join(base, "identity")
	writeTestFile(t, filepath.Join(identityDir, daemonLockFileName), "")
	writeTestFile(t, filepath.Join(identityDir, ".healthcheck_status.json"), `{"mode":"centralized"}`)

	stop := startDaemonUntilPublished(t, base)
	defer stop()

	if got := strings.TrimSpace(readTestFile(t, filepath.Join(identityDir, ".daemon.pid"))); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("identity/.daemon.pid = %q, want %d", got, os.Getpid())
	}
	if got, want := readTestFile(t, filepath.Join(identityDir, ".daemon_info.json")), readTestFile(t, health.DaemonInfoPath(base)); got != want {
		t.Fatalf("identity/.daemon_info.json = %q, want the daemon_state record %q", got, want)
	}
	if _, err := os.Stat(health.StatusPath(base)); err != nil {
		t.Fatalf("status file was not moved to daemon_state/: %v", err)
	}
}

// A start with nothing to move writes daemon_state/ only.
func TestDaemonStartWithNothingToMoveWritesOnlyDaemonState(t *testing.T) {
	base := t.TempDir()
	identityDir := filepath.Join(base, "identity")
	writeTestFile(t, filepath.Join(identityDir, ".server_identity"), "server")

	stop := startDaemonUntilPublished(t, base)
	defer stop()

	requireAbsent(t, filepath.Join(identityDir, ".daemon.pid"))
	requireAbsent(t, filepath.Join(identityDir, ".daemon_info.json"))
}
