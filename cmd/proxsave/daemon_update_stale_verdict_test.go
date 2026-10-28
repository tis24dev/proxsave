package main

import (
	"context"
	"testing"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/version"
)

// The update record does not say which version it was checked against. After an
// upgrade, an inconclusive check replayed the previous binary's "available=true
// latest=X" while X was running: /1 and a WARNING about the running version. The last
// known latest is compared with the running version again; a downgrade goes the other
// way.
func TestUpdateTickInconclusiveRechecksTheRecordAgainstTheRunningVersion(t *testing.T) {
	cases := []struct {
		name          string
		running       string
		recordedAvail bool
		recordedLast  string
		wantAvailable bool
	}{
		{"upgraded to the recorded latest", "0.45.0", true, "0.45.0", false},
		{"downgraded below the recorded latest", "0.44.1", false, "0.45.0", true},
		{"still older than the recorded latest", "0.44.1", true, "0.45.0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origVersion := version.Version
			version.Version = tc.running
			t.Cleanup(func() { version.Version = origVersion })

			rep := &fakeReporter{updates: true}
			d := newUpdateDaemon(t, rep)
			// What the previous binary left in the status file.
			if err := health.RecordUpdate(d.cfg.BaseDir, d.cfg.HealthcheckMode, d.now().Unix()-3600, tc.recordedAvail, tc.recordedLast, true, nil); err != nil {
				t.Fatalf("RecordUpdate: %v", err)
			}
			// GitHub did not answer: no Latest.
			stubUpdate(t, func() *UpdateInfo { return &UpdateInfo{Current: tc.running} })

			d.updateTick(context.Background())

			s := rep.snapshot()
			if s.updatesReported != 1 || s.lastAvailable != tc.wantAvailable {
				t.Fatalf("running %s, recorded available=%t latest=%s: ping available=%t (%d pings), want %t",
					tc.running, tc.recordedAvail, tc.recordedLast, s.lastAvailable, s.updatesReported, tc.wantAvailable)
			}
			if d.updateWarned != tc.wantAvailable {
				t.Fatalf("updateWarned=%t want %t", d.updateWarned, tc.wantAvailable)
			}
			if u := mustDaemonStatus(t, d).Update; u == nil || u.Available != tc.wantAvailable || u.Latest != tc.recordedLast {
				t.Fatalf("status record %+v, want available=%t latest=%s", u, tc.wantAvailable, tc.recordedLast)
			}
		})
	}
}
