package orchestrator

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// hcSpyChannel counts dispatches; its Name() is the healthchecks section const so the
// entries loop claims it.
type hcSpyChannel struct{ calls int }

func (s *hcSpyChannel) Name() string { return healthchecksSectionName }
func (s *hcSpyChannel) Notify(context.Context, *BackupStats) error {
	s.calls++
	return nil
}

// R7: an enabled+registered Healthchecks section is dispatched EXACTLY once (by the
// entries loop), never a second time by the remainder loop.
func TestDispatchHealthchecksDispatchedOnce(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&bytes.Buffer{})
	spy := &hcSpyChannel{}
	o := &Orchestrator{
		logger:               logger,
		cfg:                  &config.Config{HealthcheckEnabled: true}, // every other channel disabled
		notificationChannels: []NotificationChannel{spy},
	}
	o.dispatchNotifications(context.Background(), &BackupStats{})
	if spy.calls != 1 {
		t.Fatalf("Healthchecks dispatched %d times, want exactly 1", spy.calls)
	}
}

// The enabled section renders LAST (after the Telegram line) and is NOT SKIP'd.
func TestDispatchHealthchecksLastAndNotSkipped(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	// Real channel with a no-secret seam -> prints "Healthchecks: starting" + not-configured.
	ch := &HealthchecksChannel{
		cfg:        &config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"},
		logger:     logger,
		loadSecret: func(string) (string, error) { return "", nil },
	}
	o := &Orchestrator{
		logger:               logger,
		cfg:                  &config.Config{HealthcheckEnabled: true},
		notificationChannels: []NotificationChannel{ch},
	}
	o.dispatchNotifications(context.Background(), &BackupStats{})
	out := buf.String()
	if strings.Contains(out, "Healthchecks: disabled") {
		t.Fatalf("enabled Healthchecks must not be SKIP'd, got: %q", out)
	}
	iTg, iHc := strings.Index(out, "Telegram: disabled"), strings.Index(out, "Healthchecks: starting")
	if iTg < 0 || iHc < 0 || iHc < iTg {
		t.Fatalf("Healthchecks must render after Telegram, got: %q", out)
	}
}

// Disabled (and, mirroring initializeHealthcheckSection, NOT registered) -> the entries
// loop emits the single "Healthchecks: disabled" line and nothing dispatches.
func TestDispatchHealthchecksDisabledEmitsSkip(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	o := &Orchestrator{
		logger:               logger,
		cfg:                  &config.Config{HealthcheckEnabled: false},
		notificationChannels: nil,
	}
	o.dispatchNotifications(context.Background(), &BackupStats{})
	if c := strings.Count(buf.String(), "Healthchecks: disabled"); c != 1 {
		t.Fatalf("disabled Healthchecks must emit exactly one SKIP line, got %d: %q", c, buf.String())
	}
}
