package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

// personalScriptOpenTimeout bounds the OPEN of the script, which happens before the timeout
// context and the stop channel are ever consulted and is therefore the one step of this file
// that runs unprotected on the caller's goroutine.
//
// It is needed because os.OpenRoot, which safefs.OpenFileUnderRoot uses to confine the open,
// opens the PARENT DIRECTORY with a plain blocking open(2) (os/root_unix.go, wrapped in
// ignoringEINTR so a signal only makes it retry). The O_NONBLOCK below reaches the final
// component alone, so it does nothing for a parent that is a FIFO or lives on a mount that
// stopped answering. Without a bound, that open wedges the scheduler goroutine for good: the
// harm superviseChild's comment describes, reached by a path outside its protection.
//
// 5 seconds, the value and the reasoning of cronProbeTimeout: not an I/O budget, the line
// between "slow" and "never". A healthy open measured p50 33us and p99 217us over 1000 runs,
// four orders of magnitude below it, and the asymmetry runs the same way - expiring refuses one
// script for one run, while too tight a bound would silently skip a script on a working but
// slow mount. A var only so a test can shrink it.
var personalScriptOpenTimeout = 5 * time.Second

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
func personalScriptCmd(ctx context.Context, path string) (*exec.Cmd, error) {
	return configurePersonalScriptCmd(exec.CommandContext(ctx, personalScriptFDPath), path)
}

// personalScriptCmdDetached is personalScriptCmd's sibling for the abandoned-child unwind: the
// same bare command, with no context attached, because there the daemon is exiting and killing
// the script is the cgroup's job. It exists as its own function so the shape assertions can
// reach it; a second inline exec.Command would be a second thing to keep in step by hand.
func personalScriptCmdDetached(path string) (*exec.Cmd, error) {
	return configurePersonalScriptCmd(exec.Command(personalScriptFDPath), path)
}

const personalScriptFDPath = "/proc/self/fd/3"

// configurePersonalScriptCmd binds execution to the file opened here, rather than leaving
// os/exec to resolve the configured pathname later in Cmd.Start. The startup inspection still
// supplies the operator-facing diagnostic; this execution-time gate protects the interval
// after startup from a foreign-owned ancestor replacing one of its descendants.
//
// It RETURNS the refusal rather than reporting it, and the command alongside it stays usable
// only when there is none. Returning a value is not a breach of the silence rule this file is
// built on: nothing here writes anywhere, and the caller that does the writing lives in
// personal_scripts_gate.go, which is where every loud thing about this feature already lives.
func configurePersonalScriptCmd(cmd *exec.Cmd, path string) (*exec.Cmd, error) {
	cmd.Args[0] = path
	cmd.Env = personalScriptEnv()
	file, err := openPersonalScriptForExecution(path)
	if err != nil {
		return cmd, err
	}
	cmd.ExtraFiles = []*os.File{file}
	return cmd, nil
}

// personalScriptProbes holds the script paths whose parent-directory open was abandoned by
// personalScriptOpenTimeout and has not come back. It is what keeps that abandonment from
// being a leak.
//
// probeWithin bounds the WAIT, never the open, because a blocking open(2) cannot be cancelled
// at all. One abandoned goroutine is the price of not wedging the scheduler and it buys a great
// deal; one PER INVOCATION is a different thing, because a goroutine parked in a syscall holds
// an OS thread and the Go runtime aborts the process at 10000 of them (runtime.maxmcount). The
// daemon reaches here twice per backup run, pre and post, for as long as the condition lasts,
// and nothing else counts them.
//
// So a path gets one outstanding probe and no more: while the previous one is still parked,
// the script is refused without launching a second. The claim is released from inside the
// probe, so the bound lifts itself the moment the open finally returns - which is what a
// recovered NFS or CIFS mount does, and that mount is the reachable way into this state.
var personalScriptProbes = struct {
	sync.Mutex
	inFlight map[string]bool
}{inFlight: make(map[string]bool)}

// claimPersonalScriptProbe reserves the single outstanding probe for path, reporting whether
// this caller got it. A false answer means a previous open for that same path is still parked.
func claimPersonalScriptProbe(path string) bool {
	personalScriptProbes.Lock()
	defer personalScriptProbes.Unlock()
	if personalScriptProbes.inFlight[path] {
		return false
	}
	personalScriptProbes.inFlight[path] = true
	return true
}

// releasePersonalScriptProbe gives the claim back. It is called from inside the probe rather
// than from openPersonalScriptForExecution, because the claim has to outlive the caller
// exactly as long as the open outlives it.
func releasePersonalScriptProbe(path string) {
	personalScriptProbes.Lock()
	defer personalScriptProbes.Unlock()
	delete(personalScriptProbes.inFlight, path)
}

// openPersonalScriptForExecution opens the final component without following a symlink, then
// validates the opened inode itself. OpenFileUnderRoot removes the variable-path gosec sink;
// O_NONBLOCK stops a final component replaced by a FIFO from parking the check, and
// personalScriptOpenTimeout bounds the parent-directory open that flag cannot reach.
// A non-nil error makes startPersonalScriptCmd refuse the command, preserving the
// personal-script error contract without ever relying on whatever descriptor 3 the daemon
// might have inherited.
//
// Each refusal carries its own sentence because the caller reports it and the sentences do not
// mean the same thing to the operator. "Owned by uid 1000" says the inode the daemon was told
// to trust was replaced by one it will not run; "could not be opened" says it is gone. Both end
// in the script not running, which is exactly why a single shared wording would be useless.
//
// The wording is deliberately NOT personalScriptOwnerError's: that one closes with advice for an
// operator configuring a path at daemon start, and here the path was already accepted. What
// changed is the file.
func openPersonalScriptForExecution(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%s is not an absolute path", path)
	}
	if !claimPersonalScriptProbe(path) {
		return nil, fmt.Errorf("a previous open of %s has not returned", path)
	}
	// probeWithin's contract is the one this needs: the goroutine it gives up on is abandoned,
	// not cancelled, because a blocking open cannot be cancelled at all. If that open ever does
	// return, its *os.File is left in the buffered channel nobody reads and the runtime
	// finalizer os.File carries closes the descriptor, so the straggler needs no drain here.
	file, answered := probeWithin(personalScriptOpenTimeout, func() *os.File {
		defer releasePersonalScriptProbe(path)
		opened, err := safefs.OpenFileUnderRoot(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil
		}
		return opened
	})
	if !answered {
		return nil, fmt.Errorf("opening %s did not complete within %s", path, personalScriptOpenTimeout)
	}
	if file == nil {
		return nil, fmt.Errorf("%s could not be opened", path)
	}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
		}
	}()

	info, err := file.Stat()
	switch {
	case err != nil:
		return nil, fmt.Errorf("%s could not be inspected after opening it: %w", path, err)
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", path)
	case info.Mode().Perm()&0o111 == 0:
		return nil, fmt.Errorf("%s is not executable (mode %04o)", path, info.Mode().Perm())
	case info.Mode().Perm()&0o022 != 0:
		return nil, fmt.Errorf("%s is writable by group or others (mode %04o)", path, info.Mode().Perm())
	}
	if uid, ownerErr := personalScriptOwnerUID(path, info); ownerErr != nil {
		return nil, ownerErr
	} else if daemonUID := os.Geteuid(); uid != 0 && int(uid) != daemonUID {
		return nil, fmt.Errorf("%s is owned by uid %d; accepted owners are root or daemon uid %d", path, uid, daemonUID)
	}
	keep = true
	return file, nil
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
// Nothing here can change the run's outcome, its exit code or its log.
//
// It RETURNS one thing and drops everything else, and the split is the whole point. A refusal by
// the per-run gate - the path is not absolute, the file is gone, the open did not come back, the
// opened inode is not a regular executable owned by root or the daemon - is returned, because it
// says THE SCRIPT DID NOT RUN and the operator has no other way to learn that. Everything the
// script itself does is still dropped: an executable text file with no shebang fails in Start
// (os/exec has no shell fallback), and its exit code and a timeout kill go the same way.
//
// The caller decides what to do with the returned refusal; this file still writes nowhere.
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
func runPersonalScript(path string, stop <-chan struct{}) error {
	// Belt and braces: the loader already trims (parsePersonalScriptSettings), so no shipped
	// path reaches here padded and no test can cover this line through the config. It stays
	// because the two starters are the boundary, and a caller that builds a value some other
	// way must not turn a blank into a fork attempt.
	path = strings.TrimSpace(path)
	if path == "" {
		return nil // disabled: nothing is started and the run is byte-identical to before
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

	cmd, refusal := personalScriptCmd(ctx, path)
	if refusal != nil {
		return refusal
	}
	if err := startPersonalScriptCmd(cmd); err != nil {
		// The gate passed and the fork did not. That is the script's own failure to be
		// runnable - a missing shebang is the shipped example - and the contract drops it.
		return nil
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	select {
	case <-waitCh:
		return nil
	case <-stop:
		abandoned = true
		return nil // shutdown: abandon the wait, not the script (see the doc comment)
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
	return nil
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
// Like runPersonalScript, it reports nothing on any outcome, and like it, it returns a per-run
// gate refusal for its caller to report. A daemon on its way out still owes the operator the
// same sentence: the post script did not run, and here is what stopped it.
func startPersonalScriptDetached(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	cmd, refusal := personalScriptCmdDetached(path)
	if refusal != nil {
		return refusal
	}
	_ = startPersonalScriptCmd(cmd)
	return nil
}
