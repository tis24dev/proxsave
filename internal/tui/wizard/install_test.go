package wizard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	cronutil "github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/tui"
)

func TestSetEnvValueUpdateAndAppend(t *testing.T) {
	template := "KEY1=old\nKEY2=keep\n"
	updated := setEnvValue(template, "KEY1", "new")
	if !strings.Contains(updated, "KEY1=new") {
		t.Fatalf("expected KEY1 updated, got %q", updated)
	}
	updated = setEnvValue(updated, "KEY3", "added")
	if !strings.Contains(updated, "KEY3=added") {
		t.Fatalf("expected KEY3 appended, got %q", updated)
	}
}

func TestSetEnvValuePreservesComments(t *testing.T) {
	template := "FOO=old  # comment"
	updated := setEnvValue(template, "FOO", "new")
	if !strings.Contains(updated, "FOO=new") {
		t.Fatalf("expected FOO updated, got %q", updated)
	}
	if !strings.Contains(updated, "# comment") {
		t.Fatalf("expected comment preserved, got %q", updated)
	}
}

func TestSetEnvValuePreservesCommentAfterQuotedValue(t *testing.T) {
	template := `FOO="old # keep"  # trailing comment`
	updated := setEnvValue(template, "FOO", "new")
	if !strings.Contains(updated, "FOO=new") {
		t.Fatalf("expected FOO updated, got %q", updated)
	}
	if !strings.Contains(updated, "# trailing comment") {
		t.Fatalf("expected trailing comment preserved, got %q", updated)
	}
}

func TestApplyInstallDataRespectsBaseTemplate(t *testing.T) {
	baseTemplate := "BASE_DIR=\nMARKER=1\nTELEGRAM_ENABLED=false\nEMAIL_ENABLED=false\nENCRYPT_ARCHIVE=false\n"
	backupFirewallRules := false
	data := &InstallWizardData{
		BaseDir:                "/opt/proxsave",
		EnableSecondaryStorage: true,
		SecondaryPath:          "/mnt/sec",
		SecondaryLogPath:       "/mnt/sec/logs",
		EnableCloudStorage:     true,
		RcloneBackupRemote:     "remote:backups",
		RcloneLogRemote:        "remote:logs",
		BackupFirewallRules:    &backupFirewallRules,
		NotificationMode:       "both",
		EnableEncryption:       true,
	}

	result, err := ApplyInstallData(baseTemplate, data)
	if err != nil {
		t.Fatalf("ApplyInstallData returned error: %v", err)
	}

	assertContains := func(key, val string) {
		want := key + "=" + val
		if !strings.Contains(result, want) {
			t.Fatalf("missing %q in result:\n%s", want, result)
		}
	}

	assertContains("MARKER", "1")
	if strings.Contains(result, "BASE_DIR=") {
		t.Fatalf("expected BASE_DIR to be removed, got:\n%s", result)
	}
	assertContains("SECONDARY_ENABLED", "true")
	assertContains("SECONDARY_PATH", data.SecondaryPath)
	assertContains("SECONDARY_LOG_PATH", data.SecondaryLogPath)
	assertContains("CLOUD_ENABLED", "true")
	assertContains("CLOUD_REMOTE", data.RcloneBackupRemote)
	assertContains("CLOUD_LOG_PATH", data.RcloneLogRemote)
	assertContains("BACKUP_FIREWALL_RULES", "false")
	assertContains("TELEGRAM_ENABLED", "true")
	assertContains("EMAIL_ENABLED", "true")
	assertContains("ENCRYPT_ARCHIVE", "true")
}

func TestApplyInstallDataDefaultsBaseTemplate(t *testing.T) {
	data := &InstallWizardData{
		BaseDir: "/tmp/base",
	}
	result, err := ApplyInstallData("", data)
	if err != nil {
		t.Fatalf("ApplyInstallData returned error: %v", err)
	}
	if strings.Contains(result, "BASE_DIR=") {
		t.Fatalf("expected BASE_DIR not to be written in default template")
	}
}

func TestApplyInstallDataRejectsNilData(t *testing.T) {
	_, err := ApplyInstallData("", nil)
	if !errors.Is(err, ErrNilInstallData) {
		t.Fatalf("ApplyInstallData error = %v, want %v", err, ErrNilInstallData)
	}
}

func TestApplyInstallDataAllowsEmptySecondaryLogPath(t *testing.T) {
	data := &InstallWizardData{
		BaseDir:                "/tmp/base",
		EnableSecondaryStorage: true,
		SecondaryPath:          "/mnt/sec",
		SecondaryLogPath:       "",
	}

	result, err := ApplyInstallData("", data)
	if err != nil {
		t.Fatalf("ApplyInstallData returned error: %v", err)
	}
	if !strings.Contains(result, "SECONDARY_ENABLED=true") {
		t.Fatalf("expected secondary enabled in result:\n%s", result)
	}
	if !strings.Contains(result, "SECONDARY_PATH=/mnt/sec") {
		t.Fatalf("expected secondary path in result:\n%s", result)
	}
	if !strings.Contains(result, "SECONDARY_LOG_PATH=") {
		t.Fatalf("expected empty secondary log path in result:\n%s", result)
	}
}

func TestApplyInstallDataDisabledSecondaryClearsExistingValues(t *testing.T) {
	baseTemplate := strings.Join([]string{
		"SECONDARY_ENABLED=true",
		"SECONDARY_PATH=/mnt/old-secondary",
		"SECONDARY_LOG_PATH=/mnt/old-secondary/logs",
		"TELEGRAM_ENABLED=false",
		"EMAIL_ENABLED=false",
		"ENCRYPT_ARCHIVE=false",
		"",
	}, "\n")
	data := &InstallWizardData{
		BaseDir:                "/tmp/base",
		EnableSecondaryStorage: false,
	}

	result, err := ApplyInstallData(baseTemplate, data)
	if err != nil {
		t.Fatalf("ApplyInstallData returned error: %v", err)
	}

	for _, needle := range []string{
		"SECONDARY_ENABLED=false",
		"SECONDARY_PATH=",
		"SECONDARY_LOG_PATH=",
	} {
		if !strings.Contains(result, needle) {
			t.Fatalf("expected %q in result:\n%s", needle, result)
		}
	}
	if strings.Contains(result, "/mnt/old-secondary") {
		t.Fatalf("expected old secondary values to be cleared:\n%s", result)
	}
}

func TestApplyInstallDataRejectsInvalidSecondaryPath(t *testing.T) {
	data := &InstallWizardData{
		BaseDir:                "/tmp/base",
		EnableSecondaryStorage: true,
		SecondaryPath:          "relative/path",
	}

	_, err := ApplyInstallData("", data)
	if err == nil {
		t.Fatal("expected ApplyInstallData to fail")
	}
	if got, want := err.Error(), "SECONDARY_PATH must be an absolute local filesystem path"; got != want {
		t.Fatalf("ApplyInstallData error = %q, want %q", got, want)
	}
}

func TestApplyInstallDataRejectsInvalidSecondaryLogPath(t *testing.T) {
	data := &InstallWizardData{
		BaseDir:                "/tmp/base",
		EnableSecondaryStorage: true,
		SecondaryPath:          "/mnt/sec",
		SecondaryLogPath:       "remote:/logs",
	}

	_, err := ApplyInstallData("", data)
	if err == nil {
		t.Fatal("expected ApplyInstallData to fail")
	}
	if got, want := err.Error(), "SECONDARY_LOG_PATH must be an absolute local filesystem path"; got != want {
		t.Fatalf("ApplyInstallData error = %q, want %q", got, want)
	}
}

func TestValidateSecondaryInstallDataRejectsNilData(t *testing.T) {
	err := validateSecondaryInstallData(nil)
	if !errors.Is(err, ErrNilInstallData) {
		t.Fatalf("validateSecondaryInstallData error = %v, want %v", err, ErrNilInstallData)
	}
}

func TestApplyInstallDataCronAndNotifications(t *testing.T) {
	baseTemplate := "CRON_SCHEDULE=\nCRON_HOUR=\nCRON_MINUTE=\nTELEGRAM_ENABLED=true\nEMAIL_ENABLED=false\nENCRYPT_ARCHIVE=true\n"
	data := &InstallWizardData{
		BaseDir:          "/data",
		NotificationMode: "email",
		CronTime:         "3:7",
		EnableEncryption: false,
	}

	result, err := ApplyInstallData(baseTemplate, data)
	if err != nil {
		t.Fatalf("ApplyInstallData returned error: %v", err)
	}

	assertContains := func(key, val string) {
		needle := key + "=" + val
		if !strings.Contains(result, needle) {
			t.Fatalf("missing %q in result:\n%s", needle, result)
		}
	}

	assertContains("TELEGRAM_ENABLED", "false")
	assertContains("EMAIL_ENABLED", "true")
	assertContains("EMAIL_DELIVERY_METHOD", "relay")
	assertContains("EMAIL_FALLBACK_SENDMAIL", "true")
	if strings.Contains(result, "CRON_SCHEDULE=") || strings.Contains(result, "CRON_HOUR=") || strings.Contains(result, "CRON_MINUTE=") {
		t.Fatalf("expected CRON_* keys to be removed from backup.env, got:\n%s", result)
	}
	assertContains("ENCRYPT_ARCHIVE", "false")
}

func TestApplyInstallDataPreservesExistingEmailDeliveryMethod(t *testing.T) {
	baseTemplate := strings.Join([]string{
		"EMAIL_ENABLED=false",
		"EMAIL_DELIVERY_METHOD=relay",
		"EMAIL_FALLBACK_SENDMAIL=false",
		"",
	}, "\n")
	data := &InstallWizardData{
		BaseDir:          "/data",
		NotificationMode: "email",
	}

	result, err := ApplyInstallData(baseTemplate, data)
	if err != nil {
		t.Fatalf("ApplyInstallData returned error: %v", err)
	}
	if !strings.Contains(result, "EMAIL_DELIVERY_METHOD=relay") {
		t.Fatalf("expected existing relay method to be preserved:\n%s", result)
	}
	if !strings.Contains(result, "EMAIL_FALLBACK_SENDMAIL=false") {
		t.Fatalf("expected existing sendmail fallback key to be preserved:\n%s", result)
	}
	if strings.Contains(result, "EMAIL_FALLBACK_PMF") {
		t.Fatalf("expected transitional EMAIL_FALLBACK_PMF key to be removed:\n%s", result)
	}
}

func TestRunInstallWizardBlankCronIgnoresEnvOverride(t *testing.T) {
	t.Setenv("CRON_SCHEDULE", "5 1 * * *")

	originalRunner := runInstallWizardRunner
	t.Cleanup(func() { runInstallWizardRunner = originalRunner })

	runInstallWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		if ctx != t.Context() {
			t.Fatalf("ctx=%p; want %p", ctx, t.Context())
		}
		form, ok := focus.(*tview.Form)
		if !ok {
			t.Fatalf("focus primitive = %T, want *tview.Form", focus)
		}
		button := form.GetButton(0)
		if button == nil {
			t.Fatal("expected install button")
		}
		button.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), nil)
		return nil
	}

	data, err := RunInstallWizard(t.Context(), "/tmp/proxsave/backup.env", "/opt/proxsave", "sig", "")
	if err != nil {
		t.Fatalf("RunInstallWizard returned error: %v", err)
	}
	if data == nil {
		t.Fatal("expected wizard data")
	}
	if data.CronTime != cronutil.DefaultTime {
		t.Fatalf("CronTime = %q, want %q", data.CronTime, cronutil.DefaultTime)
	}
}

func TestCheckExistingConfigActions(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "prox.env")
	if err := os.WriteFile(configPath, []byte("base"), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	originalRunner := checkExistingConfigRunner
	t.Cleanup(func() { checkExistingConfigRunner = originalRunner })

	tests := []struct {
		name   string
		button string
		want   ExistingConfigAction
	}{
		{name: "overwrite", button: "Overwrite", want: ExistingConfigOverwrite},
		{name: "edit existing", button: "Edit existing", want: ExistingConfigEdit},
		{name: "keep continue", button: "Keep & continue", want: ExistingConfigKeepContinue},
		{name: "cancel", button: "Cancel", want: ExistingConfigCancel},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			checkExistingConfigRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
				done := extractModalDone(focus.(*tview.Modal))
				done(0, tc.button)
				return nil
			}

			action, err := CheckExistingConfig(context.Background(), configPath, "sig-abc")
			if err != nil {
				t.Fatalf("CheckExistingConfig returned error: %v", err)
			}
			if action != tc.want {
				t.Fatalf("got %v, want %v for button %q", action, tc.want, tc.button)
			}
		})
	}
}

func TestCheckExistingConfigMissingFileDefaultsToOverwrite(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "absent.env")
	action, err := CheckExistingConfig(context.Background(), configPath, "sig")
	if err != nil {
		t.Fatalf("CheckExistingConfig returned error: %v", err)
	}
	if action != ExistingConfigOverwrite {
		t.Fatalf("expected overwrite action when file is missing")
	}
}

func TestCheckExistingConfigPropagatesStatErrors(t *testing.T) {
	pathWithNul := string([]byte{0})
	action, err := CheckExistingConfig(context.Background(), pathWithNul, "sig")
	if err == nil {
		t.Fatalf("expected error for invalid path")
	}
	if action != ExistingConfigCancel {
		t.Fatalf("expected cancel action on stat error, got %v", action)
	}
}

func TestCheckExistingConfigRejectsNonRegularPath(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config-dir")
	if err := os.Mkdir(configPath, 0o755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	action, err := CheckExistingConfig(context.Background(), configPath, "sig")
	if err == nil {
		t.Fatal("expected error for non-regular config path")
	}
	if err.Error() != "configuration file path is not a regular file: "+configPath {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != ExistingConfigCancel {
		t.Fatalf("expected cancel action on non-regular path, got %v", action)
	}
}

func TestCheckExistingConfigDefaultsFocusToKeepContinue(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "prox.env")
	if err := os.WriteFile(configPath, []byte("base"), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	originalRunner := checkExistingConfigRunner
	t.Cleanup(func() { checkExistingConfigRunner = originalRunner })

	checkExistingConfigRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		modal, ok := focus.(*tview.Modal)
		if !ok {
			t.Fatalf("focus=%T; want *tview.Modal", focus)
		}

		var form *tview.Form
		modal.Focus(func(p tview.Primitive) {
			var ok bool
			form, ok = p.(*tview.Form)
			if !ok {
				t.Fatalf("delegate focus=%T; want *tview.Form", p)
			}
		})

		formItem, button := form.GetFocusedItemIndex()
		if formItem != -1 || button != 2 {
			t.Fatalf("focused item=(%d,%d); want (-1,2)", formItem, button)
		}

		done := extractModalDone(modal)
		done(0, "Keep & continue")
		return nil
	}

	action, err := CheckExistingConfig(context.Background(), configPath, "sig-abc")
	if err != nil {
		t.Fatalf("CheckExistingConfig returned error: %v", err)
	}
	if action != ExistingConfigKeepContinue {
		t.Fatalf("action=%v; want %v", action, ExistingConfigKeepContinue)
	}
}

func TestCheckExistingConfigPropagatesRunnerErrors(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "prox.env")
	if err := os.WriteFile(configPath, []byte("base"), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	originalRunner := checkExistingConfigRunner
	t.Cleanup(func() { checkExistingConfigRunner = originalRunner })

	expectedErr := errors.New("ui runner failure")
	checkExistingConfigRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		return expectedErr
	}

	action, err := CheckExistingConfig(context.Background(), configPath, "sig")
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected runner error %v, got %v", expectedErr, err)
	}
	if action != ExistingConfigCancel {
		t.Fatalf("expected cancel action on runner error, got %v", action)
	}
}

func TestCheckExistingConfigPassesContextToRunner(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "prox.env")
	if err := os.WriteFile(configPath, []byte("base"), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	originalRunner := checkExistingConfigRunner
	t.Cleanup(func() { checkExistingConfigRunner = originalRunner })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	expectedErr := errors.New("ui runner failure")
	checkExistingConfigRunner = func(gotCtx context.Context, app *tui.App, root, focus tview.Primitive) error {
		if gotCtx != ctx {
			t.Fatalf("ctx=%p; want %p", gotCtx, ctx)
		}
		return expectedErr
	}

	action, err := CheckExistingConfig(ctx, configPath, "sig")
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected runner error %v, got %v", expectedErr, err)
	}
	if action != ExistingConfigCancel {
		t.Fatalf("expected cancel action on runner error, got %v", action)
	}
}
