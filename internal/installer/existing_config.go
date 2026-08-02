package installer

import (
	"fmt"
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
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
	// gate for adopting the crontab run time (see ApplySchedulerTimeSeed).
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

// ApplySchedulerTimeSeed mirrors a run time adopted from the host's existing
// proxsave cron line into the wizard's in-memory base, so the "Run at" prompt
// offers the host's real time instead of the 02:00 template default. It writes
// nothing to disk.
//
// The base=="" guard is the CLI's existing guard and is deliberately an
// EXACT-empty test, not strings.TrimSpace. Without it, seeding a "" base
// produces "\nSCHEDULER_TIME=HH:MM", which flips ApplyInstallData's
// editingExisting to true, defeats its blank->embedded-default substitution and
// writes a gutted config. KNOWN RESIDUE: a whitespace-only base is != "", so it
// is still mirrored into and still gutted; fixing that requires a TrimSpace test
// that would also change the CLI (a whitespace-only backup.env + Edit would stop
// adopting the crontab time), so it is out of scope here.
func ApplySchedulerTimeSeed(base, hhmm string) string {
	if hhmm == "" || base == "" {
		return base
	}
	return setEnvValue(base, "SCHEDULER_TIME", hhmm)
}
