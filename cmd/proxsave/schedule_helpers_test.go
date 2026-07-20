package main

import (
	"os"
	"path/filepath"
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
		if got := buildInstallCronSchedule(false, "15 03 * * *", ""); got != "15 03 * * *" {
			t.Fatalf("buildInstallCronSchedule(false, schedule) = %q, want %q", got, "15 03 * * *")
		}
	})

	t.Run("wizard run with empty schedule falls back to default time not env", func(t *testing.T) {
		t.Setenv("CRON_SCHEDULE", "5 1 * * *")
		if got := buildInstallCronSchedule(false, "", ""); got != cronutil.TimeToSchedule(cronutil.DefaultTime) {
			t.Fatalf("buildInstallCronSchedule(false, \"\") = %q, want %q", got, cronutil.TimeToSchedule(cronutil.DefaultTime))
		}
	})

	t.Run("keep-config preserves the stored SCHEDULER_TIME over env and default", func(t *testing.T) {
		// F10-02: a keep-config reinstall must keep the operator's run time, not
		// silently rewrite cron to the legacy env / 02:00.
		t.Setenv("CRON_SCHEDULE", "5 1 * * *")
		cfg := filepath.Join(t.TempDir(), "backup.env")
		if err := os.WriteFile(cfg, []byte("SCHEDULER_MODE=cron\nSCHEDULER_TIME=07:30\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := buildInstallCronSchedule(true, "", cfg); got != "30 07 * * *" {
			t.Fatalf("keep-config buildInstallCronSchedule = %q, want %q (stored SCHEDULER_TIME)", got, "30 07 * * *")
		}
	})

	t.Run("keep-config with no stored time falls back to env", func(t *testing.T) {
		t.Setenv("CRON_SCHEDULE", "5 1 * * *")
		cfg := filepath.Join(t.TempDir(), "backup.env")
		if err := os.WriteFile(cfg, []byte("SCHEDULER_MODE=cron\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := buildInstallCronSchedule(true, "15 03 * * *", cfg); got != "5 1 * * *" {
			t.Fatalf("keep-config (no stored time) = %q, want %q (env fallback)", got, "5 1 * * *")
		}
	})

	t.Run("keep-config with unreadable config falls back to env", func(t *testing.T) {
		t.Setenv("CRON_SCHEDULE", "5 1 * * *")
		if got := buildInstallCronSchedule(true, "", "/nonexistent/backup.env"); got != "5 1 * * *" {
			t.Fatalf("keep-config (unreadable) = %q, want %q (env fallback)", got, "5 1 * * *")
		}
	})
}

// TestCronTimeDefault pins F10-02 (Edit path): the "Run at" prompt default is
// seeded from the stored SCHEDULER_TIME on an Edit, so a no-op edit keeps the
// operator's time instead of resetting it to 02:00.
func TestCronTimeDefault(t *testing.T) {
	tmpl := "SCHEDULER_MODE=cron\nSCHEDULER_TIME=07:30\n"

	if got := cronTimeDefault(false, tmpl); got != cronutil.DefaultTime {
		t.Fatalf("fresh (fromExisting=false) = %q, want %q", got, cronutil.DefaultTime)
	}
	if got := cronTimeDefault(true, tmpl); got != "07:30" {
		t.Fatalf("edit with stored time = %q, want %q", got, "07:30")
	}
	if got := cronTimeDefault(true, ""); got != cronutil.DefaultTime {
		t.Fatalf("edit with empty template = %q, want %q", got, cronutil.DefaultTime)
	}
	if got := cronTimeDefault(true, "SCHEDULER_TIME=99:99\n"); got != cronutil.DefaultTime {
		t.Fatalf("edit with invalid stored time = %q, want %q", got, cronutil.DefaultTime)
	}
}
