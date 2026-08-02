package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExistingConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestResolveExistingConfigDecision(t *testing.T) {
	cfgFile := writeExistingConfig(t, "EXISTING=1\n")

	overwrite, err := ResolveExistingConfigDecision(ExistingConfigOverwrite, cfgFile)
	if err != nil {
		t.Fatalf("overwrite decision error: %v", err)
	}
	if overwrite.SkipConfigWizard || overwrite.AbortInstall || overwrite.FromExistingFile {
		t.Fatalf("overwrite decision flags are invalid: %+v", overwrite)
	}
	// S1: the decision carries the RAW base. "" is load-bearing - ApplyInstallData
	// derives editingExisting from strings.TrimSpace(baseTemplate) != "", so an
	// expanded default here would flip it to true and change which keys are
	// preserved. Front-ends that drive their own prompts off the base expand it
	// themselves via BaseTemplateOrDefault.
	if overwrite.BaseTemplate != "" {
		t.Fatalf("overwrite base template must stay raw/empty, got %q", overwrite.BaseTemplate)
	}
	if BaseTemplateOrDefault(overwrite.BaseTemplate) == "" {
		t.Fatalf("BaseTemplateOrDefault must expand the empty overwrite base")
	}

	edit, err := ResolveExistingConfigDecision(ExistingConfigEdit, cfgFile)
	if err != nil {
		t.Fatalf("edit decision error: %v", err)
	}
	if edit.SkipConfigWizard || edit.AbortInstall || !edit.FromExistingFile {
		t.Fatalf("edit decision flags are invalid: %+v", edit)
	}
	if !strings.Contains(edit.BaseTemplate, "EXISTING=1") {
		t.Fatalf("expected existing content, got %q", edit.BaseTemplate)
	}

	keep, err := ResolveExistingConfigDecision(ExistingConfigKeepContinue, cfgFile)
	if err != nil {
		t.Fatalf("keep decision error: %v", err)
	}
	if !keep.SkipConfigWizard || keep.AbortInstall || keep.FromExistingFile {
		t.Fatalf("keep decision flags are invalid: %+v", keep)
	}

	cancel, err := ResolveExistingConfigDecision(ExistingConfigCancel, cfgFile)
	if err != nil {
		t.Fatalf("cancel decision error: %v", err)
	}
	if cancel.SkipConfigWizard || !cancel.AbortInstall || cancel.FromExistingFile {
		t.Fatalf("cancel decision flags are invalid: %+v", cancel)
	}
}

func TestResolveExistingConfigDecisionEditReadError(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "missing.env")
	if _, err := ResolveExistingConfigDecision(ExistingConfigEdit, cfgFile); err == nil {
		t.Fatalf("expected read error for missing file")
	}
}

func TestResolveExistingConfigDecisionUnsupportedAction(t *testing.T) {
	cfgFile := writeExistingConfig(t, "EXISTING=1\n")
	if _, err := ResolveExistingConfigDecision(ExistingConfigAction(99), cfgFile); err == nil {
		t.Fatalf("expected unsupported action error")
	}
}

func TestResolveExistingConfigDecisionEditExistingContentExact(t *testing.T) {
	content := "KEY=VALUE\nANOTHER=1\n"
	cfg := writeExistingConfig(t, content)
	decision, err := ResolveExistingConfigDecision(ExistingConfigEdit, cfg)
	if err != nil {
		t.Fatalf("ResolveExistingConfigDecision error: %v", err)
	}
	if decision.BaseTemplate != content {
		t.Fatalf("expected exact content, got %q", decision.BaseTemplate)
	}
}

func TestExistingConfigPresent(t *testing.T) {
	cfg := writeExistingConfig(t, "EXISTING=1\n")
	present, err := ExistingConfigPresent(cfg)
	if err != nil {
		t.Fatalf("ExistingConfigPresent error: %v", err)
	}
	if !present {
		t.Fatalf("expected a regular file to be reported present")
	}

	missing := filepath.Join(t.TempDir(), "missing.env")
	present, err = ExistingConfigPresent(missing)
	if err != nil {
		t.Fatalf("missing file must not be an error, got %v", err)
	}
	if present {
		t.Fatalf("expected a missing file to be reported absent")
	}

	present, err = ExistingConfigPresent(t.TempDir())
	if err == nil {
		t.Fatalf("expected error for a non-regular file")
	}
	if present {
		t.Fatalf("a non-regular file must not be reported present")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// TestApplySchedulerTimeSeedEmptyBase pins S3: the mirror keeps the CLI's
// exact-empty guard. Without it a "" base becomes "\nSCHEDULER_TIME=HH:MM",
// which flips ApplyInstallData's editingExisting to true, defeats its
// blank->embedded-default substitution and writes a gutted config.
func TestApplySchedulerTimeSeedEmptyBase(t *testing.T) {
	if got := ApplySchedulerTimeSeed("", "21:00"); got != "" {
		t.Fatalf("empty base must stay empty, got %q", got)
	}
	if got := ApplySchedulerTimeSeed("SCHEDULER_MODE=cron\n", ""); got != "SCHEDULER_MODE=cron\n" {
		t.Fatalf("empty time must leave the base untouched, got %q", got)
	}
}

func TestApplySchedulerTimeSeedMirrorsTime(t *testing.T) {
	got := ApplySchedulerTimeSeed("SCHEDULER_MODE=cron\n", "21:00")
	if !strings.Contains(got, "SCHEDULER_TIME=21:00") {
		t.Fatalf("expected SCHEDULER_TIME=21:00 in %q", got)
	}
	if !strings.Contains(got, "SCHEDULER_MODE=cron") {
		t.Fatalf("expected the existing base to survive, got %q", got)
	}
}

func TestBaseTemplateOrDefault(t *testing.T) {
	if BaseTemplateOrDefault("") == "" {
		t.Fatalf("empty base must expand to the embedded template")
	}
	if got := BaseTemplateOrDefault("KEY=VALUE\n"); got != "KEY=VALUE\n" {
		t.Fatalf("a non-empty base must pass through unchanged, got %q", got)
	}
}
