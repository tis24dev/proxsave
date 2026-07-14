package installer

import (
	"errors"
	"strings"
	"testing"
)

// Data-layer tests salvaged from the deleted internal/tui/wizard
// install_test.go (the tview screens died with the package; ApplyInstallData
// and its helpers moved here).

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
	if _, ok := parseEnvTemplate(result)["BASE_DIR"]; ok {
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
	if _, ok := parseEnvTemplate(result)["BASE_DIR"]; ok {
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

// H7 regression: a partially-filled payload (cloud enabled but a remote left
// empty) must be rejected, never written as CLOUD_ENABLED=true with an empty
// CLOUD_REMOTE/CLOUD_LOG_PATH.

func TestApplyInstallDataRejectsCloudWithoutRemote(t *testing.T) {
	cases := []struct {
		name   string
		backup string
		log    string
	}{
		{"empty backup remote", "", "remote:logs"},
		{"empty log remote", "remote:backups", ""},
		{"both empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := &InstallWizardData{
				EnableCloudStorage: true,
				RcloneBackupRemote: tc.backup,
				RcloneLogRemote:    tc.log,
			}
			result, err := ApplyInstallData("", data)
			if err == nil {
				t.Fatalf("expected error for cloud enabled without a remote, got nil (result=%q)", result)
			}
			if strings.Contains(result, "CLOUD_ENABLED=true") {
				t.Fatalf("must not write CLOUD_ENABLED=true with an empty remote; result=%q", result)
			}
		})
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
