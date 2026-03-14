package main

import (
	"testing"

	cronutil "github.com/tis24dev/proxsave/internal/cron"
)

func TestResolveCronScheduleFromEnv(t *testing.T) {
	t.Run("env CRON_SCHEDULE overrides", func(t *testing.T) {
		t.Setenv("CRON_SCHEDULE", "5 1 * * *")
		if got := resolveCronScheduleFromEnv(); got != "5 1 * * *" {
			t.Fatalf("resolveCronScheduleFromEnv() = %q, want %q", got, "5 1 * * *")
		}
	})

	t.Run("env CRON_HOUR+CRON_MINUTE", func(t *testing.T) {
		t.Setenv("CRON_SCHEDULE", "")
		t.Setenv("CRON_HOUR", "22")
		t.Setenv("CRON_MINUTE", "10")
		if got := resolveCronScheduleFromEnv(); got != "10 22 * * *" {
			t.Fatalf("resolveCronScheduleFromEnv() = %q, want %q", got, "10 22 * * *")
		}
	})

	t.Run("default fallback", func(t *testing.T) {
		t.Setenv("CRON_SCHEDULE", "")
		t.Setenv("CRON_HOUR", "")
		t.Setenv("CRON_MINUTE", "")
		if got := resolveCronScheduleFromEnv(); got != cronutil.TimeToSchedule(cronutil.DefaultTime) {
			t.Fatalf("resolveCronScheduleFromEnv() = %q, want %q", got, cronutil.TimeToSchedule(cronutil.DefaultTime))
		}
	})
}

func TestBuildInstallCronSchedule(t *testing.T) {
	t.Run("wizard schedule takes precedence over env", func(t *testing.T) {
		t.Setenv("CRON_SCHEDULE", "5 1 * * *")
		if got := buildInstallCronSchedule(false, "15 03 * * *"); got != "15 03 * * *" {
			t.Fatalf("buildInstallCronSchedule(false, schedule) = %q, want %q", got, "15 03 * * *")
		}
	})

	t.Run("wizard run with empty schedule falls back to default time not env", func(t *testing.T) {
		t.Setenv("CRON_SCHEDULE", "5 1 * * *")
		if got := buildInstallCronSchedule(false, ""); got != cronutil.TimeToSchedule(cronutil.DefaultTime) {
			t.Fatalf("buildInstallCronSchedule(false, \"\") = %q, want %q", got, cronutil.TimeToSchedule(cronutil.DefaultTime))
		}
	})

	t.Run("skip wizard uses env fallback", func(t *testing.T) {
		t.Setenv("CRON_SCHEDULE", "5 1 * * *")
		if got := buildInstallCronSchedule(true, "15 03 * * *"); got != "5 1 * * *" {
			t.Fatalf("buildInstallCronSchedule(true, schedule) = %q, want %q", got, "5 1 * * *")
		}
	})
}
