package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxmox-backup/internal/logging"
)

// FakeFS is a sandboxed filesystem rooted at a temporary directory.
// Paths are mapped under Root to avoid touching the real FS.
type FakeFS struct {
	Root       string
	StatErr    map[string]error
	StatErrors map[string]error
	WriteErr   error
}

func NewFakeFS() *FakeFS {
	root, _ := os.MkdirTemp("", "fakefs-*")
	return &FakeFS{
		Root:       root,
		StatErr:    make(map[string]error),
		StatErrors: make(map[string]error),
	}
}

func (f *FakeFS) onDisk(path string) string {
	clean := filepath.Clean(path)
	clean = strings.TrimPrefix(clean, string(filepath.Separator))
	return filepath.Join(f.Root, clean)
}

// AddFile creates a file with content.
func (f *FakeFS) AddFile(path string, content []byte) error {
	return f.WriteFile(path, content, 0o640)
}

// AddDir ensures a directory exists.
func (f *FakeFS) AddDir(path string) error {
	return f.MkdirAll(path, 0o755)
}

func (f *FakeFS) Stat(path string) (os.FileInfo, error) {
	if err, ok := f.StatErr[filepath.Clean(path)]; ok {
		return nil, err
	}
	if err, ok := f.StatErrors[filepath.Clean(path)]; ok {
		return nil, err
	}
	return os.Stat(f.onDisk(path))
}

func (f *FakeFS) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(f.onDisk(path))
}

func (f *FakeFS) Open(path string) (*os.File, error) {
	return os.Open(f.onDisk(path))
}

func (f *FakeFS) OpenFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(f.onDisk(path), flag, perm)
}

func (f *FakeFS) Create(name string) (*os.File, error) {
	return os.Create(f.onDisk(name))
}

func (f *FakeFS) WriteFile(path string, data []byte, perm os.FileMode) error {
	if f.WriteErr != nil {
		return f.WriteErr
	}
	if err := os.MkdirAll(filepath.Dir(f.onDisk(path)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(f.onDisk(path), data, perm)
}

func (f *FakeFS) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(f.onDisk(path), perm)
}

func (f *FakeFS) Remove(path string) error {
	return os.Remove(f.onDisk(path))
}

func (f *FakeFS) RemoveAll(path string) error {
	return os.RemoveAll(f.onDisk(path))
}

func (f *FakeFS) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(f.onDisk(path))
}

func (f *FakeFS) Link(oldname, newname string) error {
	return os.Link(f.onDisk(oldname), f.onDisk(newname))
}

func (f *FakeFS) Symlink(oldname, newname string) error {
	return os.Symlink(f.onDisk(oldname), f.onDisk(newname))
}

func (f *FakeFS) Readlink(path string) (string, error) {
	target, err := os.Readlink(f.onDisk(path))
	if err != nil {
		return "", err
	}
	return target, nil
}

func (f *FakeFS) CreateTemp(dir, pattern string) (*os.File, error) {
	if dir == "" {
		dir = f.Root
	} else {
		dir = f.onDisk(dir)
	}
	return os.CreateTemp(dir, pattern)
}

func (f *FakeFS) MkdirTemp(dir, pattern string) (string, error) {
	if dir == "" {
		dir = f.Root
	} else {
		dir = f.onDisk(dir)
	}
	return os.MkdirTemp(dir, pattern)
}

func (f *FakeFS) Rename(oldpath, newpath string) error {
	return os.Rename(f.onDisk(oldpath), f.onDisk(newpath))
}

// FakeTime provides deterministic time.
type FakeTime struct {
	Current time.Time
}

func (f *FakeTime) Now() time.Time {
	return f.Current
}

func (f *FakeTime) Advance(d time.Duration) {
	f.Current = f.Current.Add(d)
}

// FakeCommandRunner records invocations and returns predefined outputs/errors.
type FakeCommandRunner struct {
	Outputs map[string][]byte
	Errors  map[string]error
	Calls   []string
}

func (f *FakeCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := commandKey(name, args)
	f.Calls = append(f.Calls, key)
	if err, ok := f.Errors[key]; ok {
		return nil, err
	}
	if out, ok := f.Outputs[key]; ok {
		return out, nil
	}
	return nil, nil
}

func (f *FakeCommandRunner) ExpectCommand(cmd string, output []byte) {
	if f.Outputs == nil {
		f.Outputs = make(map[string][]byte)
	}
	f.Outputs[cmd] = output
}

func (f *FakeCommandRunner) CallsList() []string {
	return append([]string(nil), f.Calls...)
}

func commandKey(name string, args []string) string {
	if len(args) == 0 {
		return name
	}
	return fmt.Sprintf("%s %s", name, strings.Join(args, " "))
}

// FakePrompter simula le scelte utente.
type FakePrompter struct {
	Mode       RestoreMode
	Categories []Category
	Confirm    bool
	Err        error
}

func (f *FakePrompter) SelectRestoreMode(logger *logging.Logger, systemType SystemType) (RestoreMode, error) {
	return f.Mode, f.Err
}

func (f *FakePrompter) SelectCategories(logger *logging.Logger, available []Category, systemType SystemType) ([]Category, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Categories, nil
}

func (f *FakePrompter) ConfirmRestore(logger *logging.Logger) (bool, error) {
	return f.Confirm, f.Err
}

// FakeSystemDetector lets tests control the reported system type.
type FakeSystemDetector struct {
	Type SystemType
}

func (f FakeSystemDetector) DetectCurrentSystem() SystemType {
	return f.Type
}

// FakeCommandStreamRunner extends FakeCommandRunner with streaming support.
func (f *FakeCommandRunner) RunStream(ctx context.Context, name string, stdin io.Reader, args ...string) (io.ReadCloser, error) {
	out, err := f.Run(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(string(out))), nil
}
