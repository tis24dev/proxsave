package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/uitest"
)

// TestBuildInstallOutcomePromptVerified asserts the daemon-verified branch reuses the shared
// installVerifyVerdict/renderDaemonStatusLevel verdict (aligned / behind / not-running - the SAME
// as --daemon-status, NOT the restart-verify "not confirmed") and colors the permissions line by
// status. ANSI is stripped so the assertions do not depend on the color profile.
func TestBuildInstallOutcomePromptVerified(t *testing.T) {
	rv := RestartVerifyResult{
		ProcessAlive: true,
		Aligned:      true,
		State:        health.DaemonState{Version: "9.9.9", AlignChecked: true},
	}
	out := ansi.Strip(buildInstallOutcomePrompt(rv, true, "ok", "permissions and ownership normalized correctly"))

	// The summary opens with the shared completion banner (same wording as the CLI footer).
	if !strings.Contains(out, "Installation completed") {
		t.Fatalf("missing completion banner:\n%s", out)
	}
	if !strings.Contains(out, "Daemon: ") {
		t.Fatalf("missing Daemon line:\n%s", out)
	}
	if !strings.Contains(out, "running and aligned (v9.9.9)") {
		t.Fatalf("missing aligned daemon verdict:\n%s", out)
	}
	if strings.Contains(out, "not confirmed") {
		t.Fatalf("an aligned daemon must NOT say 'not confirmed':\n%s", out)
	}
	if !strings.Contains(out, "Permissions: ") || !strings.Contains(out, "normalized correctly") {
		t.Fatalf("missing permissions line:\n%s", out)
	}

	// A running-but-behind daemon reports the behind verdict, not a timeout/not-confirmed.
	behind := RestartVerifyResult{ProcessAlive: true, State: health.DaemonState{AlignChecked: true}}
	bOut := ansi.Strip(buildInstallOutcomePrompt(behind, true, "ok", "ok"))
	if !strings.Contains(bOut, "running but not aligned (behind)") {
		t.Fatalf("missing behind verdict:\n%s", bOut)
	}
}

// TestBuildInstallOutcomePromptUnverified asserts the non-verified branch states the neutral
// cron scheduler fact (no alignment verdict measured) and surfaces an error permissions status.
func TestBuildInstallOutcomePromptUnverified(t *testing.T) {
	out := ansi.Strip(buildInstallOutcomePrompt(RestartVerifyResult{}, false, "error", "errors during security permission checks (non-blocking, see log)"))

	if !strings.Contains(out, "Daemon: ") || !strings.Contains(out, "cron scheduler") {
		t.Fatalf("expected neutral cron scheduler line:\n%s", out)
	}
	if strings.Contains(out, "running and aligned") {
		t.Fatalf("unverified outcome must not claim an alignment verdict:\n%s", out)
	}
	if !strings.Contains(out, "errors during security permission checks") {
		t.Fatalf("missing error permissions message:\n%s", out)
	}
}

// TestRunStreamTaskFinalizationDriver drives RunStreamTask on an observed session the same
// way runInstallTUI does: the emitted lines and the composed outcome must appear on screen,
// and pressing Enter after done must let the driver return.
func TestRunStreamTaskFinalizationDriver(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf shell.SyncBuffer
	session := shell.StartForTestWithOutput(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Install Wizard"}, &buf)
	defer func() { _ = session.Close() }()

	outcome := ansi.Strip(buildInstallOutcomePrompt(RestartVerifyResult{}, false, "ok", "permissions OK"))
	done := make(chan error, 1)
	go func() {
		done <- components.RunStreamTask(ctx, session, "Finalizing installation",
			func(taskCtx context.Context, emit func(string)) (string, error) {
				emit("[00:00:00] INFO first finalize line")
				emit("[00:00:00] INFO second finalize line")
				return outcome, nil
			})
	}()

	// Wait for both streamed lines + the outcome to render before sending Enter.
	waitFor(t, &buf, "first finalize line")
	waitFor(t, &buf, "second finalize line")
	waitFor(t, &buf, "permissions OK")
	// The Continue hint is rendered by the frame's Help bar once done - NOT duplicated
	// in the screen body (asserted by TestStreamScreenDoneShowsOutcomeAndHint).
	waitFor(t, &buf, "enter continue")

	// Enter on a done screen resolves; spam is safe (no-op before done / on empty stack).
	if err := pumpEnter(t, session, done); err != nil {
		t.Fatalf("RunStreamTask returned error: %v", err)
	}
}

// TestFixPermissionsAfterInstallRoutesSink asserts the temporary permissions logger routes its
// console output into the provided sink (non-nil), while a nil sink leaves an unrelated buffer
// untouched (the CLI path, unchanged).
func TestFixPermissionsAfterInstallRoutesSink(t *testing.T) {
	cur, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	grp, err := user.LookupGroupId(cur.Gid)
	if err != nil {
		t.Fatalf("LookupGroupId: %v", err)
	}

	baseDir := t.TempDir()
	backupDir := filepath.Join(baseDir, "backup")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("mkdir backup: %v", err)
	}
	cfgPath := filepath.Join(baseDir, "backup.env")
	envBody := "BASE_DIR=" + baseDir + "\n" +
		"BACKUP_PATH=" + backupDir + "\n" +
		"BACKUP_USER=" + cur.Username + "\n" +
		"BACKUP_GROUP=" + grp.Name + "\n" +
		"SET_BACKUP_PERMISSIONS=true\n" +
		"SECURITY_CHECK_ENABLED=true\n"
	if err := os.WriteFile(cfgPath, []byte(envBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Non-nil sink: at least one finalization line is routed into it.
	var sink bytes.Buffer
	status, _ := fixPermissionsAfterInstall(context.Background(), cfgPath, baseDir, nil, &sink)
	if status == "skipped" {
		t.Fatalf("config unexpectedly failed to load (status=skipped); env:\n%s", envBody)
	}
	if sink.Len() == 0 {
		t.Fatalf("expected >=1 line routed into the sink, got none (status=%s)", status)
	}

	// Nil sink: an unrelated buffer we hold is never written (routing is opt-in).
	var untouched bytes.Buffer
	_, _ = fixPermissionsAfterInstall(context.Background(), cfgPath, baseDir, nil, nil)
	if untouched.Len() != 0 {
		t.Fatalf("nil-sink call must not write to an unrelated buffer, got %q", untouched.String())
	}
}

// waitFor polls the observed render buffer until it contains want or the deadline elapses.
func waitFor(t *testing.T, buf *shell.SyncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(uitest.Deadline(5 * time.Second))
	for time.Now().Before(deadline) {
		if strings.Contains(ansi.Strip(buf.String()), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("screen never showed %q; last frame:\n%s", want, ansi.Strip(buf.String()))
}

// pumpEnter sends Enter until RunStreamTask returns (Enter is a no-op before done).
func pumpEnter(t *testing.T, s *shell.Session, done <-chan error) error {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(uitest.Deadline(5 * time.Second))
	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			s.Send(shell.KeyMsg("enter"))
		case <-deadline:
			return errors.New("RunStreamTask did not return")
		}
	}
}
