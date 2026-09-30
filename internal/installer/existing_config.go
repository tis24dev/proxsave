package installer

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/safefs"
)

// ExistingConfigAction represents how to handle an already-present
// configuration file.
type ExistingConfigAction int

const (
	ExistingConfigOverwrite    ExistingConfigAction = iota // Start from embedded template (overwrite)
	ExistingConfigEdit                                     // Keep existing file as base and edit
	ExistingConfigKeepContinue                             // Leave file untouched and continue installation
	ExistingConfigCancel                                   // Abort installation
)

// ExistingConfigDecision is the engine-side outcome of the existing-config
// question, shared by the CLI prompt (cmd/proxsave/install_existing_config.go)
// and the Charm screen (internal/ui/flows/install.ResolveExistingConfig).
//
// BaseTemplate is the RAW base: "" means "no base, use the embedded default".
// That emptiness is LOAD-BEARING and must not be expanded before it reaches
// ApplyInstallData, which derives editingExisting from
// strings.TrimSpace(baseTemplate) != "": handing it an expanded default on
// Overwrite would silently flip editingExisting to true and change which keys
// are preserved. A front-end that drives its OWN prompts off the base (the CLI
// wizard) calls BaseTemplateOrDefault for that purpose only.
type ExistingConfigDecision struct {
	// BaseTemplate is the raw wizard base; "" means the embedded default.
	BaseTemplate string
	// SkipConfigWizard is set by KeepContinue: leave backup.env untouched.
	SkipConfigWizard bool
	// AbortInstall is set by Cancel: the caller must abort without changes.
	AbortInstall bool
	// FromExistingFile is true ONLY for Edit, i.e. the wizard starts from the
	// operator's current backup.env. Fresh installs and Overwrite start from the
	// embedded template, so defaults (e.g. the scheduler engine) may be the
	// recommended new values rather than the stored ones. It is also the single
	// gate for adopting the crontab schedule (see ApplyScheduleSeed).
	FromExistingFile bool
}

// ExistingConfigPresent is the stat pre-check both front-ends run before asking
// anything.
//
// (false, nil) = no file: a fresh install, the caller proceeds as Overwrite
// without showing any prompt. (true, nil) = a regular file the operator must
// decide about. (false, err) = the path is unusable. Callers keep their own
// context handling (the CLI still checks ctx.Err() on the no-file path before
// returning Overwrite).
func ExistingConfigPresent(configPath string) (bool, error) {
	info, err := os.Stat(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to access configuration file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("configuration file path is not a regular file: %s", configPath)
	}
	return true, nil
}

// ResolveExistingConfigDecision turns the operator's answer into the engine-side
// decision. Overwrite yields the RAW empty base (see ExistingConfigDecision) and
// NOT config.DefaultEnvTemplate(); Edit reads the current file and sets
// FromExistingFile; KeepContinue skips the wizard; Cancel aborts. An unknown
// action is a programming error.
func ResolveExistingConfigDecision(action ExistingConfigAction, configPath string) (ExistingConfigDecision, error) {
	switch action {
	case ExistingConfigOverwrite:
		return ExistingConfigDecision{
			BaseTemplate:     "",
			SkipConfigWizard: false,
			AbortInstall:     false,
		}, nil
	case ExistingConfigEdit:
		content, err := safefs.ReadFileUnderRoot(configPath)
		if err != nil {
			return ExistingConfigDecision{}, fmt.Errorf("read existing configuration: %w", err)
		}
		return ExistingConfigDecision{
			BaseTemplate:     string(content),
			SkipConfigWizard: false,
			AbortInstall:     false,
			FromExistingFile: true,
		}, nil
	case ExistingConfigKeepContinue:
		return ExistingConfigDecision{
			BaseTemplate:     "",
			SkipConfigWizard: true,
			AbortInstall:     false,
		}, nil
	case ExistingConfigCancel:
		return ExistingConfigDecision{
			BaseTemplate:     "",
			SkipConfigWizard: false,
			AbortInstall:     true,
		}, nil
	default:
		return ExistingConfigDecision{}, fmt.Errorf("unsupported existing configuration action: %d", action)
	}
}

// BaseTemplateOrDefault expands an empty RAW base into the embedded template.
// It exists for front-ends that compute their own prompt defaults from the base
// (the CLI wizard, whose Overwrite path used to receive an already-expanded
// template). ApplyInstallData performs the same substitution internally, so what
// is handed to IT must stay raw.
//
// It must be called ONLY when !FromExistingFile. On the Edit path a blank or
// whitespace-only backup.env is a real (if odd) operator state: expanding it here
// would rewrite that file as the FULL embedded template instead of the minimal
// mutated key set it produces today, and would flip ApplyInstallData's
// editingExisting for that base, changing which keys are treated as pre-existing.
func BaseTemplateOrDefault(base string) string {
	if strings.TrimSpace(base) == "" {
		return config.DefaultEnvTemplate()
	}
	return base
}

// ScheduleVariables is the backup.env spelling of a cadence as an adoption writes it:
// SCHEDULER_FREQUENCY, SCHEDULER_TIME and the day that frequency uses (SCHEDULER_WEEKDAY
// for weekly, SCHEDULER_MONTHDAY for monthly). The day the cadence does not use is left
// out, so an adoption never touches it, whatever value it holds: it cannot move the
// backup, and it is the operator's. When it is absent the config merge adds it with the
// template default.
func ScheduleVariables(c cron.Cadence) map[string]string {
	vars := map[string]string{
		"SCHEDULER_FREQUENCY": string(c.Frequency),
		"SCHEDULER_TIME":      c.Time,
	}
	switch c.Frequency {
	case cron.FrequencyWeekly:
		vars["SCHEDULER_WEEKDAY"] = cron.WeekdayName(c.Weekday)
	case cron.FrequencyMonthly:
		vars["SCHEDULER_MONTHDAY"] = strconv.Itoa(c.MonthDay)
	}
	return vars
}

// ApplyScheduleSeed mirrors a cadence adopted from the host's existing proxsave cron
// line into the wizard's in-memory base, so the Frequency, Weekday, Day of month and
// "Run at" fields offer the host's real schedule instead of the template defaults. It
// sets the variables ScheduleVariables names and leaves the unused day alone. It
// writes nothing to disk. A cadence with no frequency means nothing was adopted.
//
// The blank-base guard is load-bearing. Seeding a blank base produces
// "\nSCHEDULER_TIME=HH:MM", which flips ApplyInstallData's editingExisting to
// true, defeats its blank->embedded-default substitution, and writes a gutted
// config: the operator gets a handful of mutated keys where they should have got
// the whole template.
//
// The test is strings.TrimSpace, not an exact-empty comparison, so a
// whitespace-only backup.env is blank for this purpose exactly like an empty one.
// It used to be exact-empty, and the two then diverged on a single invisible
// character: an empty file produced the full template on Edit, a file holding one
// newline produced the gutted one. Nobody can predict that from the outside, and
// there is nothing in a whitespace-only file worth preserving.
//
// The cost is deliberate and small: with such a file, Edit no longer offers the
// host's crontab schedule as the default, falling back to the template's. Nothing
// then claims otherwise -- adoptCronRunTimeIntoBase logs its adoption block only
// when the seed actually changed the base (it compares the two values), so a
// discarded seed stays silent instead of promising a schedule it did not apply.
//
// A file that holds comments is NOT blank here, and must not be: that is content
// the operator wrote, and Edit keeps treating it as the existing configuration.
func ApplyScheduleSeed(base string, c cron.Cadence) string {
	if c.Frequency == "" || strings.TrimSpace(base) == "" {
		return base
	}
	vars := ScheduleVariables(c)
	for _, key := range []string{"SCHEDULER_FREQUENCY", "SCHEDULER_WEEKDAY", "SCHEDULER_MONTHDAY", "SCHEDULER_TIME"} {
		if value, ok := vars[key]; ok {
			base = setEnvValue(base, key, value)
		}
	}
	return base
}

// EnvKeyPresent reports whether content assigns key on a line that is not a comment,
// whatever the value, an empty one included. DeriveInstallWizardPrefill cannot tell: it
// reads an absent variable and an empty one as the same "", and for the schedule the two
// differ (an empty SCHEDULER_TIME is the 02:00 default the operator left in place, not a
// variable nobody has written).
func EnvKeyPresent(content, key string) bool {
	_, ok := parseEnvTemplate(content)[strings.ToUpper(strings.TrimSpace(key))]
	return ok
}
