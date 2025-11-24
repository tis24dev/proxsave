package orchestrator

import (
	"context"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"time"

	"github.com/tis24dev/proxmox-backup/internal/config"
	"github.com/tis24dev/proxmox-backup/internal/logging"
)

// FS abstracts filesystem operations to simplify testing.
type FS interface {
	Stat(path string) (os.FileInfo, error)
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
}

// Prompter encapsulates interactive prompts.
type Prompter interface {
	SelectRestoreMode(logger *logging.Logger, systemType SystemType) (RestoreMode, error)
	SelectCategories(logger *logging.Logger, available []Category, systemType SystemType) ([]Category, error)
	ConfirmRestore(logger *logging.Logger) (bool, error)
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

type osFS struct{}

func (osFS) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }
func (osFS) ReadFile(path string) ([]byte, error)  { return os.ReadFile(path) }
func (osFS) Open(path string) (*os.File, error)    { return os.Open(path) }
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

type consolePrompter struct{}

func (consolePrompter) SelectRestoreMode(logger *logging.Logger, systemType SystemType) (RestoreMode, error) {
	return ShowRestoreModeMenu(logger, systemType)
}

func (consolePrompter) SelectCategories(logger *logging.Logger, available []Category, systemType SystemType) ([]Category, error) {
	return ShowCategorySelectionMenu(logger, available, systemType)
}

func (consolePrompter) ConfirmRestore(logger *logging.Logger) (bool, error) {
	return ConfirmRestoreOperation(logger)
}

type realSystemDetector struct{}

func (realSystemDetector) DetectCurrentSystem() SystemType {
	return DetectCurrentSystem()
}

type realTimeProvider struct{}

func (realTimeProvider) Now() time.Time { return time.Now() }

type osCommandRunner struct{}

func (osCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// RunStream returns a stdout pipe for streaming commands that read from stdin.
func (osCommandRunner) RunStream(ctx context.Context, name string, stdin io.Reader, args ...string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stdout.Close()
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
		base.Config = deps.Config
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
