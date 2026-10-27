package main

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// Without a backup check the operator's monitor never hears about a failed backup:
// NOTIFY_ON must not silence the other channels.
func TestSelfMonitorUnconfirmedWithoutABackupCheck(t *testing.T) {
	cfg := &config.Config{HealthcheckMode: config.HealthcheckModeSelf, HealthcheckAliveID: "alive", BaseDir: t.TempDir()}
	if selfMonitorConfirmed(cfg, nil) {
		t.Fatal("self mode with no backup check must not count as a confirmed monitor")
	}
}
