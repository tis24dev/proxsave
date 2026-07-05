// Package main contains the proxsave command entrypoint.
package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
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
		{"self no check", &config.Config{HealthcheckMode: "self"}, "no service-alive check configured"},
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
// run start, consistent with the other notification channels: SKIP when disabled, a
// WARNING that disables the section on a config problem, or a "✓ initialized" line when
// usable. The lines go through the package-level default logger (like the others).
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

	// disabled -> a SKIP line, exactly like Email/Gotify/Webhook.
	if out := run(&config.Config{HealthcheckEnabled: false}); !strings.Contains(out, "Healthchecks: disabled") {
		t.Fatalf("disabled must print a SKIP line, out=%q", out)
	}

	// enabled + centralized without SERVER_ID -> WARNING that disables the section, and
	// definitely NOT a "✓ initialized".
	out := run(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"})
	if !strings.Contains(out, "SERVER_ID") || !strings.Contains(out, "section disabled") {
		t.Fatalf("no-server-id must WARN + disable, out=%q", out)
	}
	if strings.Contains(out, "Healthchecks initialized") {
		t.Fatalf("a broken config must NOT print initialized, out=%q", out)
	}

	// enabled + usable centralized -> "✓ Healthchecks initialized (mode: centralized)".
	if out := run(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized", ServerID: "srv1"}); !strings.Contains(out, "✓ Healthchecks initialized (mode: centralized)") {
		t.Fatalf("usable centralized must print the initialized line, out=%q", out)
	}

	// enabled + usable self -> initialized (mode: self).
	if out := run(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "self", HealthcheckAliveID: "uuid-1"}); !strings.Contains(out, "✓ Healthchecks initialized (mode: self)") {
		t.Fatalf("usable self must print the initialized line, out=%q", out)
	}
}
