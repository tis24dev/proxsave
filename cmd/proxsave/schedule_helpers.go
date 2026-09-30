package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	cronutil "github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// resolveCronScheduleFromEnv returns a cron schedule string derived from the
// legacy environment overrides, falling back to 02:00 if unavailable.
func resolveCronScheduleFromEnv() string {
	if s := strings.TrimSpace(os.Getenv("CRON_SCHEDULE")); s != "" {
		return s
	}

	hour := strings.TrimSpace(os.Getenv("CRON_HOUR"))
	min := strings.TrimSpace(os.Getenv("CRON_MINUTE"))
	if hour != "" && min != "" {
		return fmt.Sprintf("%s %s * * *", min, hour)
	}

	return cronutil.TimeToSchedule(cronutil.DefaultTime)
}

// buildInstallCronSchedule keeps wizard-driven installs independent from
// env-based overrides while preserving the operator's schedule on a keep-config
// (skip-wizard) reinstall.
func buildInstallCronSchedule(skipConfigWizard bool, cronSchedule, configPath string) string {
	if !skipConfigWizard {
		if schedule := strings.TrimSpace(cronSchedule); schedule != "" {
			return schedule
		}
		return cronutil.TimeToSchedule(cronutil.DefaultTime)
	}
	// Keep-config reinstall: preserve the SCHEDULER_TIME already stored in the
	// config instead of silently rewriting cron to the legacy env / 02:00 default
	// (which reset the operator's run time and worsened RPO). Fall back to the
	// legacy CRON_* env, then DefaultTime, only when the config has no valid time.
	if sched := keptCronScheduleFromConfig(configPath); sched != "" {
		return sched
	}
	return resolveCronScheduleFromEnv()
}

// keptCronScheduleFromConfig returns the crontab schedule built from the SCHEDULER_* values
// stored in configPath, or "" when the file is unreadable, has no SCHEDULER_TIME, or carries a
// time that is not HH:MM. A PRESENT but empty SCHEDULER_TIME is the 02:00 default (D6), so the
// stored frequency and day still apply, at 02:00. A frequency or day it cannot read keeps the
// stored time on a daily line, as before SCHEDULER_FREQUENCY existed, rather than dropping the
// operator's time for the 02:00 default.
func keptCronScheduleFromConfig(configPath string) string {
	data, err := safefs.ReadFileUnderRoot(configPath)
	if err != nil {
		return ""
	}
	content := string(data)
	p := installer.DeriveInstallWizardPrefill(content)
	stored := strings.TrimSpace(p.SchedulerTime)
	if stored == "" && !installer.EnvKeyPresent(content, "SCHEDULER_TIME") {
		return ""
	}
	norm, err := cronutil.NormalizeTime(stored, cronutil.DefaultTime)
	if err != nil {
		return ""
	}
	if c, err := cronutil.ParseCadence(p.SchedulerFrequency, p.SchedulerWeekday, p.SchedulerMonthDay, norm); err == nil {
		return c.Schedule()
	}
	return cronutil.TimeToSchedule(norm)
}

// installCronSchedule is the crontab schedule for what a wizard collected. A cadence the
// wizard could not have produced falls back to the daily line at its time.
func installCronSchedule(data *installer.InstallWizardData) string {
	if data == nil {
		return ""
	}
	if c, err := data.Cadence(); err == nil {
		return c.Schedule()
	}
	return cronutil.TimeToSchedule(data.CronTime)
}

// configCronSchedule is the crontab schedule for a loaded config, with the same daily
// fallback at SCHEDULER_TIME when the frequency or day is not valid.
func configCronSchedule(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if c, err := cfg.SchedulerCadence(); err == nil {
		return c.Schedule()
	}
	return cronutil.TimeToSchedule(cfg.SchedulerTime)
}

// Texts of the adoption and removal blocks, approved by the maintainer (todo points 44-55).
const (
	scheduleAdoptHeader         = "Adopting backup schedule from cron entry..."
	scheduleAdoptedOutcome      = theme.SymbolSuccess + " Backup schedule: adopted from cron entry"
	scheduleWriteFailedOutcome  = theme.SymbolWarning + " Backup schedule: not adopted from cron entry"
	scheduleWriteFailedWhy      = "backup.env could not be written"
	cronRemovalHeader           = "Removing proxsave cron entry..."
	cronRemovedOutcome          = theme.SymbolSuccess + " Proxsave cron entry: removed"
	cronNotRemovedOutcome       = theme.SymbolWarning + " Proxsave cron entry: not removed, remove it by hand to avoid a double backup"
	cronNotRemovedWhy           = "Crontab could not be written"
	reasonLinesDisagree         = "lines_disagree"
	reasonFileNotManaged        = "file_not_managed"
	reasonWrapperNotInterpreted = "wrapper_not_interpreted"
)

// scheduleLineLevel is the level one line of a schedule block is logged at.
type scheduleLineLevel int

const (
	scheduleLineDebug scheduleLineLevel = iota
	scheduleLineInfo
	scheduleLineWarning
)

// scheduleLine is one line of a schedule block, at its level.
type scheduleLine struct {
	Level scheduleLineLevel
	Text  string
}

// scheduleBlock is a log block in the shape the log rules fix: the header, the DEBUG evidence
// of what was read, the indented details, the operator's reason (only for an outcome that is
// not OK) and the outcome LAST. Every part is optional; an empty block renders nothing.
type scheduleBlock struct {
	Header  string
	Debug   []string
	Details []string
	Why     string
	Outcome string
	Warn    bool // the outcome is a WARNING rather than INFO
}

func (b scheduleBlock) lines() []scheduleLine {
	var out []scheduleLine
	if b.Header != "" {
		out = append(out, scheduleLine{scheduleLineInfo, b.Header})
	}
	for _, text := range b.Debug {
		out = append(out, scheduleLine{scheduleLineDebug, text})
	}
	for _, text := range b.Details {
		out = append(out, scheduleLine{scheduleLineInfo, text})
	}
	if b.Why != "" {
		out = append(out, scheduleLine{scheduleLineInfo, b.Why})
	}
	if b.Outcome != "" {
		level := scheduleLineInfo
		if b.Warn {
			level = scheduleLineWarning
		}
		out = append(out, scheduleLine{level, b.Outcome})
	}
	return out
}

// logScheduleLines logs a block through the bootstrap logger, or through the global logger
// when there is none (the --daemon-setup path, see runDaemonSetup).
func logScheduleLines(bootstrap *logging.BootstrapLogger, lines []scheduleLine) {
	for _, line := range lines {
		switch line.Level {
		case scheduleLineDebug:
			logBootstrapDebug(bootstrap, "%s", line.Text)
		case scheduleLineWarning:
			logBootstrapWarning(bootstrap, "%s", line.Text)
		default:
			logBootstrapInfo(bootstrap, "%s", line.Text)
		}
	}
}

// schedulerTimeSeed is the outcome of adopting the host's cron schedule into backup.env.
// Cadence is what was adopted (no Frequency when nothing was). Block is what the install
// front-ends log, in full. Item is the one line --upgrade-config and --upgrade list among the
// configuration warnings when the outcome is one ("" otherwise); the block then travels only
// as its DEBUG evidence, see upgradeNotes.
//
// Nothing here is LOGGED by the functions that build it, because --upgrade-config-json must
// keep stdout pure JSON (upgradeConfigWithBinary json.Unmarshals the child's entire stdout).
type schedulerTimeSeed struct {
	Cadence cronutil.Cadence
	Block   scheduleBlock
	Item    string

	// inEffect is the schedule backup.env states without the adoption, which is what runs
	// when the adoption cannot be written.
	inEffect cronutil.Cadence
}

func (s schedulerTimeSeed) adopted() bool { return s.Cadence.Frequency != "" }

// empty reports a seed with nothing to write and nothing to say.
func (s schedulerTimeSeed) empty() bool {
	return !s.adopted() && s.Item == "" && len(s.Block.lines()) == 0
}

// upgradeNotes is the block as config.UpgradeResult.Notes. When the outcome is a warning item
// only the DEBUG evidence travels: the item says the rest, in the warnings list.
func (s schedulerTimeSeed) upgradeNotes() []config.UpgradeNote {
	var notes []config.UpgradeNote
	for _, line := range s.Block.lines() {
		switch {
		case line.Level == scheduleLineDebug:
			notes = append(notes, config.UpgradeNote{Level: config.UpgradeNoteDebug, Text: line.Text})
		case line.Level == scheduleLineInfo && s.Item == "":
			notes = append(notes, config.UpgradeNote{Level: config.UpgradeNoteInfo, Text: line.Text})
		}
	}
	return notes
}

// seedSchedulerTimeFromCrontabFn is a seam so the install/upgrade tests can drive
// the callers without touching a real crontab (mirrors migrateLegacyCronEntriesFn).
var seedSchedulerTimeFromCrontabFn = seedSchedulerTimeFromCrontab

// deriveSchedulerTimeFromCrontabFn is the read-only twin of the seam above.
var deriveSchedulerTimeFromCrontabFn = deriveSchedulerTimeFromCrontab

// seedSchedulerTimeFromCrontab records the schedule the host ACTUALLY runs its backup on into
// the four SCHEDULER_* variables, derived from the proxsave cron line, so the daemon that
// replaces cron - and the cron line a (re)install rewrites from the config - inherit it
// instead of the template defaults. SCHEDULER_TIME only exists since 0.30 and
// SCHEDULER_FREQUENCY since 0.41: on older installs the crontab is the sole record of the
// operator's schedule, and both the config merge and the daemon migration used to discard it.
//
// Precedence: an EXPLICIT operator schedule always wins; this only fills what the operator
// never stated. "Never stated" is KEY ABSENCE, never a value comparison: an empty variable is
// present and means its default (A16, A19). That is why every caller decides BEFORE the writer
// that would materialize the template defaults. The gate is deriveSchedulerTimeFromCrontab's;
// once both SCHEDULER_TIME and SCHEDULER_FREQUENCY exist this is a no-op, so it is safe to call
// on every install and every upgrade.
//
// Best-effort: an unreadable config or crontab, no proxsave cron line, or a schedule the
// daemon cannot express leaves the file untouched (the defaults keep applying).
func seedSchedulerTimeFromCrontab(ctx context.Context, configPath string) schedulerTimeSeed {
	return deriveSchedulerTimeFromCrontab(ctx, configPath).written(strings.TrimSpace(configPath))
}

// written writes an adopted cadence into configPath and adds the evidence to the block, or
// turns the seed into the one that says the write failed. It writes only the variables
// installer.ScheduleVariables names (A13). A seed that adopted nothing is returned as it is.
func (s schedulerTimeSeed) written(configPath string) schedulerTimeSeed {
	if !s.adopted() {
		return s
	}
	vars := installer.ScheduleVariables(s.Cadence)
	if err := setBackupEnvKeys(configPath, vars); err != nil {
		return s.writeFailed(configPath, err)
	}
	s.Block.Debug = append(append([]string(nil), s.Block.Debug...),
		fmt.Sprintf("schedule adopt: wrote %s file=%s", formatScheduleVariables(vars), configPath))
	return s
}

// writeFailed turns an adoption whose write failed into the block that says so, and into the
// one warnings-list item the upgrade paths show (A3). The error itself is DEBUG evidence only.
func (s schedulerTimeSeed) writeFailed(configPath string, err error) schedulerTimeSeed {
	debug := append(append([]string(nil), s.Block.Debug...),
		fmt.Sprintf("schedule adopt: write failed file=%s error=%v", configPath, err),
		fmt.Sprintf("schedule adopt: kept %s source=%s", cadenceFields(s.inEffect), configPath))
	return schedulerTimeSeed{
		Block: scheduleBlock{
			Header:  scheduleAdoptHeader,
			Debug:   debug,
			Details: cadenceDetails(s.inEffect),
			Why:     scheduleWriteFailedWhy,
			Outcome: scheduleWriteFailedOutcome,
			Warn:    true,
		},
		Item: fmt.Sprintf("%s Backup schedule: not adopted from cron entry, %s, %s",
			theme.SymbolWarning, scheduleWriteFailedWhy, inEffectItemLabel(s.inEffect)),
		inEffect: s.inEffect,
	}
}

// deriveSchedulerTimeFromCrontab is seedSchedulerTimeFromCrontab without the write.
// It exists because the install wizard must NOT touch backup.env before the operator
// has committed: on the Edit path the wizard rewrites the whole file at the end from
// its in-memory template, so the adopted schedule only has to reach that template, and
// an install cancelled halfway then leaves the host byte-identical.
//
// The gate (todo point 15) is on the variables being PRESENT in the file, an empty value
// included: an empty SCHEDULER_FREQUENCY is daily and an empty SCHEDULER_TIME is 02:00, both
// stated (A16, A19). SCHEDULER_TIME and SCHEDULER_FREQUENCY both present is an explicit
// schedule and nothing is adopted. SCHEDULER_TIME present without SCHEDULER_FREQUENCY still
// leaves the frequency unstated, so a weekly or monthly line is adopted WHOLE, its time
// replacing the stored one; a daily line adds nothing the stored time does not already say,
// and the explicit time wins. With no SCHEDULER_TIME every line the daemon can express is
// adopted.
//
// A line the daemon cannot express is reported, never rounded, whether or not SCHEDULER_TIME
// is present (A4, A5): a monthly line on day 29-31, a step, a list, a range, a name, two lines
// that disagree, the /etc line and the wrapper. The install is about to rewrite the crontab
// with a line of its own, so each of them either moves the backup or runs next to it.
func deriveSchedulerTimeFromCrontab(ctx context.Context, configPath string) schedulerTimeSeed {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return schedulerTimeSeed{}
	}
	data, err := safefs.ReadFileUnderRoot(configPath)
	if err != nil {
		return schedulerTimeSeed{}
	}
	content := string(data)
	prefill := installer.DeriveInstallWizardPrefill(content)
	timePresent := installer.EnvKeyPresent(content, "SCHEDULER_TIME")
	frequencyPresent := installer.EnvKeyPresent(content, "SCHEDULER_FREQUENCY")
	if timePresent && frequencyPresent {
		return schedulerTimeSeed{} // explicit operator schedule: never overridden
	}
	lines, err := crontabReadLinesFn(ctx)
	if err != nil {
		return schedulerTimeSeed{}
	}
	inEffect := cadenceInEffect(prefill)
	reading := schedulerCadenceFromCronLines(lines)
	switch {
	case reading.Found && reading.Reason == "":
		if timePresent && reading.Cadence.Frequency == cronutil.FrequencyDaily {
			return schedulerTimeSeed{}
		}
		return schedulerTimeSeed{
			Cadence: reading.Cadence,
			Block: scheduleBlock{
				Header: scheduleAdoptHeader,
				Debug: append(reading.debugLines(),
					fmt.Sprintf("schedule adopt: SCHEDULER_TIME=%s SCHEDULER_FREQUENCY=%s source=%s",
						storedValue(timePresent, prefill.SchedulerTime), storedValue(frequencyPresent, prefill.SchedulerFrequency), configPath)),
				Details: cadenceDetails(reading.Cadence),
				Outcome: scheduleAdoptedOutcome,
			},
			inEffect: inEffect,
		}
	case reading.Found && reading.Reason == cronutil.ReasonMonthDayOutOfRange:
		return monthDayOutOfRangeSeed(reading, inEffect, configPath)
	case reading.Found:
		return unadoptableLineSeed(reading, inEffect, configPath)
	}
	// The root crontab schedules nothing that NAMES the proxsave binary. Two other places may
	// still run it, and each is reported, not adopted: the line survives the install either way,
	// so adopting its schedule would put ProxSave's own line in the very minute it occupies.
	if etc := systemCronProxsaveCadences(); len(etc) > 0 {
		if at, ok := agreedDailySystemCronLine(etc); ok {
			return etcLineSeed(etc[at].Ref, etc[at].Cadence, inEffect)
		}
		return etcTwoSchedulesSeed(etc, inEffect)
	}
	// No cron line NAMES the proxsave binary, but one may still run it indirectly (#298). Its
	// schedule belongs to a script we did not write and cannot interpret. Lexical rules only
	// (cronProbeNamesOnly): this also runs in the install wizard and in --upgrade-config-json,
	// neither of which should be reading scripts off disk.
	if refs := indirectProxsaveCronRefs(lines, cronProbeNamesOnly); len(refs) > 0 {
		return wrapperSeed(refs[0], inEffect)
	}
	return schedulerTimeSeed{}
}

// monthDayOutOfRangeSeed reports a monthly line on day 29-31. Its day is outside the 1-28 a
// cadence allows, so no month skips the backup, and it is not rounded to one the operator did
// not choose. It is shared by install, upgrade and the cron -> daemon switch.
func monthDayOutOfRangeSeed(r cronCadenceReading, inEffect cronutil.Cadence, configPath string) schedulerTimeSeed {
	schedule := cronScheduleFields(r.Line)
	day := cronMonthDayField(r.Line)
	return schedulerTimeSeed{
		Block: scheduleBlock{
			Header:  scheduleAdoptHeader,
			Debug:   append(r.debugLines(), fmt.Sprintf("schedule adopt: kept %s source=%s", cadenceFields(inEffect), configPath)),
			Details: cadenceDetails(inEffect),
			Why:     fmt.Sprintf("Day %s is outside 1-%d", day, cronutil.MaxMonthDay),
			Outcome: fmt.Sprintf("%s Backup schedule: cron entry %q not adopted", theme.SymbolWarning, schedule),
			Warn:    true,
		},
		Item: fmt.Sprintf("%s Backup schedule: cron entry %q not adopted, day %s is outside 1-%d, %s",
			theme.SymbolWarning, schedule, day, cronutil.MaxMonthDay, inEffectItemLabel(inEffect)),
		inEffect: inEffect,
	}
}

// unadoptableLineSeed reports root-crontab proxsave lines that are not one cadence for a reason
// other than day 29-31 (A2): a step, a list, a range, a name, a restricted month, an
// unsupported shortcut, or two lines on different schedules. It is shared by install, upgrade
// and the cron -> daemon switch. Two lines that disagree are named by their first two different
// schedules.
func unadoptableLineSeed(r cronCadenceReading, inEffect cronutil.Cadence, configPath string) schedulerTimeSeed {
	label := inEffectItemLabel(inEffect)
	var why, outcome, item string
	if r.Reason == reasonLinesDisagree {
		first, second := cronScheduleFields(r.First), cronScheduleFields(r.Line)
		why = "Two schedules in the crontab"
		outcome = fmt.Sprintf("%s Backup schedule: cron entries %q and %q not adopted", theme.SymbolWarning, first, second)
		item = fmt.Sprintf("%s Backup schedule: cron entries %q and %q not adopted, two schedules, %s", theme.SymbolWarning, first, second, label)
	} else {
		schedule := cronScheduleFields(r.Line)
		why = fmt.Sprintf("%q is not daily, weekly or monthly", schedule)
		outcome = fmt.Sprintf("%s Backup schedule: cron entry %q not adopted", theme.SymbolWarning, schedule)
		item = fmt.Sprintf("%s Backup schedule: cron entry %q not adopted, not daily, weekly or monthly, %s", theme.SymbolWarning, schedule, label)
	}
	return schedulerTimeSeed{
		Block: scheduleBlock{
			Header:  scheduleAdoptHeader,
			Debug:   append(r.debugLines(), fmt.Sprintf("schedule adopt: kept %s source=%s", cadenceFields(inEffect), configPath)),
			Details: cadenceDetails(inEffect),
			Why:     why,
			Outcome: outcome,
			Warn:    true,
		},
		Item:     item,
		inEffect: inEffect,
	}
}

// etcLineSeed reports a proxsave line under /etc/crontab or /etc/cron.d, which ProxSave never
// edits, so it keeps running next to the line ProxSave writes. It is for the one agreed DAILY
// cadence: that is two backups a day. Anything else is etcTwoSchedulesSeed.
func etcLineSeed(ref indirectCronRef, kept cronutil.Cadence, inEffect cronutil.Cadence) schedulerTimeSeed {
	return schedulerTimeSeed{
		Block: scheduleBlock{
			Header: scheduleAdoptHeader,
			Debug: []string{
				"schedule adopt: root crontab has no proxsave line",
				fmt.Sprintf("schedule adopt: %s line=%q parsed %s, not adopted reason=%s", ref.Source, ref.Line, cadenceFields(kept), reasonFileNotManaged),
			},
			Details: append(cadenceDetails(inEffect), fmt.Sprintf("  Kept: %s, %s", ref.Source, cadenceLabel(kept))),
			Why:     fmt.Sprintf("Two backups a day: %s is not edited by ProxSave", ref.Source),
			Outcome: fmt.Sprintf("%s Backup schedule: cron entry in %s not adopted", theme.SymbolWarning, ref.Source),
			Warn:    true,
		},
		Item: fmt.Sprintf("%s Backup schedule: cron entry in %s not adopted, two backups a day, %s",
			theme.SymbolWarning, ref.Source, inEffectItemLabel(inEffect)),
		inEffect: inEffect,
	}
}

// etcTwoSchedulesSeed reports /etc proxsave lines that are not one agreed daily cadence (A7): a
// weekly or monthly line, a line whose schedule does not parse, or several lines on different
// schedules, in one file or several. Each line gets its own Kept detail with its own file; the
// reason and the outcome name the first line's file.
func etcTwoSchedulesSeed(etc []systemCronCadence, inEffect cronutil.Cadence) schedulerTimeSeed {
	first := etc[0].Ref.Source
	debug := []string{"schedule adopt: root crontab has no proxsave line"}
	details := cadenceDetails(inEffect)
	for _, l := range etc {
		if l.Err != nil {
			debug = append(debug, fmt.Sprintf("schedule adopt: %s line=%q not parsed reason=%s, not adopted reason=%s",
				l.Ref.Source, l.Ref.Line, scheduleErrorReason(l.Err), reasonFileNotManaged))
			details = append(details, fmt.Sprintf("  Kept: %s, %q", l.Ref.Source, cronScheduleFields(l.Ref.Line)))
			continue
		}
		debug = append(debug, fmt.Sprintf("schedule adopt: %s line=%q parsed %s, not adopted reason=%s",
			l.Ref.Source, l.Ref.Line, cadenceFields(l.Cadence), reasonFileNotManaged))
		details = append(details, fmt.Sprintf("  Kept: %s, %s", l.Ref.Source, cadenceLabel(l.Cadence)))
	}
	return schedulerTimeSeed{
		Block: scheduleBlock{
			Header:  scheduleAdoptHeader,
			Debug:   debug,
			Details: details,
			Why:     fmt.Sprintf("Two schedules: %s is not edited by ProxSave", first),
			Outcome: fmt.Sprintf("%s Backup schedule: cron entry in %s not adopted", theme.SymbolWarning, first),
			Warn:    true,
		},
		Item: fmt.Sprintf("%s Backup schedule: cron entry in %s not adopted, two schedules, %s",
			theme.SymbolWarning, first, inEffectItemLabel(inEffect)),
		inEffect: inEffect,
	}
}

// wrapperSeed reports a root-crontab line whose command appears to run ProxSave through a
// script (#298). Its schedule is named when it parses, and never adopted. A daily one is two
// backups a day; a weekly, monthly or unreadable one is two schedules (A8).
func wrapperSeed(ref indirectCronRef, inEffect cronutil.Cadence) schedulerTimeSeed {
	parsed, kept := "", ref.Command
	lead, itemLead := "Two schedules", "two schedules"
	if c, err := cronutil.ParseSchedule(ref.Line); err == nil {
		parsed = "parsed " + cadenceFields(c)
		kept = ref.Command + ", " + cadenceLabel(c)
		if c.Frequency == cronutil.FrequencyDaily {
			lead, itemLead = "Two backups a day", "two backups a day"
		}
	} else {
		parsed = "not parsed reason=" + scheduleErrorReason(err)
	}
	return schedulerTimeSeed{
		Block: scheduleBlock{
			Header: scheduleAdoptHeader,
			Debug: []string{
				"schedule adopt: root crontab has no proxsave line",
				fmt.Sprintf("schedule adopt: indirect command=%s line=%q %s, not adopted reason=%s", ref.Command, ref.Line, parsed, reasonWrapperNotInterpreted),
			},
			Details: append(cadenceDetails(inEffect), "  Kept: "+kept),
			Why:     fmt.Sprintf("%s: %s appears to run ProxSave", lead, ref.Command),
			Outcome: fmt.Sprintf("%s Backup schedule: cron entry for %s not adopted", theme.SymbolWarning, ref.Command),
			Warn:    true,
		},
		Item: fmt.Sprintf("%s Backup schedule: cron entry for %s not adopted, %s, %s",
			theme.SymbolWarning, ref.Command, itemLead, inEffectItemLabel(inEffect)),
		inEffect: inEffect,
	}
}

// systemCronCadence is one proxsave line under /etc/crontab or /etc/cron.d and the cadence it
// parses to, or Err when it does not parse.
type systemCronCadence struct {
	Ref     indirectCronRef
	Cadence cronutil.Cadence
	Err     error
}

// systemCronProxsaveCadences reads the proxsave lines under /etc/crontab and /etc/cron.d, in the
// order systemCronDirectProxsaveLines finds them, each with its cadence. Its caller reports them
// and never adopts them; see below.
//
// It runs only after the root crontab has yielded nothing, and that order is the priority
// rule, not an implementation detail: the root crontab is the table ProxSave owns and is
// about to rewrite, so a schedule found there is the one it is going to reinstate.
//
// A schedule found here is REPORTED, never adopted, and that is the whole difference between
// the two habitats. Adopting a root-crontab schedule is continuity, because the line it came
// from is the line ProxSave is about to replace. Adopting an /etc schedule is a collision:
// ProxSave never edits /etc, so that entry SURVIVES the install, and writing it into the
// SCHEDULER_* variables puts the line ProxSave is about to write in the exact minute the
// surviving one already occupies. The two runs then meet on the per-run lock and one exits 16
// every night. Left alone, the host keeps its /etc entry and gains ProxSave's at the schedule
// backup.env states: still two schedules, both of which succeed.
func systemCronProxsaveCadences() []systemCronCadence {
	var out []systemCronCadence
	for _, ref := range systemCronDirectProxsaveLines() {
		c, err := cronutil.ParseSchedule(ref.Line)
		out = append(out, systemCronCadence{Ref: ref, Cadence: c, Err: err})
	}
	return out
}

// agreedDailySystemCronLine reports whether every /etc proxsave line parses to the same daily
// cadence, the one case the report may call "two backups a day", and which line it names: the
// last one, as before SCHEDULER_FREQUENCY existed.
func agreedDailySystemCronLine(etc []systemCronCadence) (int, bool) {
	for _, l := range etc {
		if l.Err != nil || l.Cadence.Frequency != cronutil.FrequencyDaily || l.Cadence.Schedule() != etc[0].Cadence.Schedule() {
			return -1, false
		}
	}
	return len(etc) - 1, true
}

// cronCadenceReading is what the proxsave lines of a crontab say about the schedule. Found is
// false when no line names the binary. Otherwise Reason is "" and Cadence is the schedule all
// of them agree on, or Reason says why there is none and Line is the line it is about. When
// the reason is that two lines disagree, First is the first line, FirstCadence its cadence,
// and Line and Cadence the first line on a different schedule.
type cronCadenceReading struct {
	Found        bool
	Line         string
	Cadence      cronutil.Cadence
	Reason       string
	First        string
	FirstCadence cronutil.Cadence
}

// schedulerCadenceFromCronLines reads the one cadence the proxsave cron entries run on. Every
// proxsave-owned line (matched the same way dropCanonicalCronLines matches the lines it
// deletes, so we read exactly what is about to be removed) must parse with
// cronutil.ParseSchedule, and all of them must describe the same cadence: picking one of
// several would move the backup on purpose.
func schedulerCadenceFromCronLines(lines []string) cronCadenceReading {
	var reading cronCadenceReading
	for _, line := range lines {
		if !commandTokenMatchesTarget(strings.Trim(cronCommandToken(line), "\"'")) {
			continue
		}
		c, err := cronutil.ParseSchedule(line)
		if err != nil {
			return cronCadenceReading{Found: true, Line: line, Reason: scheduleErrorReason(err)}
		}
		if !reading.Found {
			reading = cronCadenceReading{Found: true, Line: line, Cadence: c}
			continue
		}
		if c.Schedule() != reading.Cadence.Schedule() {
			return cronCadenceReading{Found: true, Line: line, Cadence: c, Reason: reasonLinesDisagree,
				First: reading.Line, FirstCadence: reading.Cadence}
		}
	}
	return reading
}

// debugLines is the DEBUG evidence of a reading: what the line parsed to, or why it did not.
// Two lines that disagree are both named, each with what it parsed to.
func (r cronCadenceReading) debugLines() []string {
	switch r.Reason {
	case "":
		return []string{fmt.Sprintf("schedule adopt: cron line=%q parsed %s", r.Line, cadenceFields(r.Cadence))}
	case reasonLinesDisagree:
		return []string{
			fmt.Sprintf("schedule adopt: cron line=%q parsed %s", r.First, cadenceFields(r.FirstCadence)),
			fmt.Sprintf("schedule adopt: cron line=%q parsed %s, not adoptable reason=%s", r.Line, cadenceFields(r.Cadence), r.Reason),
		}
	default:
		return []string{fmt.Sprintf("schedule adopt: cron line=%q not adoptable reason=%s", r.Line, r.Reason)}
	}
}

func scheduleErrorReason(err error) string {
	var scheduleErr *cronutil.ScheduleError
	if errors.As(err, &scheduleErr) {
		return scheduleErr.Reason
	}
	return "unparsed"
}

// cadenceInEffect is the schedule backup.env states: its SCHEDULER_* values with their
// defaults, or a daily run at the stored time (the 02:00 default when that is unreadable too)
// when the frequency or a day is not valid.
func cadenceInEffect(p installer.InstallWizardPrefill) cronutil.Cadence {
	if c, err := cronutil.ParseCadence(p.SchedulerFrequency, p.SchedulerWeekday, p.SchedulerMonthDay, p.SchedulerTime); err == nil {
		return c
	}
	hhmm, err := cronutil.NormalizeTime(p.SchedulerTime, cronutil.DefaultTime)
	if err != nil {
		hhmm = cronutil.DefaultTime
	}
	return cronutil.Cadence{Frequency: cronutil.FrequencyDaily, Weekday: cronutil.DefaultWeekday, MonthDay: cronutil.DefaultMonthDay, Time: hhmm}
}

// cadenceFields is a cadence in DEBUG key=value form.
func cadenceFields(c cronutil.Cadence) string {
	switch c.Frequency {
	case cronutil.FrequencyWeekly:
		return fmt.Sprintf("frequency=weekly weekday=%s time=%s", cronutil.WeekdayName(c.Weekday), c.Time)
	case cronutil.FrequencyMonthly:
		return fmt.Sprintf("frequency=monthly monthday=%d time=%s", c.MonthDay, c.Time)
	default:
		return fmt.Sprintf("frequency=%s time=%s", c.Frequency, c.Time)
	}
}

// cadenceDetails is a cadence as the indented detail lines of a block: Frequency, then the
// day that frequency uses (none for daily), then Time.
func cadenceDetails(c cronutil.Cadence) []string {
	out := []string{"  Frequency: " + string(c.Frequency)}
	switch c.Frequency {
	case cronutil.FrequencyWeekly:
		out = append(out, "  Weekday: "+c.Weekday.String())
	case cronutil.FrequencyMonthly:
		out = append(out, fmt.Sprintf("  Day of month: %d", c.MonthDay))
	}
	return append(out, "  Time: "+c.Time)
}

// inEffectItemLabel is the schedule a warnings-list item says stays in effect. It says
// "default" only when that schedule really is the default one, daily at 02:00.
func inEffectItemLabel(c cronutil.Cadence) string {
	if c.Frequency == cronutil.FrequencyDaily && c.Time == cronutil.DefaultTime {
		return "default " + cadenceLabel(c)
	}
	return cadenceLabel(c)
}

// cronScheduleFields is the schedule part of a cron line: its five time fields, or the
// @shortcut.
func cronScheduleFields(line string) string {
	fields := strings.Fields(line)
	if len(fields) > 0 && strings.HasPrefix(fields[0], "@") {
		return fields[0]
	}
	if len(fields) < 5 {
		return strings.TrimSpace(line)
	}
	return strings.Join(fields[:5], " ")
}

// cronMonthDayField is the day-of-month field of a cron line.
func cronMonthDayField(line string) string {
	if fields := strings.Fields(line); len(fields) >= 3 {
		return fields[2]
	}
	return ""
}

// storedValue is a SCHEDULER_* variable as the DEBUG evidence reports it: absent, empty (its
// default, A16/A19) or its value.
func storedValue(present bool, v string) string {
	switch {
	case !present:
		return "absent"
	case strings.TrimSpace(v) == "":
		return "empty"
	default:
		return strings.TrimSpace(v)
	}
}

// formatScheduleVariables renders variables as sorted KEY=value pairs for a DEBUG line.
func formatScheduleVariables(vars map[string]string) string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+vars[k])
	}
	return strings.Join(parts, " ")
}

// adoptSchedulerTimeForDaemon carries the host's real schedule across a cron -> daemon switch,
// by overwriting the four SCHEDULER_* variables with the cadence of the proxsave cron entry
// that is about to be deleted.
//
// It must run BEFORE removeCanonicalCronEntry, which is the only record of that schedule on a
// cron host: in cron mode the crontab IS the schedule and the SCHEDULER_* variables are
// leftovers nothing keeps in step, so an operator who edited the cron line moved the backup
// while the variables stayed where the installer left them. The daemon then reads them, and
// the host would silently start running on a different schedule the moment it is
// retrofitted. It takes the crontab lines rather than reading them itself, so the schedule it
// adopts and the lines the removal acts on are the same snapshot.
//
// It OVERWRITES, unlike the install-time seeding, which fills the variables only when they
// are ABSENT because "an explicit operator value is never overridden"
// (deriveSchedulerTimeFromCrontab). That gate is right at install time, where the variables
// and the crontab are two independent statements of intent and neither has been in force
// over the other. Here it is not: the host has been running on cron, so the crontab is the
// statement that has been in force.
//
// It writes nothing when there is no single cadence to carry: no proxsave cron line at all,
// two lines that disagree, or a schedule no cadence expresses. Picking one of several would
// move the backup on purpose. The values already in backup.env then stand, which is the same
// answer the install path gives, and every line that could not be carried is reported with the
// same WARNING block the install logs (a day 29-31 line, and A2: a step, a list, a range, a
// name, two schedules).
//
// The ROOT crontab only. A proxsave line under /etc is deliberately not read here: ProxSave
// never edits /etc, so that line SURVIVES the switch, and adopting its schedule would put the
// daemon in the exact minute it already occupies.
func adoptSchedulerTimeForDaemon(configPath string, lines []string, bootstrap *logging.BootstrapLogger) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return
	}
	reading := schedulerCadenceFromCronLines(lines)
	if !reading.Found {
		return
	}
	data, err := safefs.ReadFileUnderRoot(configPath)
	if err != nil {
		logBootstrapDebug(bootstrap, "schedule adopt: config unreadable file=%s error=%v, nothing adopted", configPath, err)
		return
	}
	prefill := installer.DeriveInstallWizardPrefill(string(data))
	inEffect := cadenceInEffect(prefill)
	switch {
	case reading.Reason == cronutil.ReasonMonthDayOutOfRange:
		logScheduleLines(bootstrap, monthDayOutOfRangeSeed(reading, inEffect, configPath).Block.lines())
		return
	case reading.Reason != "":
		logScheduleLines(bootstrap, unadoptableLineSeed(reading, inEffect, configPath).Block.lines())
		return
	}
	// Already in step: nothing to write and nothing to say. An ABSENT SCHEDULER_TIME is not in
	// step even when the default matches: nothing has been stated, and writing it records the
	// schedule the host has been running on.
	if strings.TrimSpace(prefill.SchedulerTime) != "" && inEffect.Schedule() == reading.Cadence.Schedule() {
		logScheduleLines(bootstrap, scheduleBlock{Debug: append(reading.debugLines(),
			fmt.Sprintf("schedule adopt: backup.env already has %s source=%s, nothing written", cadenceFields(inEffect), configPath))}.lines())
		return
	}
	block := scheduleBlock{
		Header: scheduleAdoptHeader,
		Debug:  append(reading.debugLines(), fmt.Sprintf("schedule adopt: before %s source=%s", cadenceFields(inEffect), configPath)),
	}
	vars := installer.ScheduleVariables(reading.Cadence)
	if err := setBackupEnvKeys(configPath, vars); err != nil {
		// The removal that follows goes ahead either way - the switch before this adoption
		// existed removed the cron entry unconditionally, so refusing here would be a new
		// refusal rather than a fix - and the host then runs on whatever backup.env already
		// said. The operator cannot infer that from anything else on screen, so it is a
		// WARNING, with the schedule in effect as its details.
		block.Debug = append(block.Debug,
			fmt.Sprintf("schedule adopt: write failed file=%s error=%v", configPath, err),
			fmt.Sprintf("schedule adopt: kept %s source=%s", cadenceFields(inEffect), configPath))
		block.Details = cadenceDetails(inEffect)
		block.Why = scheduleWriteFailedWhy
		block.Outcome = scheduleWriteFailedOutcome
		block.Warn = true
	} else {
		block.Debug = append(block.Debug, fmt.Sprintf("schedule adopt: wrote %s file=%s", formatScheduleVariables(vars), configPath))
		block.Details = cadenceDetails(reading.Cadence)
		block.Outcome = scheduleAdoptedOutcome
	}
	logScheduleLines(bootstrap, block.lines())
}

// reportCronRemoval is the block that says whether the proxsave cron entry the switch to the
// daemon removes is really gone. lines is the crontab snapshot the adoption read, so the
// entries named are the ones the removal was asked to take away.
//
// When the removal failed the adopted schedule stays. It replaced a RESTORE that put
// SCHEDULER_TIME back to what the adoption had overwritten, and where the variable had been
// absent it wrote the compiled default - which was worse than doing nothing twice over. The
// SCHEDULER_* variables are ProxSave's own, so a failed crontab write is no reason to rewrite
// them; and writing the default over an absent variable turned "never recorded" into
// "recorded as 02:00", which is exactly the gate that stops any later install or upgrade
// adopting the host's real schedule from the crontab.
//
// So the adopted schedule stays and the operator is told. This is one of the cases where
// ProxSave says what to do rather than only what happened, and it earns it: it has just proved
// it cannot remove that line itself, and the operator asked for this switch. The per-run lock
// keeps the two schedules from overlapping; it does not stop the backup running twice.
//
// Two cases say nothing at WARNING level. The snapshot had no proxsave line and the removal
// worked: there was nothing to remove. The snapshot had one and the second read found none,
// with no error: the line went away between the two reads, so there is nothing to remove and
// nothing to warn about (A11), only DEBUG evidence. A removal that FAILED while the snapshot had
// no proxsave line still gets the WARNING block (A17): the second read may have found one, and
// nothing confirms the crontab holds none.
func reportCronRemoval(lines []string, outcome cronRemovalOutcome, err error, bootstrap *logging.BootstrapLogger) {
	present := proxsaveCronLines(lines)
	if len(present) == 0 {
		if err != nil {
			logScheduleLines(bootstrap, scheduleBlock{
				Header:  cronRemovalHeader,
				Debug:   []string{fmt.Sprintf("cron removal: no proxsave line in the first read, crontab write failed error=%v", err)},
				Why:     cronNotRemovedWhy,
				Outcome: cronNotRemovedOutcome,
				Warn:    true,
			}.lines())
		}
		return
	}
	block := scheduleBlock{Header: cronRemovalHeader}
	if err == nil && outcome.Verified && outcome.Removed > 0 {
		for _, line := range present {
			block.Debug = append(block.Debug, fmt.Sprintf("cron removal: removed line=%q verified=%t", line, outcome.Verified))
		}
		block.Outcome = cronRemovedOutcome
		logScheduleLines(bootstrap, block.lines())
		return
	}
	if err == nil && outcome.Verified && outcome.Removed == 0 {
		for _, line := range present {
			logBootstrapDebug(bootstrap, "cron removal: line=%q in the first read, absent in the second, nothing to remove", line)
		}
		return
	}
	if err != nil {
		block.Debug = append(block.Debug, fmt.Sprintf("cron removal: crontab write failed error=%v", err))
		for _, line := range present {
			block.Debug = append(block.Debug, fmt.Sprintf("cron removal: still present line=%q, per-run lock prevents overlap", line))
		}
		block.Why = cronNotRemovedWhy
	} else {
		// Unverified with no error: removeCanonicalCronEntry never returns that, and nothing
		// confirms the line is gone. "Crontab could not be written" would be untrue.
		for _, line := range present {
			block.Debug = append(block.Debug, fmt.Sprintf("cron removal: removed=%d verified=%t, line=%q not confirmed removed", outcome.Removed, outcome.Verified, line))
		}
	}
	block.Details = cronEntryDetails(lines, present)
	block.Outcome = cronNotRemovedOutcome
	block.Warn = true
	logScheduleLines(bootstrap, block.lines())
}

// cronEntryDetails names the proxsave entries a failed removal left in place (A10): their one
// cadence when they agree on a readable one, otherwise each line's schedule fields.
func cronEntryDetails(lines, present []string) []string {
	if r := schedulerCadenceFromCronLines(lines); r.Found && r.Reason == "" {
		return []string{"  Cron entry: " + cadenceLabel(r.Cadence)}
	}
	details := make([]string, 0, len(present))
	for _, line := range present {
		details = append(details, fmt.Sprintf("  Cron entry: %q", cronScheduleFields(line)))
	}
	return details
}

// adoptCronRunTimeIntoBase is the ONE place both front-ends adopt the host's cron
// schedule into the wizard's in-memory base. It returns the (possibly seeded) base
// and writes nothing to disk: on Edit the wizard rewrites the whole file at the end
// from this template, so an install cancelled halfway leaves the host byte-identical.
//
// The gate is decision.FromExistingFile, i.e. Edit ONLY. Cancel must leave the host
// untouched and Overwrite is about to replace the file, so an adoption block there
// would describe a value nobody will use. Keep existing has no wizard to carry the
// value, so its write stays deferred to the commit point in runInstall/runInstallTUI,
// which is also the single place its block is logged.
func adoptCronRunTimeIntoBase(ctx context.Context, decision installer.ExistingConfigDecision, configPath string, bootstrap *logging.BootstrapLogger) string {
	if !decision.FromExistingFile {
		return decision.BaseTemplate
	}
	seed := deriveSchedulerTimeFromCrontabFn(ctx, configPath)
	if seed.empty() {
		return decision.BaseTemplate
	}
	seeded := installer.ApplyScheduleSeed(decision.BaseTemplate, seed.Cadence)
	// The adoption block says the schedule was adopted, so it may only be logged when
	// the value actually reached the base: ApplyScheduleSeed discards it on a blank base
	// (see its guard), and a block the code then contradicts is worse than silence. The
	// other blocks adopt nothing and are truthful whatever the base looks like.
	if !seed.adopted() || seeded != decision.BaseTemplate {
		logScheduleLines(bootstrap, seed.Block.lines())
	}
	return seeded
}

// proxsaveCronLines returns the crontab lines that schedule proxsave (the same matcher
// dropCanonicalCronLines deletes by).
func proxsaveCronLines(lines []string) []string {
	var out []string
	for _, line := range lines {
		if commandTokenMatchesTarget(strings.Trim(cronCommandToken(line), "\"'")) {
			out = append(out, line)
		}
	}
	return out
}
