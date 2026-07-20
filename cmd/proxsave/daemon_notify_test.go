package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
)

// notifyDaemon builds a minimal daemon wired for reportNotifyOutcomes: a temp BaseDir keeps
// the status writes out of the source tree, self mode avoids any centralized rebuild/network.
func notifyDaemon(base, mode string) *daemon {
	return &daemon{
		cfg: &config.Config{BaseDir: base, HealthcheckEnabled: true, HealthcheckMode: mode},
		now: time.Now,
	}
}

// pingByName indexes a snapshot's recorded pings by check name.
func pingByName(pings []fakePing) map[string]fakePing {
	m := make(map[string]fakePing, len(pings))
	for _, p := range pings {
		m[p.name] = p
	}
	return m
}

func TestSeverityToSuffix(t *testing.T) {
	tests := []struct {
		in         string
		wantSuffix string
		wantDown   bool
		wantSkip   bool
	}{
		{"ok", "/0", false, false},
		{"warning", "/1", true, false},
		{"error", "/1", true, false},
		{"disabled", "", false, true},
		{"", "", false, true},
		{"nonsense", "", false, true},
		{"OK ", "/0", false, false},   // case- and space-insensitive
		{" Error", "/1", true, false}, // ditto
	}
	for _, tc := range tests {
		suffix, down, skip := severityToSuffix(tc.in)
		if suffix != tc.wantSuffix || down != tc.wantDown || skip != tc.wantSkip {
			t.Fatalf("severityToSuffix(%q) = (%q,%v,%v), want (%q,%v,%v)",
				tc.in, suffix, down, skip, tc.wantSuffix, tc.wantDown, tc.wantSkip)
		}
	}
}

// The happy path: one ping per channel the child reported, with the right /0-vs-/1 suffix,
// skipping a "disabled" channel, and persisting the Down + OK signals into the status file.
func TestReportNotifyOutcomesPingsPerChannel(t *testing.T) {
	base := t.TempDir()
	rid := "rid-per-channel"
	rep := &fakeReporter{checks: map[string]bool{
		"notify-email":    true,
		"notify-telegram": true,
		"notify-gotify":   true,
	}}
	d := notifyDaemon(base, "self")

	results := map[string]string{
		"Email":    "ok",
		"Telegram": "error",
		"Gotify":   "warning",
		"Webhook":  "disabled",
	}
	if err := health.WriteNotifyResults(base, rid, time.Now().Unix(), results); err != nil {
		t.Fatalf("WriteNotifyResults: %v", err)
	}

	d.reportNotifyOutcomes(context.Background(), rep, rid)

	pings := pingByName(rep.snapshot().pings)
	if len(pings) != 3 {
		t.Fatalf("want 3 transmitted pings, got %d: %#v", len(pings), pings)
	}
	if p, ok := pings["notify-email"]; !ok || p.suffix != "/0" || p.rid != rid {
		t.Fatalf("notify-email ping = %+v (present=%v), want suffix /0 rid %q", p, ok, rid)
	}
	if p, ok := pings["notify-telegram"]; !ok || p.suffix != "/1" {
		t.Fatalf("notify-telegram ping = %+v (present=%v), want suffix /1", p, ok)
	}
	if p, ok := pings["notify-gotify"]; !ok || p.suffix != "/1" {
		t.Fatalf("notify-gotify ping = %+v (present=%v), want suffix /1", p, ok)
	}
	if _, ok := pings["notify-webhook"]; ok {
		t.Fatalf("a disabled channel must not be pinged, got %+v", pings["notify-webhook"])
	}

	st, err := health.LoadStatus(base)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	tg := st.Record("notify-telegram")
	if tg == nil || !tg.OK || !tg.Down {
		t.Fatalf("notify-telegram record = %+v, want OK && Down", tg)
	}
	em := st.Record("notify-email")
	if em == nil || !em.OK || em.Down {
		t.Fatalf("notify-email record = %+v, want OK && !Down", em)
	}
	if r := st.Record("notify-webhook"); r != nil {
		t.Fatalf("a skipped channel must record nothing, got %+v", r)
	}
}

// A results file whose rid does not match this run (a stale file, or a child that crashed
// before Phase-7) is rejected: no pings, no notify records.
func TestReportNotifyOutcomesStaleRidSkips(t *testing.T) {
	base := t.TempDir()
	rep := &fakeReporter{checks: map[string]bool{"notify-email": true}}
	d := notifyDaemon(base, "self")

	if err := health.WriteNotifyResults(base, "old", time.Now().Unix(), map[string]string{"Email": "ok"}); err != nil {
		t.Fatalf("WriteNotifyResults: %v", err)
	}

	d.reportNotifyOutcomes(context.Background(), rep, "new")

	if pings := rep.snapshot().pings; len(pings) != 0 {
		t.Fatalf("stale rid must ping nothing, got %#v", pings)
	}
	st, _ := health.LoadStatus(base)
	if r := st.Record("notify-email"); r != nil {
		t.Fatalf("stale rid must record nothing, got %+v", r)
	}
}

// No handoff file at all -> nothing to do.
func TestReportNotifyOutcomesMissingFileSkips(t *testing.T) {
	base := t.TempDir()
	rep := &fakeReporter{checks: map[string]bool{"notify-email": true}}
	d := notifyDaemon(base, "self")

	d.reportNotifyOutcomes(context.Background(), rep, "rid-missing")

	if pings := rep.snapshot().pings; len(pings) != 0 {
		t.Fatalf("missing file must ping nothing, got %#v", pings)
	}
}

// A channel whose check URL is not resolved (self mode / old server) transmits nothing but
// STILL records a liveness trace: OK==false with Reason no_url, so the run-side section can
// tell "not provisioned yet" from a real transmit failure.
func TestReportNotifyOutcomesUnresolvedRecordsNoURL(t *testing.T) {
	base := t.TempDir()
	rid := "rid-unresolved"
	rep := &fakeReporter{checks: map[string]bool{}} // notify-email NOT resolved
	d := notifyDaemon(base, "self")

	if err := health.WriteNotifyResults(base, rid, time.Now().Unix(), map[string]string{"Email": "ok"}); err != nil {
		t.Fatalf("WriteNotifyResults: %v", err)
	}

	d.reportNotifyOutcomes(context.Background(), rep, rid)

	if pings := rep.snapshot().pings; len(pings) != 0 {
		t.Fatalf("an unresolved channel must transmit no ping, got %#v", pings)
	}
	st, err := health.LoadStatus(base)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	rec := st.Record("notify-email")
	if rec == nil {
		t.Fatal("an unresolved channel must still record a no_url trace, got nil")
	}
	if rec.OK {
		t.Fatalf("unresolved record must be OK=false, got %+v", rec)
	}
	if rec.Reason != health.ReasonNoURL {
		t.Fatalf("Reason = %q, want %q", rec.Reason, health.ReasonNoURL)
	}
}

// A nil reporter (no ping URL ever resolved) must not panic and records the same no_url trace.
func TestReportNotifyOutcomesNilReporterNoPanic(t *testing.T) {
	base := t.TempDir()
	rid := "rid-nil"
	d := notifyDaemon(base, "self")

	if err := health.WriteNotifyResults(base, rid, time.Now().Unix(), map[string]string{"Email": "ok"}); err != nil {
		t.Fatalf("WriteNotifyResults: %v", err)
	}

	d.reportNotifyOutcomes(context.Background(), nil, rid) // nil backupReporter

	st, err := health.LoadStatus(base)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	rec := st.Record("notify-email")
	if rec == nil {
		t.Fatal("a nil reporter must still record a no_url trace, got nil")
	}
	if rec.OK || rec.Reason != health.ReasonNoURL {
		t.Fatalf("nil-reporter record = %+v, want OK=false Reason=%q", rec, health.ReasonNoURL)
	}
}

// buildBackupCmd stamps the run id onto the child's environment so the child's handoff file
// can be correlated with this run; a blank rid leaves the environment untouched.
func TestBuildBackupCmdSetsRunIDEnv(t *testing.T) {
	d := newTestDaemon(t, nil, func(ctx context.Context) *exec.Cmd {
		return exec.Command("true")
	}, time.Hour)

	// rid set -> PROXSAVE_RUN_ID=rid42 is appended.
	cmd := d.buildBackupCmd(context.Background(), nil, "rid42")
	if !envContains(cmd.Env, health.EnvRunID+"=rid42") {
		t.Fatalf("cmd.Env must carry %s=rid42, got %v", health.EnvRunID, cmd.Env)
	}

	// rid empty -> no PROXSAVE_RUN_ID entry is added.
	cmd = d.buildBackupCmd(context.Background(), nil, "")
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, health.EnvRunID+"=") {
			t.Fatalf("empty rid must not add %s, got %q", health.EnvRunID, e)
		}
	}
}

func envContains(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}
