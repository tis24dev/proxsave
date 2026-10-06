package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// daemonReportsUpdates decides whether a run may stay silent about an available update: only
// when the daemon schedules the backups, healthchecks is enabled and the updates check can be
// resolved. In centralized mode the ProxSave HC Server always provisions that check; in self
// mode it exists only when the operator set its URL or its ID.
func TestDaemonReportsUpdates(t *testing.T) {
	daemonHC := func(mode string) *config.Config {
		return &config.Config{
			SchedulerMode:           "daemon",
			HealthcheckEnabled:      true,
			HealthcheckMode:         mode,
			HealthcheckPingEndpoint: "https://hc-ping.com",
		}
	}
	tests := []struct {
		name string
		cfg  func() *config.Config
		want bool
	}{
		{"nil config", func() *config.Config { return nil }, false},
		{"cron scheduler", func() *config.Config {
			c := daemonHC(config.HealthcheckModeCentralized)
			c.SchedulerMode = "cron"
			return c
		}, false},
		{"healthchecks disabled", func() *config.Config {
			c := daemonHC(config.HealthcheckModeCentralized)
			c.HealthcheckEnabled = false
			return c
		}, false},
		{"centralized", func() *config.Config { return daemonHC(config.HealthcheckModeCentralized) }, true},
		{"self without updates check", func() *config.Config { return daemonHC(config.HealthcheckModeSelf) }, false},
		{"self with updates URL", func() *config.Config {
			c := daemonHC(config.HealthcheckModeSelf)
			c.HealthcheckUpdatesURL = "https://hc-ping.com/updates-uuid"
			return c
		}, true},
		{"self with updates ID", func() *config.Config {
			c := daemonHC(config.HealthcheckModeSelf)
			c.HealthcheckUpdatesID = "updates-uuid"
			return c
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := daemonReportsUpdates(tt.cfg()); got != tt.want {
				t.Fatalf("daemonReportsUpdates = %v, want %v", got, tt.want)
			}
		})
	}
}

// An available update is a WARNING of the run only where no daemon reports it to the
// healthchecks updates check: the WARNING is counted into the exit code, and the daemon hands
// that exit code to the backup check, which went down for an update alone (issue #334).
func TestLogUpdateAvailable(t *testing.T) {
	centralized := &config.Config{SchedulerMode: "daemon", HealthcheckEnabled: true, HealthcheckMode: config.HealthcheckModeCentralized}

	t.Run("cron host warns without naming a command", func(t *testing.T) {
		info := &UpdateInfo{NewVersion: true, Current: "0.41.0", Latest: "0.42.0"}
		var out bytes.Buffer
		logger := logging.New(types.LogLevelDebug, false)
		logger.SetOutput(&out)

		logUpdateAvailable(logger, &config.Config{SchedulerMode: "cron"}, info)

		if n := logger.WarningCount(); n != 1 {
			t.Fatalf("logged %d warning(s), want 1", n)
		}
		if !strings.Contains(out.String(), "New ProxSave version 0.42.0 (current 0.41.0).") {
			t.Fatalf("warning text missing from output:\n%s", out.String())
		}
		if strings.Contains(out.String(), "--upgrade") {
			t.Fatalf("the notice must not send the operator to a command:\n%s", out.String())
		}
		if !info.NoticeLogged {
			t.Fatal("NoticeLogged is false after a WARNING: the notifications would list the notice twice")
		}
	})

	t.Run("daemon with healthchecks keeps it out of the exit code", func(t *testing.T) {
		info := &UpdateInfo{NewVersion: true, Current: "0.41.0", Latest: "0.42.0"}
		var out bytes.Buffer
		logger := logging.New(types.LogLevelDebug, false)
		logger.SetOutput(&out)

		logUpdateAvailable(logger, centralized, info)

		if n := logger.WarningCount(); n != 0 {
			t.Fatalf("logged %d warning(s), want 0: the warning would raise the run's exit code and take the backup check down", n)
		}
		if !strings.Contains(out.String(), "DEBUG") {
			t.Fatalf("the finding must stay visible at DEBUG:\n%s", out.String())
		}
		if info.NoticeLogged {
			t.Fatal("NoticeLogged is true without a WARNING: the notifications would drop the notice")
		}
	})

	t.Run("no update says nothing", func(t *testing.T) {
		var out bytes.Buffer
		logger := logging.New(types.LogLevelDebug, false)
		logger.SetOutput(&out)

		logUpdateAvailable(logger, &config.Config{SchedulerMode: "cron"}, &UpdateInfo{Current: "0.42.0", Latest: "0.42.0"})
		logUpdateAvailable(logger, &config.Config{SchedulerMode: "cron"}, nil)

		if out.Len() != 0 || logger.WarningCount() != 0 {
			t.Fatalf("an up-to-date or failed check logged something:\n%s", out.String())
		}
	})
}
