package main

import (
	"errors"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
)

// A backup check given as an ID with no ping endpoint resolves to no URL: no backup
// ping is ever sent, so the monitor is not confirmed (docs/HEALTHCHECKS.md: the
// filter applies when both ping URLs are valid).
func TestSelfMonitorUnconfirmedWithABackupIDAndNoEndpoint(t *testing.T) {
	base := t.TempDir()
	if err := health.RecordOutcomePing(base, "self", health.KindHeartbeat, time.Now().Unix(), true, false, nil); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{HealthcheckMode: config.HealthcheckModeSelf, HealthcheckAliveURL: "https://hc.example.invalid/ping/alive", HealthcheckBackupID: "backup-uuid", HealthcheckPingEndpoint: "", BaseDir: base}
	if selfMonitorConfirmed(cfg, nil) {
		t.Fatal("a backup ID without a ping endpoint must not count as a confirmed monitor")
	}
	cfg.HealthcheckPingEndpoint = "https://hc-ping.com"
	if !selfMonitorConfirmed(cfg, nil) {
		t.Fatal("a backup ID with a ping endpoint and a fresh heartbeat is a confirmed monitor")
	}
}

// The last backup ping answered HTTP 404: the check does not exist on the monitor, so
// nothing alerts about backups and NOTIFY_ON must not silence the other channels. A
// timeout or a 5xx leaves a check that alerts when its pings stop: still confirmed.
func TestSelfMonitorUnconfirmedWhenTheBackupCheckIsMissing(t *testing.T) {
	for _, tc := range []struct {
		pingErr string
		want    bool
	}{
		{"healthcheck finish: HTTP 404", false},
		{"healthcheck hang: HTTP 404", false},
		{"healthcheck finish: HTTP 503", true},
		{"healthcheck finish: Post: context deadline exceeded", true},
	} {
		base := t.TempDir()
		now := time.Now().Unix()
		if err := health.RecordOutcomePing(base, "self", health.KindHeartbeat, now, true, false, nil); err != nil {
			t.Fatal(err)
		}
		kind := health.KindRunFinished
		if tc.pingErr == "healthcheck hang: HTTP 404" {
			kind = health.KindRunHang
		}
		if err := health.RecordOutcomePing(base, "self", kind, now, false, false, errors.New(tc.pingErr)); err != nil {
			t.Fatal(err)
		}
		cfg := &config.Config{HealthcheckMode: config.HealthcheckModeSelf, HealthcheckAliveURL: "https://hc.example.invalid/ping/alive", HealthcheckBackupURL: "https://hc.example.invalid/ping/backup", BaseDir: base}
		if got := selfMonitorConfirmed(cfg, nil); got != tc.want {
			t.Fatalf("last backup ping %q: selfMonitorConfirmed = %v, want %v", tc.pingErr, got, tc.want)
		}
	}
}
