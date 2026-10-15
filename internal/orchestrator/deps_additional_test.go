package orchestrator

import (
	"context"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestConsolePrompterWrappers(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	old := os.Stdin
	t.Cleanup(func() { os.Stdin = old })

	t.Run("SelectRestoreMode", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe: %v", err)
		}
		_, _ = w.WriteString("1\n")
		_ = w.Close()
		os.Stdin = r
		defer func() { _ = r.Close() }()

		mode, err := (consolePrompter{}).SelectRestoreMode(context.Background(), logger, SystemTypePVE)
		if err != nil {
			t.Fatalf("SelectRestoreMode error: %v", err)
		}
		if mode != RestoreModeFull {
			t.Fatalf("mode=%q; want %q", mode, RestoreModeFull)
		}
	})

	t.Run("SelectCategories", func(t *testing.T) {
		available := []Category{
			{ID: "pve_cluster", Name: "Cluster", Type: CategoryTypePVE, IsAvailable: true},
			{ID: "network", Name: "Network", Type: CategoryTypeCommon, IsAvailable: true},
			{ID: "ssh", Name: "SSH", Type: CategoryTypeCommon, IsAvailable: true},
		}

		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe: %v", err)
		}
		_, _ = w.WriteString("a\nc\n")
		_ = w.Close()
		os.Stdin = r
		defer func() { _ = r.Close() }()

		cats, err := (consolePrompter{}).SelectCategories(context.Background(), logger, available, SystemTypePVE)
		if err != nil {
			t.Fatalf("SelectCategories error: %v", err)
		}
		if len(cats) != 3 {
			t.Fatalf("len(categories)=%d; want 3", len(cats))
		}
	})

	t.Run("ConfirmRestore", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe: %v", err)
		}
		_, _ = w.WriteString("RESTORE\n")
		_ = w.Close()
		os.Stdin = r
		defer func() { _ = r.Close() }()

		ok, err := (consolePrompter{}).ConfirmRestore(context.Background(), logger)
		if err != nil {
			t.Fatalf("ConfirmRestore error: %v", err)
		}
		if !ok {
			t.Fatalf("ConfirmRestore returned false; want true")
		}
	})
}

func TestOSCommandRunner_RunAndRunStream(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX commands")
	}

	runner := osCommandRunner{}

	out, err := runner.Run(context.Background(), "sh", "-c", "printf hello")
	if err != nil {
		t.Fatalf("Run error: %v (out=%q)", err, string(out))
	}
	if string(out) != "hello" {
		t.Fatalf("Run output=%q; want %q", string(out), "hello")
	}

	stream, err := runner.RunStream(context.Background(), "cat", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("RunStream error: %v", err)
	}
	data, err := io.ReadAll(stream)
	if err != nil {
		_ = stream.Close()
		t.Fatalf("ReadAll error: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if string(data) != "payload" {
		t.Fatalf("RunStream output=%q; want %q", string(data), "payload")
	}
}

// TestRealSystemDetectorUsesTheDetectionLadder: the injected detector must reach the
// same rule the backup side uses. It used to read /etc/pve off compatFS, which is how
// restore ended up with a second, weaker opinion about what this host is.
func TestRealSystemDetectorUsesTheDetectionLadder(t *testing.T) {
	stubDetection(t, &environment.EnvironmentInfo{Type: types.ProxmoxVE}, nil)

	got := (realSystemDetector{}).DetectCurrentSystem()
	if got != SystemTypePVE {
		t.Fatalf("DetectCurrentSystem()=%q; want %q", got, SystemTypePVE)
	}
}
