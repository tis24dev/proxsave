package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/uitest"
)

type driver struct {
	t       *testing.T
	buf     *shell.SyncBuffer
	pushes  chan string
	session *shell.Session
}

func newDriver(t *testing.T) *driver {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &driver{t: t, buf: &shell.SyncBuffer{}, pushes: make(chan string, 64)}
	d.session = shell.StartObservedForTest(ctx, shell.Config{
		AppName:  "ProxSave",
		Subtitle: "Install Wizard",
	}, d.buf, func(title string) { d.pushes <- title })
	t.Cleanup(func() {
		_ = d.session.Close()
		cancel()
	})
	return d
}

func (d *driver) waitScreen(title string) {
	d.t.Helper()
	deadline := time.After(uitest.Deadline(60 * time.Second))
	for {
		select {
		case got := <-d.pushes:
			if got == title {
				return
			}
		case <-deadline:
			out := ansi.Strip(d.buf.String())
			if len(out) > 2000 {
				out = out[len(out)-2000:]
			}
			d.t.Fatalf("timed out waiting for screen %q; output tail:\n%s", title, out)
		}
	}
}

func (d *driver) keys(script string) {
	d.t.Helper()
	for _, msg := range shell.Keys(script) {
		d.session.Send(msg)
	}
}

// waitText blocks until the (ANSI-stripped) rendered buffer contains want, so tests
// can assert on styled content that lands slightly after the screen-title push.
func (d *driver) waitText(want string) {
	d.t.Helper()
	deadline := time.After(uitest.Deadline(60 * time.Second))
	for {
		if strings.Contains(ansi.Strip(d.buf.String()), want) {
			return
		}
		select {
		case <-deadline:
			out := ansi.Strip(d.buf.String())
			if len(out) > 2000 {
				out = out[len(out)-2000:]
			}
			d.t.Fatalf("timed out waiting for text %q; output tail:\n%s", want, out)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestResolveExistingConfig(t *testing.T) {
	d := newDriver(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "backup.env")

	// Missing file: overwrite without any screen.
	action, err := ResolveExistingConfig(context.Background(), d.session, configPath)
	if err != nil || action != installer.ExistingConfigOverwrite {
		t.Fatalf("missing file: action=%v err=%v", action, err)
	}

	if err := os.WriteFile(configPath, []byte("X=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	type result struct {
		action installer.ExistingConfigAction
		err    error
	}
	resCh := make(chan result, 1)
	ask := func() {
		go func() {
			action, err := ResolveExistingConfig(context.Background(), d.session, configPath)
			resCh <- result{action, err}
		}()
	}

	// Bare Enter picks the safe default: keep existing and continue.
	ask()
	d.waitScreen("Existing configuration")
	d.keys("enter")
	if res := <-resCh; res.err != nil || res.action != installer.ExistingConfigKeepContinue {
		t.Fatalf("default must be keep-continue, got %+v", res)
	}

	ask()
	d.waitScreen("Existing configuration")
	d.keys("down enter")
	if res := <-resCh; res.err != nil || res.action != installer.ExistingConfigEdit {
		t.Fatalf("expected edit, got %+v", res)
	}

	ask()
	d.waitScreen("Existing configuration")
	d.keys("esc")
	if res := <-resCh; !errors.Is(res.err, installer.ErrInstallCancelled) {
		t.Fatalf("esc must cancel the install, got %+v", res)
	}
}

func TestCollectWizardDataDeclineAll(t *testing.T) {
	d := newDriver(t)

	type result struct {
		data *installer.InstallWizardData
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		data, err := CollectWizardData(context.Background(), d.session, "")
		resCh <- result{data, err}
	}()

	// Single aligned form: inactive dependent rows are skipped. Fresh install
	// defaults to the daemon, so the Healthchecks row is active too: Enter through
	// the 9 active rows reaches Continue; the final Enter submits.
	d.waitScreen("Configuration")
	for i := 0; i < 10; i++ {
		d.keys("enter")
	}

	res := <-resCh
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	data := res.data
	if data.EnableSecondaryStorage || data.EnableCloudStorage || data.EnableEncryption {
		t.Fatalf("decline-all produced enabled toggles: %+v", data)
	}
	// Fresh daemon default: Healthchecks defaults to centralized.
	if data.SchedulerMode != "daemon" || data.HealthcheckMode != "centralized" {
		t.Fatalf("fresh daemon must default Healthchecks to centralized: mode=%q hc=%q", data.SchedulerMode, data.HealthcheckMode)
	}
	if data.NotificationMode != "none" {
		t.Fatalf("notification mode = %q, want none", data.NotificationMode)
	}
	if data.BackupFirewallRules == nil || *data.BackupFirewallRules {
		t.Fatal("firewall must be explicitly false")
	}
	if data.CronTime == "" {
		t.Fatal("cron time must default")
	}
	// The collected data must produce a valid config via the shared builder.
	if _, err := installer.ApplyInstallData("", data); err != nil {
		t.Fatalf("ApplyInstallData rejected collected data: %v", err)
	}
}

// TestCollectWizardDataPrefillNoOp locks the anti-drift core: with a fully
// populated existing template, an Enter-only run returns exactly the stored
// settings (the historical no-op-edit reset bug).
func TestCollectWizardDataPrefillNoOp(t *testing.T) {
	d := newDriver(t)
	template := config.DefaultEnvTemplate()
	for _, kv := range [][2]string{
		{"SECONDARY_ENABLED", "true"},
		{"SECONDARY_PATH", "/mnt/nas-backup"},
		{"SECONDARY_LOG_PATH", "/mnt/nas-backup/log"},
		{"CLOUD_ENABLED", "true"},
		{"CLOUD_REMOTE", "myremote:pbs-backups"},
		{"CLOUD_LOG_PATH", "myremote:/logs"},
		{"BACKUP_FIREWALL_RULES", "true"},
		{"TELEGRAM_ENABLED", "true"},
		{"BOT_TELEGRAM_TYPE", "personal"},
		{"EMAIL_ENABLED", "true"},
		{"EMAIL_DELIVERY_METHOD", "pmf"},
		{"ENCRYPT_ARCHIVE", "true"},
	} {
		template = installer.SetEnvValueInTemplate(template, kv[0], kv[1])
	}

	type result struct {
		data *installer.InstallWizardData
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		data, err := CollectWizardData(context.Background(), d.session, template)
		resCh <- result{data, err}
	}()

	// Single aligned form, everything prefilled/active: Enter through all
	// 13 rows plus the Continue button is the no-op edit.
	d.waitScreen("Configuration")
	for i := 0; i < 14; i++ {
		d.keys("enter")
	}

	res := <-resCh
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	data := res.data
	if !data.EnableSecondaryStorage || data.SecondaryPath != "/mnt/nas-backup" || data.SecondaryLogPath != "/mnt/nas-backup/log" {
		t.Fatalf("secondary prefill lost: %+v", data)
	}
	if !data.EnableCloudStorage || data.RcloneBackupRemote != "myremote:pbs-backups" || data.RcloneLogRemote != "myremote:/logs" {
		t.Fatalf("cloud prefill lost: %+v", data)
	}
	if data.BackupFirewallRules == nil || !*data.BackupFirewallRules {
		t.Fatal("firewall prefill lost")
	}
	if data.NotificationMode != "both" {
		t.Fatalf("notification mode = %q, want both", data.NotificationMode)
	}
	if data.EmailDeliveryMethod != "pmf" {
		t.Fatalf("email delivery method = %q, want pmf (prefill)", data.EmailDeliveryMethod)
	}
	if !data.EnableEncryption {
		t.Fatal("encryption prefill lost")
	}

	// End to end through the shared builder: BOT_TELEGRAM_TYPE=personal must
	// survive a no-op edit.
	out, err := installer.ApplyInstallData(template, data)
	if err != nil {
		t.Fatalf("ApplyInstallData: %v", err)
	}
	prefill := installer.DeriveInstallWizardPrefill(out)
	if prefill.TelegramType != "personal" || prefill.EmailDeliveryMethod != "pmf" {
		t.Fatalf("no-op edit reset stored settings: %+v", prefill)
	}
}

// TestCollectWizardDataEditWithoutSchedulerModeDefaultsCron locks the no-op-edit
// invariant for a legacy/pre-daemon config that lacks SCHEDULER_MODE: an Enter-only
// edit must NOT silently flip the scheduler to daemon (it stays on cron, matching
// the CLI's schedulerEngineDefault).
func TestCollectWizardDataEditWithoutSchedulerModeDefaultsCron(t *testing.T) {
	d := newDriver(t)
	template := installer.UnsetEnvValueInTemplate(config.DefaultEnvTemplate(), "SCHEDULER_MODE")

	type result struct {
		data *installer.InstallWizardData
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		data, err := CollectWizardData(context.Background(), d.session, template)
		resCh <- result{data, err}
	}()

	// All toggles default off -> 8 active rows + Continue.
	d.waitScreen("Configuration")
	for i := 0; i < 9; i++ {
		d.keys("enter")
	}

	res := <-resCh
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	if res.data.SchedulerMode != "cron" {
		t.Fatalf("editing a config without SCHEDULER_MODE must default to cron, got %q", res.data.SchedulerMode)
	}
}

// collectWizardAsync runs CollectWizardData on baseTemplate and returns a channel
// with the result so a driver can script keys against the form.
func collectWizardAsync(t *testing.T, d *driver, baseTemplate string) chan struct {
	data *installer.InstallWizardData
	err  error
} {
	t.Helper()
	resCh := make(chan struct {
		data *installer.InstallWizardData
		err  error
	}, 1)
	go func() {
		data, err := CollectWizardData(context.Background(), d.session, baseTemplate)
		resCh <- struct {
			data *installer.InstallWizardData
			err  error
		}{data, err}
	}()
	return resCh
}

// TestCollectWizardDataHealthcheckSelect exercises the new Healthchecks FieldSelect:
// on a fresh (daemon) install the row is active and cycles off/centralized/self, and
// switching the scheduler to cron dims it and forces the mode off.
func TestCollectWizardDataHealthcheckSelect(t *testing.T) {
	// Self: from the daemon default (centralized) press right once -> self.
	t.Run("daemon_self", func(t *testing.T) {
		d := newDriver(t)
		resCh := collectWizardAsync(t, d, "")
		d.waitScreen("Configuration")
		d.keys("down down down down down down down")  // 7 downs -> Healthchecks row
		d.keys("right")                               // centralized -> self
		d.keys("down down down down down down enter") // to Continue, submit
		res := <-resCh
		if res.err != nil {
			t.Fatalf("unexpected error: %v", res.err)
		}
		if res.data.SchedulerMode != "daemon" || res.data.HealthcheckMode != "self" {
			t.Fatalf("mode=%q hc=%q, want daemon/self", res.data.SchedulerMode, res.data.HealthcheckMode)
		}
	})

	// Off: from the daemon default (centralized) press left once -> off.
	t.Run("daemon_off", func(t *testing.T) {
		d := newDriver(t)
		resCh := collectWizardAsync(t, d, "")
		d.waitScreen("Configuration")
		d.keys("down down down down down down down") // 7 downs -> Healthchecks row
		d.keys("left")                               // centralized -> off
		d.keys("down down down down down down enter")
		res := <-resCh
		if res.err != nil {
			t.Fatalf("unexpected error: %v", res.err)
		}
		if res.data.HealthcheckMode != "off" {
			t.Fatalf("hc=%q, want off", res.data.HealthcheckMode)
		}
	})

	// Cron forces off: switch the scheduler (row 6) to cron; the Healthchecks row
	// then dims and the collected mode is forced off regardless of its select.
	t.Run("cron_forces_off", func(t *testing.T) {
		d := newDriver(t)
		resCh := collectWizardAsync(t, d, "")
		d.waitScreen("Configuration")
		d.keys("down down down down down down") // 6 downs -> Scheduler row
		d.keys("right")                         // daemon -> cron
		d.keys("down down down down down down enter")
		res := <-resCh
		if res.err != nil {
			t.Fatalf("unexpected error: %v", res.err)
		}
		if res.data.SchedulerMode != "cron" || res.data.HealthcheckMode != "off" {
			t.Fatalf("mode=%q hc=%q, want cron/off", res.data.SchedulerMode, res.data.HealthcheckMode)
		}
	})

	// Prefill round-trip: an existing daemon+self config prefills the row to self and
	// a no-op edit preserves it.
	t.Run("prefill_self_roundtrip", func(t *testing.T) {
		d := newDriver(t)
		template := config.DefaultEnvTemplate()
		template = installer.SetEnvValueInTemplate(template, "SCHEDULER_MODE", "daemon")
		template = installer.SetEnvValueInTemplate(template, "HEALTHCHECK_ENABLED", "true")
		template = installer.SetEnvValueInTemplate(template, "HEALTHCHECK_MODE", "self")
		resCh := collectWizardAsync(t, d, template)
		d.waitScreen("Configuration")
		d.keys("down down down down down down down down down down down down enter") // spam to Continue + submit
		res := <-resCh
		if res.err != nil {
			t.Fatalf("unexpected error: %v", res.err)
		}
		if res.data.SchedulerMode != "daemon" || res.data.HealthcheckMode != "self" {
			t.Fatalf("prefill lost: mode=%q hc=%q, want daemon/self", res.data.SchedulerMode, res.data.HealthcheckMode)
		}
	})
}

func TestCollectWizardDataEscCancels(t *testing.T) {
	d := newDriver(t)
	resCh := make(chan error, 1)
	go func() {
		_, err := CollectWizardData(context.Background(), d.session, "")
		resCh <- err
	}()
	d.waitScreen("Configuration")
	d.keys("esc")
	if err := <-resCh; !errors.Is(err, installer.ErrInstallCancelled) {
		t.Fatalf("esc must cancel, got %v", err)
	}
}

func TestConfirmNewInstall(t *testing.T) {
	d := newDriver(t)
	type result struct {
		ok  bool
		err error
	}
	resCh := make(chan result, 1)
	ask := func() {
		go func() {
			ok, err := ConfirmNewInstall(context.Background(), d.session, "/opt/proxsave", []string{"build", "env"})
			resCh <- result{ok, err}
		}()
	}

	// Bare Enter (and single-key y) must not wipe the base dir.
	ask()
	d.waitScreen("Confirm new install")
	d.keys("y")
	d.keys("enter")
	if res := <-resCh; res.err != nil || res.ok {
		t.Fatalf("default must cancel, got %+v", res)
	}

	ask()
	d.waitScreen("Confirm new install")
	d.keys("left enter")
	if res := <-resCh; res.err != nil || !res.ok {
		t.Fatalf("deliberate continue failed: %+v", res)
	}
}

func TestRunPostInstallAudit(t *testing.T) {
	d := newDriver(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "backup.env")
	if err := os.WriteFile(configPath, []byte("BACKUP_X=true\nBACKUP_Y=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	origCollect := auditCollect
	auditCollect = func(ctx context.Context, execPath, cfgPath string) ([]installer.PostInstallAuditSuggestion, error) {
		return []installer.PostInstallAuditSuggestion{
			{Key: "BACKUP_X", Messages: []string{"unused collector X"}},
			{Key: "BACKUP_Y", Messages: []string{"unused collector Y"}},
		}, nil
	}
	t.Cleanup(func() { auditCollect = origCollect })

	type result struct {
		res installer.PostInstallAuditResult
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		res, err := RunPostInstallAudit(context.Background(), d.session, "/fake/proxsave", configPath, false)
		resCh <- result{res, err}
	}()

	d.waitScreen("Post-install check")
	d.keys("enter") // default: run the check
	d.waitScreen("Unused components")
	// 2 items (rows 0-1), Select ALL (2), Disable Selected (3). Select the first
	// suggestion, then move to the Disable Selected button and press it (a plain
	// Enter on the item now just toggles it - it no longer confirms the screen).
	d.keys("space down down down enter")
	// The outcome is now the shared styled "Post-install check" result screen (a
	// selector with a colored Status keyword), not a Notice. Assert the green
	// "✓ UPDATED" keyword + the disabled-component summary, which is now a header
	// line plus one "- KEY" column row (so a long list is not truncated). In the
	// install flow the leave item is "Continue" (mirrors the Telegram check that
	// follows), NOT "Back".
	d.waitScreen("Post-install check")
	d.waitText("✓ UPDATED")
	d.waitText("Disabled 1 component(s):")
	d.waitText("- BACKUP_X")
	d.waitText("Continue")
	d.keys("enter")

	res := <-resCh
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	if !res.res.Ran || len(res.res.Suggestions) != 2 {
		t.Fatalf("unexpected result: %+v", res.res)
	}
	if len(res.res.AppliedKeys) != 1 || res.res.AppliedKeys[0] != "BACKUP_X" {
		t.Fatalf("applied keys = %v", res.res.AppliedKeys)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "BACKUP_X=false") || !strings.Contains(content, "BACKUP_Y=true") {
		t.Fatalf("config not updated correctly:\n%s", content)
	}
}

// TestRunPostInstallAuditNoSuggestions drives the empty-suggestions outcome: it must
// render the shared styled "Post-install check" result screen with the green
// "✓ NO UNUSED COMPONENTS" Status keyword (and, per the daemon-result convention, no
// redundant explanation sentence), dismissible via Continue in the install flow.
func TestRunPostInstallAuditNoSuggestions(t *testing.T) {
	d := newDriver(t)

	origCollect := auditCollect
	auditCollect = func(ctx context.Context, execPath, cfgPath string) ([]installer.PostInstallAuditSuggestion, error) {
		return nil, nil
	}
	t.Cleanup(func() { auditCollect = origCollect })

	type result struct {
		res installer.PostInstallAuditResult
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		res, err := RunPostInstallAudit(context.Background(), d.session, "/fake/proxsave", "/tmp/nonexistent.env", false)
		resCh <- result{res, err}
	}()

	d.waitScreen("Post-install check") // the Run check confirm
	d.keys("enter")                    // run the dry-run
	d.waitScreen("Post-install check") // the outcome screen (same title)
	d.waitText("✓ NO UNUSED COMPONENTS")
	d.keys("enter") // dismiss via Back

	res := <-resCh
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	if !res.res.Ran || len(res.res.Suggestions) != 0 || len(res.res.AppliedKeys) != 0 {
		t.Fatalf("unexpected result: %+v", res.res)
	}
}

func TestRunPostInstallAuditSkipAndEsc(t *testing.T) {
	d := newDriver(t)

	type result struct {
		res installer.PostInstallAuditResult
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		res, err := RunPostInstallAudit(context.Background(), d.session, "/fake/proxsave", "/tmp/nonexistent.env", false)
		resCh <- result{res, err}
	}()
	d.waitScreen("Post-install check")
	d.keys("down enter") // choose Skip (the 2nd selector item, after Check)
	res := <-resCh
	if res.err != nil || res.res.Ran {
		t.Fatalf("skip must not run the check: %+v", res)
	}

	// Ctrl+C on the selector is a non-blocking skip too (parity with the
	// CLI, where a prompt EOF abandons the optional step).
	go func() {
		res, err := RunPostInstallAudit(context.Background(), d.session, "/fake/proxsave", "/tmp/nonexistent.env", false)
		resCh <- result{res, err}
	}()
	d.waitScreen("Post-install check")
	d.keys("ctrl+c")
	res = <-resCh
	if res.err != nil || res.res.Ran {
		t.Fatalf("ctrl+c must skip non-blockingly: %+v", res)
	}
}

// TestFormatPreservedEntriesResolvesAgainstBaseDir guards the directory
// detection against the CWD (regression salvaged from the deleted wizard
// suite: entries must resolve against baseDir, not the working directory).
func TestFormatPreservedEntriesResolvesAgainstBaseDir(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(baseDir, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := formatPreservedEntries(baseDir, []string{"env", "build", " ", ""})
	if got != "env/ build" {
		t.Fatalf("formatPreservedEntries = %q, want %q", got, "env/ build")
	}
	if formatPreservedEntries(baseDir, nil) != "(none)" {
		t.Fatal("empty entries must render (none)")
	}
}

func TestBuildTelegramPrompt(t *testing.T) {
	// Linked -> green "✓ LINKED"; Server ID boxed.
	v := buildTelegramPrompt("123456789", "/id/.server_identity", true, "Linked.", "Linked", orchestrator.TelegramSeveritySuccess, 200)
	if !strings.Contains(ansi.Strip(v), "✓ LINKED") || !strings.Contains(v, "34;197;94") {
		t.Fatalf("linked must be green ✓: %q", ansi.Strip(v))
	}
	if !strings.Contains(ansi.Strip(v), "╭") || !strings.Contains(ansi.Strip(v), "123456789") {
		t.Fatalf("Server ID must be boxed: %q", ansi.Strip(v))
	}

	// Partial -> yellow "⚠ LINKED (FINISHING SETUP)".
	p := buildTelegramPrompt("1", "", false, "token unsaved", "Linked (finishing setup)", orchestrator.TelegramSeverityPartial, 200)
	if !strings.Contains(ansi.Strip(p), "⚠ LINKED (FINISHING SETUP)") || !strings.Contains(p, "234;179;8") {
		t.Fatalf("partial must be yellow ⚠: %q", ansi.Strip(p))
	}

	// Action (not paired, 409) -> yellow "⚠ NOT PAIRED YET (HTTP 409)".
	a := buildTelegramPrompt("1", "", false, "send the id", "Not paired yet", orchestrator.TelegramSeverityAction, 409)
	if !strings.Contains(ansi.Strip(a), "⚠ NOT PAIRED YET") || !strings.Contains(a, "234;179;8") {
		t.Fatalf("action must be yellow ⚠: %q", ansi.Strip(a))
	}
	if !strings.Contains(ansi.Strip(a), "(HTTP 409)") {
		t.Fatalf("action must show the HTTP code: %q", ansi.Strip(a))
	}

	// Unreachable (code 0) -> yellow "⚠ SERVER UNREACHABLE" (retryable), no HTTP code.
	u := buildTelegramPrompt("1", "", false, "could not reach", "Server unreachable", orchestrator.TelegramSeverityUnreachable, 0)
	if !strings.Contains(ansi.Strip(u), "⚠ SERVER UNREACHABLE") || !strings.Contains(u, "234;179;8") {
		t.Fatalf("unreachable must be yellow ⚠: %q", ansi.Strip(u))
	}
	if strings.Contains(ansi.Strip(u), "HTTP 0") {
		t.Fatalf("code 0 must not show an HTTP code: %q", ansi.Strip(u))
	}

	// Fatal (invalid id, 422) -> red "✗ INVALID SERVER ID (HTTP 422)".
	f := buildTelegramPrompt("1", "", false, "invalid", "Invalid Server ID", orchestrator.TelegramSeverityFatal, 422)
	if !strings.Contains(ansi.Strip(f), "✗ INVALID SERVER ID") || !strings.Contains(f, "239;68;68") {
		t.Fatalf("fatal must be red ✗: %q", ansi.Strip(f))
	}

	// Neutral (pre-check) -> yellow "NOT CHECKED" (no symbol) + the guidance message.
	n := buildTelegramPrompt("1", "", false, "Not checked yet.", "", orchestrator.TelegramSeverityNeutral, 0)
	if !strings.Contains(ansi.Strip(n), "NOT CHECKED") || !strings.Contains(ansi.Strip(n), "Not checked yet.") {
		t.Fatalf("neutral must show yellow NOT CHECKED + message: %q", ansi.Strip(n))
	}
	if !strings.Contains(n, "234;179;8") || strings.ContainsAny(ansi.Strip(n), "✓✗⚠ℹ") || strings.Contains(ansi.Strip(n), "HTTP") {
		t.Fatalf("neutral must be yellow, no symbol, no HTTP: %q", ansi.Strip(n))
	}
}

func TestRunTelegramSetup(t *testing.T) {
	d := newDriver(t)

	origBootstrap := telegramBuildBootstrap
	origCheck := telegramCheckRegistration
	t.Cleanup(func() {
		telegramBuildBootstrap = origBootstrap
		telegramCheckRegistration = origCheck
	})

	telegramBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{
			Eligibility: orchestrator.TelegramSetupEligibleCentralized,
			ServerID:    "12345678",
		}, nil
	}

	// Skip path.
	type result struct {
		res installer.TelegramSetupResult
		err error
	}
	resCh := make(chan result, 1)
	ask := func() {
		go func() {
			res, err := RunTelegramSetup(context.Background(), d.session, t.TempDir(), "/tmp/backup.env", false)
			resCh <- result{res, err}
		}()
	}

	ask()
	d.waitScreen("Telegram setup")
	d.keys("down enter") // Skip
	res := <-resCh
	if res.err != nil || !res.res.Shown || !res.res.SkippedVerification {
		t.Fatalf("skip path: %+v", res)
	}

	// Verified path: Check succeeds (200 = linked), then Continue appears.
	telegramCheckRegistration = func(ctx context.Context, host, serverID, baseDir string, logger *logging.Logger) notify.TelegramRegistrationResult {
		res := notify.TelegramRegistrationResult{}
		res.Status.Code = 200
		return res
	}
	ask()
	d.waitScreen("Telegram setup")
	d.keys("enter") // Check
	d.waitScreen("Telegram setup")
	d.keys("down enter") // Continue (verified)
	res = <-resCh
	if res.err != nil || !res.res.Verified || res.res.SkippedVerification {
		t.Fatalf("verified path: %+v", res)
	}
	if res.res.CheckAttempts != 1 || res.res.LastStatusCode != 200 {
		t.Fatalf("check bookkeeping: %+v", res.res)
	}

	// Not eligible: no screens, Shown=false.
	telegramBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{Eligibility: orchestrator.TelegramSetupSkipPersonalMode}, nil
	}
	notShown, err := RunTelegramSetup(context.Background(), d.session, t.TempDir(), "/tmp/backup.env", false)
	if err != nil || notShown.Shown {
		t.Fatalf("not-eligible must be silent: %+v err=%v", notShown, err)
	}
}
