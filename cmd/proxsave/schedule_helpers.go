package main

import (
	"fmt"
	"os"
	"strings"

	cronutil "github.com/tis24dev/proxsave/internal/cron"
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
// env-based overrides while preserving the existing skip-wizard behavior.
func buildInstallCronSchedule(skipConfigWizard bool, cronSchedule string) string {
	if !skipConfigWizard {
		if schedule := strings.TrimSpace(cronSchedule); schedule != "" {
			return schedule
		}
		return cronutil.TimeToSchedule(cronutil.DefaultTime)
	}
	return resolveCronScheduleFromEnv()
}
