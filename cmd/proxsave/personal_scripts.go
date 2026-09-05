package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/safefs"
)

// personalScriptTimeout bounds ONE PERSONAL_SCRIPT_PRE_RUN or PERSONAL_SCRIPT_POST_RUN
// invocation. A var rather than a const only so a test can shrink it (the shape
// cronProbeTimeout uses, cron_indirect_refs.go:128); 10 minutes is the shipped value.
var personalScriptTimeout = 10 * time.Minute

// personalScriptReapSlack is how long runPersonalScript keeps waiting AFTER that timeout
// fired and os/exec SIGKILLed the script, before it stops waiting at all.
//
// It exists for the reason superviseChild exists (its doc comment in daemon.go): os/exec's
// Cmd.Wait does a wait4(2) BEFORE it ever reads the cancellation result, so neither the
// context nor a WaitDelay can unblock it. A script parked in TASK_UNINTERRUPTIBLE, one living
// on a dead NFS or CIFS mount, which is exactly the kind of place an operator keeps a personal
// script, never dequeues that SIGKILL and is never reaped, so wait4 never returns. Waiting for
// it inline would wedge the scheduler goroutine, and a script we were explicitly told not to
// care about would take the daemon down with it. 15s mirrors daemonReapSlack: a process
// SIGKILL can actually kill is reaped within microseconds, so this margin is only ever spent
// on one that is already lost.
var personalScriptReapSlack = 15 * time.Second

// personalScriptEnv is the daemon's own environment with two variables removed.
//
// LOG_FILE names the run log ProxSave is writing at that very moment (exported by
// initializeRunLogFile), and BASE_DIR names the installation. Both are inherited by everything
// the daemon forks, and both are the only way a personal script could learn anything about the
// run it brackets. LOG_FILE is the sharper of the two: a script that appends to it puts its own
// text inside ProxSave's log, which is the one thing the whole feature promises cannot happen.
// The maintainer chose to strip both rather than document them.
//
// Everything else is passed through untouched. This is a subtraction, never an injection: no
// variable is added, renamed or rewritten.
func personalScriptEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if key, _, ok := strings.Cut(kv, "="); ok && (key == "LOG_FILE" || key == "BASE_DIR") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// personalScriptCmd builds the command for one operator script, and most of what it is is
// what it does NOT set.
//
// Stdin, Stdout and Stderr are left nil, and that one choice carries two separate
// requirements at once. It is the SILENCE: os/exec connects each nil descriptor to
// os.DevNull, so nothing the script prints can reach journald, the daemon's own on-disk log
// file (initializeRunLogFile, main_runtime.go:308), the run recap, a notification, a
// healthchecks ping or a metric, and no writer is left for a later edit to repoint at a
// logger. It is ALSO the hang immunity, and that part is easy to undo by tidying nil into
// io.Discard: nil becomes an *os.File and os/exec spawns NO copy goroutine, whereas
// io.Discard is an ordinary io.Writer, which makes os/exec build a pipe plus a copy
// goroutine, and Wait then blocks until that pipe reaches EOF, which a backgrounded
// grandchild holding the descriptor withholds for its own whole lifetime (safeexec.go:321
// records the same mechanism). A nil Stdin is also why a script that reads standard input
// gets EOF immediately instead of blocking forever.
//
// Env is the daemon's own environment minus LOG_FILE and BASE_DIR (see personalScriptEnv).
// Nothing is added: in particular the script does NOT get health.EnvRunID the way
// buildBackupCmd's child does, because the run id is context about the run and these scripts
// are given none. Dir is left empty, so the script runs in the daemon's own working directory.
// No arguments are passed and no shell is involved. The configured file is opened first and
// inherited by the child as descriptor 3; exec resolves /proc/self/fd/3, so replacing the
// pathname after validation cannot change the inode that runs. Args[0] remains the configured
// path for native executables.
//
// Cancel and WaitDelay are both left at their defaults, and the default is right twice over.
// exec.CommandContext already sets Cancel to Process.Kill, which is the kill-on-timeout this
// feature asks for and which no trap can catch. WaitDelay would add nothing: its two
// documented triggers are a child that fails to exit after cancellation (unreachable, the
// cancellation is SIGKILL) and a child that exits leaving I/O pipes unclosed (unreachable,
// there are no pipes).
func personalScriptCmd(ctx context.Context, path string) *exec.Cmd {
	return configurePersonalScriptCmd(exec.CommandContext(ctx, personalScriptFDPath), path)
}

// personalScriptCmdDetached is personalScriptCmd's sibling for the abandoned-child unwind: the
// same bare command, with no context attached, because there the daemon is exiting and killing
// the script is the cgroup's job. It exists as its own function so the shape assertions can
// reach it; a second inline exec.Command would be a second thing to keep in step by hand.
func personalScriptCmdDetached(path string) *exec.Cmd {
	return configurePersonalScriptCmd(exec.Command(personalScriptFDPath), path)
}

const personalScriptFDPath = "/proc/self/fd/3"

// configurePersonalScriptCmd binds execution to the file opened here, rather than leaving
// os/exec to resolve the configured pathname later in Cmd.Start. The startup inspection still
// supplies the operator-facing diagnostic; this execution-time gate protects the interval
// after startup from a foreign-owned ancestor replacing one of its descendants.
func configurePersonalScriptCmd(cmd *exec.Cmd, path string) *exec.Cmd {
	cmd.Args[0] = path
	cmd.Env = personalScriptEnv()
	if file := openPersonalScriptForExecution(path); file != nil {
		cmd.ExtraFiles = []*os.File{file}
	}
	return cmd
}

// openPersonalScriptForExecution opens the final component without following a symlink, then
// validates the opened inode itself. OpenFileUnderRoot removes the variable-path gosec sink;
// O_NONBLOCK prevents a replacement with a FIFO from parking the daemon during the check.
// Returning nil makes startPersonalScriptCmd refuse the command silently, preserving the
// personal-script error contract without ever relying on whatever descriptor 3 the daemon
// might have inherited.
func openPersonalScriptForExecution(path string) *os.File {
	if !filepath.IsAbs(path) {
		return nil
	}
	file, err := safefs.OpenFileUnderRoot(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
		}
	}()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return nil
	}
	if err := personalScriptOwnerError(path, info, os.Geteuid()); err != nil {
		return nil
	}
	keep = true
	return file
}

// startPersonalScriptCmd refuses to call Start unless configurePersonalScriptCmd supplied
// exactly one validated file, then closes the parent's copy after Start has either duplicated
// it into the child or failed. The executed script keeps descriptor 3 as required for shebang
// interpreters resolving /proc/self/fd/3.
func startPersonalScriptCmd(cmd *exec.Cmd) error {
	if len(cmd.ExtraFiles) != 1 || cmd.ExtraFiles[0] == nil {
		for _, file := range cmd.ExtraFiles {
			if file != nil {
				_ = file.Close()
			}
		}
		cmd.ExtraFiles = nil
		return os.ErrInvalid
	}

	err := cmd.Start()
	for _, file := range cmd.ExtraFiles {
		if file != nil {
			_ = file.Close()
		}
	}
	cmd.ExtraFiles = nil
	return err
}

// runPersonalScript starts one operator script, waits for it, and reports nothing at all:
// not a log line at any level, not a warning, not a metric, not a recap row, not a ping.
// Every error it can meet is dropped on purpose. A path that does not exist, is not absolute,
// is a directory, has no execute bit or fails the inode trust checks is refused before Start;
// an executable text file with no shebang fails in Start (os/exec has no shell fallback).
// The script's own exit code and a timeout kill are discarded the same way.
// Nothing here can change the run's outcome, its exit code or its log.
//
// The wait is bounded in two phases rather than by a plain cmd.Run(), for the reason spelled
// out on personalScriptReapSlack. Phase one is the whole normal case. Phase two starts the
// instant the context is done, which is the instant os/exec begins the kill, so the slack is
// only ever spent on a script that is already lost. waitCh is buffered so the abandoned
// goroutine can never block on its send if the script is somehow reaped much later, and that
// goroutine is deliberately dumb: its only statement is the send, it touches no daemon state,
// and nothing it can reach is read after we walk away.
//
// The context is rooted at context.Background() and NOT at any context of the daemon's: a
// shutdown must not silently truncate the budget the feature grants, and the helper has no
// business observing the run's lifecycle.
//
// stop bounds the WAIT, never the script. It is the daemon's shutdown signal
// (parentCtx.Done()): when it fires, the helper walks away exactly as the detached starter
// would have, and the script keeps everything the paragraph above promises it - the context
// deliberately stays uncancelled, so os/exec's watcher still delivers the 10-minute SIGKILL,
// and a script alive at unit teardown is collected by KillMode=control-group like the
// detached post-run. Without the stop arm, a shutdown landing mid-script parked the
// scheduler goroutine for up to timeout+slack, blew the stock 90-second TimeoutStopSec, and
// the daemon was SIGKILLed with the pid and info files still on disk - the exact harm the
// detached post-run path documents avoiding. A nil stop never fires.
func runPersonalScript(path string, stop <-chan struct{}) {
	// Belt and braces: the loader already trims (parsePersonalScriptSettings), so no shipped
	// path reaches here padded and no test can cover this line through the config. It stays
	// because the two starters are the boundary, and a caller that builds a value some other
	// way must not turn a blank into a fork attempt.
	path = strings.TrimSpace(path)
	if path == "" {
		return // disabled: nothing is started and the run is byte-identical to before
	}

	// The stop arms below leave ON PURPOSE without cancelling, so the script keeps
	// its budget after the wait is abandoned; the timeout's own timer releases the
	// context's resources when it fires. The guarded defer is that intent spelled
	// in a shape vet's lostcancel check can read.
	ctx, cancel := context.WithTimeout(context.Background(), personalScriptTimeout)
	abandoned := false
	defer func() {
		if !abandoned {
			cancel()
		}
	}()

	cmd := personalScriptCmd(ctx, path)
	if err := startPersonalScriptCmd(cmd); err != nil {
		return
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	select {
	case <-waitCh:
		return
	case <-stop:
		abandoned = true
		return // shutdown: abandon the wait, not the script (see the doc comment)
	case <-ctx.Done():
	}

	timer := time.NewTimer(personalScriptReapSlack)
	defer timer.Stop()
	select {
	case <-waitCh:
	case <-timer.C:
	case <-stop:
		abandoned = true
	}
}

// startPersonalScriptDetached starts the post-run script and does NOT wait for it. It serves
// the two paths where waiting would be harmful.
//
// The first is the abandoned-child unwind (the !reaped return in runOnce, which hands off to
// abandonChild): runOnce returns true, the daemon exits so systemd restarts it, and every other
// step there is bounded to 2 to 15 seconds precisely so that restart is not delayed. A waited
// script would put up to 10 minutes in front of it, on the one host whose I/O is already wedged.
//
// The second is any shutdown: a stop or a restart that lands while the run is in flight. The
// daemon's whole teardown budget is a stock TimeoutStopSec of 90 seconds, so a waited script
// would not merely be slow, it would be SIGKILLed together with the daemon, leaving .daemon.pid
// and .daemon_info.json behind and no clean-stop line. Started and left behind, the script gets
// its chance and the daemon still exits cleanly.
//
// Two consequences, both accepted: there is no 10 minute kill here, because the killer would
// die with the daemon, and the script is instead collected by the unit's default
// KillMode=control-group when the restart tears the cgroup down. Nothing is waited on and
// nothing is reaped, which costs nothing because the daemon is on its way out and the script
// is reparented to init.
//
// Like runPersonalScript, it reports nothing on any outcome.
func startPersonalScriptDetached(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	_ = startPersonalScriptCmd(personalScriptCmdDetached(path))
}
