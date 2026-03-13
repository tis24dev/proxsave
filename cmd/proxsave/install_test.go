package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cronutil "github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/logging"
)

func TestPrintInstallBanner(t *testing.T) {
	output := captureStdout(t, func() {
		printInstallBanner("/etc/proxmox-backup/backup.env")
	})
	if !strings.Contains(output, "ProxSave - Go Version") {
		t.Fatalf("banner missing title: %q", output)
	}
	if !strings.Contains(output, "Version:") {
		t.Fatalf("banner missing version: %q", output)
	}
	if !strings.Contains(output, "Build Signature:") {
		t.Fatalf("banner missing build signature: %q", output)
	}
	if !strings.Contains(output, "Configuration file: /etc/proxmox-backup/backup.env") {
		t.Fatalf("banner missing config path: %q", output)
	}
}

func TestPrintInstallFooterVariants(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantSnippet string
	}{
		{"success", nil, "Go-based installation completed"},
		{"aborted", wrapInstallError(errInteractiveAborted), "Go-based installation aborted"},
		{"failure", errors.New("boom"), "Go-based installation failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			permStatus := ""
			permMessage := ""
			if tt.err == nil {
				permStatus = "ok"
				permMessage = "permissions and ownership normalized correctly"
			}
			output := captureStdout(t, func() {
				printInstallFooter(tt.err, "/etc/proxmox-backup/backup.env", "/opt/proxsave", "CODE123", permStatus, permMessage)
			})
			if !strings.Contains(output, tt.wantSnippet) {
				t.Fatalf("output %q does not contain %q", output, tt.wantSnippet)
			}
			if tt.err == nil {
				if !strings.Contains(output, "Edit configuration: /etc/proxmox-backup/backup.env") {
					t.Fatalf("expected config path reference in footer")
				}
				if !strings.Contains(output, "Check logs: tail -f /opt/proxsave/log/*.log") {
					t.Fatalf("expected log path guidance")
				}
				if !strings.Contains(output, "enter code: CODE123") {
					t.Fatalf("expected telegram code mention")
				}
				if !strings.Contains(output, "permissions and ownership normalized correctly") {
					t.Fatalf("expected permissions normalization confirmation line in footer, got %q", output)
				}
			}
		})
	}
}

func TestWrapInstallError(t *testing.T) {
	if wrapInstallError(nil) != nil {
		t.Fatalf("wrapInstallError(nil) should be nil")
	}
	sentinel := errors.New("boom")
	if wrapInstallError(sentinel) != sentinel {
		t.Fatalf("non-abort errors should pass through")
	}
	err := wrapInstallError(errInteractiveAborted)
	if err == nil || !errors.Is(err, errInteractiveAborted) {
		t.Fatalf("wrapped error should retain sentinel")
	}
	if !strings.Contains(err.Error(), "installation aborted by user") {
		t.Fatalf("wrapped error should include user message, got %v", err)
	}
}

func TestIsInstallAbortedError(t *testing.T) {
	if isInstallAbortedError(nil) {
		t.Fatalf("nil should not be aborted")
	}
	if !isInstallAbortedError(errInteractiveAborted) {
		t.Fatalf("sentinel error should be aborted")
	}
	if !isInstallAbortedError(errors.New("installation aborted by user")) {
		t.Fatalf("message containing aborted should be detected")
	}
	if isInstallAbortedError(errors.New("other failure")) {
		t.Fatalf("unrelated errors should not be aborted")
	}
}

func TestResetInstallBaseDirPreservesCoreDirectories(t *testing.T) {
	base := t.TempDir()

	// setup contents
	if err := os.WriteFile(filepath.Join(base, "delete.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("setup file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(base, "remove-dir"), 0o755); err != nil {
		t.Fatalf("setup dir: %v", err)
	}

	envDir := filepath.Join(base, "env")
	if err := os.Mkdir(envDir, 0o755); err != nil {
		t.Fatalf("setup env: %v", err)
	}
	envFile := filepath.Join(envDir, "keep.env")
	if err := os.WriteFile(envFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("setup env file: %v", err)
	}

	identityDir := filepath.Join(base, "identity")
	if err := os.Mkdir(identityDir, 0o755); err != nil {
		t.Fatalf("setup identity: %v", err)
	}
	idFile := filepath.Join(identityDir, "id.txt")
	if err := os.WriteFile(idFile, []byte("id"), 0o600); err != nil {
		t.Fatalf("setup identity file: %v", err)
	}

	buildDir := filepath.Join(base, "build")
	if err := os.Mkdir(buildDir, 0o755); err != nil {
		t.Fatalf("setup build: %v", err)
	}
	buildFile := filepath.Join(buildDir, "keep.txt")
	if err := os.WriteFile(buildFile, []byte("build"), 0o600); err != nil {
		t.Fatalf("setup build file: %v", err)
	}

	logger := logging.NewBootstrapLogger()
	if err := resetInstallBaseDir(base, logger); err != nil {
		t.Fatalf("resetInstallBaseDir returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(base, "delete.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected delete.txt to be removed, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "remove-dir")); !os.IsNotExist(err) {
		t.Fatalf("expected remove-dir to be removed, got err=%v", err)
	}
	if _, err := os.Stat(envDir); err != nil {
		t.Fatalf("env dir should remain: %v", err)
	}
	if _, err := os.Stat(envFile); err != nil {
		t.Fatalf("env file should remain: %v", err)
	}
	if _, err := os.Stat(identityDir); err != nil {
		t.Fatalf("identity dir should remain: %v", err)
	}
	if _, err := os.Stat(idFile); err != nil {
		t.Fatalf("identity file should remain: %v", err)
	}
	if _, err := os.Stat(buildDir); err != nil {
		t.Fatalf("build dir should remain: %v", err)
	}
	if _, err := os.Stat(buildFile); err != nil {
		t.Fatalf("build file should remain: %v", err)
	}
}

func TestResetInstallBaseDirRespectsSharedPreserveSet(t *testing.T) {
	base := t.TempDir()
	for _, entry := range newInstallPreservedEntries() {
		dirPath := filepath.Join(base, entry)
		if err := os.MkdirAll(dirPath, 0o755); err != nil {
			t.Fatalf("setup %s: %v", entry, err)
		}
		filePath := filepath.Join(dirPath, "keep.txt")
		if err := os.WriteFile(filePath, []byte(entry), 0o600); err != nil {
			t.Fatalf("setup %s file: %v", entry, err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "drop.txt"), []byte("drop"), 0o600); err != nil {
		t.Fatalf("setup drop file: %v", err)
	}

	logger := logging.NewBootstrapLogger()
	if err := resetInstallBaseDir(base, logger); err != nil {
		t.Fatalf("resetInstallBaseDir returned error: %v", err)
	}

	for _, entry := range newInstallPreservedEntries() {
		filePath := filepath.Join(base, entry, "keep.txt")
		if _, err := os.Stat(filePath); err != nil {
			t.Fatalf("expected preserved file for %s, got %v", entry, err)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "drop.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected drop.txt removed, got err=%v", err)
	}
}

func TestResetInstallBaseDirRefusesRoot(t *testing.T) {
	logger := logging.NewBootstrapLogger()
	if err := resetInstallBaseDir("/", logger); err == nil {
		t.Fatal("expected error when trying to reset root directory")
	}
}

func TestPrepareBaseTemplateExistingSkip(t *testing.T) {
	cfgFile := createTempFile(t, "existing config")
	reader := bufio.NewReader(strings.NewReader("3\n"))
	var tmpl string
	var skip bool
	var err error
	captureStdout(t, func() {
		tmpl, skip, err = prepareBaseTemplate(context.Background(), reader, cfgFile)
	})
	if err != nil {
		t.Fatalf("prepareBaseTemplate error: %v", err)
	}
	if !skip {
		t.Fatalf("expected skip when user declines overwrite")
	}
	if tmpl != "" {
		t.Fatalf("template should be empty when skipping wizard")
	}
}

func TestPrepareBaseTemplateOverwrite(t *testing.T) {
	cfgFile := createTempFile(t, "old")
	reader := bufio.NewReader(strings.NewReader("1\n"))
	var tmpl string
	var skip bool
	var err error
	captureStdout(t, func() {
		tmpl, skip, err = prepareBaseTemplate(context.Background(), reader, cfgFile)
	})
	if err != nil {
		t.Fatalf("prepareBaseTemplate error: %v", err)
	}
	if skip {
		t.Fatalf("expected skip=false after overwrite confirmation")
	}
	if tmpl == "" {
		t.Fatalf("expected template contents")
	}
}

func TestPrepareBaseTemplateEditExisting(t *testing.T) {
	cfgFile := createTempFile(t, "EXISTING=1\n")
	reader := bufio.NewReader(strings.NewReader("2\n"))
	var tmpl string
	var skip bool
	var err error
	captureStdout(t, func() {
		tmpl, skip, err = prepareBaseTemplate(context.Background(), reader, cfgFile)
	})
	if err != nil {
		t.Fatalf("prepareBaseTemplate error: %v", err)
	}
	if skip {
		t.Fatalf("expected skip=false for edit existing")
	}
	if !strings.Contains(tmpl, "EXISTING=1") {
		t.Fatalf("expected existing template content, got %q", tmpl)
	}
}

func TestPrepareBaseTemplateCancel(t *testing.T) {
	cfgFile := createTempFile(t, "EXISTING=1\n")
	reader := bufio.NewReader(strings.NewReader("0\n"))
	_, _, err := prepareBaseTemplate(context.Background(), reader, cfgFile)
	if !errors.Is(err, errInteractiveAborted) {
		t.Fatalf("expected interactive abort, got %v", err)
	}
}

func TestConfigureSecondaryStorageEnabled(t *testing.T) {
	var result string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\n/mnt/secondary\n/mnt/secondary/log\n"))
	captureStdout(t, func() {
		result, err = configureSecondaryStorage(ctx, reader, "")
	})
	if err != nil {
		t.Fatalf("configureSecondaryStorage error: %v", err)
	}
	if !strings.Contains(result, "SECONDARY_ENABLED=true") {
		t.Fatalf("expected SECONDARY_ENABLED=true in template: %q", result)
	}
	if !strings.Contains(result, "SECONDARY_PATH=/mnt/secondary") {
		t.Fatalf("expected secondary path in template: %q", result)
	}
	if !strings.Contains(result, "SECONDARY_LOG_PATH=/mnt/secondary/log") {
		t.Fatalf("expected secondary log path in template: %q", result)
	}
}

func TestConfigureSecondaryStorageEnabledWithEmptyLogPath(t *testing.T) {
	var result string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\n/mnt/secondary\n\n"))
	captureStdout(t, func() {
		result, err = configureSecondaryStorage(ctx, reader, "")
	})
	if err != nil {
		t.Fatalf("configureSecondaryStorage error: %v", err)
	}
	if !strings.Contains(result, "SECONDARY_ENABLED=true") {
		t.Fatalf("expected SECONDARY_ENABLED=true in template: %q", result)
	}
	if !strings.Contains(result, "SECONDARY_PATH=/mnt/secondary") {
		t.Fatalf("expected secondary path in template: %q", result)
	}
	if !strings.Contains(result, "SECONDARY_LOG_PATH=") {
		t.Fatalf("expected empty secondary log path in template: %q", result)
	}
}

func TestConfigureSecondaryStorageRejectsInvalidBackupPath(t *testing.T) {
	var result string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\nrelative/path\n/mnt/secondary\n\n"))
	captureStdout(t, func() {
		result, err = configureSecondaryStorage(ctx, reader, "")
	})
	if err != nil {
		t.Fatalf("configureSecondaryStorage error: %v", err)
	}
	if !strings.Contains(result, "SECONDARY_PATH=/mnt/secondary") {
		t.Fatalf("expected corrected secondary path in template: %q", result)
	}
}

func TestConfigureSecondaryStorageRejectsInvalidLogPath(t *testing.T) {
	var result string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\n/mnt/secondary\nremote:/logs\n\n"))
	captureStdout(t, func() {
		result, err = configureSecondaryStorage(ctx, reader, "")
	})
	if err != nil {
		t.Fatalf("configureSecondaryStorage error: %v", err)
	}
	if !strings.Contains(result, "SECONDARY_LOG_PATH=") {
		t.Fatalf("expected empty secondary log path in template: %q", result)
	}
}

func TestConfigureSecondaryStorageDisabled(t *testing.T) {
	var result string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("n\n"))
	captureStdout(t, func() {
		result, err = configureSecondaryStorage(ctx, reader, "")
	})
	if err != nil {
		t.Fatalf("configureSecondaryStorage error: %v", err)
	}
	if !strings.Contains(result, "SECONDARY_ENABLED=false") {
		t.Fatalf("expected disabled flag in template: %q", result)
	}
	if !strings.Contains(result, "SECONDARY_PATH=") {
		t.Fatalf("expected cleared secondary path in template: %q", result)
	}
	if !strings.Contains(result, "SECONDARY_LOG_PATH=") {
		t.Fatalf("expected cleared secondary log path in template: %q", result)
	}
}

func TestConfigureSecondaryStorageDisabledClearsExistingValues(t *testing.T) {
	var result string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("n\n"))
	template := "SECONDARY_ENABLED=true\nSECONDARY_PATH=/mnt/old-secondary\nSECONDARY_LOG_PATH=/mnt/old-secondary/logs\n"
	captureStdout(t, func() {
		result, err = configureSecondaryStorage(ctx, reader, template)
	})
	if err != nil {
		t.Fatalf("configureSecondaryStorage error: %v", err)
	}
	for _, needle := range []string{
		"SECONDARY_ENABLED=false",
		"SECONDARY_PATH=",
		"SECONDARY_LOG_PATH=",
	} {
		if !strings.Contains(result, needle) {
			t.Fatalf("expected %q in template: %q", needle, result)
		}
	}
	if strings.Contains(result, "/mnt/old-secondary") {
		t.Fatalf("expected old secondary values to be cleared: %q", result)
	}
}

func TestConfigureCloudStorageEnabled(t *testing.T) {
	var result string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\nremote:pbs\nremote:/logs\n"))
	captureStdout(t, func() {
		result, err = configureCloudStorage(ctx, reader, "")
	})
	if err != nil {
		t.Fatalf("configureCloudStorage error: %v", err)
	}
	if !strings.Contains(result, "CLOUD_ENABLED=true") {
		t.Fatalf("expected enabled flag: %q", result)
	}
	if !strings.Contains(result, "CLOUD_REMOTE=remote:pbs") {
		t.Fatalf("expected remote entry: %q", result)
	}
	if !strings.Contains(result, "CLOUD_LOG_PATH=remote:/logs") {
		t.Fatalf("expected log remote entry: %q", result)
	}
}

func TestConfigureCloudStorageDisabled(t *testing.T) {
	var result string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("n\n"))
	captureStdout(t, func() {
		result, err = configureCloudStorage(ctx, reader, "")
	})
	if err != nil {
		t.Fatalf("configureCloudStorage error: %v", err)
	}
	if !strings.Contains(result, "CLOUD_ENABLED=false") {
		t.Fatalf("expected disabled flag: %q", result)
	}
}

func TestConfigureFirewallRulesDefaultsToDisabled(t *testing.T) {
	var result string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("\n"))
	captureStdout(t, func() {
		result, err = configureFirewallRules(ctx, reader, "")
	})
	if err != nil {
		t.Fatalf("configureFirewallRules error: %v", err)
	}
	if !strings.Contains(result, "BACKUP_FIREWALL_RULES=false") {
		t.Fatalf("expected disabled flag: %q", result)
	}
}

func TestConfigureFirewallRulesDisabled(t *testing.T) {
	var result string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("n\n"))
	captureStdout(t, func() {
		result, err = configureFirewallRules(ctx, reader, "")
	})
	if err != nil {
		t.Fatalf("configureFirewallRules error: %v", err)
	}
	if !strings.Contains(result, "BACKUP_FIREWALL_RULES=false") {
		t.Fatalf("expected disabled flag: %q", result)
	}
}

func TestConfigureNotifications(t *testing.T) {
	var result string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\nn\n"))
	captureStdout(t, func() {
		result, err = configureNotifications(ctx, reader, "")
	})
	if err != nil {
		t.Fatalf("configureNotifications error: %v", err)
	}
	if !strings.Contains(result, "TELEGRAM_ENABLED=true") {
		t.Fatalf("expected telegram enabled in template: %q", result)
	}
	if !strings.Contains(result, "EMAIL_ENABLED=false") {
		t.Fatalf("expected email disabled in template: %q", result)
	}
}

func TestConfigureEncryption(t *testing.T) {
	var enabled bool
	var err error
	template := ""
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\n"))
	captureStdout(t, func() {
		enabled, err = configureEncryption(ctx, reader, &template)
	})
	if err != nil {
		t.Fatalf("configureEncryption error: %v", err)
	}
	if !enabled {
		t.Fatalf("expected encryption enabled")
	}
	if !strings.Contains(template, "ENCRYPT_ARCHIVE=true") {
		t.Fatalf("expected ENCRYPT_ARCHIVE flag, got %q", template)
	}

	reader = bufio.NewReader(strings.NewReader("n\n"))
	captureStdout(t, func() {
		enabled, err = configureEncryption(ctx, reader, &template)
	})
	if err != nil {
		t.Fatalf("configureEncryption disable error: %v", err)
	}
	if enabled {
		t.Fatalf("expected disabled encryption")
	}
	if !strings.Contains(template, "ENCRYPT_ARCHIVE=false") {
		t.Fatalf("expected disabled flag")
	}
}

func TestConfigureCronTime(t *testing.T) {
	t.Run("empty input uses default", func(t *testing.T) {
		var cronTime string
		var err error
		reader := bufio.NewReader(strings.NewReader("\n"))
		captureStdout(t, func() {
			cronTime, err = configureCronTime(context.Background(), reader, cronutil.DefaultTime)
		})
		if err != nil {
			t.Fatalf("configureCronTime returned error: %v", err)
		}
		if cronTime != cronutil.DefaultTime {
			t.Fatalf("configureCronTime default = %q, want %q", cronTime, cronutil.DefaultTime)
		}
	})

	t.Run("invalid input re-prompts until valid", func(t *testing.T) {
		var cronTime string
		var err error
		reader := bufio.NewReader(strings.NewReader("24:00\n3:7\n"))
		output := captureStdout(t, func() {
			cronTime, err = configureCronTime(context.Background(), reader, cronutil.DefaultTime)
		})
		if err != nil {
			t.Fatalf("configureCronTime returned error: %v", err)
		}
		if cronTime != "03:07" {
			t.Fatalf("configureCronTime normalized = %q, want %q", cronTime, "03:07")
		}
		if !strings.Contains(output, "cron hour must be between 00 and 23") {
			t.Fatalf("expected validation error in output, got %q", output)
		}
	})

	t.Run("aborted input returns sentinel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		reader := bufio.NewReader(strings.NewReader("03:15\n"))
		_, err := configureCronTime(ctx, reader, cronutil.DefaultTime)
		if !errors.Is(err, errInteractiveAborted) {
			t.Fatalf("expected errInteractiveAborted, got %v", err)
		}
	})
}

func TestRunConfigWizardCLIReturnsCronSchedule(t *testing.T) {
	cfgDir := t.TempDir()
	configPath := filepath.Join(cfgDir, "env", "backup.env")
	tmpConfigPath := configPath + ".tmp"
	reader := bufio.NewReader(strings.NewReader("n\nn\nn\nn\nn\nn\n03:15\n"))

	var result installConfigResult
	var err error
	captureStdout(t, func() {
		result, err = runConfigWizardCLI(context.Background(), reader, configPath, tmpConfigPath, "/opt/proxsave", nil)
	})
	if err != nil {
		t.Fatalf("runConfigWizardCLI returned error: %v", err)
	}
	if result.SkipConfigWizard {
		t.Fatal("expected SkipConfigWizard=false")
	}
	if result.EnableEncryption {
		t.Fatal("expected EnableEncryption=false")
	}
	if result.CronSchedule != "15 03 * * *" {
		t.Fatalf("CronSchedule = %q, want %q", result.CronSchedule, "15 03 * * *")
	}

	content, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("expected config file to be written: %v", readErr)
	}
	if !strings.Contains(string(content), "ENCRYPT_ARCHIVE=false") {
		t.Fatalf("expected config content to be written, got %q", string(content))
	}
}

func TestRunConfigWizardCLISkipLeavesCronScheduleEmpty(t *testing.T) {
	cfgFile := createTempFile(t, "EXISTING=1\n")
	tmpConfigPath := cfgFile + ".tmp"
	reader := bufio.NewReader(strings.NewReader("3\n"))

	var result installConfigResult
	var err error
	captureStdout(t, func() {
		result, err = runConfigWizardCLI(context.Background(), reader, cfgFile, tmpConfigPath, "/opt/proxsave", nil)
	})
	if err != nil {
		t.Fatalf("runConfigWizardCLI returned error: %v", err)
	}
	if !result.SkipConfigWizard {
		t.Fatal("expected SkipConfigWizard=true")
	}
	if result.CronSchedule != "" {
		t.Fatalf("expected empty CronSchedule when skipping wizard, got %q", result.CronSchedule)
	}
}

func createTempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config-*.env")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	_ = f.Close()
	return f.Name()
}
