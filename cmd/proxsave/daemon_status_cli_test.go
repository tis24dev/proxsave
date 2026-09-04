package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestParseProcEffectiveUIDUsesTheEffectiveColumn(t *testing.T) {
	data := []byte("Name:\tproxsave\nUid:\t1000\t1001\t1002\t1003\n")
	uid, err := parseProcEffectiveUID(data)
	if err != nil {
		t.Fatalf("parse effective uid: %v", err)
	}
	if uid != 1001 {
		t.Fatalf("effective uid = %d, want 1001", uid)
	}
}

func TestParseProcEffectiveUIDHandlesMaximumLinuxUIDWithoutOverflow(t *testing.T) {
	data := []byte("Uid:\t0\t4294967295\t0\t0\n")
	uid, err := parseProcEffectiveUID(data)
	if strconv.IntSize == 32 {
		if err == nil {
			t.Fatalf("effective uid overflowed native int as %d", uid)
		}
		return
	}
	if err != nil {
		t.Fatalf("parse maximum Linux uid: %v", err)
	}
	if uint64(uid) != 4294967295 {
		t.Fatalf("effective uid = %d, want 4294967295", uid)
	}
}

func TestParseProcEffectiveUIDRejectsMissingAndMalformedData(t *testing.T) {
	for name, data := range map[string]string{
		"missing":     "Name:\tproxsave\n",
		"too short":   "Uid:\t1000\n",
		"not numeric": "Uid:\t1000\toperator\t1002\t1003\n",
		"negative":    "Uid:\t0\t-1\t0\t0\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseProcEffectiveUID([]byte(data)); err == nil {
				t.Fatalf("parseProcEffectiveUID(%q) unexpectedly succeeded", data)
			}
		})
	}
}

func TestResolveDaemonEffectiveUIDUsesLiveProcessAndFallsBack(t *testing.T) {
	origRead := daemonProcStatusReadFile
	origEUID := daemonCurrentEUID
	t.Cleanup(func() {
		daemonProcStatusReadFile = origRead
		daemonCurrentEUID = origEUID
	})

	daemonCurrentEUID = func() int { return 9001 }
	t.Run("live pid", func(t *testing.T) {
		daemonProcStatusReadFile = func(path string) ([]byte, error) {
			if path != "/proc/42/status" {
				t.Fatalf("status path = %q", path)
			}
			return []byte("Uid:\t1000\t1001\t1002\t1003\n"), nil
		}
		got := resolveDaemonEffectiveUID(health.DaemonState{ProcessAlive: true, PID: 42})
		if got.Value != 1001 || got.Source != "running daemon /proc" || got.FallbackReason != "" {
			t.Fatalf("unexpected live daemon uid: %+v", got)
		}
	})

	t.Run("proc read failure", func(t *testing.T) {
		daemonProcStatusReadFile = func(string) ([]byte, error) { return nil, errors.New("permission denied") }
		got := resolveDaemonEffectiveUID(health.DaemonState{ProcessAlive: true, PID: 42})
		if got.Value != 9001 || got.Source != "current process" || !strings.Contains(got.FallbackReason, "permission denied") {
			t.Fatalf("unexpected read fallback: %+v", got)
		}
	})

	t.Run("no live pid", func(t *testing.T) {
		read := false
		daemonProcStatusReadFile = func(string) ([]byte, error) {
			read = true
			return nil, nil
		}
		got := resolveDaemonEffectiveUID(health.DaemonState{})
		if read {
			t.Fatal("read /proc without a live daemon pid")
		}
		if got.Value != 9001 || got.Source != "current process" || !strings.Contains(got.FallbackReason, "not live") {
			t.Fatalf("unexpected no-pid fallback: %+v", got)
		}
	})
}

func TestCollectDaemonDiagnosticsInspectsScriptsWithResolvedUID(t *testing.T) {
	origUID := daemonUIDResolver
	origScripts := personalScriptsInspector
	origInstalled := daemonStatusUnitInstalledProbe
	origActive := daemonStatusActiveStateProbe
	origPresence := daemonPresenceProbe
	t.Cleanup(func() {
		daemonUIDResolver = origUID
		personalScriptsInspector = origScripts
		daemonStatusUnitInstalledProbe = origInstalled
		daemonStatusActiveStateProbe = origActive
		daemonPresenceProbe = origPresence
	})

	daemonStatusUnitInstalledProbe = func() bool { return true }
	daemonStatusActiveStateProbe = func(context.Context) string { return "active" }
	daemonPresenceProbe = func(context.Context) health.DaemonPresence {
		return health.DaemonPresence{Probed: true, Installed: true, Active: true}
	}
	daemonUIDResolver = func(health.DaemonState) daemonUIDDiagnostic {
		return daemonUIDDiagnostic{Value: 4242, Source: "test daemon"}
	}
	inspections := 0
	personalScriptsInspector = func(cfg *config.Config, uid int) personalScriptsDiagnostics {
		inspections++
		if uid != 4242 || cfg.PersonalScriptPreRun != "/pre" || cfg.PersonalScriptPostRun != "/post" {
			t.Fatalf("inspector input: uid=%d cfg=%+v", uid, cfg)
		}
		return personalScriptsDiagnostics{
			Pre:  personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_PRE_RUN", Path: "/pre", State: personalScriptReady, DaemonUID: uid},
			Post: personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_POST_RUN", Path: "/post", State: personalScriptRefused, Reason: "unsafe", DaemonUID: uid},
		}
	}

	cfg := &config.Config{
		SchedulerMode:         "daemon",
		BaseDir:               t.TempDir(),
		PersonalScriptPreRun:  "/pre",
		PersonalScriptPostRun: "/post",
	}
	got := collectDaemonDiagnostics(context.Background(), cfg, cfg.BaseDir)
	if inspections != 1 {
		t.Fatalf("script inspections = %d, want 1", inspections)
	}
	if got.DaemonUID.Value != 4242 || got.Scripts.Pre.State != personalScriptReady || got.Scripts.Post.State != personalScriptRefused {
		t.Fatalf("collector omitted shared script diagnostics: %+v", got)
	}
}

func TestLogDaemonDiagnosticsUsesStandardEnvelopeAndSeverity(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "executed")
	script := writePersonalScript(t, dir, "must-not-run.sh", "touch "+marker)
	reason := "parent directory is unsafe"
	diagnostics := daemonDiagnostics{
		Mode:        "daemon",
		Unit:        "installed",
		Active:      "active",
		State:       health.DaemonState{HaveInfo: true, Version: "1.2.3", Commit: "abc", AlignChecked: true, Aligned: true},
		Level:       orchestrator.HealthcheckSetupLevelOk,
		Keyword:     "running",
		Explanation: "daemon is healthy",
		DaemonUID:   daemonUIDDiagnostic{Value: 1001, Source: "running daemon /proc"},
		Scripts: personalScriptsDiagnostics{
			Pre: personalScriptDiagnostic{
				Key: "PERSONAL_SCRIPT_PRE_RUN", Path: script, State: personalScriptReady, DaemonUID: 1001,
				Components: []personalScriptPathComponent{{Path: script, UID: 1001, Mode: 0o700}},
			},
			Post: personalScriptDiagnostic{
				Key: "PERSONAL_SCRIPT_POST_RUN", Path: "/home/operator/post.sh", State: personalScriptRefused, Reason: reason, DaemonUID: 1001,
			},
		},
	}

	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	logDaemonDiagnostics(logger, diagnostics)
	out := buf.String()

	start := strings.Index(out, "DEBUG    Start daemon diagnostics")
	ready := strings.Index(out, "INFO     Personal pre-run script: READY")
	refused := strings.Index(out, "WARNING  Personal post-run script: REFUSED")
	end := strings.Index(out, "DEBUG    End daemon diagnostics")
	if start < 0 || ready < 0 || refused < 0 || end < 0 || start >= ready || ready >= refused || refused >= end {
		t.Fatalf("diagnostic lines are not enclosed and ordered correctly:\n%s", out)
	}
	if strings.Count(out, reason) != 1 {
		t.Fatalf("refusal reason count = %d, want 1:\n%s", strings.Count(out, reason), out)
	}
	timestamped := regexp.MustCompile(`^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\] (DEBUG|INFO|WARNING) +`)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !timestamped.MatchString(line) {
			t.Errorf("line does not use standard CLI timestamp/severity: %q", line)
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("diagnostic rendering executed a personal script; marker stat error = %v", err)
	}
}

func TestRunDaemonStatusExitRemainsBasedOnDaemonState(t *testing.T) {
	origCollector := daemonDiagnosticsCollector
	t.Cleanup(func() { daemonDiagnosticsCollector = origCollector })
	daemonDiagnosticsCollector = func(context.Context, *config.Config, string) daemonDiagnostics {
		return daemonDiagnostics{
			Level:   orchestrator.HealthcheckSetupLevelOk,
			Keyword: "running",
			Scripts: personalScriptsDiagnostics{
				Post: personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_POST_RUN", Path: "/unsafe", State: personalScriptRefused, Reason: "unsafe"},
			},
		}
	}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&bytes.Buffer{})
	if code := runDaemonStatus(&appRuntime{ctx: context.Background(), logger: logger}); code != types.ExitSuccess.Int() {
		t.Fatalf("script refusal changed daemon-status exit code to %d", code)
	}
}

func TestBuildDaemonStatusPromptRendersSanitizedPersonalScripts(t *testing.T) {
	diagnostics := daemonDiagnostics{
		Mode:        "daemon",
		Unit:        "installed",
		Active:      "active",
		Level:       orchestrator.HealthcheckSetupLevelOk,
		Keyword:     "RUNNING",
		Explanation: "daemon is healthy",
		DaemonUID:   daemonUIDDiagnostic{Value: 1001, Source: "running daemon /proc"},
		Scripts: personalScriptsDiagnostics{
			Pre:  personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_PRE_RUN", Path: "/safe/pre.sh", State: personalScriptReady},
			Post: personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_POST_RUN", Path: "/bad\x1b]0;pwned\x07/post.sh", State: personalScriptRefused, Reason: "unsafe\x9b\x1b[2Jreason"},
		},
	}
	prompt := buildDaemonStatusPrompt(diagnostics)
	assertNoRawInjection(t, prompt)
	plain := ansi.Strip(prompt)
	for _, want := range []string{
		"Personal pre-run script: READY",
		"/safe/pre.sh",
		"Personal post-run script: REFUSED",
		"reason",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("prompt missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "pwned") {
		t.Fatalf("prompt retained OSC payload:\n%s", plain)
	}
}
