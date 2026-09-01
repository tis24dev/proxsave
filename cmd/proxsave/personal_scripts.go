package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// personalScriptTimeout bounds ONE PERSONAL_SCRIPT_PRE_RUN or PERSONAL_SCRIPT_POST_RUN
// invocation. A var rather than a const only so a test can shrink it (the shape
// cronProbeTimeout uses, cron_indirect_refs.go:128); 10 minutes is the shipped value.
var personalScriptTimeout = 10 * time.Minute

// personalScriptReapSlack is how long runPersonalScript keeps waiting AFTER that timeout
// fired and os/exec SIGKILLed the script, before it stops waiting at all.
//
// It exists for the reason superviseChild exists (daemon.go:684): os/exec's Cmd.Wait does a
// wait4(2) BEFORE it ever reads the cancellation result, so neither the context nor a
// WaitDelay can unblock it. A script parked in TASK_UNINTERRUPTIBLE, one living on a dead NFS
// or CIFS mount, which is exactly the kind of place an operator keeps a personal script,
// never dequeues that SIGKILL and is never reaped, so wait4 never returns. Waiting for it
// inline would wedge the scheduler goroutine, and a script we were explicitly told not to
// care about would take the daemon down with it. 15s mirrors daemonReapSlack: a process
// SIGKILL can actually kill is reaped within microseconds, so this margin is only ever spent
// on one that is already lost.
var personalScriptReapSlack = 15 * time.Second

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
// Env is left nil, so the script inherits the daemon's environment unchanged. In particular
// it does NOT get health.EnvRunID the way buildBackupCmd's child does (daemon.go:2216): the
// run id is context about the run, and these scripts are given none. Dir is left empty, so
// the script runs in the daemon's own working directory. No arguments are passed and no
// shell is involved: the path goes to execve as it stands.
//
// Cancel and WaitDelay are both left at their defaults, and the default is right twice over.
// exec.CommandContext already sets Cancel to Process.Kill, which is the kill-on-timeout this
// feature asks for and which no trap can catch. WaitDelay would add nothing: its two
// documented triggers are a child that fails to exit after cancellation (unreachable, the
// cancellation is SIGKILL) and a child that exits leaving I/O pipes unclosed (unreachable,
// there are no pipes).
func personalScriptCmd(ctx context.Context, path string) *exec.Cmd {
	// #nosec G204 -- the path is the operator's own PERSONAL_SCRIPT_PRE_RUN or
	// PERSONAL_SCRIPT_POST_RUN value, read from the root-owned backup.env and executed
	// directly with no shell, no arguments and no injected environment. safeexec's allowlist
	// is for the external tools proxsave itself drives, and safeexec.TrustedCommandContext
	// refuses a relative or world-writable path, which under this feature's silence rule
	// would be an undebuggable non-execution of a script the operator deliberately
	// configured.
	return exec.CommandContext(ctx, path)
}

// runPersonalScript starts one operator script, waits for it, and reports nothing at all:
// not a log line at any level, not a warning, not a metric, not a recap row, not a ping.
// Every error it can meet is dropped on purpose. A path that does not exist, is a directory,
// has no execute bit, or is an executable text file with no shebang all fail identically
// inside cmd.Start (os/exec has no shell fallback), and a bare name that is not on PATH fails
// there too; the script's own exit code, and a timeout kill, are discarded the same way.
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
func runPersonalScript(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return // disabled: nothing is started and the run is byte-identical to before
	}

	ctx, cancel := context.WithTimeout(context.Background(), personalScriptTimeout)
	defer cancel()

	cmd := personalScriptCmd(ctx, path)
	if err := cmd.Start(); err != nil {
		return
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	select {
	case <-waitCh:
		return
	case <-ctx.Done():
	}

	timer := time.NewTimer(personalScriptReapSlack)
	defer timer.Stop()
	select {
	case <-waitCh:
	case <-timer.C:
	}
}

// startPersonalScriptDetached starts the post-run script and does NOT wait for it. It is used
// on the one path where waiting would be harmful: the abandoned-child unwind (daemon.go:628),
// where runOnce returns true so the daemon exits and systemd restarts it, and where every
// other step is bounded to 2 to 15 seconds precisely so that restart is not delayed. A waited
// script would put up to 10 minutes in front of it, on the one host whose I/O is already
// wedged.
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
	// #nosec G204 -- same operator-supplied path, same rationale as personalScriptCmd. No
	// context is attached on purpose: a cancel func would be pointless in a process that is
	// exiting, and killing the script is the cgroup's job here.
	cmd := exec.Command(path)
	_ = cmd.Start()
}
