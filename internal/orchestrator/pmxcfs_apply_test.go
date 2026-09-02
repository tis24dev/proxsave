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
