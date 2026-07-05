// Package main contains the proxsave command entrypoint.
package main

import (
	"bytes"
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

	discard := logging.New(types.LogLevelInfo, false)
	discard.SetOutput(io.Discard)

	run := func(cfg *config.Config) string {
		var buf bytes.Buffer
		def := logging.New(types.LogLevelDebug, false)
		def.SetOutput(&buf)
		logging.SetDefaultLogger(def)
		orch := orchestrator.New(discard, false)
		initializeHealthcheckSection(backupModeOptions{cfg: cfg, logger: discard}, orch)
		return buf.String()
	}
	// writeHeartbeat records a heartbeat into a fresh temp BaseDir at the given age.
	usableCfg := func(t *testing.T, hbAge time.Duration, hasBeat bool) *config.Config {
		t.Helper()
		base := t.TempDir()
		if hasBeat {
			if err := health.RecordPing(base, "centralized", health.KindHeartbeat, time.Now().Add(-hbAge).Unix(), true, nil); err != nil {
				t.Fatalf("seed heartbeat: %v", err)
			}
		}
		return &config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized", ServerID: "srv1", BaseDir: base}
	}

	// disabled -> a SKIP line, exactly like Email/Gotify/Webhook.
	if out := run(&config.Config{HealthcheckEnabled: false}); !strings.Contains(out, "Healthchecks: disabled") {
		t.Fatalf("disabled must print a SKIP line, out=%q", out)
	}

	// enabled + centralized without SERVER_ID -> config-problem WARNING, no "✓".
	out := run(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"})
	if !strings.Contains(out, "SERVER_ID") || !strings.Contains(out, "Healthchecks disabled") {
		t.Fatalf("no-server-id must WARN + disable, out=%q", out)
	}
	if strings.Contains(out, "Healthchecks initialized") {
		t.Fatalf("a broken config must NOT print initialized, out=%q", out)
	}

	// usable config BUT daemon not running (no status file) -> WARNING, NOT initialized.
	out = run(usableCfg(t, 0, false))
	if !strings.Contains(out, "daemon not running") || !strings.Contains(out, "Healthchecks disabled") {
		t.Fatalf("no daemon must WARN 'not running' + disable, out=%q", out)
	}
	if strings.Contains(out, "Healthchecks initialized") {
		t.Fatalf("a dead daemon must NOT print initialized, out=%q", out)
	}

	// usable config + STALE heartbeat (1h old, > default 10m stale window) -> stale.
	out = run(usableCfg(t, time.Hour, true))
	if !strings.Contains(out, "daemon stale") || !strings.Contains(out, "Healthchecks disabled") {
		t.Fatalf("a stale daemon must WARN 'daemon stale' + disable, out=%q", out)
	}
	if strings.Contains(out, "Healthchecks initialized") {
		t.Fatalf("a stale daemon must NOT print initialized, out=%q", out)
	}

	// usable config + FRESH heartbeat -> "✓ Healthchecks initialized (mode: centralized)".
	out = run(usableCfg(t, 30*time.Second, true))
	if !strings.Contains(out, "✓ Healthchecks initialized (mode: centralized)") {
		t.Fatalf("usable config + live daemon must print the initialized line, out=%q", out)
	}
}
