package main

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// handoffOpts builds backupModeOptions for the run-side decision helper: a temp BaseDir keeps the
// handoff write out of the source tree; healthchecks + backups enabled so gating passes.
func handoffOpts(base string) backupModeOptions {
	return backupModeOptions{
		cfg: &config.Config{BaseDir: base, HealthcheckEnabled: true, BackupEnabled: true},
	}
}

// attemptedResult is a backupModeResult that looks like a real backup attempt (supportStats set),
// so the handoff gate ("a backup was attempted") passes.
func attemptedResult(exit int) backupModeResult {
	return backupModeResult{supportStats: &orchestrator.BackupStats{}, exitCode: exit}
}

// withProbe swaps the daemonAliveProbe seam for the duration of a test.
func withProbe(t *testing.T, fn func(pid int) bool) {
	t.Helper()
	prev := daemonAliveProbe
	daemonAliveProbe = fn
	t.Cleanup(func() { daemonAliveProbe = prev })
}

// A STANDALONE run whose "daemon alive" probe returns true writes the handoff file with the run's
// exit code (the daemon then pings it on SIGUSR1).
func TestMaybeHandoffWritesWhenDaemonAlive(t *testing.T) {
	base := t.TempDir()
	t.Setenv(health.EnvRunID, "") // standalone (PROXSAVE_RUN_ID unset)

	// The pid we record is our own; the probe is faked, but the helper still SIGUSR1s the pid, so
	// catch that self-directed wake instead of taking the default terminate action.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGUSR1)
	defer signal.Stop(sigCh)

	if err := health.WriteDaemonPID(base, os.Getpid()); err != nil {
		t.Fatalf("WriteDaemonPID: %v", err)
	}
	withProbe(t, func(int) bool { return true })

	maybeHandoffManualBackup(handoffOpts(base), attemptedResult(5))

	// Consume the self-directed wake before the handler is removed so it never hits the default.
	select {
	case <-sigCh:
	case <-time.After(time.Second):
		t.Fatal("expected the helper to SIGUSR1 the daemon pid")
	}

	mo, err := health.LoadManualOutcome(base)
	if err != nil {
		t.Fatalf("LoadManualOutcome: %v", err)
	}
	if mo.RID == "" {
		t.Fatal("handoff file must be written when the daemon is alive")
	}
	if mo.ExitCode != 5 {
		t.Fatalf("handoff exit code = %d, want 5", mo.ExitCode)
	}
}

// A STANDALONE run whose probe returns false (pid recorded but not a live proxsave daemon) writes
// NOTHING and sends no signal.
func TestMaybeHandoffSkipsWhenDaemonDead(t *testing.T) {
	base := t.TempDir()
	t.Setenv(health.EnvRunID, "")

	if err := health.WriteDaemonPID(base, 4242); err != nil {
		t.Fatalf("WriteDaemonPID: %v", err)
	}
	withProbe(t, func(int) bool { return false })

	maybeHandoffManualBackup(handoffOpts(base), attemptedResult(0))

	if mo, _ := health.LoadManualOutcome(base); mo.RID != "" {
		t.Fatalf("no handoff must be written when the daemon is not alive, got %+v", mo)
	}
}

// The daemon's OWN supervised child (PROXSAVE_RUN_ID set) must NOT hand off -- the daemon already
// pings that run's outcome in runOnce, so a handoff would double-ping. The probe must not even be
// consulted.
func TestMaybeHandoffSkipsSupervisedChild(t *testing.T) {
	base := t.TempDir()
	t.Setenv(health.EnvRunID, "child-rid-123")

	if err := health.WriteDaemonPID(base, os.Getpid()); err != nil {
		t.Fatalf("WriteDaemonPID: %v", err)
	}
	withProbe(t, func(int) bool {
		t.Fatal("a supervised child must return before probing the daemon")
		return false
	})

	maybeHandoffManualBackup(handoffOpts(base), attemptedResult(0))

	if mo, _ := health.LoadManualOutcome(base); mo.RID != "" {
		t.Fatalf("a supervised child must write nothing, got %+v", mo)
	}
}

// A run that did NOT attempt a backup (disabled / benign concurrency skip: supportStats AND
// earlyErrorState both nil) never hands off, regardless of the daemon being alive.
func TestMaybeHandoffSkipsWhenNoBackupAttempted(t *testing.T) {
	base := t.TempDir()
	t.Setenv(health.EnvRunID, "")

	if err := health.WriteDaemonPID(base, os.Getpid()); err != nil {
		t.Fatalf("WriteDaemonPID: %v", err)
	}
	withProbe(t, func(int) bool {
		t.Fatal("a run that attempted no backup must return before probing")
		return false
	})

	// Both supportStats and earlyErrorState nil = disabled/concurrency-skip.
	maybeHandoffManualBackup(handoffOpts(base), backupModeResult{exitCode: 0})

	if mo, _ := health.LoadManualOutcome(base); mo.RID != "" {
		t.Fatalf("no backup attempted must write nothing, got %+v", mo)
	}
}
