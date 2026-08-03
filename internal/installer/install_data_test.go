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

// TestApplyInstallDataEmailFallbackSendmail pins all four outcomes of the key that
// gates the local /usr/sbin/sendmail delivery leg, plus the retirement of its
// transitional EMAIL_FALLBACK_PMF spelling.
//
// The load-bearing one is preserve: neither front-end asks the operator about this
// key, so both send a nil pointer, and anything other than "keep what is stored" means
// a wizard pass that asked no question re-opens a delivery route the operator closed
// on purpose. Preserve here means the stored LINE is not rewritten at all, which is
// why the fixtures below use spellings a canonicalizing rewrite would visibly change.
func TestApplyInstallDataEmailFallbackSendmail(t *testing.T) {
	emailOn := func() *InstallWizardData {
		return &InstallWizardData{BaseDir: "/data", NotificationMode: "email"}
	}
	boolPtr := func(v bool) *bool { return &v }

	t.Run("fresh install ends with the fallback on", func(t *testing.T) {
		// A fresh install / Overwrite passes an EMPTY base: existingValues stays empty
		// (ApplyInstallData only parses a non-blank base) while template becomes the
		// embedded default, which already ships EMAIL_FALLBACK_SENDMAIL=true.
		//
		// So this pins the END STATE, not the arm that produced it -- the assertion
		// holds whether the seed arm ran or the shipped line was simply left alone.
		// The seed ARM is pinned by the sibling subtest below, which uses a base that
		// carries neither key. Kept because a change to the shipped template that
		// dropped the key would still be caught here.
		result, err := ApplyInstallData("", emailOn())
		if err != nil {
			t.Fatalf("ApplyInstallData: %v", err)
		}
		if !strings.Contains(result, "EMAIL_FALLBACK_SENDMAIL=true") {
			t.Fatalf("fresh install must end with the fallback on:\n%s", result)
		}
	})

	t.Run("edit without either fallback key seeds true", func(t *testing.T) {
		// The engine side of the CLI blank-Edit marker path: editingExisting is true
		// but the config predates both spellings. This is the ONLY subtest that
		// reaches the seed arm with a template that does not already carry the key,
		// so it is the one that dies if the arm is deleted.
		result, err := ApplyInstallData("# proxsave install wizard: blank existing configuration\nEMAIL_ENABLED=false\n", emailOn())
		if err != nil {
			t.Fatalf("ApplyInstallData: %v", err)
		}
		if !strings.Contains(result, "EMAIL_FALLBACK_SENDMAIL=true") {
			t.Fatalf("an edit with nothing stored must seed the fallback on:\n%s", result)
		}
	})

	t.Run("a present but empty value is preserved, not seeded over", func(t *testing.T) {
		// "KEY=" is a stored OFF, not an absent key: the loader reads it as false
		// (getBoolWithFallback stops at the first key it FINDS, and ParseBool("") is
		// false). Selecting the arm on the VALUE rather than on presence treated this
		// as "nothing stored" and seeded true over it -- the same silent re-open of
		// the sendmail route as the "false" case, spelled differently.
		result, err := ApplyInstallData("EMAIL_ENABLED=true\nEMAIL_FALLBACK_SENDMAIL=\n", emailOn())
		if err != nil {
			t.Fatalf("ApplyInstallData: %v", err)
		}
		if strings.Contains(result, "EMAIL_FALLBACK_SENDMAIL=true") {
			t.Fatalf("a bare KEY= is a stored off; a no-op edit must not turn it on:\n%s", result)
		}
	})

	t.Run("a present but empty legacy key migrates as off", func(t *testing.T) {
		// Same trap one spelling down. The loader reads this config as false, so the
		// migration must carry that across rather than read the empty value as
		// "nothing stored" and seed true.
		result, err := ApplyInstallData("EMAIL_ENABLED=true\nEMAIL_FALLBACK_PMF=\n", emailOn())
		if err != nil {
			t.Fatalf("ApplyInstallData: %v", err)
		}
		if !strings.Contains(result, "EMAIL_FALLBACK_SENDMAIL=false") {
			t.Fatalf("an empty legacy key means off and must migrate as off:\n%s", result)
		}
		if strings.Contains(result, "EMAIL_FALLBACK_PMF") {
			t.Fatalf("the transitional key must be retired by the migration:\n%s", result)
		}
	})

	t.Run("stored value is preserved verbatim", func(t *testing.T) {
		// Non-canonical on purpose: an export prefix, quotes, a value ParseBool reads
		// as false and an inline comment. All of it must survive an edit untouched.
		stored := `export EMAIL_FALLBACK_SENDMAIL="no"           # closed deliberately`
		result, err := ApplyInstallData("EMAIL_ENABLED=true\n"+stored+"\n", emailOn())
		if err != nil {
			t.Fatalf("ApplyInstallData: %v", err)
		}
		if !strings.Contains(result, stored) {
			t.Fatalf("the stored fallback line must survive byte-identical, want %q in:\n%s", stored, result)
		}
		if strings.Contains(result, "EMAIL_FALLBACK_SENDMAIL=true") {
			t.Fatalf("a no-op edit must not re-open the sendmail route:\n%s", result)
		}
	})

	t.Run("stored false is preserved", func(t *testing.T) {
		result, err := ApplyInstallData("EMAIL_ENABLED=true\nEMAIL_DELIVERY_METHOD=relay\nEMAIL_FALLBACK_SENDMAIL=false\n", emailOn())
		if err != nil {
			t.Fatalf("ApplyInstallData: %v", err)
		}
		if !strings.Contains(result, "EMAIL_FALLBACK_SENDMAIL=false") {
			t.Fatalf("a stored false must survive an edit:\n%s", result)
		}
		if strings.Contains(result, "EMAIL_FALLBACK_SENDMAIL=true") {
			t.Fatalf("a no-op edit must not re-open the sendmail route:\n%s", result)
		}
	})

	t.Run("legacy pmf value is migrated", func(t *testing.T) {
		// "no" rather than "false" so the migrated VALUE is pinned, not just its
		// presence: writing a literal true here would destroy the operator's choice.
		result, err := ApplyInstallData("EMAIL_ENABLED=true\nEMAIL_FALLBACK_PMF=no\n", emailOn())
		if err != nil {
			t.Fatalf("ApplyInstallData: %v", err)
		}
		if !strings.Contains(result, "EMAIL_FALLBACK_SENDMAIL=false") {
			t.Fatalf("the legacy value must be migrated onto the current key:\n%s", result)
		}
		if strings.Contains(result, "EMAIL_FALLBACK_PMF") {
			t.Fatalf("the transitional key must be retired by the migration:\n%s", result)
		}
	})

	t.Run("stale pmf is dropped while the stored value is preserved", func(t *testing.T) {
		// Both spellings present. config.getBoolWithFallback reads
		// EMAIL_FALLBACK_SENDMAIL first, so the engine must agree on that precedence
		// (preserve false, not adopt the alias's true) and must not leave the config
		// carrying two sources of truth.
		result, err := ApplyInstallData("EMAIL_ENABLED=true\nEMAIL_FALLBACK_SENDMAIL=false\nEMAIL_FALLBACK_PMF=true\n", emailOn())
		if err != nil {
			t.Fatalf("ApplyInstallData: %v", err)
		}
		if !strings.Contains(result, "EMAIL_FALLBACK_SENDMAIL=false") {
			t.Fatalf("the stored key must win over the transitional alias:\n%s", result)
		}
		if strings.Contains(result, "EMAIL_FALLBACK_PMF") {
			t.Fatalf("the transitional key must be dropped on the preserve path too:\n%s", result)
		}
	})

	t.Run("explicit false overrides a stored true", func(t *testing.T) {
		data := emailOn()
		data.EmailFallbackSendmail = boolPtr(false)
		result, err := ApplyInstallData("EMAIL_ENABLED=true\nEMAIL_FALLBACK_SENDMAIL=true\n", data)
		if err != nil {
			t.Fatalf("ApplyInstallData: %v", err)
		}
		if !strings.Contains(result, "EMAIL_FALLBACK_SENDMAIL=false") {
			t.Fatalf("an explicit answer must beat the stored value:\n%s", result)
		}
	})

	t.Run("explicit true overrides a stored false", func(t *testing.T) {
		// No front-end sends this today; the arm exists so the engine stays the one
		// place that can be told, for the day a prompt is signed off.
		data := emailOn()
		data.EmailFallbackSendmail = boolPtr(true)
		result, err := ApplyInstallData("EMAIL_ENABLED=true\nEMAIL_FALLBACK_SENDMAIL=false\n", data)
		if err != nil {
			t.Fatalf("ApplyInstallData: %v", err)
		}
		if !strings.Contains(result, "EMAIL_FALLBACK_SENDMAIL=true") {
			t.Fatalf("an explicit answer must beat the stored value:\n%s", result)
		}
	})

	t.Run("email disabled touches neither key", func(t *testing.T) {
		// The scope line: with email off the engine writes no
		// EMAIL_FALLBACK_SENDMAIL, so retiring the alias here would silently flip a
		// PMF-only config to the loader's true default.
		base := "EMAIL_ENABLED=true\nEMAIL_FALLBACK_SENDMAIL=false\nEMAIL_FALLBACK_PMF=true\n"
		data := &InstallWizardData{BaseDir: "/data", NotificationMode: "none"}
		result, err := ApplyInstallData(base, data)
		if err != nil {
			t.Fatalf("ApplyInstallData: %v", err)
		}
		if !strings.Contains(result, "EMAIL_FALLBACK_SENDMAIL=false") || !strings.Contains(result, "EMAIL_FALLBACK_PMF=true") {
			t.Fatalf("with email off both fallback keys must survive verbatim:\n%s", result)
		}
	})
}

// TestApplyInstallDataFirewallAnswerBeatsTheStoredValue pins the direction no golden
// covers: an operator CHANGING the firewall answer on an edit.
//
// The three characterization goldens all agree with whatever was already stored --
// the two fresh ones have nothing stored, and edit_noop answers true over a stored
// true -- so nothing in the repo notices if the engine starts preferring the stored
// value over the answered one. That is a one-line change away (guarding the write on
// the key being absent) and it lands on the arm production actually takes: both
// wizards ASK this question, so the explicit arm is the live one.
//
// The failure it prevents is silent and total: the wizard asks, the operator answers,
// and the answer is discarded.
func TestApplyInstallDataFirewallAnswerBeatsTheStoredValue(t *testing.T) {
	boolPtr := func(v bool) *bool { return &v }
	answered := func(v bool) *InstallWizardData {
		return &InstallWizardData{BaseDir: "/data", NotificationMode: "none", BackupFirewallRules: boolPtr(v)}
	}
	stored := func(v string) string {
		return "TELEGRAM_ENABLED=false\nBACKUP_FIREWALL_RULES=" + v + "\n"
	}
	assertKey := func(t *testing.T, result, want string) {
		t.Helper()
		got, ok := parseEnvTemplate(result)["BACKUP_FIREWALL_RULES"]
		if !ok || got != want {
			t.Fatalf("BACKUP_FIREWALL_RULES = %q (present=%v), want %q:\n%s", got, ok, want, result)
		}
		// A second line would let the loader read the intended value while the file
		// carries two sources of truth, so the value assertion alone is not enough.
		if n := strings.Count(result, "BACKUP_FIREWALL_RULES"); n != 1 {
			t.Fatalf("the key must appear exactly once, found %d:\n%s", n, result)
		}
	}

	t.Run("an explicit no turns off a stored yes", func(t *testing.T) {
		assertKey(t, mustApply(t, stored("true"), answered(false)), "false")
	})

	t.Run("an explicit yes turns on a stored no", func(t *testing.T) {
		assertKey(t, mustApply(t, stored("false"), answered(true)), "true")
	})
}

// mustApply runs ApplyInstallData and fails the test on error.
func mustApply(t *testing.T, base string, data *InstallWizardData) string {
	t.Helper()
	result, err := ApplyInstallData(base, data)
	if err != nil {
		t.Fatalf("ApplyInstallData: %v", err)
	}
	return result
}
