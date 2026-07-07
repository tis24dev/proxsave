package main

import (
	"context"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
)

// manualDaemon builds a minimal daemon wired for processManualOutcome: a temp BaseDir keeps the
// status/handoff writes out of the source tree; self mode avoids any centralized rebuild/network.
func manualDaemon(base, mode string, rep backupReporter) *daemon {
	return &daemon{
		cfg:      &config.Config{BaseDir: base, HealthcheckEnabled: true, HealthcheckMode: mode},
		reporter: rep,
		now:      time.Now,
	}
}

// The happy path: a handed-off standalone outcome pings the backup FINISH with the run's exit
// code, records it under KindRunFinished (the same kind a supervised run uses), and removes the
// handoff (processed-once).
func TestProcessManualOutcomePingsFinish(t *testing.T) {
	base := t.TempDir()
	rep := &fakeReporter{backupURL: true}
	d := manualDaemon(base, "self", rep)

	if err := health.WriteManualOutcome(base, "rid-manual", time.Now().Unix(), 4); err != nil {
		t.Fatalf("WriteManualOutcome: %v", err)
	}

	d.processManualOutcome(context.Background())

	s := rep.snapshot()
	if s.finished != 1 {
		t.Fatalf("want exactly 1 finish ping, got %d", s.finished)
	}
	if s.lastCode != 4 {
		t.Fatalf("finish ping exit code = %d, want 4", s.lastCode)
	}

	st, err := health.LoadStatus(base)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	rec := st.Record(health.KindRunFinished)
	if rec == nil || !rec.OK {
		t.Fatalf("KindRunFinished record = %+v, want set && OK", rec)
	}

	mo, _ := health.LoadManualOutcome(base)
	if mo.RID != "" {
		t.Fatalf("handoff must be removed after processing, still present: %+v", mo)
	}
}

// A stale outcome (older than the window) is dropped WITHOUT pinging and the handoff is removed,
// so a wake that arrives long after the run never flips the backup check.
func TestProcessManualOutcomeStaleDropped(t *testing.T) {
	base := t.TempDir()
	rep := &fakeReporter{backupURL: true}
	d := manualDaemon(base, "self", rep)

	stale := time.Now().Add(-30 * time.Minute).Unix()
	if err := health.WriteManualOutcome(base, "rid-stale", stale, 0); err != nil {
		t.Fatalf("WriteManualOutcome: %v", err)
	}

	d.processManualOutcome(context.Background())

	if s := rep.snapshot(); s.finished != 0 {
		t.Fatalf("stale outcome must not ping, got %d finish pings", s.finished)
	}
	st, _ := health.LoadStatus(base)
	if rec := st.Record(health.KindRunFinished); rec != nil {
		t.Fatalf("stale outcome must record nothing, got %+v", rec)
	}
	mo, _ := health.LoadManualOutcome(base)
	if mo.RID != "" {
		t.Fatalf("stale handoff must be removed, still present: %+v", mo)
	}
}

// No handoff file at all -> a no-op (no ping, no record).
func TestProcessManualOutcomeMissingNoop(t *testing.T) {
	base := t.TempDir()
	rep := &fakeReporter{backupURL: true}
	d := manualDaemon(base, "self", rep)

	d.processManualOutcome(context.Background())

	if s := rep.snapshot(); s.finished != 0 {
		t.Fatalf("missing handoff must ping nothing, got %d finish pings", s.finished)
	}
	st, _ := health.LoadStatus(base)
	if rec := st.Record(health.KindRunFinished); rec != nil {
		t.Fatalf("missing handoff must record nothing, got %+v", rec)
	}
}

// A nil reporter (no backup ping URL ever resolved) transmits nothing but STILL records a no_url
// liveness trace (OK=false, Reason no_url) and removes the handoff, so the section can tell
// "daemon up but not provisioned" from a real transmit failure.
func TestProcessManualOutcomeNilReporterRecordsNoURL(t *testing.T) {
	base := t.TempDir()
	d := manualDaemon(base, "self", nil) // nil reporter

	if err := health.WriteManualOutcome(base, "rid-nourl", time.Now().Unix(), 0); err != nil {
		t.Fatalf("WriteManualOutcome: %v", err)
	}

	d.processManualOutcome(context.Background())

	st, err := health.LoadStatus(base)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	rec := st.Record(health.KindRunFinished)
	if rec == nil {
		t.Fatal("nil reporter must still record a no_url trace, got nil")
	}
	if rec.OK || rec.Reason != health.ReasonNoURL {
		t.Fatalf("nil-reporter record = %+v, want OK=false Reason=%q", rec, health.ReasonNoURL)
	}
	mo, _ := health.LoadManualOutcome(base)
	if mo.RID != "" {
		t.Fatalf("handoff must be removed even on a no_url outcome, still present: %+v", mo)
	}
}

// Healthchecks disabled -> processManualOutcome is a no-op even if a handoff is present (the SOLE
// pinger has nothing to ping to), and it leaves the handoff untouched.
func TestProcessManualOutcomeDisabledNoop(t *testing.T) {
	base := t.TempDir()
	rep := &fakeReporter{backupURL: true}
	d := manualDaemon(base, "self", rep)
	d.cfg.HealthcheckEnabled = false

	if err := health.WriteManualOutcome(base, "rid-disabled", time.Now().Unix(), 0); err != nil {
		t.Fatalf("WriteManualOutcome: %v", err)
	}

	d.processManualOutcome(context.Background())

	if s := rep.snapshot(); s.finished != 0 {
		t.Fatalf("disabled healthchecks must ping nothing, got %d finish pings", s.finished)
	}
}
