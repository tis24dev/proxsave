package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/cron"
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

// weeklyAt21 is the cadence of "0 21 * * 1", the shape an adoption hands the mirror.
var weeklyAt21 = cron.Cadence{Frequency: cron.FrequencyWeekly, Weekday: time.Monday, MonthDay: 1, Time: "21:00"}

// TestApplyScheduleSeedEmptyBase pins S3: the mirror keeps its blank-base
// guard. Without it a blank base becomes "\nSCHEDULER_TIME=HH:MM", which flips
// ApplyInstallData's editingExisting to true, defeats its
// blank->embedded-default substitution and writes a gutted config.
func TestApplyScheduleSeedEmptyBase(t *testing.T) {
	if got := ApplyScheduleSeed("", weeklyAt21); got != "" {
		t.Fatalf("empty base must stay empty, got %q", got)
	}
	if got := ApplyScheduleSeed("SCHEDULER_MODE=cron\n", cron.Cadence{}); got != "SCHEDULER_MODE=cron\n" {
		t.Fatalf("no adopted cadence must leave the base untouched, got %q", got)
	}
}

// TestApplyScheduleSeedWhitespaceBase pins the half that used to escape.
// The guard was an exact-empty comparison, so a backup.env holding nothing but a
// newline was seeded, turned editingExisting on, and got the gutted config -- while
// a 0-byte file one keystroke away got the full template. There is nothing in a
// whitespace-only file to preserve, and no operator can predict a difference made
// of invisible characters.
//
// A comments-only base is the deliberate other side of that line: it is content the
// operator wrote, so it stays an existing configuration and is still seeded.
func TestApplyScheduleSeedWhitespaceBase(t *testing.T) {
	for _, base := range []string{" ", "\n", "\n\n", "  \t\n  "} {
		if got := ApplyScheduleSeed(base, weeklyAt21); got != base {
			t.Errorf("whitespace-only base %q must be left alone like an empty one, got %q", base, got)
		}
	}
	const commented = "# SCHEDULER_MODE=cron\n"
	if got := ApplyScheduleSeed(commented, weeklyAt21); got == commented {
		t.Errorf("a comments-only base is real content and must still be seeded, got %q", got)
	}
}

// The whole cadence reaches the base, not only the time: the wizard prefills Frequency,
// Weekday and Day of month from it, and ApplyInstallData writes back what they show. The
// day the cadence does not use is never touched (A13), whatever it holds.
func TestApplyScheduleSeedMirrorsTheWholeCadence(t *testing.T) {
	const base = "SCHEDULER_MODE=cron\nSCHEDULER_TIME=02:00\nSCHEDULER_MONTHDAY=31\n"
	got := ApplyScheduleSeed(base, weeklyAt21)
	want := "SCHEDULER_MODE=cron\nSCHEDULER_TIME=21:00\nSCHEDULER_MONTHDAY=31\n\nSCHEDULER_FREQUENCY=weekly\nSCHEDULER_WEEKDAY=mon"
	if got != want {
		t.Fatalf("ApplyScheduleSeed weekly =\n%q\nwant\n%q", got, want)
	}
}

// A13: each frequency writes only its own day; the other day and its value stay as they were,
// absent included.
func TestApplyScheduleSeedLeavesTheUnusedDayAlone(t *testing.T) {
	const base = "SCHEDULER_MODE=cron\nSCHEDULER_WEEKDAY=someday\nSCHEDULER_MONTHDAY=31\n"
	for _, tc := range []struct {
		name string
		c    cron.Cadence
		want string
	}{
		{"daily touches neither day", cron.Cadence{Frequency: cron.FrequencyDaily, Weekday: time.Monday, MonthDay: 1, Time: "03:00"},
			"SCHEDULER_MODE=cron\nSCHEDULER_WEEKDAY=someday\nSCHEDULER_MONTHDAY=31\n\nSCHEDULER_FREQUENCY=daily\nSCHEDULER_TIME=03:00"},
		{"monthly touches only the day of the month", cron.Cadence{Frequency: cron.FrequencyMonthly, Weekday: time.Monday, MonthDay: 15, Time: "03:00"},
			"SCHEDULER_MODE=cron\nSCHEDULER_WEEKDAY=someday\nSCHEDULER_MONTHDAY=15\n\nSCHEDULER_FREQUENCY=monthly\nSCHEDULER_TIME=03:00"},
		{"weekly touches only the weekday", cron.Cadence{Frequency: cron.FrequencyWeekly, Weekday: time.Sunday, MonthDay: 1, Time: "03:00"},
			"SCHEDULER_MODE=cron\nSCHEDULER_WEEKDAY=sun\nSCHEDULER_MONTHDAY=31\n\nSCHEDULER_FREQUENCY=weekly\nSCHEDULER_TIME=03:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ApplyScheduleSeed(base, tc.c); got != tc.want {
				t.Fatalf("ApplyScheduleSeed =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
	if got := ApplyScheduleSeed("SCHEDULER_MODE=cron\n", cron.Cadence{Frequency: cron.FrequencyMonthly, MonthDay: 15, Time: "03:00"}); strings.Contains(got, "SCHEDULER_WEEKDAY") {
		t.Fatalf("an absent unused day must stay absent, got %q", got)
	}
}

// A13: the variables an adoption writes are the frequency, the time and the day it uses.
func TestScheduleVariablesNamesOnlyTheDayItUses(t *testing.T) {
	for _, tc := range []struct {
		c    cron.Cadence
		want map[string]string
	}{
		{cron.Cadence{Frequency: cron.FrequencyMonthly, Weekday: time.Sunday, MonthDay: 15, Time: "03:00"},
			map[string]string{"SCHEDULER_FREQUENCY": "monthly", "SCHEDULER_MONTHDAY": "15", "SCHEDULER_TIME": "03:00"}},
		{cron.Cadence{Frequency: cron.FrequencyWeekly, Weekday: time.Sunday, MonthDay: 15, Time: "03:00"},
			map[string]string{"SCHEDULER_FREQUENCY": "weekly", "SCHEDULER_WEEKDAY": "sun", "SCHEDULER_TIME": "03:00"}},
		{cron.Cadence{Frequency: cron.FrequencyDaily, Weekday: time.Sunday, MonthDay: 15, Time: "03:00"},
			map[string]string{"SCHEDULER_FREQUENCY": "daily", "SCHEDULER_TIME": "03:00"}},
	} {
		got := ScheduleVariables(tc.c)
		if len(got) != len(tc.want) {
			t.Fatalf("ScheduleVariables(%s) = %v, want %v", tc.c.Frequency, got, tc.want)
		}
		for k, v := range tc.want {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", tc.c.Frequency, k, got[k], v)
			}
		}
	}
}

// A16/A19: an empty value is present; a commented or missing one is not.
func TestEnvKeyPresent(t *testing.T) {
	for _, tc := range []struct {
		content string
		want    bool
	}{
		{"SCHEDULER_TIME=\n", true},
		{"SCHEDULER_TIME=03:00\n", true},
		{"export SCHEDULER_TIME=03:00\n", true},
		{"scheduler_time=03:00\n", true},
		{"# SCHEDULER_TIME=03:00\n", false},
		{"SCHEDULER_TIMEOUT=5\n", false},
		{"", false},
	} {
		if got := EnvKeyPresent(tc.content, "SCHEDULER_TIME"); got != tc.want {
			t.Errorf("EnvKeyPresent(%q) = %v, want %v", tc.content, got, tc.want)
		}
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
