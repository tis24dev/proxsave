package orchestrator

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestNewWithDepsInjectsDepsAndSetsGlobals(t *testing.T) {
	origRestoreFS := restoreFS
	origCompatFS := compatFS
	origRestoreTime := restoreTime
	origRestorePrompter := restorePrompter
	origRestoreCmd := restoreCmd
	origRestoreSystem := restoreSystem
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		compatFS = origCompatFS
		restoreTime = origRestoreTime
		restorePrompter = origRestorePrompter
		restoreCmd = origRestoreCmd
		restoreSystem = origRestoreSystem
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	fakeTime := &FakeTime{Current: time.Unix(1700000000, 0)}
	fakePrompter := &FakePrompter{
		Mode:    RestoreModeFull,
		Confirm: true,
	}
	fakeCmd := &FakeCommandRunner{}
	fakeSys := FakeSystemDetector{Type: SystemTypePBS}

	cfg := &config.Config{DryRun: true}
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	o := NewWithDeps(Deps{
		Logger:   logger,
		Config:   cfg,
		FS:       fakeFS,
		Prompter: fakePrompter,
		System:   fakeSys,
		Time:     fakeTime,
		Command:  fakeCmd,
	})

	if o == nil {
		t.Fatalf("NewWithDeps returned nil")
	}
	if o.cfg != cfg {
		t.Fatalf("orchestrator cfg mismatch")
	}
	if !o.dryRun {
		t.Fatalf("dryRun = false; want true (from config)")
	}
	if o.fs != fakeFS {
		t.Fatalf("fs not injected")
	}
	if o.prompter != fakePrompter {
		t.Fatalf("prompter not injected")
	}
	if o.clock != fakeTime {
		t.Fatalf("time provider not injected")
	}
	if o.cmdRunner != fakeCmd {
		t.Fatalf("command runner not injected")
	}
	if got := o.system.DetectCurrentSystem(); got != SystemTypePBS {
		t.Fatalf("system detector returned %q; want %q", got, SystemTypePBS)
	}

	// NewWithDeps also updates restore globals (used by restore/decrypt helpers).
	if restoreFS != fakeFS {
		t.Fatalf("restoreFS not updated by NewWithDeps")
	}
	if compatFS != fakeFS {
		t.Fatalf("compatFS not updated by NewWithDeps")
	}
	if restoreTime != fakeTime {
		t.Fatalf("restoreTime not updated by NewWithDeps")
	}
	if restorePrompter != fakePrompter {
		t.Fatalf("restorePrompter not updated by NewWithDeps")
	}
	if restoreCmd != fakeCmd {
		t.Fatalf("restoreCmd not updated by NewWithDeps")
	}
	if got := restoreSystem.DetectCurrentSystem(); got != SystemTypePBS {
		t.Fatalf("restoreSystem returned %q; want %q", got, SystemTypePBS)
	}
}

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
		defer r.Close()

		mode, err := (consolePrompter{}).SelectRestoreMode(logger, SystemTypePVE)
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
		defer r.Close()

		cats, err := (consolePrompter{}).SelectCategories(logger, available, SystemTypePVE)
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
		defer r.Close()

		ok, err := (consolePrompter{}).ConfirmRestore(logger)
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

func TestRealSystemDetectorUsesCompatFS(t *testing.T) {
	orig := compatFS
	t.Cleanup(func() { compatFS = orig })

	fake := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fake.Root) })
	compatFS = fake

	if err := fake.AddDir(filepath.Join(string(os.PathSeparator), "etc", "pve")); err != nil {
		t.Fatalf("AddDir: %v", err)
	}

	got := (realSystemDetector{}).DetectCurrentSystem()
	if got != SystemTypePVE {
		t.Fatalf("DetectCurrentSystem()=%q; want %q", got, SystemTypePVE)
	}
}
