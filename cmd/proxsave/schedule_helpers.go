package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	cronutil "github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/installer"
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
		return schedulerTimeSeed{}
	}
	if err := setBackupEnvKeys(configPath, map[string]string{"SCHEDULER_TIME": hhmm}); err != nil {
		return schedulerTimeSeed{Note: fmt.Sprintf("Failed to record the existing cron run time %s as SCHEDULER_TIME: %v", hhmm, err)}
	}
	return schedulerTimeSeed{Time: hhmm, Note: fmt.Sprintf(
		"SCHEDULER_TIME was not set: adopted %s from the existing proxsave cron entry so the daily run time does not change.", hhmm)}
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
