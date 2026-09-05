package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestBuildDaemonRuntimeStatePreservesStartupVerdicts(t *testing.T) {
	d := &daemon{cfg: &config.Config{BaseDir: "/opt/proxsave"}, configPath: "/etc/proxsave.env"}
	daemonUID := os.Geteuid()
	scripts := personalScriptsDiagnostics{
		Pre: personalScriptDiagnostic{
			Path:      "/home/operator/pre.sh",
			State:     personalScriptReadyWithWarning,
			Reason:    "trusted administrator",
			DaemonUID: daemonUID,
			Components: []personalScriptPathComponent{
				{Path: "/home/operator/pre.sh", UID: 0, Mode: 0o755},
			},
		},
		Post: personalScriptDiagnostic{State: personalScriptNotConfigured, DaemonUID: daemonUID},
	}
	got := buildDaemonRuntimeState(d, 1_700_000_000, scripts)
	if got.SchemaVersion != health.DaemonRuntimeSchemaVersion ||
		got.PID != os.Getpid() || got.StartTS != 1_700_000_000 {
		t.Fatalf("runtime identity mismatch: %+v", got)
	}
	if got.ConfigPath != d.configPath || got.DaemonUID != daemonUID {
		t.Fatalf("runtime source mismatch: %+v", got)
	}
	if got.PersonalScripts.Pre.State != string(personalScriptReadyWithWarning) ||
		len(got.PersonalScripts.Pre.Components) != 1 {
		t.Fatalf("script mismatch: %+v", got.PersonalScripts.Pre)
	}
}

func TestPublishDaemonRuntimeWarnsButDoesNotFail(t *testing.T) {
	original := daemonRuntimeWrite
	daemonRuntimeWrite = func(string, health.DaemonRuntimeState) error {
		return errors.New("disk unavailable")
	}
	t.Cleanup(func() { daemonRuntimeWrite = original })

	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	previous := logging.GetDefaultLogger()
	logging.SetDefaultLogger(logger)
	t.Cleanup(func() { logging.SetDefaultLogger(previous) })

	d := &daemon{cfg: &config.Config{BaseDir: t.TempDir()}, configPath: "/etc/proxsave.env"}
	d.publishDaemonRuntime(100, personalScriptsDiagnostics{})
	if out := buf.String(); !strings.Contains(out, "WARNING") || !strings.Contains(out, "disk unavailable") {
		t.Fatalf("missing warning:\n%s", out)
	}
}
