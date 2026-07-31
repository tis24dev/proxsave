package cron

import "testing"

func TestScheduleToTime(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "daily schedule", in: "0 21 * * *", want: "21:00"},
		{name: "zero padded schedule", in: "00 21 * * *", want: "21:00"},
		{name: "whole crontab line", in: "00 21 * * * /usr/local/bin/proxsave --backup", want: "21:00"},
		{name: "leading whitespace", in: "  5 1 * * *  ", want: "01:05"},
		{name: "midnight shortcut", in: "@midnight /usr/local/bin/proxsave --backup", want: "00:00"},
		{name: "daily shortcut", in: "@daily", want: "00:00"},
		{name: "weekly shortcut", in: "@weekly", want: ""},
		{name: "reboot shortcut", in: "@reboot", want: ""},
		{name: "step minute", in: "*/15 * * * *", want: ""},
		{name: "list minute", in: "0,30 21 * * *", want: ""},
		{name: "range day of week", in: "0 21 * * 1-5", want: ""},
		{name: "pinned day of month", in: "0 21 1 * *", want: ""},
		{name: "pinned month", in: "0 21 * 6 *", want: ""},
		{name: "wildcard hour", in: "0 * * * *", want: ""},
		{name: "wildcard minute", in: "* 21 * * *", want: ""},
		{name: "hour out of range", in: "0 24 * * *", want: ""},
		{name: "minute out of range", in: "60 1 * * *", want: ""},
		{name: "signed minute", in: "+1 21 * * *", want: ""},
		{name: "too few fields", in: "0 21 * *", want: ""},
		{name: "empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ScheduleToTime(tt.in); got != tt.want {
				t.Fatalf("ScheduleToTime(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestScheduleToTimeRoundTrip pins ScheduleToTime as the exact inverse of
// TimeToSchedule: the daemon's HH:MM and the crontab line must never drift.
func TestScheduleToTimeRoundTrip(t *testing.T) {
	for _, hhmm := range []string{"00:00", DefaultTime, "21:00", "07:30", "23:59"} {
		schedule := TimeToSchedule(hhmm)
		if schedule == "" {
			t.Fatalf("TimeToSchedule(%q) returned empty", hhmm)
		}
		if got := ScheduleToTime(schedule); got != hhmm {
			t.Fatalf("ScheduleToTime(TimeToSchedule(%q)) = %q, want %q", hhmm, got, hhmm)
		}
	}
}
