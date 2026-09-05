package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
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
	if got.DaemonUID.Value != 4242 || got.ScriptComparisons.Pre.Current.State != personalScriptReady || got.ScriptComparisons.Post.Current.State != personalScriptRefused {
		t.Fatalf("collector omitted shared script diagnostics: %+v", got)
	}
}

func TestCollectDaemonDiagnosticsComparesResidentReadyWithCurrentEmpty(t *testing.T) {
	// Keep a live process with the daemon's argv identity for the real liveness probe.
	resident := exec.Command("sh", "-c", "read unused", "proxsave", "--daemon")
	stdin, err := resident.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := resident.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = resident.Wait() })

	origInstalled := daemonStatusUnitInstalledProbe
	origActive := daemonStatusActiveStateProbe
	origPresence := daemonPresenceProbe
	origRead := daemonProcStatusReadFile
	t.Cleanup(func() {
		daemonStatusUnitInstalledProbe = origInstalled
		daemonStatusActiveStateProbe = origActive
		daemonPresenceProbe = origPresence
		daemonProcStatusReadFile = origRead
	})
	daemonStatusUnitInstalledProbe = func() bool { return true }
	daemonStatusActiveStateProbe = func(context.Context) string { return "active" }
	daemonPresenceProbe = func(context.Context) health.DaemonPresence {
		return health.DaemonPresence{Probed: true, Installed: true, Active: true}
	}
	daemonProcStatusReadFile = func(path string) ([]byte, error) {
		if path != "/proc/"+strconv.Itoa(resident.Process.Pid)+"/status" {
			t.Fatalf("unexpected process UID path: %s", path)
		}
		return []byte("Uid:\t1000\t4242\t1000\t1000\n"), nil
	}
	base := t.TempDir()
	cfg := &config.Config{SchedulerMode: "daemon", BaseDir: base, ConfigPath: "/daemon.env"}
	if err := health.WriteDaemonInfo(base, health.DaemonInfo{PID: resident.Process.Pid, StartTS: 100}); err != nil {
		t.Fatal(err)
	}
	if err := health.WriteDaemonRuntime(base, health.DaemonRuntimeState{
		SchemaVersion: health.DaemonRuntimeSchemaVersion, PID: resident.Process.Pid, StartTS: 100, ConfigPath: "/daemon.env", DaemonUID: 1234,
		PersonalScripts: health.DaemonRuntimeScripts{
			Pre:  health.DaemonRuntimeScript{Path: "/pre", State: "ready"},
			Post: health.DaemonRuntimeScript{State: "not-configured"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	got := collectDaemonDiagnostics(context.Background(), cfg, "/ignored-base")
	if got.Runtime.Availability != daemonRuntimeAvailable || !got.State.ProcessAlive {
		t.Fatalf("resident state unavailable: %+v", got)
	}
	pre := got.ScriptComparisons.Pre
	if pre.Running.Path != "/pre" || pre.Running.State != personalScriptReady || pre.Current.Path != "" || pre.Current.State != personalScriptNotConfigured || pre.Synchronization != personalScriptConfigurationDrift {
		t.Fatalf("resident-ready/current-empty drift lost: %+v", pre)
	}
	if got.DaemonUID.Value != 4242 || got.DaemonUID.Source != "running daemon /proc" || pre.Current.DaemonUID != 4242 || pre.Running.DaemonUID != 1234 || got.Runtime.DaemonUID != 1234 {
		t.Fatalf("persisted UID replaced current process evidence: %+v", got)
	}
	if got.ScriptComparisons.Pre.Current.State != personalScriptNotConfigured || got.ScriptComparisons.Post.Synchronization != personalScriptInSync {
		t.Fatalf("current inspection or unchanged post script incorrect: %+v", got)
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
		Runtime: daemonRuntimeDiagnostic{
			Availability: daemonRuntimeAvailable,
			ConfigPath:   "/opt/proxsave/configs/backup.env",
			StartTS:      1_700_000_000,
			DaemonUID:    1001,
		},
		ScriptComparisons: personalScriptComparisons{
			Pre: personalScriptComparison{
				Running: personalScriptDiagnostic{
					Key: "PERSONAL_SCRIPT_PRE_RUN", Path: script, State: personalScriptReady, DaemonUID: 1001,
					Components: []personalScriptPathComponent{{Path: script, UID: 1001, Mode: 0o700}},
				},
				Current:         personalScriptDiagnostic{State: personalScriptNotConfigured, DaemonUID: 1001},
				Synchronization: personalScriptConfigurationDrift,
				SyncReason:      "restart the daemon to apply current personal-script configuration",
			},
			Post: personalScriptComparison{
				Running:         personalScriptDiagnostic{Path: "/home/operator/post.sh", State: personalScriptReadyWithWarning, Reason: reason, DaemonUID: 1001},
				Current:         personalScriptDiagnostic{Path: "/home/operator/post.sh", State: personalScriptReadyWithWarning, Reason: reason, DaemonUID: 1001},
				Synchronization: personalScriptInSync,
			},
		},
	}

	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	logDaemonDiagnostics(logger, diagnostics)
	out := buf.String()

	start := strings.Index(out, "DEBUG    Start daemon diagnostics")
	ready := strings.Index(out, "INFO       Running daemon: READY")
	warning := strings.Index(out, "WARNING    Running daemon: READY WITH WARNING")
	end := strings.Index(out, "DEBUG    End daemon diagnostics")
	if start < 0 || ready < 0 || warning < 0 || end < 0 || start >= ready || ready >= warning || warning >= end {
		t.Fatalf("diagnostic lines are not enclosed and ordered correctly:\n%s", out)
	}
	if strings.Count(out, reason) != 2 {
		t.Fatalf("warning reason count = %d, want 2 (one per source):\n%s", strings.Count(out, reason), out)
	}
	for _, want := range []string{
		"Running daemon configuration: /opt/proxsave/configs/backup.env",
		"Running daemon loaded at: ",
		"Personal pre-run script:", "Running daemon: READY", "Current configuration: NOT CONFIGURED",
		"Synchronization: OUT OF SYNC", "Personal post-run script:", "READY WITH WARNING", "Synchronization: IN SYNC",
		"running daemon pre-run: key=", "current config pre-run: key=",
		"running daemon post-run: key=", "current config post-run: key=",
		"running daemon pre-run component: path=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
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
			Runtime: daemonRuntimeDiagnostic{Availability: daemonRuntimeAvailable},
			ScriptComparisons: personalScriptComparisons{
				Post: personalScriptComparison{
					Running:         personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_POST_RUN", Path: "/unsafe", State: personalScriptRefused, Reason: "unsafe"},
					Current:         personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_POST_RUN", Path: "/unsafe", State: personalScriptRefused, Reason: "unsafe"},
					Synchronization: personalScriptInSync,
				},
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
	diagnostics := personalScriptRendererInjectionFixture()
	prompt := buildDaemonStatusPrompt(diagnostics)
	assertNoRawInjection(t, prompt)
	plain := ansi.Strip(prompt)
	for _, want := range []string{
		"Personal pre-run script:", "Running daemon: READY", "/safe/pre.sh",
		"Personal post-run script:", "Current configuration: READY WITH WARNING",
		"Running daemon configuration: /bad/backup.env", "unsafeowner", "restartdaemon",
		"Synchronization: IN SYNC", "Synchronization: OUT OF SYNC",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("prompt missing %q:\n%s", want, plain)
		}
	}
	for _, forbidden := range []string{"runtime-config", "script-path", "reason", "sync-reason"} {
		if strings.Contains(plain, forbidden) {
			t.Fatalf("sanitizer retained %q:\n%s", forbidden, plain)
		}
	}
}

func personalScriptRendererInjectionFixture() daemonDiagnostics {
	return daemonDiagnostics{
		Runtime: daemonRuntimeDiagnostic{
			Availability: daemonRuntimeAvailable,
			ConfigPath:   "/bad\x1b]0;runtime-config\x07/backup.env", StartTS: 1_700_000_000, DaemonUID: 1001,
		},
		ScriptComparisons: personalScriptComparisons{
			Pre: personalScriptComparison{
				Running:         personalScriptDiagnostic{Path: "/safe/pre.sh", State: personalScriptReady},
				Current:         personalScriptDiagnostic{Path: "/safe/pre.sh", State: personalScriptReady},
				Synchronization: personalScriptInSync,
			},
			Post: personalScriptComparison{
				Running:         personalScriptDiagnostic{Path: "/bad\x1b]0;script-path\x07/post.sh", State: personalScriptReadyWithWarning, Reason: "unsafe\x1b]0;reason\x07owner"},
				Current:         personalScriptDiagnostic{Path: "/bad\x1b]0;script-path\x07/post.sh", State: personalScriptReadyWithWarning, Reason: "unsafe\x1b]0;reason\x07owner"},
				Synchronization: personalScriptConfigurationDrift,
				SyncReason:      "restart\x1b]0;sync-reason\x07daemon",
			},
		},
	}
}

func TestLogDaemonDiagnosticsDoesNotCallUnavailableRuntimeNotConfigured(t *testing.T) {
	diagnostics := daemonDiagnostics{
		Runtime: daemonRuntimeDiagnostic{Availability: daemonRuntimeMissing, Reason: "running daemon did not publish runtime state"},
		ScriptComparisons: personalScriptComparisons{
			Pre: personalScriptComparison{
				Current:         personalScriptDiagnostic{Path: "/current/pre.sh", State: personalScriptReady},
				Synchronization: personalScriptRuntimeUnavailable,
				SyncReason:      "running daemon did not publish runtime state",
			},
		},
	}
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	logDaemonDiagnostics(logger, diagnostics)
	out := buf.String()
	if !strings.Contains(out, "Running daemon state: UNAVAILABLE") {
		t.Fatalf("missing unavailable state:\n%s", out)
	}
	if strings.Contains(out, "Running daemon: NOT CONFIGURED") {
		t.Fatalf("unavailable runtime was mislabeled:\n%s", out)
	}
	if !strings.Contains(out, "Current configuration: READY (/current/pre.sh)") || !strings.Contains(out, "Synchronization: UNKNOWN") {
		t.Fatalf("unavailable runtime hid current configuration or synchronization:\n%s", out)
	}
}

func TestLogDaemonDiagnosticsSanitizesComparisonSourcesAndEvidence(t *testing.T) {
	diagnostics := personalScriptRendererInjectionFixture()
	for _, diagnostic := range []*personalScriptDiagnostic{
		&diagnostics.ScriptComparisons.Pre.Running, &diagnostics.ScriptComparisons.Pre.Current,
		&diagnostics.ScriptComparisons.Post.Running, &diagnostics.ScriptComparisons.Post.Current,
	} {
		diagnostic.Key = "PERSONAL\x1b]0;key-payload\x07_SCRIPT"
		diagnostic.Components = []personalScriptPathComponent{{Path: "/bad\x1b]0;component-payload\x07/file", UID: 1001, Mode: 0o755}}
	}
	for _, level := range []types.LogLevel{types.LogLevelInfo, types.LogLevelDebug} {
		logger := logging.New(level, false)
		buf := &bytes.Buffer{}
		logger.SetOutput(buf)
		logDaemonDiagnostics(logger, diagnostics)
		out := buf.String()
		assertNoRawInjection(t, out)
		for _, forbidden := range []string{"runtime-config", "script-path", "sync-reason", "key-payload", "component-payload"} {
			if strings.Contains(out, forbidden) {
				t.Errorf("sanitizer retained %q:\n%s", forbidden, out)
			}
		}
		for _, want := range []string{"Running daemon configuration: /bad/backup.env", "Running daemon: READY WITH WARNING (/bad/post.sh): unsafeowner", "Current configuration: READY WITH WARNING (/bad/post.sh): unsafeowner", "Synchronization: OUT OF SYNC (restartdaemon)"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q:\n%s", want, out)
			}
		}
		for _, source := range []string{"running daemon pre-run", "current config pre-run", "running daemon post-run", "current config post-run"} {
			want := source + " component: path=\"/bad/file\" uid=1001 mode=0755"
			if strings.Contains(out, want) != (level == types.LogLevelDebug) {
				t.Errorf("unexpected evidence visibility at %v for %q:\n%s", level, want, out)
			}
		}
	}
}
