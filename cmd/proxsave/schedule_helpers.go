package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	cronutil "github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
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

// keptCronScheduleFromConfig returns the cron schedule ("MM HH * * *") built from
// the SCHEDULER_TIME stored in configPath, or "" when the file is unreadable or
// carries no valid HH:MM time.
func keptCronScheduleFromConfig(configPath string) string {
	data, err := safefs.ReadFileUnderRoot(configPath)
	if err != nil {
		return ""
	}
	stored := strings.TrimSpace(installer.DeriveInstallWizardPrefill(string(data)).SchedulerTime)
	if stored == "" {
		return ""
	}
	norm, err := cronutil.NormalizeTime(stored, cronutil.DefaultTime)
	if err != nil {
		return ""
	}
	return cronutil.TimeToSchedule(norm)
}

// schedulerTimeSeed is the outcome of a SCHEDULER_TIME seeding attempt: Time is
// the HH:MM written into backup.env ("" when nothing was written) and Note is a
// one-line operator-facing explanation ("" when there is nothing to report). The
// note is RETURNED rather than logged because --upgrade-config-json must keep
// stdout pure JSON (upgradeConfigWithBinary json.Unmarshals the child's entire
// stdout); that caller surfaces it as an UpgradeResult warning instead.
type schedulerTimeSeed struct {
	Time string
	Note string
}

// seedSchedulerTimeFromCrontabFn is a seam so the install/upgrade tests can drive
// the callers without touching a real crontab (mirrors migrateLegacyCronEntriesFn).
var seedSchedulerTimeFromCrontabFn = seedSchedulerTimeFromCrontab

// deriveSchedulerTimeFromCrontabFn is the read-only twin of the seam above.
var deriveSchedulerTimeFromCrontabFn = deriveSchedulerTimeFromCrontab

// seedSchedulerTimeFromCrontab records the time the host ACTUALLY runs its backup
// at into SCHEDULER_TIME, derived from the proxsave cron line, so the daemon that
// replaces cron - and the cron line a (re)install rewrites from the config -
// inherit it instead of the 02:00 template default. SCHEDULER_TIME only exists
// since 0.30: on every older install the crontab is the sole record of the
// operator's run time, and both the config merge and the daemon migration used to
// discard it.
//
// Precedence: an EXPLICIT operator SCHEDULER_TIME always wins; this only fills a
// value the operator never set. "Never set" is KEY ABSENCE (or an empty value),
// never a value comparison, which is why every caller runs this BEFORE the writer
// that would materialize the template default. Once the key exists this is a
// no-op, so it is safe to call on every install and every upgrade.
//
// Best-effort: an unreadable config or crontab, no proxsave cron line, or a
// schedule the daemon cannot express leaves the file untouched (DefaultTime keeps
// applying).
func seedSchedulerTimeFromCrontab(ctx context.Context, configPath string) schedulerTimeSeed {
	seed := deriveSchedulerTimeFromCrontab(ctx, configPath)
	if seed.Time == "" {
		return seed
	}
	if err := setBackupEnvKeys(configPath, map[string]string{"SCHEDULER_TIME": seed.Time}); err != nil {
		return schedulerTimeSeed{Note: fmt.Sprintf("Failed to record the existing cron run time %s as SCHEDULER_TIME: %v", seed.Time, err)}
	}
	return seed
}

// deriveSchedulerTimeFromCrontab is seedSchedulerTimeFromCrontab without the write.
// It exists because the install wizard must NOT touch backup.env before the operator
// has committed: on the Edit path the wizard rewrites the whole file at the end from
// its in-memory template, so the adopted time only has to reach that template, and an
// install cancelled halfway then leaves the host byte-identical.
func deriveSchedulerTimeFromCrontab(ctx context.Context, configPath string) schedulerTimeSeed {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return schedulerTimeSeed{}
	}
	data, err := safefs.ReadFileUnderRoot(configPath)
	if err != nil {
		return schedulerTimeSeed{}
	}
	if strings.TrimSpace(installer.DeriveInstallWizardPrefill(string(data)).SchedulerTime) != "" {
		return schedulerTimeSeed{} // explicit operator value: never overridden
	}
	lines, err := crontabReadLinesFn(ctx)
	if err != nil {
		return schedulerTimeSeed{}
	}
	hhmm, ok := schedulerTimeFromCronLines(lines)
	if !ok {
		if hasProxsaveCronLine(lines) {
			return schedulerTimeSeed{Note: fmt.Sprintf(
				"The existing proxsave cron entry is not a single daily time; SCHEDULER_TIME stays at the %s default - set it in backup.env if the backup must run at another time.",
				cronutil.DefaultTime)}
		}
		// No cron line NAMES the proxsave binary, but one may still run it indirectly
		// (#298). That wrapper's schedule is then the ONLY record of the host's real run
		// time, which is exactly why the silence hurt: SCHEDULER_TIME kept the 02:00
		// default, i.e. the very minute the wrapper already occupied. The time is still
		// not adopted - it belongs to a script we did not write and cannot interpret - so
		// say so and let the operator set it. Lexical rules only (cronProbeNamesOnly):
		// this also runs in the install wizard and in --upgrade-config-json, neither of
		// which should be reading scripts off disk.
		if hhmm, source := schedulerTimeFromSystemCron(); hhmm != "" {
			return schedulerTimeSeed{Note: fmt.Sprintf(
				"A proxsave cron entry in %s runs the backup at %s and ProxSave does not edit files it did not place, so that entry stays; SCHEDULER_TIME keeps the %s default and applies only to the entry ProxSave writes.", source, hhmm, cronutil.DefaultTime)}
		}
		if refs := indirectProxsaveCronRefs(lines, cronProbeNamesOnly); len(refs) > 0 {
			at := ""
			if t := cronutil.ScheduleToTime(refs[0].Line); t != "" {
				at = fmt.Sprintf(" at %s", t)
			}
			return schedulerTimeSeed{Note: fmt.Sprintf(
				"No proxsave cron entry was found, but %s appears to run ProxSave%s; SCHEDULER_TIME stays at the %s default - set it in backup.env so the scheduler does not collide with that entry.",
				refs[0].Command, at, cronutil.DefaultTime)}
		}
		return schedulerTimeSeed{}
	}
	return schedulerTimeSeed{Time: hhmm, Note: fmt.Sprintf(
		"SCHEDULER_TIME was not set: adopted %s from the existing proxsave cron entry so the daily run time does not change.", hhmm)}
}

// schedulerTimeFromSystemCron reads the run time of a proxsave cron line under /etc/crontab or
// /etc/cron.d, returning the time and the file it came from, or "" when the habitat says
// nothing unambiguous. Its caller reports it and does not adopt it; see below.
//
// It runs only after the root crontab has yielded nothing, and that order is the priority
// rule, not an implementation detail: the root crontab is the table ProxSave owns and is
// about to rewrite, so a time found there is the one it is going to reinstate.
//
// The time it returns is REPORTED, never adopted, and that is the whole difference between
// the two habitats. Adopting a root-crontab time is continuity, because the line it came from
// is the line ProxSave is about to replace. Adopting an /etc time is a collision: ProxSave
// never edits /etc, so that entry SURVIVES the install, and writing its hour into
// SCHEDULER_TIME puts the line ProxSave is about to write in the exact minute the surviving
// one already occupies. The two runs then meet on the per-run lock and one exits 16 every
// night. Left alone, the host keeps its /etc entry and gains ProxSave's at the default hour:
// still two backups, both of which succeed.
//
// Same unanimity rule as schedulerTimeFromCronLines: two proxsave lines at different times, or
// a schedule the daemon cannot express, say nothing. A finding that names one of several hours
// would read as the host's run time.
func schedulerTimeFromSystemCron() (string, string) {
	found, source := "", ""
	for _, ref := range systemCronDirectProxsaveLines() {
		hhmm := cronutil.ScheduleToTime(ref.Line)
		if hhmm == "" || (found != "" && found != hhmm) {
			return "", ""
		}
		found, source = hhmm, ref.Source
	}
	return found, source
}

// schedulerTimeFromCronLines derives the single daily HH:MM the proxsave cron
// entries run at. It returns ok=false unless the crontab expresses exactly ONE
// unambiguous daily time for proxsave: every proxsave-owned line (matched the same
// way dropCanonicalCronLines matches the lines it deletes, so we read exactly what
// is about to be removed) must convert to the same HH:MM. No proxsave line, a
// schedule the daemon cannot express, or two proxsave lines at different times all
// return false.
func schedulerTimeFromCronLines(lines []string) (string, bool) {
	found := ""
	for _, line := range lines {
		if !commandTokenMatchesTarget(strings.Trim(cronCommandToken(line), "\"'")) {
			continue
		}
		hhmm := cronutil.ScheduleToTime(line)
		if hhmm == "" || (found != "" && found != hhmm) {
			return "", false
		}
		found = hhmm
	}
	return found, found != ""
}

// adoptSchedulerTimeForDaemon carries the host's real run time across a cron -> daemon switch,
// by overwriting SCHEDULER_TIME with the time of the proxsave cron entry that is about to be
// deleted.
//
// It must run BEFORE removeCanonicalCronEntry, which is the only record of that time on a cron
// host: in cron mode the crontab IS the schedule and SCHEDULER_TIME is a leftover nothing keeps
// in step, so an operator who edited the cron line moved the backup while the key stayed where
// the installer left it. The daemon then reads the key, and the host would silently start
// running at a different hour the moment it is retrofitted. It takes the crontab lines rather
// than reading them itself, so the hour it adopts and the lines the removal acts on are the same
// snapshot.
//
// It OVERWRITES, unlike the install-time seeding, which fills the key only when it is ABSENT
// because "an explicit operator value is never overridden" (deriveSchedulerTimeFromCrontab).
// That gate is right at install time, where the key and the crontab are two independent
// statements of intent and neither has been in force over the other. Here it is not: the host
// has been running on cron, so the crontab is the statement that has been in force.
//
// It says nothing and writes nothing when there is no single daily time to carry: no proxsave
// cron line at all, two lines at different times, or a cadence that is not one run a day. The
// daemon runs once daily, so picking one of several would move the backup on purpose. The
// value already in the key then stands, which is the same answer the install path gives.
//
// The ROOT crontab only. A proxsave line under /etc is deliberately not read here: ProxSave
// never edits /etc, so that line SURVIVES the switch, and adopting its time would schedule the
// daemon in the exact minute it already occupies.
// It returns the value it OVERWROTE, or "" when it wrote nothing. The caller needs that to put
// the hour back when the removal it was written for does not go through: the line then survives
// at its own hour with the daemon pointed at the same minute, a collision ProxSave would have
// created on a host that had exactly one schedule.
func adoptSchedulerTimeForDaemon(configPath string, lines []string, bootstrap *logging.BootstrapLogger) string {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return ""
	}
	hhmm, ok := schedulerTimeFromCronLines(lines)
	if !ok {
		return ""
	}
	data, err := safefs.ReadFileUnderRoot(configPath)
	if err != nil {
		return ""
	}
	previous := strings.TrimSpace(installer.DeriveInstallWizardPrefill(string(data)).SchedulerTime)
	if previous == hhmm {
		return ""
	}
	if err := setBackupEnvKeys(configPath, map[string]string{"SCHEDULER_TIME": hhmm}); err != nil {
		logging.DebugStepBootstrap(bootstrap, "daemon setup", "could not record the adopted run time: %v", err)
		return ""
	}
	logBootstrapInfo(bootstrap, "SCHEDULER_TIME set to %s, the time of the proxsave cron entry this switch removes, so the daily run time does not change.", hhmm)
	return previous
}

// restoreSchedulerTime undoes an adoption whose removal did not go through. An empty previous
// value is left alone: the key was absent before, and writing an empty hour would be worse than
// the adopted one.
func restoreSchedulerTime(configPath, previous string, bootstrap *logging.BootstrapLogger) {
	if strings.TrimSpace(previous) == "" {
		return
	}
	if err := setBackupEnvKeys(configPath, map[string]string{"SCHEDULER_TIME": previous}); err != nil {
		logging.DebugStepBootstrap(bootstrap, "daemon setup", "could not restore the previous run time: %v", err)
		return
	}
	logBootstrapWarning(bootstrap, "The proxsave cron entry could not be removed, so SCHEDULER_TIME was put back to %s: leaving the adopted hour would schedule the daemon in the same minute as that entry.", previous)
}

// adoptCronRunTimeIntoBase is the ONE place both front-ends adopt the host's cron
// run time into the wizard's in-memory base. It returns the (possibly seeded) base
// and writes nothing to disk: on Edit the wizard rewrites the whole file at the end
// from this template, so an install cancelled halfway leaves the host byte-identical.
//
// The gate is decision.FromExistingFile, i.e. Edit ONLY. Cancel must leave the host
// untouched and Overwrite is about to replace the file, so an adoption note there
// would describe a value nobody will use. Keep existing has no wizard to carry the
// value, so its write stays deferred to the commit point in runInstall/runInstallTUI,
// which is also the single place its note is logged.
func adoptCronRunTimeIntoBase(ctx context.Context, decision installer.ExistingConfigDecision, configPath string, bootstrap *logging.BootstrapLogger) string {
	if !decision.FromExistingFile {
		return decision.BaseTemplate
	}
	seed := deriveSchedulerTimeFromCrontabFn(ctx, configPath)
	if seed.Note == "" {
		return decision.BaseTemplate
	}
	seeded := installer.ApplySchedulerTimeSeed(decision.BaseTemplate, seed.Time)
	// The adoption note promises "the daily run time does not change", so it may
	// only be logged when the value actually reached the base:
	// ApplySchedulerTimeSeed discards it on a blank base (see its guard), and a
	// note the code then contradicts is worse than silence. The other variant
	// carries no Time -- it warns that the cron entry could not be interpreted --
	// and is truthful whatever the base looks like.
	if seed.Time == "" || seeded != decision.BaseTemplate {
		logBootstrapInfo(bootstrap, "%s", seed.Note)
	}
	return seeded
}

// hasProxsaveCronLine reports whether the crontab schedules proxsave at all (used
// to warn only when there was a schedule we refused to interpret).
func hasProxsaveCronLine(lines []string) bool {
	for _, line := range lines {
		if commandTokenMatchesTarget(strings.Trim(cronCommandToken(line), "\"'")) {
			return true
		}
	}
	return false
}
