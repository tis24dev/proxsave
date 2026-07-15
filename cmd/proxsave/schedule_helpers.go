package main

import (
	"fmt"
	"os"
	"strings"

	cronutil "github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/installer"
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
	data, err := os.ReadFile(configPath)
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
