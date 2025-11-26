package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/tis24dev/proxmox-backup/internal/tui/wizard"
)

// resolveCronSchedule returns a cron schedule string (e.g. "0 2 * * *") derived from
// wizard data or environment variables, falling back to 02:00 if unavailable.
func resolveCronSchedule(data *wizard.InstallWizardData) string {
	// Try wizard data first
	if data != nil {
		cron := strings.TrimSpace(data.CronTime)
		if cron != "" {
			if schedule := cronToSchedule(cron); schedule != "" {
				return schedule
			}
		}
	}

	// Environment overrides
	if s := strings.TrimSpace(os.Getenv("CRON_SCHEDULE")); s != "" {
		return s
	}
	hour := strings.TrimSpace(os.Getenv("CRON_HOUR"))
	min := strings.TrimSpace(os.Getenv("CRON_MINUTE"))
	if hour != "" && min != "" {
		return fmt.Sprintf("%s %s * * *", min, hour)
	}

	// Default: 02:00
	return "0 2 * * *"
}

// cronToSchedule converts HH:MM into "MM HH * * *".
func cronToSchedule(cron string) string {
	parts := strings.Split(cron, ":")
	if len(parts) != 2 {
		return ""
	}
	hour, errH := strconv.Atoi(parts[0])
	min, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil || hour < 0 || hour > 23 || min < 0 || min > 59 {
		return ""
	}
	return fmt.Sprintf("%02d %02d * * *", min, hour)
}
