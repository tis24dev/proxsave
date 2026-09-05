package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safeexec"
)

// FS abstracts filesystem operations to simplify testing.
type FS interface {
	Stat(path string) (os.FileInfo, error)
	Lstat(path string) (os.FileInfo, error)
	ReadFile(path string) ([]byte, error)
	Open(path string) (*os.File, error)
	OpenFile(path string, flag int, perm fs.FileMode) (*os.File, error)
	Create(name string) (*os.File, error)
	WriteFile(path string, data []byte, perm fs.FileMode) error
	MkdirAll(path string, perm fs.FileMode) error
	Remove(path string) error
	RemoveAll(path string) error
	ReadDir(path string) ([]os.DirEntry, error)
	Link(oldname, newname string) error
	Symlink(oldname, newname string) error
	Readlink(path string) (string, error)
	CreateTemp(dir, pattern string) (*os.File, error)
	MkdirTemp(dir, pattern string) (string, error)
	Rename(oldpath, newpath string) error
	Lchown(path string, uid, gid int) error
	UtimesNano(path string, times []syscall.Timespec) error
}

// Prompter encapsulates interactive prompts.
type Prompter interface {
	SelectRestoreMode(ctx context.Context, logger *logging.Logger, systemType SystemType) (RestoreMode, error)
	SelectCategories(ctx context.Context, logger *logging.Logger, available []Category, systemType SystemType) ([]Category, error)
	ConfirmRestore(ctx context.Context, logger *logging.Logger) (bool, error)
}

// SystemDetector abstracts system-type detection.
type SystemDetector interface {
	DetectCurrentSystem() SystemType
}

// TimeProvider abstracts time acquisition for determinism in tests.
type TimeProvider interface {
	Now() time.Time
}

// CommandRunner executes system commands (non-bash scripts).
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Deps groups optional orchestrator dependencies.
type Deps struct {
	Logger   *logging.Logger
	Config   *config.Config
	DryRun   bool
	FS       FS
	Prompter Prompter
	System   SystemDetector
	Time     TimeProvider
	Command  CommandRunner
}

// osFS is the production passthrough for the injected FS interface. Its methods
// forward to os.* verbatim and are deliberately NOT confined through os.Root: the
// seam is used generically via restoreFS across the orchestrator with per-call
// trust contexts (absolute system paths, /sys symlinks, operator-chosen restore
// targets), and confining the generic seam would refuse legitimate reads such as
// restoreFS.ReadFile("/etc/resolv.conf") when that is a symlink into /run on a
// systemd host. Path safety is enforced at the concrete call sites where the
// trust boundary is known (see safefs.OpenFileUnderRoot / ReadFileUnderRoot).
// gosec flags the raw os.* here (G304); that is an accepted false positive at the
// dependency-injection boundary, not a suppression.
type osFS struct{}

func (osFS) Stat(path string) (os.FileInfo, error)  { return os.Stat(path) }
func (osFS) Lstat(path string) (os.FileInfo, error) { return os.Lstat(path) }
func (osFS) ReadFile(path string) ([]byte, error)   { return os.ReadFile(path) }
func (osFS) Open(path string) (*os.File, error)     { return os.Open(path) }
func (osFS) OpenFile(path string, flag int, perm fs.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag, perm)
}
func (osFS) Create(name string) (*os.File, error) { return os.Create(name) }
func (osFS) WriteFile(path string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(path, data, perm)
}
func (osFS) MkdirAll(path string, perm fs.FileMode) error { return os.MkdirAll(path, perm) }
func (osFS) Remove(path string) error                     { return os.Remove(path) }
func (osFS) RemoveAll(path string) error                  { return os.RemoveAll(path) }
func (osFS) ReadDir(path string) ([]os.DirEntry, error)   { return os.ReadDir(path) }
func (osFS) Link(oldname, newname string) error           { return os.Link(oldname, newname) }
func (osFS) Symlink(oldname, newname string) error        { return os.Symlink(oldname, newname) }
func (osFS) Readlink(path string) (string, error)         { return os.Readlink(path) }
func (osFS) CreateTemp(dir, pattern string) (*os.File, error) {
	return os.CreateTemp(dir, pattern)
}
func (osFS) MkdirTemp(dir, pattern string) (string, error) { return os.MkdirTemp(dir, pattern) }
func (osFS) Rename(oldpath, newpath string) error          { return os.Rename(oldpath, newpath) }
func (osFS) Lchown(path string, uid, gid int) error        { return os.Lchown(path, uid, gid) }
func (osFS) UtimesNano(path string, times []syscall.Timespec) error {
	return syscall.UtimesNano(path, times)
}

type consolePrompter struct{}

func (consolePrompter) SelectRestoreMode(ctx context.Context, logger *logging.Logger, systemType SystemType) (RestoreMode, error) {
	return ShowRestoreModeMenu(ctx, logger, systemType)
}

func (consolePrompter) SelectCategories(ctx context.Context, logger *logging.Logger, available []Category, systemType SystemType) ([]Category, error) {
	return ShowCategorySelectionMenu(ctx, logger, available, systemType)
}

func (consolePrompter) ConfirmRestore(ctx context.Context, logger *logging.Logger) (bool, error) {
	return ConfirmRestoreOperation(ctx, logger)
}

type realSystemDetector struct{}

func (realSystemDetector) DetectCurrentSystem() SystemType {
	return DetectCurrentSystem()
}

type realTimeProvider struct{}

func (realTimeProvider) Now() time.Time { return time.Now() }

type osCommandRunner struct{}

// defaultCommandWaitDelay is INITIALISED FROM safeexec.CommandWaitDelay, which is a
// copy taken once at package init and not a link: changing safeexec.CommandWaitDelay
// at runtime does not move this one, and a test that means to shrink both has to set
// both. What the initialiser buys is that the number lives in one place in the source.
// internal/storage/cloud.go still carries its own, for rclone, and says why there.
var defaultCommandWaitDelay = safeexec.CommandWaitDelay

func (osCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd, err := safeexec.CommandContext(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	cmd.WaitDelay = defaultCommandWaitDelay
	out, err := cmd.CombinedOutput()
	// The translation is correct HERE and only because of the sink. CombinedOutput
	// drains into a bytes.Buffer, which never blocks, so every byte the child wrote is
	// already in it before the timer can start; what the budget interrupts is the wait
	// for an EOF a surviving descendant is withholding. Measured on this path at 16
	// bytes, 64 KiB and 5 MiB: the output is byte for byte complete alongside the
	// error. So ErrWaitDelay means "something is still holding a descriptor", not
	// "your answer is short".
	//
	// Returning the error instead was tried and reverted. This []byte reaches nine bare
	// availability gates ("which pvesh", "which systemctl") and the systemd-run
	// rollback arming: each reads a non-nil error as "the tool is missing" or "arming
	// failed". A complete, correct answer turned into a failure made a restore skip
	// applying datacenter.cfg and report success, and made the network rollback arm a
	// SECOND timer on top of one it could no longer disarm.
	//
	// This is NOT the rule for every sink. internal/storage/cloud.go refuses the same
	// translation for rclone and is right to: an operation, not a capture, really is
	// incomplete when it is cut short. Do not generalise either way without checking
	// what the command's output is being copied INTO.
	if err != nil && errors.Is(err, exec.ErrWaitDelay) {
		return out, nil
	}
	return out, err
}

// RunStdout captures ONLY stdout, for a command whose output is DATA a caller
// parses rather than a transcript a caller logs.
//
// Measured on a live PVE 9.1.9 node (2026-09-05): with a locale the host does not
// have, `pvesh get /cluster/resources --output-format=json` exits 0 and writes
// perfectly valid JSON to stdout while Perl writes "perl: warning: Setting locale
// failed." to stderr. CombinedOutput merges the two, the merged bytes start with
// 'p', and every json.Unmarshal on them fails. That is not exotic: the stock
// sshd_config on that node carries `AcceptEnv LANG LC_*`, and macOS Terminal
// forwards LC_CTYPE=UTF-8 by default, so an operator running a restore over ssh
// from a Mac is enough to reach it.
//
// On failure stderr is folded into the error, because that is where pvesh puts
// its reason and dropping it would trade one silent loss for another. On success
// stderr is discarded on purpose: those are exactly the bytes that must not reach
// the parser, and no caller of this method has ever consumed them - today they
// silently corrupt the parse instead. Use Run for anything whose output is meant
// to be read by a human.
func (osCommandRunner) RunStdout(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd, err := safeexec.CommandContext(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	cmd.WaitDelay = defaultCommandWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	// Same translation Run documents at length, and for the same reason: both
	// sinks are bytes.Buffer, so every byte the child wrote is already captured
	// before the timer can start.
	if runErr != nil && errors.Is(runErr, exec.ErrWaitDelay) {
		runErr = nil
	}
	if runErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return stdout.Bytes(), fmt.Errorf("%w: %s", runErr, msg)
		}
	}
	return stdout.Bytes(), runErr
}

// RunStream returns a stdout pipe for streaming commands that read from stdin.
func (osCommandRunner) RunStream(ctx context.Context, name string, stdin io.Reader, args ...string) (io.ReadCloser, error) {
	cmd, err := safeexec.CommandContext(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	cmd.Stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		return nil, err
	}
	return &waitReadCloser{ReadCloser: stdout, wait: cmd.Wait}, nil
}

type waitReadCloser struct {
	io.ReadCloser
	wait func() error
}

func (w *waitReadCloser) Close() error {
	if w.wait == nil {
		return w.ReadCloser.Close()
	}
	if err := w.ReadCloser.Close(); err != nil {
		_ = w.wait()
		return err
	}
	return w.wait()
}

func defaultDeps(logger *logging.Logger, dryRun bool) Deps {
	return Deps{
		Logger:   logger,
		FS:       osFS{},
		Prompter: consolePrompter{},
		System:   realSystemDetector{},
		Time:     realTimeProvider{},
		Command:  osCommandRunner{},
		DryRun:   dryRun,
	}
}

// NewWithDeps builds an orchestrator using custom dependencies while preserving defaults.
func NewWithDeps(deps Deps) *Orchestrator {
	logger := deps.Logger
	if logger == nil {
		logger = logging.New(logging.GetDefaultLogger().GetLevel(), false)
	}
	base := defaultDeps(logger, deps.DryRun)

	if deps.FS != nil {
		base.FS = deps.FS
	}
	if deps.Command != nil {
		base.Command = deps.Command
	}
	if deps.Prompter != nil {
		base.Prompter = deps.Prompter
	}
	if deps.System != nil {
		base.System = deps.System
	}
	if deps.Time != nil {
		base.Time = deps.Time
	}
	if deps.Config != nil {
		base.DryRun = deps.Config.DryRun
	}

	o := New(logger, base.DryRun)
	o.fs = base.FS
	o.prompter = base.Prompter
	o.system = base.System
	o.clock = base.Time
	o.cmdRunner = base.Command
	setRestoreDeps(base.FS, base.Time, base.Prompter, base.Command, base.System)
	if deps.Config != nil {
		o.SetConfig(deps.Config)
	}
	return o
}
