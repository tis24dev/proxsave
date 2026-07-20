package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/installer"
)

// Characterization lock for the CLI install config wizard: these goldens
// freeze the exact prompt transcript and the exact configuration bytes each
// scripted run produces, so the Phase-4 extraction of the install engine
// into internal/installer can be proven byte-identical for the CLI.
// Regenerate deliberately with: go test -run Characterization -update
var updateGoldens = flag.Bool("update", false, "rewrite characterization golden files")

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "install_characterization", name)
	if *updateGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create): %v", name, err)
	}
	if string(want) != string(got) {
		t.Fatalf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

type wizardCharacterizationRun struct {
	transcript string
	configData []byte
	result     installConfigResult
	err        error
}

func runWizardCharacterization(t *testing.T, existingConfig string, script string) wizardCharacterizationRun {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "backup.env")
	if existingConfig != "" {
		if err := os.WriteFile(configPath, []byte(existingConfig), 0o600); err != nil {
			t.Fatalf("seed existing config: %v", err)
		}
	}

	var run wizardCharacterizationRun
	run.transcript = captureStdout(t, func() {
		reader := bufio.NewReader(strings.NewReader(script))
		run.result, run.err = runConfigWizardCLI(context.Background(), reader, configPath, configPath+".tmp", dir, nil)
	})
	// The temp dir changes per run: normalize it so transcripts are golden-stable.
	run.transcript = strings.ReplaceAll(run.transcript, dir, "<TMPDIR>")
	if data, err := os.ReadFile(configPath); err == nil {
		run.configData = data
	}
	return run
}

// editedExistingConfig builds a deterministic, fully-populated existing
// config exercising every prefill the wizard reads, including values the
// wizard must PRESERVE on a no-op edit (personal bot type, pmf delivery).
func editedExistingConfig() string {
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
		template = setEnvValue(template, kv[0], kv[1])
	}
	return template
}

func TestInstallWizardCharacterization_FreshDeclineAll(t *testing.T) {
	// No existing config: overwrite mode is implicit (no prompt); every
	// toggle declined via bare Enter; default scheduler (daemon), default
	// healthchecks mode (centralized, daemon-only prompt), + run-at accepted.
	run := runWizardCharacterization(t, "", strings.Repeat("\n", 9))
	if run.err != nil {
		t.Fatalf("wizard error: %v", run.err)
	}
	if run.result.EnableEncryption || run.result.SkipConfigWizard {
		t.Fatalf("unexpected result: %+v", run.result)
	}
	if run.result.CronSchedule == "" {
		t.Fatal("expected a cron schedule")
	}
	assertGolden(t, "fresh_decline_all.transcript", []byte(run.transcript))
	assertGolden(t, "fresh_decline_all.env", run.configData)
}

func TestInstallWizardCharacterization_FreshEnableAll(t *testing.T) {
	script := strings.Join([]string{
		"y",                   // secondary
		"/mnt/nas-backup",     // secondary path
		"/mnt/nas-backup/log", // secondary log path
		"y",                   // cloud
		"myremote:pbs-backups",
		"myremote:/logs",
		"y",     // firewall
		"y",     // telegram
		"y",     // email
		"",      // delivery method: default relay
		"y",     // encryption
		"",      // scheduler engine: default daemon
		"self",  // healthchecks mode (daemon-only): self
		"03:30", // run at
	}, "\n") + "\n"
	run := runWizardCharacterization(t, "", script)
	if run.err != nil {
		t.Fatalf("wizard error: %v", run.err)
	}
	if !run.result.EnableEncryption {
		t.Fatal("encryption must be enabled")
	}
	if run.result.CronSchedule != "30 03 * * *" {
		t.Fatalf("cron schedule = %q", run.result.CronSchedule)
	}
	assertGolden(t, "fresh_enable_all.transcript", []byte(run.transcript))
	assertGolden(t, "fresh_enable_all.env", run.configData)
}

func TestInstallWizardCharacterization_EditExistingNoOp(t *testing.T) {
	// Historical Tier-1 regression: a no-op edit must preserve every stored
	// setting (including BOT_TELEGRAM_TYPE=personal and
	// EMAIL_DELIVERY_METHOD=pmf, which the wizard must not reset).
	existing := editedExistingConfig()
	script := "2\n" + strings.Repeat("\n", 15)
	run := runWizardCharacterization(t, existing, script)
	if run.err != nil {
		t.Fatalf("wizard error: %v", run.err)
	}
	assertGolden(t, "edit_noop.transcript", []byte(run.transcript))
	assertGolden(t, "edit_noop.env", run.configData)

	values := installer.DeriveInstallWizardPrefill(string(run.configData))
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"SECONDARY_PATH", values.SecondaryPath, "/mnt/nas-backup"},
		{"CLOUD_REMOTE", values.CloudRemote, "myremote:pbs-backups"},
		{"BOT_TELEGRAM_TYPE", values.TelegramType, "personal"},
		{"EMAIL_DELIVERY_METHOD", values.EmailDeliveryMethod, "pmf"},
	}
	for _, c := range checks {
		if strings.TrimSpace(c.got) != c.want {
			t.Errorf("no-op edit reset %s: got %q, want %q", c.name, c.got, c.want)
		}
	}
	if !values.EncryptionEnabled {
		t.Error("no-op edit reset ENCRYPT_ARCHIVE")
	}

}

func TestInstallWizardCharacterization_KeepExisting(t *testing.T) {
	existing := editedExistingConfig()
	run := runWizardCharacterization(t, existing, "3\n")
	if run.err != nil {
		t.Fatalf("wizard error: %v", run.err)
	}
	if !run.result.SkipConfigWizard {
		t.Fatal("keep-existing must skip the wizard")
	}
	if string(run.configData) != existing {
		t.Fatal("keep-existing must leave the configuration bytes untouched")
	}
	assertGolden(t, "keep_existing.transcript", []byte(run.transcript))
}

func TestInstallWizardCharacterization_CancelAborts(t *testing.T) {
	existing := editedExistingConfig()
	run := runWizardCharacterization(t, existing, "0\n")
	if run.err == nil || !errors.Is(run.err, errInteractiveAborted) {
		t.Fatalf("expected interactive abort, got %v", run.err)
	}
	if string(run.configData) != existing {
		t.Fatal("cancel must leave the configuration bytes untouched")
	}
	assertGolden(t, "cancel.transcript", []byte(run.transcript))
}
