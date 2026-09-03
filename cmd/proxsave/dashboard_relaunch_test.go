package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

func TestRelaunchInstalledDashboardStartsBareWithInheritedStreams(t *testing.T) {
	orig := dashboardRelaunchCommandContext
	t.Cleanup(func() { dashboardRelaunchCommandContext = orig })

	var gotName string
	var gotArgs []string
	var gotCmd *exec.Cmd
	dashboardRelaunchCommandContext = func(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		gotCmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDashboardRelaunchHelperProcess$")
		gotCmd.Env = append(os.Environ(), "PROXSAVE_TEST_DASHBOARD_RELAUNCH=success")
		return gotCmd, nil
	}

	if err := relaunchInstalledDashboard(context.Background(), "/opt/proxsave/proxsave"); err != nil {
		t.Fatalf("relaunchInstalledDashboard: %v", err)
	}
	if gotName != "/opt/proxsave/proxsave" || len(gotArgs) != 0 {
		t.Fatalf("relaunch command = %q %q, want installed binary with no flags", gotName, gotArgs)
	}
	if gotCmd.Stdin != os.Stdin || gotCmd.Stdout != os.Stdout || gotCmd.Stderr != os.Stderr {
		t.Fatal("replacement dashboard must inherit all standard streams")
	}
}

func TestRelaunchInstalledDashboardReportsSetupAndChildErrors(t *testing.T) {
	t.Run("empty executable", func(t *testing.T) {
		orig := dashboardRelaunchCommandContext
		t.Cleanup(func() { dashboardRelaunchCommandContext = orig })
		called := false
		dashboardRelaunchCommandContext = func(context.Context, string, ...string) (*exec.Cmd, error) {
			called = true
			return nil, nil
		}
		if err := relaunchInstalledDashboard(context.Background(), "  "); err == nil {
			t.Fatal("empty executable path must fail")
		}
		if called {
			t.Fatal("empty executable path must fail before command construction")
		}
	})

	t.Run("command construction", func(t *testing.T) {
		orig := dashboardRelaunchCommandContext
		t.Cleanup(func() { dashboardRelaunchCommandContext = orig })
		want := errors.New("invalid executable")
		dashboardRelaunchCommandContext = func(context.Context, string, ...string) (*exec.Cmd, error) {
			return nil, want
		}
		if err := relaunchInstalledDashboard(context.Background(), "/opt/proxsave/proxsave"); !errors.Is(err, want) {
			t.Fatalf("construction error = %v, want %v", err, want)
		}
	})

	t.Run("child exit", func(t *testing.T) {
		orig := dashboardRelaunchCommandContext
		t.Cleanup(func() { dashboardRelaunchCommandContext = orig })
		dashboardRelaunchCommandContext = func(ctx context.Context, _ string, _ ...string) (*exec.Cmd, error) {
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDashboardRelaunchHelperProcess$")
			cmd.Env = append(os.Environ(), "PROXSAVE_TEST_DASHBOARD_RELAUNCH=failure")
			return cmd, nil
		}
		if err := relaunchInstalledDashboard(context.Background(), "/opt/proxsave/proxsave"); err == nil {
			t.Fatal("non-zero child exit must fail")
		}
	})
}

func TestRelaunchDashboardErrorIsInsideDebugWorkflow(t *testing.T) {
	orig := dashboardRelaunchCommandContext
	t.Cleanup(func() { dashboardRelaunchCommandContext = orig })
	dashboardRelaunchCommandContext = func(context.Context, string, ...string) (*exec.Cmd, error) {
		return nil, errors.New("construction boom")
	}

	bootstrap := logging.NewBootstrapLogger()
	bootstrap.SetLevel(types.LogLevelDebug)
	bootstrap.SetConsoleQuiet(true)
	var buf bytes.Buffer
	mirror := logging.New(types.LogLevelDebug, false)
	mirror.SetOutput(&buf)
	bootstrap.SetMirrorLogger(mirror)

	relaunchDashboardAfterUpgrade(context.Background(), "/opt/proxsave/proxsave", bootstrap)
	out := buf.String()
	start := strings.Index(out, "Start dashboard relaunch")
	visibleErr := strings.Index(out, "Dashboard reload failed after upgrade: construction boom")
	end := strings.Index(out, "End dashboard relaunch (error=construction boom")
	if start < 0 || visibleErr < 0 || end < 0 || !(start < visibleErr && visibleErr < end) {
		t.Fatalf("relaunch log order must be DEBUG start, timestamped error, DEBUG end; got:\n%s", out)
	}
	for _, marker := range []string{"Start dashboard relaunch", "Dashboard reload failed after upgrade", "End dashboard relaunch"} {
		lineStart := strings.LastIndex(out[:strings.Index(out, marker)], "\n") + 1
		line := out[lineStart:]
		if len(line) < 21 || line[0] != '[' || line[20] != ']' {
			t.Fatalf("log line for %q lacks the standard timestamp prefix: %q", marker, line)
		}
	}
}

func TestCloseDashboardAndRelaunchClosesSessionFirst(t *testing.T) {
	orig := dashboardRelaunchAfterUpgrade
	t.Cleanup(func() { dashboardRelaunchAfterUpgrade = orig })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var output bytes.Buffer
	session := shell.StartObservedForTest(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Dashboard"}, &output, nil)
	called := false
	dashboardRelaunchAfterUpgrade = func(context.Context, string, *logging.BootstrapLogger) {
		called = true
		select {
		case <-session.Done():
		default:
			t.Fatal("dashboard session was still active when replacement process started")
		}
	}

	closeDashboardAndRelaunch(context.Background(), session, "/opt/proxsave/proxsave", nil)
	if !called {
		t.Fatal("replacement process was not started")
	}
}

func TestDashboardRelaunchHelperProcess(t *testing.T) {
	switch os.Getenv("PROXSAVE_TEST_DASHBOARD_RELAUNCH") {
	case "success":
		os.Exit(0)
	case "failure":
		os.Exit(23)
	}
}
