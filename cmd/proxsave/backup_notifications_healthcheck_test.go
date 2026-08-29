// Package main contains the proxsave command entrypoint.
package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestHealthcheckConfigProblem(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *config.Config
		wantHas string // substring the problem must contain; "" means "no problem"
	}{
		{"centralized ok", &config.Config{HealthcheckMode: "centralized", ServerID: "srv1"}, ""},
		{"centralized no server id", &config.Config{HealthcheckMode: "centralized"}, "SERVER_ID"},
		{"centralized blank server id", &config.Config{HealthcheckMode: "centralized", ServerID: "   "}, "SERVER_ID"},
		{"self via alive url", &config.Config{HealthcheckMode: "self", HealthcheckAliveURL: "https://hc/x"}, ""},
		{"self via alive id", &config.Config{HealthcheckMode: "self", HealthcheckAliveID: "uuid-1"}, ""},
		{"self no check", &config.Config{HealthcheckMode: "self"}, "no alive check"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := healthcheckConfigProblem(tc.cfg)
			if tc.wantHas == "" {
				if got != "" {
					t.Fatalf("want no problem, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantHas) {
				t.Fatalf("problem=%q want substring %q", got, tc.wantHas)
			}
		})
	}
}

// TestInitializeHealthcheckSectionLines pins that Healthchecks emits a REAL init line at
// run start, consistent with the other channels: SKIP when disabled, a WARNING that
// disables the section on a config problem OR when the monitoring daemon is not
// running/down, and a "✓ initialized" line ONLY when config is usable AND the daemon is
// actually alive (fresh heartbeat in its status file).
func TestInitializeHealthcheckSectionLines(t *testing.T) {
	orig := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(orig) })

	origProbe := daemonPresenceProbe
	t.Cleanup(func() { daemonPresenceProbe = origProbe })

	discard := logging.New(types.LogLevelInfo, false)
	discard.SetOutput(io.Discard)

	// run drives the init with an explicit systemd presence. The default (unprobed) keeps
	// the heartbeat-only behaviour the pre-existing cases assert; the presence cases below
	// pin the systemd-refined verdicts.
	run := func(cfg *config.Config, p health.DaemonPresence) string {
		daemonPresenceProbe = func(context.Context) health.DaemonPresence { return p }
		var buf bytes.Buffer
		def := logging.New(types.LogLevelDebug, false)
		def.SetOutput(&buf)
		logging.SetDefaultLogger(def)
		orch := orchestrator.New(discard, false)
		initializeHealthcheckSection(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: discard}, orch)
		return buf.String()
	}
	unprobed := health.DaemonPresence{}
	activeDaemon := health.DaemonPresence{Probed: true, Installed: true, Active: true}
	// writeHeartbeat records a heartbeat into a fresh temp BaseDir at the given age.
	usableCfg := func(t *testing.T, hbAge time.Duration, hasBeat bool) *config.Config {
		t.Helper()
		base := t.TempDir()
		if hasBeat {
			if err := health.RecordPing(base, "centralized", health.KindHeartbeat, time.Now().Add(-hbAge).Unix(), true, nil); err != nil {
				t.Fatalf("seed heartbeat: %v", err)
			}
		}
		// SchedulerMode is stated explicitly: every case in this test is about the DAEMON
		// engine's warning arm, and reportHealthchecksUnusable re-ranks that same reason to a
		// SKIP on the cron engine. Leaving the mode blank would let a future default flip
		// silently repurpose the whole test.
		return &config.Config{SchedulerMode: "daemon", HealthcheckEnabled: true, HealthcheckMode: "centralized", ServerID: "srv1", BaseDir: base}
	}

	// disabled -> a SKIP line, exactly like Email/Gotify/Webhook.
	if out := run(&config.Config{HealthcheckEnabled: false}, unprobed); !strings.Contains(out, "Healthchecks: disabled") {
		t.Fatalf("disabled must print a SKIP line, out=%q", out)
	}

	// On ANY problem the section must (like Telegram): WARN the reason, SKIP a clean
	// "Healthchecks: disabled", flip cfg.HealthcheckEnabled=false, and NOT print "✓".
	assertDisabled := func(t *testing.T, name string, c *config.Config, p health.DaemonPresence, wantReason string) {
		t.Helper()
		out := run(c, p)
		if !strings.Contains(out, wantReason) {
			t.Fatalf("%s: want reason %q, out=%q", name, wantReason, out)
		}
		if !strings.Contains(out, "Healthchecks: disabled") {
			t.Fatalf("%s: want a clean 'Healthchecks: disabled' SKIP, out=%q", name, out)
		}
		if strings.Contains(out, "Healthchecks initialized") {
			t.Fatalf("%s: must NOT print initialized, out=%q", name, out)
		}
		// Every case here is on the DAEMON engine, where the problem is a real regression and
		// must keep its warning weight. If the cron re-rank ever leaked onto this engine the
		// substring assertions above would still pass while the run silently stopped exiting 1.
		if strings.Contains(out, "cron mode") {
			t.Fatalf("%s: the daemon engine must WARN, not emit the cron-mode SKIP, out=%q", name, out)
		}
		if c.HealthcheckEnabled {
			t.Fatalf("%s: must flip HealthcheckEnabled=false so the flow treats it as disabled", name)
		}
	}

	// enabled + centralized without SERVER_ID -> config problem.
	assertDisabled(t, "no-server-id", &config.Config{SchedulerMode: "daemon", HealthcheckEnabled: true, HealthcheckMode: "centralized"}, unprobed, "SERVER_ID")
	// Heartbeat-only fallback (systemctl unavailable): no beat -> "daemon not running".
	assertDisabled(t, "no-daemon", usableCfg(t, 0, false), unprobed, "daemon not running")
	// Heartbeat-only fallback: STALE heartbeat -> "daemon stale".
	assertDisabled(t, "stale-daemon", usableCfg(t, time.Hour, true), unprobed, "daemon stale")

	// Systemd-refined verdicts (the completeness fix): presence dominates the heartbeat.
	// Unit absent -> "daemon not installed", even with a fresh beat seeded.
	assertDisabled(t, "not-installed", usableCfg(t, 30*time.Second, true),
		health.DaemonPresence{Probed: true, Installed: false}, "daemon not installed")
	// Installed but systemd inactive -> "daemon not running" (truly stopped).
	assertDisabled(t, "not-active", usableCfg(t, 30*time.Second, true),
		health.DaemonPresence{Probed: true, Installed: true, Active: false}, "daemon not running")
	// systemd ACTIVE but no fresh beat -> "daemon running, not reporting" (stale binary).
	assertDisabled(t, "running-not-reporting", usableCfg(t, 0, false), activeDaemon, "daemon running, not reporting")

	// usable config + FRESH heartbeat + systemd active -> initialized.
	out := run(usableCfg(t, 30*time.Second, true), activeDaemon)
	if !strings.Contains(out, "✓ Healthchecks initialized (mode: centralized)") {
		t.Fatalf("usable config + live daemon must print the initialized line, out=%q", out)
	}
}

// TestInitializeHealthcheckSectionCronModeSkipsInsteadOfWarning is the issue #298
// regression. --daemon-remove left HEALTHCHECK_ENABLED=true behind, so every later cron run
// warned "Healthchecks: daemon not installed" and applyIssueExitCode promoted an otherwise
// clean backup to exit 1. On the cron engine the daemon is absent by construction and is the
// only thing that pings, so the honest report is a SKIP - emitted at info level, therefore
// invisible to the warning counter that drives the exit code - that still NAMES what was
// observed, because re-ranking a report is not the same as swallowing it.
func TestInitializeHealthcheckSectionCronModeSkipsInsteadOfWarning(t *testing.T) {
	orig := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(orig) })
	origProbe := daemonPresenceProbe
	t.Cleanup(func() { daemonPresenceProbe = origProbe })
	daemonPresenceProbe = func(context.Context) health.DaemonPresence {
		return health.DaemonPresence{Probed: true, Installed: false}
	}

	discard := logging.New(types.LogLevelInfo, false)
	discard.SetOutput(io.Discard)

	var buf bytes.Buffer
	def := logging.New(types.LogLevelDebug, false)
	def.SetOutput(&buf)
	logging.SetDefaultLogger(def)

	cfg := &config.Config{
		SchedulerMode:      "cron",
		HealthcheckEnabled: true,
		HealthcheckMode:    "centralized",
		ServerID:           "srv1",
		BaseDir:            t.TempDir(),
	}
	orch := orchestrator.New(discard, false)
	initializeHealthcheckSection(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: discard}, orch)

	out := buf.String()
	if def.WarningCount() != 0 {
		t.Fatalf("cron mode must emit no WARNING (one warning is enough to promote a clean backup to exit 1), out=%q", out)
	}
	if strings.Contains(out, "WARNING") {
		t.Fatalf("cron mode must not log a WARNING line, out=%q", out)
	}
	if !strings.Contains(out, "Healthchecks: disabled") {
		t.Fatalf("cron mode must still print the SKIP line, out=%q", out)
	}
	if !strings.Contains(out, "cron mode") {
		t.Fatalf("the SKIP must say WHY the section is inert on this engine, out=%q", out)
	}
	if !strings.Contains(out, "daemon not installed") {
		t.Fatalf("the SKIP must still name the observed reason (re-rank, not swallow), out=%q", out)
	}
	if cfg.HealthcheckEnabled {
		t.Fatal("cron mode must still flip HealthcheckEnabled=false so the Phase-7 entries loop renders it disabled")
	}
}

// TestInitializeHealthcheckSectionCronModeKeepsLiveDaemon pins that the cron gate is a
// SEVERITY decision on a problem, not a short-circuit on the engine. applyCronMode persists
// SCHEDULER_MODE=cron BEFORE it tears the unit down (F09-06), so a teardown that fails leaves
// a live, transmitting daemon on a host whose config already reads cron. In that window the
// section is reporting something real and must still render.
func TestInitializeHealthcheckSectionCronModeKeepsLiveDaemon(t *testing.T) {
	orig := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(orig) })
	origProbe := daemonPresenceProbe
	t.Cleanup(func() { daemonPresenceProbe = origProbe })
	daemonPresenceProbe = func(context.Context) health.DaemonPresence {
		return health.DaemonPresence{Probed: true, Installed: true, Active: true}
	}

	discard := logging.New(types.LogLevelInfo, false)
	discard.SetOutput(io.Discard)

	var buf bytes.Buffer
	def := logging.New(types.LogLevelDebug, false)
	def.SetOutput(&buf)
	logging.SetDefaultLogger(def)

	base := t.TempDir()
	if err := health.RecordPing(base, "centralized", health.KindHeartbeat, time.Now().Add(-30*time.Second).Unix(), true, nil); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}
	cfg := &config.Config{
		SchedulerMode:      "cron",
		HealthcheckEnabled: true,
		HealthcheckMode:    "centralized",
		ServerID:           "srv1",
		BaseDir:            base,
	}
	orch := orchestrator.New(discard, false)
	initializeHealthcheckSection(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: discard}, orch)

	out := buf.String()
	if !strings.Contains(out, "Healthchecks initialized (mode: centralized)") {
		t.Fatalf("a live daemon must still initialize the section even when the config reads cron, out=%q", out)
	}
	if strings.Contains(out, "cron mode") {
		t.Fatalf("the cron re-rank must not fire when the probe reports no problem, out=%q", out)
	}
	if !cfg.HealthcheckEnabled {
		t.Fatal("a working section must keep HealthcheckEnabled=true so the Phase-7 dispatch renders it")
	}
}

// TestInitializeHealthcheckSectionDaemonModeStillWarns is the counterweight to the cron gate.
// On the daemon engine the daemon IS the scheduler, so a missing unit means the host believes
// it is monitored and is not - exactly the failure the WARNING (and the exit code it earns)
// exists to surface. A suppression rule with no test on this side is one refactor away from
// hiding a real outage.
func TestInitializeHealthcheckSectionDaemonModeStillWarns(t *testing.T) {
	orig := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(orig) })
	origProbe := daemonPresenceProbe
	t.Cleanup(func() { daemonPresenceProbe = origProbe })
	daemonPresenceProbe = func(context.Context) health.DaemonPresence {
		return health.DaemonPresence{Probed: true, Installed: false}
	}

	discard := logging.New(types.LogLevelInfo, false)
	discard.SetOutput(io.Discard)

	var buf bytes.Buffer
	def := logging.New(types.LogLevelDebug, false)
	def.SetOutput(&buf)
	logging.SetDefaultLogger(def)

	cfg := &config.Config{
		SchedulerMode:      "daemon",
		HealthcheckEnabled: true,
		HealthcheckMode:    "centralized",
		ServerID:           "srv1",
		BaseDir:            t.TempDir(),
	}
	orch := orchestrator.New(discard, false)
	initializeHealthcheckSection(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: discard}, orch)

	out := buf.String()
	if def.WarningCount() != 1 {
		t.Fatalf("daemon mode must WARN exactly once on a missing daemon (got %d), out=%q", def.WarningCount(), out)
	}
	if !strings.Contains(out, "daemon not installed") {
		t.Fatalf("daemon mode must name the problem, out=%q", out)
	}
	if strings.Contains(out, "cron mode") {
		t.Fatalf("the cron re-rank must never reach the daemon engine, out=%q", out)
	}
	if cfg.HealthcheckEnabled {
		t.Fatal("daemon mode must still flip HealthcheckEnabled=false")
	}
}
