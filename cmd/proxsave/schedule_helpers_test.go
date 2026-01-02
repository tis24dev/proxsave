package main

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/tui/wizard"
)

func TestCronToSchedule(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"valid with padding", "2:5", "05 02 * * *"},
		{"valid already padded", "02:05", "05 02 * * *"},
		{"invalid format", "0205", ""},
		{"invalid hour", "24:00", ""},
		{"invalid minute", "00:60", ""},
		{"non numeric", "aa:bb", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cronToSchedule(tt.in); got != tt.want {
				t.Fatalf("cronToSchedule(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveCronSchedule(t *testing.T) {
	t.Run("wizard data takes precedence", func(t *testing.T) {
		t.Setenv("CRON_SCHEDULE", "0 4 * * *")
		data := &wizard.InstallWizardData{CronTime: "03:15"}
		if got := resolveCronSchedule(data); got != "15 03 * * *" {
			t.Fatalf("resolveCronSchedule(wizard) = %q, want %q", got, "15 03 * * *")
		}
	})

	t.Run("env CRON_SCHEDULE overrides", func(t *testing.T) {
		t.Setenv("CRON_SCHEDULE", "5 1 * * *")
		if got := resolveCronSchedule(nil); got != "5 1 * * *" {
			t.Fatalf("resolveCronSchedule(env) = %q, want %q", got, "5 1 * * *")
		}
	})

	t.Run("env CRON_HOUR+CRON_MINUTE", func(t *testing.T) {
		t.Setenv("CRON_SCHEDULE", "")
		t.Setenv("CRON_HOUR", "22")
		t.Setenv("CRON_MINUTE", "10")
		if got := resolveCronSchedule(nil); got != "10 22 * * *" {
			t.Fatalf("resolveCronSchedule(hour/minute) = %q, want %q", got, "10 22 * * *")
		}
	})

	t.Run("default fallback", func(t *testing.T) {
		t.Setenv("CRON_SCHEDULE", "")
		t.Setenv("CRON_HOUR", "")
		t.Setenv("CRON_MINUTE", "")
		if got := resolveCronSchedule(nil); got != "0 2 * * *" {
			t.Fatalf("resolveCronSchedule(default) = %q, want %q", got, "0 2 * * *")
		}
	})
}
