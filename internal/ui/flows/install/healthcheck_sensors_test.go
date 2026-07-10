package install

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// TestBuildHealthcheckPromptSensors: the prompt renders one colored line per sensor after
// the Status block, reusing the unified palette - green ✓ Ok, yellow ⚠ Warn, red ✗ Error
// (an available update), and yellow-no-symbol Neutral - each with its "last ping" age.
func TestBuildHealthcheckPromptSensors(t *testing.T) {
	sensors := []health.SensorRow{
		{Name: "proxsave-alive", Level: health.SensorOk, State: "ok", Age: "10s ago"},
		{Name: "proxsave-backup", Level: health.SensorWarn, State: "stale", Age: "2h ago"},
		{Name: "proxsave-updates", Level: health.SensorError, State: "update available", Age: "1m ago"},
		{Name: "proxsave-neutral", Level: health.SensorNeutral, State: "no data"},
	}
	v := buildHealthcheckPrompt(false, "", "WORKING", "It is reporting.", orchestrator.HealthcheckSetupLevelOk, sensors)
	plain := ansi.Strip(v)

	if !strings.Contains(plain, "Sensors:") {
		t.Fatalf("sensors header missing:\n%s", plain)
	}
	for _, want := range []string{
		"proxsave-alive: ok (last ping 10s ago)",
		"proxsave-backup: stale (last ping 2h ago)",
		"proxsave-updates: update available (last ping 1m ago)",
		"proxsave-neutral: no data",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("sensor line %q missing:\n%s", want, plain)
		}
	}
	// An available update is RED (theme.Red SGR) and carries the ✗ symbol.
	if !strings.Contains(v, "239;68;68") {
		t.Fatalf("an available update must render red")
	}
	if !strings.Contains(plain, "✗ proxsave-updates") {
		t.Fatalf("available update must carry the error symbol:\n%s", plain)
	}
	// The Neutral sensor carries NO symbol.
	if strings.Contains(plain, "✗ proxsave-neutral") || strings.Contains(plain, "⚠ proxsave-neutral") || strings.Contains(plain, "✓ proxsave-neutral") {
		t.Fatalf("neutral sensor must carry no symbol:\n%s", plain)
	}

	// No sensors -> no "Sensors:" block (pre-check state).
	none := ansi.Strip(buildHealthcheckPrompt(false, "", "NOT CHECKED", "Choose Check.", orchestrator.HealthcheckSetupLevelNeutral, nil))
	if strings.Contains(none, "Sensors:") {
		t.Fatalf("no sensors must render no Sensors block:\n%s", none)
	}
}

// TestBuildHealthcheckPromptSanitizesInjection: the explanation (free-form probe
// error text, e.g. orNA(d.Err) read raw from the status file) and a sensor Name
// (a status-file record key) reach the verbatim styled-prompt path, so raw escape
// bytes in them must be stripped. Assert absence of the injected OSC/BEL/C1/CSI
// markers (theme rendering adds its own legitimate SGR), and that real text survives.
func TestBuildHealthcheckPromptSanitizesInjection(t *testing.T) {
	sensors := []health.SensorRow{
		{Name: "proxsave-\x1b]0;pwned\x07alive", Level: health.SensorOk, State: "ok", Age: "10s ago"},
	}
	v := buildHealthcheckPrompt(false, "", "UNREACHABLE",
		"probe failed: \x1b[2J\x07connection refused\x1b]0;evil\x07",
		orchestrator.HealthcheckSetupLevelError, sensors)
	for _, bad := range []string{"\x1b]0;", "\x07", "\x9b", "\x1b[2J", "pwned", "evil"} {
		if strings.Contains(v, bad) {
			t.Fatalf("healthcheck prompt leaks injected sequence %q:\n%q", bad, v)
		}
	}
	for _, want := range []string{"probe failed:", "connection refused", "proxsave-", "alive"} {
		if !strings.Contains(v, want) {
			t.Fatalf("sanitized prompt dropped legitimate text %q:\n%q", want, v)
		}
	}
}
