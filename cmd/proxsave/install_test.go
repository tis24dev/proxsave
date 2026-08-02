package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	cronutil "github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
)

func TestPrintInstallBanner(t *testing.T) {
	output := captureStdout(t, func() {
		printInstallBanner("/etc/proxmox-backup/backup.env")
	})
	if !strings.Contains(output, "ProxSave") {
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
		{"success", nil, "Installation completed"},
		{"aborted", wrapInstallError(errInteractiveAborted), "Installation aborted"},
		{"failure", errors.New("boom"), "Installation failed"},
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

func TestResetInstallBaseDirAllowsNilBootstrap(t *testing.T) {
	base := t.TempDir()
	preservedDir := filepath.Join(base, "env")
	if err := os.MkdirAll(preservedDir, 0o755); err != nil {
		t.Fatalf("setup env: %v", err)
	}
	preservedFile := filepath.Join(preservedDir, "backup.env")
	if err := os.WriteFile(preservedFile, []byte("KEEP=1"), 0o600); err != nil {
		t.Fatalf("setup env file: %v", err)
	}
	removedFile := filepath.Join(base, "drop.txt")
	if err := os.WriteFile(removedFile, []byte("drop"), 0o600); err != nil {
		t.Fatalf("setup drop file: %v", err)
	}

	captureStdout(t, func() {
		if err := resetInstallBaseDir(base, nil); err != nil {
			t.Fatalf("resetInstallBaseDir returned error: %v", err)
		}
	})

	if _, err := os.Stat(preservedFile); err != nil {
		t.Fatalf("expected preserved file to remain, got %v", err)
	}
	if _, err := os.Stat(removedFile); !os.IsNotExist(err) {
		t.Fatalf("expected drop.txt removed, got err=%v", err)
	}
}

func TestResetInstallBaseDirRefusesRoot(t *testing.T) {
	logger := logging.NewBootstrapLogger()
	if err := resetInstallBaseDir("/", logger); err == nil {
		t.Fatal("expected error when trying to reset root directory")
	}
}

func TestResetInstallBaseDirWithContext_CanceledBeforeRemoval(t *testing.T) {
	base := t.TempDir()
	dropFile := filepath.Join(base, "drop.txt")
	if err := os.WriteFile(dropFile, []byte("drop"), 0o600); err != nil {
		t.Fatalf("setup drop file: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := resetInstallBaseDirWithContext(ctx, base, logging.NewBootstrapLogger())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v; want %v", err, context.Canceled)
	}
	if _, statErr := os.Stat(dropFile); statErr != nil {
		t.Fatalf("expected file to remain after canceled reset, got %v", statErr)
	}
}

func TestPrepareBaseTemplateExistingSkip(t *testing.T) {
	cfgFile := createTempFile(t, "existing config")
	reader := bufio.NewReader(strings.NewReader("3\n"))
	var base installWizardBase
	var skip bool
	var err error
	captureStdout(t, func() {
		base, skip, _, err = prepareBaseTemplate(context.Background(), reader, cfgFile, nil)
	})
	if err != nil {
		t.Fatalf("prepareBaseTemplate error: %v", err)
	}
	if !skip {
		t.Fatalf("expected skip when user declines overwrite")
	}
	if base.Prompt != "" || base.Raw != "" {
		t.Fatalf("template should be empty when skipping wizard, got %+v", base)
	}
}

func TestPrepareBaseTemplateOverwrite(t *testing.T) {
	cfgFile := createTempFile(t, "old")
	reader := bufio.NewReader(strings.NewReader("1\n"))
	var base installWizardBase
	var skip bool
	var err error
	captureStdout(t, func() {
		base, skip, _, err = prepareBaseTemplate(context.Background(), reader, cfgFile, nil)
	})
	if err != nil {
		t.Fatalf("prepareBaseTemplate error: %v", err)
	}
	if skip {
		t.Fatalf("expected skip=false after overwrite confirmation")
	}
	if base.Prompt == "" {
		t.Fatalf("expected template contents")
	}
}

func TestPrepareBaseTemplateEditExisting(t *testing.T) {
	cfgFile := createTempFile(t, "EXISTING=1\n")
	reader := bufio.NewReader(strings.NewReader("2\n"))
	var base installWizardBase
	var skip bool
	var err error
	captureStdout(t, func() {
		base, skip, _, err = prepareBaseTemplate(context.Background(), reader, cfgFile, nil)
	})
	if err != nil {
		t.Fatalf("prepareBaseTemplate error: %v", err)
	}
	if skip {
		t.Fatalf("expected skip=false for edit existing")
	}
	if !strings.Contains(base.Prompt, "EXISTING=1") {
		t.Fatalf("expected existing template content, got %q", base.Prompt)
	}
}

func TestPrepareBaseTemplateCancel(t *testing.T) {
	cfgFile := createTempFile(t, "EXISTING=1\n")
	reader := bufio.NewReader(strings.NewReader("0\n"))
	_, _, _, err := prepareBaseTemplate(context.Background(), reader, cfgFile, nil)
	if !errors.Is(err, errInteractiveAborted) {
		t.Fatalf("expected interactive abort, got %v", err)
	}
}

func TestPromptSecondaryStorageEnabled(t *testing.T) {
	var enabled bool
	var path, logPath string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\n/mnt/secondary\n/mnt/secondary/log\n"))
	captureStdout(t, func() {
		enabled, path, logPath, err = promptSecondaryStorage(ctx, reader, installer.DeriveInstallWizardPrefill(""))
	})
	if err != nil {
		t.Fatalf("promptSecondaryStorage error: %v", err)
	}
	if !enabled {
		t.Fatal("expected secondary storage enabled")
	}
	if path != "/mnt/secondary" {
		t.Fatalf("secondary path = %q, want /mnt/secondary", path)
	}
	if logPath != "/mnt/secondary/log" {
		t.Fatalf("secondary log path = %q, want /mnt/secondary/log", logPath)
	}
}

func TestPromptSecondaryStorageEnabledWithEmptyLogPath(t *testing.T) {
	var enabled bool
	var path, logPath string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\n/mnt/secondary\n\n"))
	captureStdout(t, func() {
		enabled, path, logPath, err = promptSecondaryStorage(ctx, reader, installer.DeriveInstallWizardPrefill(""))
	})
	if err != nil {
		t.Fatalf("promptSecondaryStorage error: %v", err)
	}
	if !enabled {
		t.Fatal("expected secondary storage enabled")
	}
	if path != "/mnt/secondary" {
		t.Fatalf("secondary path = %q, want /mnt/secondary", path)
	}
	if logPath != "" {
		t.Fatalf("secondary log path = %q, want empty", logPath)
	}
}

func TestPromptSecondaryStorageRejectsInvalidBackupPath(t *testing.T) {
	var path string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\nrelative/path\n/mnt/secondary\n\n"))
	captureStdout(t, func() {
		_, path, _, err = promptSecondaryStorage(ctx, reader, installer.DeriveInstallWizardPrefill(""))
	})
	if err != nil {
		t.Fatalf("promptSecondaryStorage error: %v", err)
	}
	if path != "/mnt/secondary" {
		t.Fatalf("expected corrected secondary path, got %q", path)
	}
}

func TestPromptSecondaryStorageRejectsInvalidLogPath(t *testing.T) {
	var logPath string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\n/mnt/secondary\nremote:/logs\n\n"))
	captureStdout(t, func() {
		_, _, logPath, err = promptSecondaryStorage(ctx, reader, installer.DeriveInstallWizardPrefill(""))
	})
	if err != nil {
		t.Fatalf("promptSecondaryStorage error: %v", err)
	}
	if logPath != "" {
		t.Fatalf("expected empty secondary log path, got %q", logPath)
	}
}

func TestPromptSecondaryStorageDisabled(t *testing.T) {
	var enabled bool
	var path, logPath string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("n\n"))
	captureStdout(t, func() {
		enabled, path, logPath, err = promptSecondaryStorage(ctx, reader, installer.DeriveInstallWizardPrefill(""))
	})
	if err != nil {
		t.Fatalf("promptSecondaryStorage error: %v", err)
	}
	if enabled {
		t.Fatal("expected secondary storage disabled")
	}
	if path != "" || logPath != "" {
		t.Fatalf("declining must clear both paths, got path=%q log=%q", path, logPath)
	}
}

func TestPromptSecondaryStorageDisabledClearsExistingValues(t *testing.T) {
	var enabled bool
	var path, logPath string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("n\n"))
	template := "SECONDARY_ENABLED=true\nSECONDARY_PATH=/mnt/old-secondary\nSECONDARY_LOG_PATH=/mnt/old-secondary/logs\n"
	captureStdout(t, func() {
		enabled, path, logPath, err = promptSecondaryStorage(ctx, reader, installer.DeriveInstallWizardPrefill(template))
	})
	if err != nil {
		t.Fatalf("promptSecondaryStorage error: %v", err)
	}
	if enabled {
		t.Fatal("expected secondary storage disabled")
	}
	// The stored values must not survive the decline: the payload carries empty
	// paths, and config.ApplySecondaryStorageSettings clears both keys
	// (pinned by internal/config env_mutation_test.go).
	if path != "" || logPath != "" {
		t.Fatalf("expected old secondary values to be cleared, got path=%q log=%q", path, logPath)
	}
}

func TestPromptCloudStorageEnabled(t *testing.T) {
	var enabled bool
	var remote, logRemote string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\nremote:pbs\nremote:/logs\n"))
	captureStdout(t, func() {
		enabled, remote, logRemote, err = promptCloudStorage(ctx, reader, installer.DeriveInstallWizardPrefill(""))
	})
	if err != nil {
		t.Fatalf("promptCloudStorage error: %v", err)
	}
	if !enabled {
		t.Fatal("expected cloud storage enabled")
	}
	if remote != "remote:pbs" {
		t.Fatalf("remote = %q, want remote:pbs", remote)
	}
	if logRemote != "remote:/logs" {
		t.Fatalf("log remote = %q, want remote:/logs", logRemote)
	}
}

func TestPromptCloudStorageDisabled(t *testing.T) {
	var enabled bool
	var remote, logRemote string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("n\n"))
	captureStdout(t, func() {
		enabled, remote, logRemote, err = promptCloudStorage(ctx, reader, installer.DeriveInstallWizardPrefill(""))
	})
	if err != nil {
		t.Fatalf("promptCloudStorage error: %v", err)
	}
	if enabled {
		t.Fatal("expected cloud storage disabled")
	}
	if remote != "" || logRemote != "" {
		t.Fatalf("declining must clear both remotes, got remote=%q log=%q", remote, logRemote)
	}
}

// TestPromptCloudStorageRejectsValueEmptiedBySanitize pins the guard that keeps
// installer.ApplyInstallData's validateCloudInstallData unreachable: an answer that
// is non-empty before sanitizeEnvValue but empty after it must re-prompt instead of
// producing CLOUD_ENABLED=true with an empty remote.
func TestPromptCloudStorageRejectsValueEmptiedBySanitize(t *testing.T) {
	var enabled bool
	var remote, logRemote string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\n\x00\nremote:pbs\nremote:/logs\n"))
	output := captureStdout(t, func() {
		enabled, remote, logRemote, err = promptCloudStorage(ctx, reader, installer.DeriveInstallWizardPrefill(""))
	})
	if err != nil {
		t.Fatalf("promptCloudStorage error: %v", err)
	}
	if !enabled || remote != "remote:pbs" || logRemote != "remote:/logs" {
		t.Fatalf("enabled=%v remote=%q log=%q", enabled, remote, logRemote)
	}
	if !strings.Contains(output, "Value cannot be empty.") {
		t.Fatalf("expected the empty-value re-prompt message, got %q", output)
	}
}

func TestPromptCloudStorageKeepsExistingOnEdit(t *testing.T) {
	var enabled bool
	var remote, logRemote string
	var err error
	ctx := context.Background()
	// Pressing Enter through every prompt while editing an existing config must
	// keep the stored values rather than silently reset CLOUD_ENABLED to false.
	reader := bufio.NewReader(strings.NewReader("\n\n\n"))
	template := "CLOUD_ENABLED=true\nCLOUD_REMOTE=remote:pbs\nCLOUD_LOG_PATH=remote:/logs\n"
	captureStdout(t, func() {
		enabled, remote, logRemote, err = promptCloudStorage(ctx, reader, installer.DeriveInstallWizardPrefill(template))
	})
	if err != nil {
		t.Fatalf("promptCloudStorage error: %v", err)
	}
	if !enabled {
		t.Fatal("a no-op edit must keep cloud storage enabled")
	}
	if remote != "remote:pbs" || logRemote != "remote:/logs" {
		t.Fatalf("expected stored remotes preserved on no-op edit, got remote=%q log=%q", remote, logRemote)
	}
}

func TestPromptFirewallRulesDefaultsToDisabled(t *testing.T) {
	var enabled bool
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("\n"))
	captureStdout(t, func() {
		enabled, err = promptFirewallRules(ctx, reader, installer.DeriveInstallWizardPrefill(""))
	})
	if err != nil {
		t.Fatalf("promptFirewallRules error: %v", err)
	}
	if enabled {
		t.Fatal("expected firewall rules disabled by default")
	}
}

func TestPromptFirewallRulesDisabled(t *testing.T) {
	var enabled bool
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("n\n"))
	captureStdout(t, func() {
		enabled, err = promptFirewallRules(ctx, reader, installer.DeriveInstallWizardPrefill(""))
	})
	if err != nil {
		t.Fatalf("promptFirewallRules error: %v", err)
	}
	if enabled {
		t.Fatal("expected firewall rules disabled")
	}
}

func TestPromptNotifications(t *testing.T) {
	var telegram, email bool
	var method string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\nn\n"))
	captureStdout(t, func() {
		telegram, email, method, err = promptNotifications(ctx, reader, installer.DeriveInstallWizardPrefill(""))
	})
	if err != nil {
		t.Fatalf("promptNotifications error: %v", err)
	}
	if !telegram {
		t.Fatal("expected telegram enabled")
	}
	if email {
		t.Fatal("expected email disabled")
	}
	if method != "" {
		t.Fatalf("declining email must not collect a delivery method, got %q", method)
	}
}

func TestPromptNotificationsEmailDefaultsToRelay(t *testing.T) {
	var telegram, email bool
	var method string
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("n\ny\n\n"))
	captureStdout(t, func() {
		telegram, email, method, err = promptNotifications(ctx, reader, installer.DeriveInstallWizardPrefill(""))
	})
	if err != nil {
		t.Fatalf("promptNotifications error: %v", err)
	}
	if telegram {
		t.Fatal("expected telegram disabled")
	}
	if !email {
		t.Fatal("expected email enabled")
	}
	if method != "relay" {
		t.Fatalf("delivery method = %q, want relay", method)
	}
	// EMAIL_FALLBACK_SENDMAIL=true is written by installer.ApplyInstallData from the
	// non-nil EmailFallbackSendmail the collector always sends (pinned by
	// TestCollectInstallWizardDataCLIAlwaysSendsNonNilFlags and
	// internal/installer/install_data_test.go).
}

func TestPromptNotificationsKeepsExistingOnEdit(t *testing.T) {
	var telegram, email bool
	var method string
	var err error
	ctx := context.Background()
	// Enter through telegram, email and the email-method prompts: a no-op edit
	// must preserve the stored pmf delivery method instead of clobbering it to
	// relay. The stored personal bot mode is preserved by installer.ApplyInstallData
	// (it only seeds BOT_TELEGRAM_TYPE when the existing config has none), pinned by
	// the EditExistingNoOp characterization golden.
	reader := bufio.NewReader(strings.NewReader("\n\n\n"))
	template := "TELEGRAM_ENABLED=true\nBOT_TELEGRAM_TYPE=personal\nEMAIL_ENABLED=true\nEMAIL_DELIVERY_METHOD=pmf\n"
	captureStdout(t, func() {
		telegram, email, method, err = promptNotifications(ctx, reader, installer.DeriveInstallWizardPrefill(template))
	})
	if err != nil {
		t.Fatalf("promptNotifications error: %v", err)
	}
	if !telegram || !email {
		t.Fatalf("a no-op edit must keep both channels enabled, got telegram=%v email=%v", telegram, email)
	}
	if method != "pmf" {
		t.Fatalf("delivery method = %q, want pmf preserved", method)
	}
}

func TestPromptEmailDeliveryMethodAcceptsProxmoxAlias(t *testing.T) {
	method, err := promptEmailDeliveryMethod(context.Background(), bufio.NewReader(strings.NewReader("proxmox-notifications\n")), "relay")
	if err != nil {
		t.Fatalf("promptEmailDeliveryMethod error: %v", err)
	}
	if method != "pmf" {
		t.Fatalf("method=%q, want pmf", method)
	}
}

func TestRunPostInstallAuditCLIAbortIsNonBlocking(t *testing.T) {
	// EOF on the first audit prompt (Ctrl-D) must NOT abort the install: the
	// optional audit is skipped and runInstall continues to the entrypoint/cron
	// finalization, matching the TUI's non-blocking audit contract.
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader(""))
	var err error
	captureStdout(t, func() {
		err = runPostInstallAuditCLI(ctx, reader, "/nonexistent/proxsave", "/nonexistent/backup.env", nil)
	})
	if err != nil {
		t.Fatalf("post-install audit abort must be non-blocking, got: %v", err)
	}
}

func TestPromptEncryption(t *testing.T) {
	var enabled bool
	var err error
	ctx := context.Background()
	reader := bufio.NewReader(strings.NewReader("y\n"))
	captureStdout(t, func() {
		enabled, err = promptEncryption(ctx, reader, installer.DeriveInstallWizardPrefill(""))
	})
	if err != nil {
		t.Fatalf("promptEncryption error: %v", err)
	}
	if !enabled {
		t.Fatalf("expected encryption enabled")
	}

	// Declining while the stored config has it enabled must return false (the
	// ENCRYPT_ARCHIVE=false write is installer.ApplyInstallData's job).
	reader = bufio.NewReader(strings.NewReader("n\n"))
	captureStdout(t, func() {
		enabled, err = promptEncryption(ctx, reader, installer.DeriveInstallWizardPrefill("ENCRYPT_ARCHIVE=true\n"))
	})
	if err != nil {
		t.Fatalf("promptEncryption disable error: %v", err)
	}
	if enabled {
		t.Fatalf("expected disabled encryption")
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
	// 6 toggle declines, empty scheduler-engine answer (defaults to daemon on a
	// fresh install), empty healthcheck-mode answer (daemon-only prompt, defaults
	// to centralized), then the run-at time.
	reader := bufio.NewReader(strings.NewReader("n\nn\nn\nn\nn\nn\n\n\n03:15\n"))

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
	if result.SchedulerMode != "daemon" {
		t.Fatalf("fresh install should default to daemon, got %q", result.SchedulerMode)
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

func TestRunConfigWizardCLIEditExistingRemovesRuntimeDerivedKeys(t *testing.T) {
	cfgFile := createTempFile(t, "BASE_DIR=/custom\nCRON_HOUR=2\nMARKER=1\n")
	tmpConfigPath := cfgFile + ".tmp"
	// "2" = overwrite/keep decision, 6 toggle declines, empty scheduler answer, run-at.
	reader := bufio.NewReader(strings.NewReader("2\nn\nn\nn\nn\nn\nn\n\n03:15\n"))

	var err error
	captureStdout(t, func() {
		_, err = runConfigWizardCLI(context.Background(), reader, cfgFile, tmpConfigPath, "/opt/proxsave", nil)
	})
	if err != nil {
		t.Fatalf("runConfigWizardCLI returned error: %v", err)
	}

	content, readErr := os.ReadFile(cfgFile)
	if readErr != nil {
		t.Fatalf("expected config file to be written: %v", readErr)
	}
	values := parseWrittenEnvForTest(string(content))
	for _, key := range []string{"BASE_DIR", "CRON_SCHEDULE", "CRON_HOUR", "CRON_MINUTE"} {
		if _, ok := values[key]; ok {
			t.Fatalf("expected %s to be removed from config:\n%s", key, content)
		}
	}
	if values["MARKER"] != "1" {
		t.Fatalf("expected existing MARKER to be preserved, got %q in:\n%s", values["MARKER"], content)
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

func TestRunConfigWizardCLIAbortAtCronPromptDoesNotWriteConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "env", "backup.env")
	tmpConfigPath := configPath + ".tmp"

	originalConfigureCronTime := configureCronTimeFunc
	t.Cleanup(func() { configureCronTimeFunc = originalConfigureCronTime })

	configureCronTimeFunc = func(ctx context.Context, reader *bufio.Reader, defaultCron string) (string, error) {
		return "", errInteractiveAborted
	}

	reader := bufio.NewReader(strings.NewReader("n\nn\nn\nn\nn\nn\n"))

	_, err := runConfigWizardCLI(context.Background(), reader, configPath, tmpConfigPath, "/opt/proxsave", nil)
	if !errors.Is(err, errInteractiveAborted) {
		t.Fatalf("expected errInteractiveAborted, got %v", err)
	}
	if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected config file not to exist, got err=%v", statErr)
	}
	if _, statErr := os.Stat(tmpConfigPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected temp config file not to exist, got err=%v", statErr)
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

func parseWrittenEnvForTest(content string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if fields := strings.Fields(key); len(fields) >= 2 && fields[0] == "export" {
			key = fields[1]
		}
		values[strings.ToUpper(key)] = strings.TrimSpace(parts[1])
	}
	return values
}

// TestPrepareBaseTemplateEditBlankBaseStaysRaw pins the conditionality of the
// BaseTemplateOrDefault expansion, not merely its presence. Editing a blank
// backup.env must keep the base RAW: expanding it here would rewrite that file as
// the full embedded template instead of the minimal key set the wizard produces
// today, and would flip ApplyInstallData's editingExisting for that base.
//
// Without this test the gate is unpinned -- making the expansion unconditional
// leaves the whole suite, characterization goldens included, green.
func TestPrepareBaseTemplateEditBlankBaseStaysRaw(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"zero byte", ""},
		{"whitespace only", "  \n\t\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgFile := createTempFile(t, tc.content)
			reader := bufio.NewReader(strings.NewReader("2\n"))
			var base installWizardBase
			var fromExisting bool
			var err error
			captureStdout(t, func() {
				base, _, fromExisting, err = prepareBaseTemplate(context.Background(), reader, cfgFile, nil)
			})
			if err != nil {
				t.Fatalf("prepareBaseTemplate error: %v", err)
			}
			if !fromExisting {
				t.Fatal("edit must report fromExisting=true")
			}
			if base.Prompt != tc.content {
				t.Fatalf("blank base must stay raw; got %d bytes (%q), want the file content back", len(base.Prompt), base.Prompt)
			}
			if base.Raw != tc.content {
				t.Fatalf("blank base must reach the engine raw; got %d bytes (%q), want the file content back", len(base.Raw), base.Raw)
			}
		})
	}
}

// stubCrontabDerivation neutralizes the host's real crontab so the Edit path is
// deterministic (adoptCronRunTimeIntoBase would otherwise adopt a run time from it).
func stubCrontabDerivation(t *testing.T) {
	t.Helper()
	original := deriveSchedulerTimeFromCrontabFn
	t.Cleanup(func() { deriveSchedulerTimeFromCrontabFn = original })
	deriveSchedulerTimeFromCrontabFn = func(ctx context.Context, configPath string) schedulerTimeSeed {
		return schedulerTimeSeed{}
	}
}

// TestRunConfigWizardCLIHealthcheckModeResult pins installConfigResult.HealthcheckMode,
// which nothing asserted before and which gates runHealthcheckSelfParamsCLI in
// runInstall: getting it wrong silently skips (or wrongly shows) the self-mode
// ping-URL screen.
func TestRunConfigWizardCLIHealthcheckModeResult(t *testing.T) {
	tests := []struct {
		name          string
		script        string
		wantMode      string
		wantHCPrompt  bool
		wantScheduler string
	}{
		{"daemon self", "n\nn\nn\nn\nn\nn\ndaemon\nself\n03:15\n", "self", true, "daemon"},
		{"daemon off", "n\nn\nn\nn\nn\nn\ndaemon\noff\n03:15\n", "off", true, "daemon"},
		{"cron forces off without a prompt", "n\nn\nn\nn\nn\nn\ncron\n03:15\n", "off", false, "cron"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "env", "backup.env")
			var result installConfigResult
			var err error
			output := captureStdout(t, func() {
				reader := bufio.NewReader(strings.NewReader(tt.script))
				result, err = runConfigWizardCLI(context.Background(), reader, configPath, configPath+".tmp", "/opt/proxsave", nil)
			})
			if err != nil {
				t.Fatalf("runConfigWizardCLI error: %v", err)
			}
			if result.HealthcheckMode != tt.wantMode {
				t.Fatalf("HealthcheckMode = %q, want %q", result.HealthcheckMode, tt.wantMode)
			}
			if result.SchedulerMode != tt.wantScheduler {
				t.Fatalf("SchedulerMode = %q, want %q", result.SchedulerMode, tt.wantScheduler)
			}
			if got := strings.Contains(output, "Healthchecks monitoring:"); got != tt.wantHCPrompt {
				t.Fatalf("healthcheck prompt shown = %v, want %v", got, tt.wantHCPrompt)
			}
		})
	}
}

// TestCollectInstallWizardDataCLIBlankEditKeepsStoredDefaults pins
// wizardBlankBaseStandIn. The scheduler/healthcheck/run-at defaults used to be read
// off the RUNNING template, which the secondary-storage step had already made
// non-blank; feeding a genuinely blank Edit base to the *Default helpers instead
// flips the prompts to [daemon]/[centralized], shows the healthcheck prompt where it
// was never shown, consumes an extra scripted answer and flips HealthcheckMode
// off -> centralized. Deleting the stand-in fails this test.
func TestCollectInstallWizardDataCLIBlankEditKeepsStoredDefaults(t *testing.T) {
	stubCrontabDerivation(t)
	cfgFile := createTempFile(t, "")
	var result installConfigResult
	var err error
	output := captureStdout(t, func() {
		reader := bufio.NewReader(strings.NewReader("2\nn\nn\nn\nn\nn\nn\n\n03:15\n"))
		result, err = runConfigWizardCLI(context.Background(), reader, cfgFile, cfgFile+".tmp", "/opt/proxsave", nil)
	})
	if err != nil {
		t.Fatalf("runConfigWizardCLI error: %v", err)
	}
	if !strings.Contains(output, "or cron [cron]: ") {
		t.Fatalf("blank Edit must keep the [cron] scheduler default, got:\n%s", output)
	}
	if strings.Contains(output, "Healthchecks monitoring:") {
		t.Fatalf("blank Edit defaults to cron, so no healthcheck prompt may appear:\n%s", output)
	}
	if result.SchedulerMode != "cron" {
		t.Fatalf("SchedulerMode = %q, want cron", result.SchedulerMode)
	}
	if result.HealthcheckMode != "off" {
		t.Fatalf("HealthcheckMode = %q, want off", result.HealthcheckMode)
	}

	// Same base, but answering daemon: the healthcheck default must stay [off].
	daemonOutput := captureStdout(t, func() {
		reader := bufio.NewReader(strings.NewReader("2\nn\nn\nn\nn\nn\nn\ndaemon\n\n03:15\n"))
		_, err = runConfigWizardCLI(context.Background(), reader, cfgFile, cfgFile+".tmp", "/opt/proxsave", nil)
	})
	if err != nil {
		t.Fatalf("runConfigWizardCLI (daemon) error: %v", err)
	}
	if !strings.Contains(daemonOutput, "off, centralized, or self [off]: ") {
		t.Fatalf("blank Edit must keep the [off] healthcheck default, got:\n%s", daemonOutput)
	}
}

// TestRunConfigWizardCLIBlankEditKeepsMinimalKeySet pins the deliberate
// bug-compatibility of wizardBlankEditBaseMarker: editing an empty backup.env still
// writes the ~290-byte minimal key set, NOT the full embedded template that
// installer.ApplyInstallData substitutes for a blank base. Realigning this with the
// Charm front-end is a separate, signed-off behavior change; without the marker this
// test fails and the whole suite - characterization goldens included - stays green.
func TestRunConfigWizardCLIBlankEditKeepsMinimalKeySet(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"zero byte", ""},
		{"whitespace only", "  \n\t\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubCrontabDerivation(t)
			cfgFile := createTempFile(t, tc.content)
			var err error
			captureStdout(t, func() {
				reader := bufio.NewReader(strings.NewReader("2\nn\nn\nn\nn\nn\nn\n\n03:15\n"))
				_, err = runConfigWizardCLI(context.Background(), reader, cfgFile, cfgFile+".tmp", "/opt/proxsave", nil)
			})
			if err != nil {
				t.Fatalf("runConfigWizardCLI error: %v", err)
			}
			written, readErr := os.ReadFile(cfgFile)
			if readErr != nil {
				t.Fatalf("read back: %v", readErr)
			}
			if strings.Contains(string(written), "BACKUP_ENABLED") {
				t.Fatalf("blank Edit must not expand to the embedded template (%d bytes):\n%s", len(written), written)
			}
			if strings.Contains(string(written), wizardBlankEditBaseMarker) {
				t.Fatalf("the blank-base marker must never reach backup.env:\n%s", written)
			}
			values := parseWrittenEnvForTest(string(written))
			if values["SCHEDULER_MODE"] != "cron" || values["HEALTHCHECK_MODE"] != "off" {
				t.Fatalf("unexpected minimal key set:\n%s", written)
			}
			if !strings.HasPrefix(string(written), tc.content) {
				t.Fatalf("the operator's original bytes must survive verbatim:\n%q", string(written))
			}
		})
	}
}

// TestPrepareBaseTemplateRawIsEmptyOffTheEditPath pins the raw/expanded split that
// installWizardBase exists for. It is NOT covered by the characterization goldens:
// handing installer.ApplyInstallData the EXPANDED base instead of the raw one is
// byte-identical on every golden scenario (measured), because the embedded template
// already carries BOT_TELEGRAM_TYPE=centralized and the collector always supplies a
// non-empty EmailDeliveryMethod and a non-nil EmailFallbackSendmail. So this is the
// only thing that keeps the split honest.
func TestPrepareBaseTemplateRawIsEmptyOffTheEditPath(t *testing.T) {
	expanded := config.DefaultEnvTemplate()

	t.Run("no existing file", func(t *testing.T) {
		stubCrontabDerivation(t)
		configPath := filepath.Join(t.TempDir(), "backup.env")
		var base installWizardBase
		var err error
		captureStdout(t, func() {
			base, _, _, err = prepareBaseTemplate(context.Background(), bufio.NewReader(strings.NewReader("")), configPath, nil)
		})
		if err != nil {
			t.Fatalf("prepareBaseTemplate error: %v", err)
		}
		if base.Raw != "" {
			t.Fatalf("fresh install must hand the engine an empty raw base, got %d bytes", len(base.Raw))
		}
		if base.Prompt != expanded {
			t.Fatalf("fresh install must read prompt defaults from the embedded template")
		}
	})

	t.Run("overwrite", func(t *testing.T) {
		stubCrontabDerivation(t)
		cfgFile := createTempFile(t, "EXISTING=1\n")
		var base installWizardBase
		var err error
		captureStdout(t, func() {
			base, _, _, err = prepareBaseTemplate(context.Background(), bufio.NewReader(strings.NewReader("1\n")), cfgFile, nil)
		})
		if err != nil {
			t.Fatalf("prepareBaseTemplate error: %v", err)
		}
		if base.Raw != "" {
			t.Fatalf("overwrite must hand the engine an empty raw base, got %q", base.Raw)
		}
		if base.Prompt != expanded {
			t.Fatalf("overwrite must read prompt defaults from the embedded template")
		}
	})

	t.Run("keep existing", func(t *testing.T) {
		stubCrontabDerivation(t)
		cfgFile := createTempFile(t, "EXISTING=1\n")
		var base installWizardBase
		var skip bool
		var err error
		captureStdout(t, func() {
			base, skip, _, err = prepareBaseTemplate(context.Background(), bufio.NewReader(strings.NewReader("3\n")), cfgFile, nil)
		})
		if err != nil {
			t.Fatalf("prepareBaseTemplate error: %v", err)
		}
		if !skip {
			t.Fatal("expected skip=true")
		}
		if base.Raw != "" || base.Prompt != "" {
			t.Fatalf("keep existing must return a zero base, got %+v", base)
		}
	})

	t.Run("edit keeps raw and prompt equal", func(t *testing.T) {
		stubCrontabDerivation(t)
		const content = "EXISTING=1\n"
		cfgFile := createTempFile(t, content)
		var base installWizardBase
		var err error
		captureStdout(t, func() {
			base, _, _, err = prepareBaseTemplate(context.Background(), bufio.NewReader(strings.NewReader("2\n")), cfgFile, nil)
		})
		if err != nil {
			t.Fatalf("prepareBaseTemplate error: %v", err)
		}
		if base.Raw != content || base.Prompt != content {
			t.Fatalf("edit must keep both views raw, got raw=%q prompt=%q", base.Raw, base.Prompt)
		}
	})
}

// TestCollectInstallWizardDataCLIAlwaysSendsNonNilFlags pins two deliberate
// bug-compatibilities, so the follow-up commits that change them fail loudly instead
// of silently: BACKUP_FIREWALL_RULES is always written (never "keep the stored
// value"), and EMAIL_FALLBACK_SENDMAIL is always forced true when email is on
// (never installer.ApplyInstallData's 3-branch preserve logic).
func TestCollectInstallWizardDataCLIAlwaysSendsNonNilFlags(t *testing.T) {
	promptBase := config.DefaultEnvTemplate()

	t.Run("email enabled", func(t *testing.T) {
		var data *installer.InstallWizardData
		var err error
		captureStdout(t, func() {
			reader := bufio.NewReader(strings.NewReader("n\nn\nn\nn\ny\n\nn\ncron\n03:15\n"))
			data, err = collectInstallWizardDataCLI(context.Background(), reader, promptBase, false, nil)
		})
		if err != nil {
			t.Fatalf("collectInstallWizardDataCLI error: %v", err)
		}
		if data.BackupFirewallRules == nil {
			t.Fatal("BackupFirewallRules must never be nil")
		}
		if data.EmailFallbackSendmail == nil || !*data.EmailFallbackSendmail {
			t.Fatalf("EmailFallbackSendmail must be non-nil true when email is on, got %v", data.EmailFallbackSendmail)
		}
		if data.NotificationMode != "email" {
			t.Fatalf("NotificationMode = %q, want email", data.NotificationMode)
		}
		if strings.TrimSpace(data.EmailDeliveryMethod) == "" {
			t.Fatal("EmailDeliveryMethod must never be empty when email is on")
		}
		if strings.TrimSpace(data.CronTime) == "" {
			t.Fatal("CronTime must never be empty (it gates the SCHEDULER_TIME write)")
		}
	})

	t.Run("email declined", func(t *testing.T) {
		var data *installer.InstallWizardData
		var err error
		captureStdout(t, func() {
			reader := bufio.NewReader(strings.NewReader("n\nn\ny\ny\nn\nn\ncron\n03:15\n"))
			data, err = collectInstallWizardDataCLI(context.Background(), reader, promptBase, false, nil)
		})
		if err != nil {
			t.Fatalf("collectInstallWizardDataCLI error: %v", err)
		}
		if data.BackupFirewallRules == nil || !*data.BackupFirewallRules {
			t.Fatalf("BackupFirewallRules must be non-nil true, got %v", data.BackupFirewallRules)
		}
		if data.EmailFallbackSendmail != nil {
			t.Fatal("EmailFallbackSendmail must stay nil when email is off (the engine touches neither fallback key)")
		}
		if data.NotificationMode != "telegram" {
			t.Fatalf("NotificationMode = %q, want telegram", data.NotificationMode)
		}
	})
}

// TestApplyInstallDataCLIFeedsTheEngineTheRawBase pins the OTHER half of the
// raw/expanded split: prepareBaseTemplate producing the two views is useless if the
// wizard forwards the wrong one. This cannot be covered by the characterization
// goldens - handing installer.ApplyInstallData the expanded base instead of the raw
// one is byte-identical on every golden scenario (measured), because the embedded
// template already carries BOT_TELEGRAM_TYPE=centralized and the collector always
// supplies a non-empty EmailDeliveryMethod and a non-nil EmailFallbackSendmail. So
// the Prompt view here is deliberately a base the engine would treat very
// differently, making the mistake visible.
func TestApplyInstallDataCLIFeedsTheEngineTheRawBase(t *testing.T) {
	fallbackSendmail := true
	firewall := false
	data := &installer.InstallWizardData{
		NotificationMode:      "both",
		EmailDeliveryMethod:   "", // blank: the engine only substitutes a stored value when editingExisting
		EmailFallbackSendmail: &fallbackSendmail,
		BackupFirewallRules:   &firewall,
		SchedulerMode:         "cron",
		HealthcheckMode:       "off",
		CronTime:              "03:15",
	}

	t.Run("off the edit path the engine gets the raw empty base", func(t *testing.T) {
		base := installWizardBase{
			Raw:    "",
			Prompt: "BOT_TELEGRAM_TYPE=personal\nEMAIL_DELIVERY_METHOD=pmf\n",
		}
		got, err := applyInstallDataCLI(base, false, data)
		if err != nil {
			t.Fatalf("applyInstallDataCLI error: %v", err)
		}
		want, err := installer.ApplyInstallData(base.Raw, data)
		if err != nil {
			t.Fatalf("ApplyInstallData(raw) error: %v", err)
		}
		wrong, err := installer.ApplyInstallData(base.Prompt, data)
		if err != nil {
			t.Fatalf("ApplyInstallData(prompt) error: %v", err)
		}
		if want == wrong {
			t.Fatal("test is vacuous: the two bases must produce different output")
		}
		if got != want {
			t.Fatalf("the engine must receive base.Raw, not base.Prompt (got %d bytes, want %d)", len(got), len(want))
		}
		prefill := installer.DeriveInstallWizardPrefill(got)
		if prefill.TelegramType != "centralized" || prefill.EmailDeliveryMethod != "relay" {
			t.Fatalf("a fresh install must not inherit the prompt base's stored values: telegramType=%q method=%q",
				prefill.TelegramType, prefill.EmailDeliveryMethod)
		}
	})

	t.Run("on the edit path the raw base is forwarded verbatim", func(t *testing.T) {
		const stored = "BOT_TELEGRAM_TYPE=personal\nEMAIL_DELIVERY_METHOD=pmf\n"
		base := installWizardBase{Raw: stored, Prompt: stored}
		got, err := applyInstallDataCLI(base, true, data)
		if err != nil {
			t.Fatalf("applyInstallDataCLI error: %v", err)
		}
		want, err := installer.ApplyInstallData(stored, data)
		if err != nil {
			t.Fatalf("ApplyInstallData error: %v", err)
		}
		if got != want {
			t.Fatalf("edit must forward the raw base untouched:\ngot:\n%s\nwant:\n%s", got, want)
		}
		prefill := installer.DeriveInstallWizardPrefill(got)
		if prefill.TelegramType != "personal" || prefill.EmailDeliveryMethod != "pmf" {
			t.Fatalf("an edit must preserve the stored values: telegramType=%q method=%q",
				prefill.TelegramType, prefill.EmailDeliveryMethod)
		}
	})
}

// TestRunConfigWizardCLIRuntimeOnlyEditKeepsByteIdentity covers the second way an
// Edit base can be "blank" as far as the engine is concerned: it is non-empty on
// disk, but consists solely of runtime-derived keys that ApplyInstallData strips
// (BASE_DIR, CRON_SCHEDULE, CRON_HOUR, CRON_MINUTE). Testing only TrimSpace of the
// raw base missed it, and the appended keys landed after the leftover newline.
//
// The expectations are PARITY WITH THE PRE-REFACTOR CLI, measured at aedbe0c on a
// worktree, not what looks tidy: a base WITHOUT a trailing newline wrote 290 bytes
// and no leading blank line, while a base WITH one wrote 291 bytes AND a leading
// blank line, because the empty trailing element survives the key removal. The
// refactor must reproduce both, including the ugly one.
func TestRunConfigWizardCLIRuntimeOnlyEditKeepsByteIdentity(t *testing.T) {
	for _, tc := range []struct {
		name             string
		content          string
		wantLeadingBlank bool
	}{
		{name: "base dir without trailing newline", content: "BASE_DIR=/opt/proxsave"},
		{name: "cron keys without trailing newline", content: "CRON_HOUR=2\nCRON_MINUTE=30"},
		{name: "export form without trailing newline", content: "export BASE_DIR=/opt"},
		{
			name:             "all runtime keys with trailing newline",
			content:          "BASE_DIR=/opt\nCRON_SCHEDULE=0 2 * * *\nCRON_HOUR=2\nCRON_MINUTE=30\n",
			wantLeadingBlank: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubCrontabDerivation(t)
			cfgFile := createTempFile(t, tc.content)
			var err error
			captureStdout(t, func() {
				reader := bufio.NewReader(strings.NewReader("2\nn\nn\nn\nn\nn\nn\n\n03:15\n"))
				_, err = runConfigWizardCLI(context.Background(), reader, cfgFile, cfgFile+".tmp", "/opt/proxsave", nil)
			})
			if err != nil {
				t.Fatalf("runConfigWizardCLI error: %v", err)
			}
			written, readErr := os.ReadFile(cfgFile)
			if readErr != nil {
				t.Fatalf("read back: %v", readErr)
			}
			if got := strings.HasPrefix(string(written), "\n"); got != tc.wantLeadingBlank {
				t.Fatalf("leading blank line = %v, want %v (%d bytes):\n%q", got, tc.wantLeadingBlank, len(written), string(written))
			}
			if strings.Contains(string(written), "BACKUP_ENABLED") {
				t.Fatalf("runtime-only Edit must not expand to the embedded template (%d bytes)", len(written))
			}
			if strings.Contains(string(written), wizardBlankEditBaseMarker) {
				t.Fatalf("the blank-base marker must never reach backup.env:\n%s", written)
			}
		})
	}
}

// TestPromptSanitizedNonEmptySanitizesTheDefault pins the second half of the
// sanitize guard: a stored value made of control characters reaches the prompt
// through DeriveInstallWizardPrefill unchanged, and strings.TrimSpace does not
// consider NUL to be whitespace, so it used to be OFFERED as an acceptable default
// that the sanitize check then rejected on every iteration.
//
// This does NOT restore the pre-refactor outcome -- once a non-empty-after-sanitize
// value is required, Enter-only input on a poisoned default cannot succeed either
// way. What it removes is a default the operator is invited to accept and that can
// never be accepted, which is why the assertion is on the prompt text.
func TestPromptSanitizedNonEmptySanitizesTheDefault(t *testing.T) {
	var got string
	var err error
	out := captureStdout(t, func() {
		reader := bufio.NewReader(strings.NewReader("myremote:backups\n"))
		got, err = promptSanitizedNonEmptyWithDefault(context.Background(), reader, "Remote: ", "\x00\x00")
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "myremote:backups" {
		t.Fatalf("value = %q, want the typed answer", got)
	}
	if strings.Contains(out, "[") {
		t.Fatalf("a default that can never satisfy the sanitize check must not be offered; prompt was %q", out)
	}
	if strings.Contains(out, "\x00") {
		t.Fatalf("control characters must not be echoed into the prompt; prompt was %q", out)
	}
}
