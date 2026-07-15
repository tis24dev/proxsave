// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
)

// mustDaemonStatus reads the daemon's on-disk healthcheck status file, failing on error.
func mustDaemonStatus(t *testing.T, d *daemon) health.Status {
	t.Helper()
	st, err := health.LoadStatus(d.cfg.BaseDir)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	return st
}

// stubUpdate overrides the update-check seam for the duration of a test.
func stubUpdate(t *testing.T, fn func() *UpdateInfo) {
	t.Helper()
	orig := daemonEvaluateUpdate
	t.Cleanup(func() { daemonEvaluateUpdate = orig })
	daemonEvaluateUpdate = func(ctx context.Context, logger *logging.Logger, current string) *UpdateInfo {
		return fn()
	}
}

func newUpdateDaemon(t *testing.T, rep *fakeReporter) *daemon {
	t.Helper()
	return &daemon{
		cfg:      &config.Config{BaseDir: t.TempDir(), HealthcheckMode: "self"},
		reporter: rep,
		now:      time.Now,
	}
}

// TestUpdateTickPingsAndThrottles drives updateTick through up-to-date -> available ->
// still-available -> cleared, asserting the /0-vs-/1 ping, the once-per-transition WARNING
// throttle (updateWarned), and that the status records Available orthogonally to Ping.OK.
func TestUpdateTickPingsAndThrottles(t *testing.T) {
	available := false
	latest := "1.2.3"
	stubUpdate(t, func() *UpdateInfo {
		return &UpdateInfo{NewVersion: available, Current: "1.0.0", Latest: latest}
	})
	rep := &fakeReporter{updates: true}
	d := newUpdateDaemon(t, rep)
	ctx := context.Background()

	// up to date -> /0, no warn, Available=false + Ping.OK=true + Latest recorded
	d.updateTick(ctx)
	if d.updateWarned {
		t.Fatalf("up-to-date must not set updateWarned")
	}
	if s := rep.snapshot(); s.updatesReported != 1 || s.lastAvailable {
		t.Fatalf("first tick: want 1 ping available=false, got %d/%v", s.updatesReported, s.lastAvailable)
	}
	if u := mustDaemonStatus(t, d).Update; u == nil || u.Available || !u.Ping.OK || u.Latest != latest {
		t.Fatalf("first tick status: %+v", u)
	}

	// transition to available -> /1, updateWarned set
	available = true
	d.updateTick(ctx)
	if !d.updateWarned {
		t.Fatalf("transition to available must set updateWarned")
	}
	if s := rep.snapshot(); s.updatesReported != 2 || !s.lastAvailable {
		t.Fatalf("second tick: want /1, got %d/%v", s.updatesReported, s.lastAvailable)
	}

	// still available -> keeps updateWarned, pings again (a persistent /1)
	d.updateTick(ctx)
	if !d.updateWarned {
		t.Fatalf("still-available must keep updateWarned set")
	}
	if s := rep.snapshot(); s.updatesReported != 3 {
		t.Fatalf("third tick must still ping, got %d", s.updatesReported)
	}
	if u := mustDaemonStatus(t, d).Update; u == nil || !u.Available {
		t.Fatalf("third tick status must show available: %+v", u)
	}

	// cleared -> /0, updateWarned reset (a later update warns again)
	available = false
	d.updateTick(ctx)
	if d.updateWarned {
		t.Fatalf("clearing must reset updateWarned")
	}
	if s := rep.snapshot(); s.updatesReported != 4 || s.lastAvailable {
		t.Fatalf("fourth tick: want /0, got %d/%v", s.updatesReported, s.lastAvailable)
	}
}

// TestUpdateTickInconclusiveKeepsVerdict is the regression for the flapping bug: a failed
// GitHub check (NewVersion=false, empty Latest) must NOT flip a live /1 (update available)
// to /0 (green); it re-affirms the last persisted verdict instead.
func TestUpdateTickInconclusiveKeepsVerdict(t *testing.T) {
	rep := &fakeReporter{updates: true}
	d := newUpdateDaemon(t, rep)
	ctx := context.Background()

	// establish an "available" verdict
	stubUpdate(t, func() *UpdateInfo {
		return &UpdateInfo{NewVersion: true, Current: "1.0.0", Latest: "9.9.9"}
	})
	d.updateTick(ctx)
	if s := rep.snapshot(); s.updatesReported != 1 || !s.lastAvailable {
		t.Fatalf("setup: want /1, got %d/%v", s.updatesReported, s.lastAvailable)
	}

	// inconclusive check (GitHub unreachable -> NewVersion:false, empty Latest)
	stubUpdate(t, func() *UpdateInfo {
		return &UpdateInfo{NewVersion: false, Current: "1.0.0", Latest: ""}
	})
	d.updateTick(ctx)
	if s := rep.snapshot(); s.updatesReported != 2 || !s.lastAvailable {
		t.Fatalf("inconclusive must re-affirm /1, not flip to /0; got %d ping available=%v", s.updatesReported, s.lastAvailable)
	}
	if u := mustDaemonStatus(t, d).Update; u == nil || !u.Available {
		t.Fatalf("inconclusive must keep Available=true in status: %+v", u)
	}
}

// TestUpdateTickInconclusiveNoPriorSkips: an inconclusive check with no prior verdict skips
// the ping entirely (do not invent an "up to date" /0 from a failed check).
func TestUpdateTickInconclusiveNoPriorSkips(t *testing.T) {
	stubUpdate(t, func() *UpdateInfo {
		return &UpdateInfo{NewVersion: false, Current: "1.0.0", Latest: ""}
	})
	rep := &fakeReporter{updates: true}
	d := newUpdateDaemon(t, rep)

	d.updateTick(context.Background())
	if s := rep.snapshot(); s.updatesReported != 0 {
		t.Fatalf("inconclusive with no prior verdict must skip the ping, got %d", s.updatesReported)
	}
	if u := mustDaemonStatus(t, d).Update; u != nil {
		t.Fatalf("no ping -> no update record, got %+v", u)
	}
}

// TestUpdateTickNoURLSwallowed: with no updates URL resolved, ReportUpdate's ErrNoUpdatesURL
// is swallowed and the status records a liveness trace (Ping.OK=false, reason=no_url).
func TestUpdateTickNoURLSwallowed(t *testing.T) {
	stubUpdate(t, func() *UpdateInfo {
		return &UpdateInfo{NewVersion: false, Current: "1.0.0", Latest: "1.0.0"}
	})
	rep := &fakeReporter{updates: false} // no updates URL resolved
	d := newUpdateDaemon(t, rep)

	d.updateTick(context.Background())
	if s := rep.snapshot(); s.updatesReported != 0 {
		t.Fatalf("a no-url fake must not count a transmitted ping, got %d", s.updatesReported)
	}
	if u := mustDaemonStatus(t, d).Update; u == nil || u.Ping.OK || u.Ping.Reason != health.ReasonNoURL {
		t.Fatalf("no-url must record reason=no_url, ok=false: %+v", u)
	}
}
