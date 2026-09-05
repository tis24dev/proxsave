package main

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestBuildDaemonRuntimeStatePreservesCompletePreAndPostEvidence(t *testing.T) {
	daemonUID := os.Geteuid()
	scripts := personalScriptsDiagnostics{
		Pre: personalScriptDiagnostic{
			Key: "PERSONAL_SCRIPT_PRE_RUN", Path: "/home/operator/pre.sh", State: personalScriptReadyWithWarning,
			Reason: "foreign owned parent", DaemonUID: daemonUID,
			Components: []personalScriptPathComponent{
				{Path: "/home/operator/pre.sh", UID: 0, Mode: 0o750},
				{Path: "/home/operator", UID: 4242, Mode: os.ModeDir | 0o700},
				{Path: "/home", UID: 0, Mode: os.ModeDir | 0o755},
				{Path: "/", UID: 0, Mode: os.ModeDir | 0o755},
			},
		},
		Post: personalScriptDiagnostic{
			Key: "PERSONAL_SCRIPT_POST_RUN", Path: "/tmp/post.sh", State: personalScriptRefused,
			Reason: "foreign owned target", DaemonUID: daemonUID,
			Components: []personalScriptPathComponent{{Path: "/tmp/post.sh", UID: 4243, Mode: 0o700}},
		},
	}
	d := &daemon{cfg: &config.Config{}, configPath: "/etc/proxsave.env"}
	got := buildDaemonRuntimeState(d, 1_700_000_000, scripts)
	want := health.DaemonRuntimeState{
		SchemaVersion: health.DaemonRuntimeSchemaVersion, PID: os.Getpid(), StartTS: 1_700_000_000,
		ConfigPath: "/etc/proxsave.env", DaemonUID: daemonUID,
		PersonalScripts: health.DaemonRuntimeScripts{
			Pre: health.DaemonRuntimeScript{
				Path: "/home/operator/pre.sh", State: "ready-with-warning", Reason: "foreign owned parent",
				Components: []health.DaemonRuntimePathComponent{
					{Path: "/home/operator/pre.sh", UID: 0, Mode: 0o750},
					{Path: "/home/operator", UID: 4242, Mode: uint32(os.ModeDir | 0o700)},
					{Path: "/home", UID: 0, Mode: uint32(os.ModeDir | 0o755)},
					{Path: "/", UID: 0, Mode: uint32(os.ModeDir | 0o755)},
				},
			},
			Post: health.DaemonRuntimeScript{
				Path: "/tmp/post.sh", State: "refused", Reason: "foreign owned target",
				Components: []health.DaemonRuntimePathComponent{{Path: "/tmp/post.sh", UID: 4243, Mode: 0o700}},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime conversion lost evidence:\ngot  %+v\nwant %+v", got, want)
	}
	original := daemonRuntimeReader
	t.Cleanup(func() { daemonRuntimeReader = original })
	daemonRuntimeReader = func(string) (health.DaemonRuntimeState, bool, error) { return got, true, nil }
	runtime, restored := resolveDaemonRuntime(health.DaemonState{
		ProcessAlive: true, HaveInfo: true, PID: os.Getpid(), StartTS: 1_700_000_000,
	}, "/base")
	if runtime.Availability != daemonRuntimeAvailable || !reflect.DeepEqual(restored, scripts) {
		t.Fatalf("restored records lost evidence: runtime=%+v\ngot  %+v\nwant %+v", runtime, restored, scripts)
	}
}

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
