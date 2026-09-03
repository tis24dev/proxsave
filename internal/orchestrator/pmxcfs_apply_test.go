package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func seamPmxcfs(t *testing.T, root string, mounted bool, mountErr error) {
	t.Helper()
	origRoot, origMounted := pmxcfsRoot, pmxcfsIsMounted
	t.Cleanup(func() { pmxcfsRoot, pmxcfsIsMounted = origRoot, origMounted })
	pmxcfsRoot = root
	pmxcfsIsMounted = func(string) (bool, error) { return mounted, mountErr }
}

func useRealPmxcfsRestoreFS(t *testing.T) {
	t.Helper()
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}
}

func TestPmxcfsWriteFileWritesUnderTheRoot(t *testing.T) {
	root := t.TempDir()
	seamPmxcfs(t, root, true, nil)
	logger := logging.New(types.LogLevelDebug, false)

	if err := pmxcfsWriteFile(logger, "nodes/pve/qemu-server/101.conf", []byte("cores: 2\n")); err != nil {
		t.Fatalf("pmxcfsWriteFile: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "nodes/pve/qemu-server/101.conf"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "cores: 2\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestPmxcfsWriteFileRefusesWhenNotMounted(t *testing.T) {
	root := t.TempDir()
	seamPmxcfs(t, root, false, nil)
	logger := logging.New(types.LogLevelDebug, false)

	err := pmxcfsWriteFile(logger, "datacenter.cfg", []byte("keyboard: it\n"))
	if err == nil || !strings.Contains(err.Error(), "not mounted") {
		t.Fatalf("want a not-mounted refusal, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "datacenter.cfg")); !os.IsNotExist(statErr) {
		t.Fatalf("a shadow write happened despite the guard: %v", statErr)
	}
}

func TestPmxcfsWriteFileSurfacesTheMountCheckError(t *testing.T) {
	seamPmxcfs(t, t.TempDir(), false, errors.New("proc unreadable"))
	logger := logging.New(types.LogLevelDebug, false)

	err := pmxcfsWriteFile(logger, "datacenter.cfg", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "proc unreadable") {
		t.Fatalf("want the mount-check cause surfaced, got %v", err)
	}
}

func TestPmxcfsWriteFileRejectsUnsafeRelativePaths(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	tests := []struct {
		name    string
		relPath func(base string) string
	}{
		{name: "empty", relPath: func(string) string { return "" }},
		{name: "current directory", relPath: func(string) string { return "." }},
		{name: "parent traversal", relPath: func(string) string { return "../outside.conf" }},
		{name: "nested parent traversal", relPath: func(string) string { return "nodes/../../outside.conf" }},
		{name: "absolute", relPath: func(base string) string { return filepath.Join(base, "absolute.conf") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "pmxcfs")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatalf("create pmxcfs root: %v", err)
			}
			seamPmxcfs(t, root, true, nil)
			useRealPmxcfsRestoreFS(t)

			err := pmxcfsWriteFile(logger, tt.relPath(base), []byte("unsafe"))
			if err == nil || !strings.Contains(err.Error(), "invalid pmxcfs path") {
				t.Fatalf("want an invalid-path refusal, got %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(base, "outside.conf")); !os.IsNotExist(statErr) {
				t.Fatalf("a parent traversal created a file outside pmxcfs: %v", statErr)
			}
			if _, statErr := os.Stat(filepath.Join(base, "absolute.conf")); !os.IsNotExist(statErr) {
				t.Fatalf("an absolute path created a file outside pmxcfs: %v", statErr)
			}
		})
	}
}

func TestPmxcfsWriteFileRefusesEscapingSymlink(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "pmxcfs")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}
	seamPmxcfs(t, root, true, nil)
	useRealPmxcfsRestoreFS(t)
	logger := logging.New(types.LogLevelDebug, false)

	err := pmxcfsWriteFile(logger, "escape/guest.conf", []byte("unsafe"))
	if err == nil {
		t.Fatal("want an escaping-symlink refusal, got nil")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "guest.conf")); !os.IsNotExist(statErr) {
		t.Fatalf("an escaping symlink created a file outside pmxcfs: %v", statErr)
	}
}

func TestPmxcfsWriteFileRefusesReplacedMountRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "pmxcfs")
	detached := filepath.Join(base, "pmxcfs-detached")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create pmxcfs root: %v", err)
	}

	origRoot, origMounted, origFS := pmxcfsRoot, pmxcfsIsMounted, restoreFS
	t.Cleanup(func() {
		pmxcfsRoot, pmxcfsIsMounted, restoreFS = origRoot, origMounted, origFS
	})
	pmxcfsRoot = root
	restoreFS = osFS{}
	pmxcfsIsMounted = func(string) (bool, error) {
		if err := os.Rename(root, detached); err != nil {
			return false, err
		}
		if err := os.Mkdir(root, 0o755); err != nil {
			return false, err
		}
		return true, nil
	}
	logger := logging.New(types.LogLevelDebug, false)

	err := pmxcfsWriteFile(logger, "nodes/pve/qemu-server/101.conf", []byte("unsafe"))
	if err == nil || !strings.Contains(err.Error(), "changed while preparing write") {
		t.Fatalf("want a mount-root identity refusal, got %v", err)
	}
	for _, path := range []string{
		filepath.Join(root, "nodes/pve/qemu-server/101.conf"),
		filepath.Join(detached, "nodes/pve/qemu-server/101.conf"),
	} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("mount replacement received a write at %s: %v", path, statErr)
		}
	}
}
