package wizard

import (
	"strings"
	"testing"
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

func TestApplyInstallDataRespectsBaseTemplate(t *testing.T) {
	baseTemplate := "BASE_DIR=\nMARKER=1\nTELEGRAM_ENABLED=false\nEMAIL_ENABLED=false\nENCRYPT_ARCHIVE=false\n"
	data := &InstallWizardData{
		BaseDir:                "/opt/proxsave",
		EnableSecondaryStorage: true,
		SecondaryPath:          "/mnt/sec",
		SecondaryLogPath:       "/mnt/sec/logs",
		EnableCloudStorage:     true,
		RcloneBackupRemote:     "remote:backups",
		RcloneLogRemote:        "remote:logs",
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
	assertContains("BASE_DIR", data.BaseDir)
	assertContains("SECONDARY_ENABLED", "true")
	assertContains("SECONDARY_PATH", data.SecondaryPath)
	assertContains("SECONDARY_LOG_PATH", data.SecondaryLogPath)
	assertContains("CLOUD_ENABLED", "true")
	assertContains("CLOUD_REMOTE", data.RcloneBackupRemote)
	assertContains("CLOUD_LOG_PATH", data.RcloneLogRemote)
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
	if !strings.Contains(result, "BASE_DIR="+data.BaseDir) {
		t.Fatalf("expected BASE_DIR to be set in default template")
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
	assertContains("CRON_SCHEDULE", "7 3 * * *")
	assertContains("CRON_HOUR", "3")
	assertContains("CRON_MINUTE", "7")
	assertContains("ENCRYPT_ARCHIVE", "false")
}
