package cron

import (
	"errors"
	"testing"
	"time"
)

func TestParseCadenceFields(t *testing.T) {
	c, err := ParseCadence("", "", "", "")
	if err != nil {
		t.Fatalf("empty fields: %v", err)
	}
	if c != (Cadence{Frequency: FrequencyDaily, Weekday: time.Monday, MonthDay: 1, Time: DefaultTime}) {
		t.Fatalf("empty fields = %+v; want the daily defaults", c)
	}

	c, err = ParseCadence(" Weekly ", "FRI", "", "3:05")
	if err != nil {
		t.Fatalf("weekly: %v", err)
	}
	if c.Frequency != FrequencyWeekly || c.Weekday != time.Friday || c.Time != "03:05" {
		t.Fatalf("weekly = %+v", c)
	}

	c, err = ParseCadence("monthly", "", "28", "23:59")
	if err != nil {
		t.Fatalf("monthly: %v", err)
	}
	if c.Frequency != FrequencyMonthly || c.MonthDay != 28 {
		t.Fatalf("monthly = %+v", c)
	}

	for _, tc := range []struct{ freq, weekday, monthday, hhmm string }{
		{"hourly", "", "", "02:00"},
		{"weekly", "monday", "", "02:00"},
		{"weekly", "7", "", "02:00"},
		{"monthly", "", "29", "02:00"},
		{"monthly", "", "0", "02:00"},
		{"monthly", "", "x", "02:00"},
		{"daily", "", "", "25:00"},
	} {
		if _, err := ParseCadence(tc.freq, tc.weekday, tc.monthday, tc.hhmm); err == nil {
			t.Errorf("ParseCadence(%q, %q, %q, %q) accepted an invalid value", tc.freq, tc.weekday, tc.monthday, tc.hhmm)
		}
	}
}

// TestParseCadenceIgnoresAnInvalidUnusedDay: only the day the frequency uses is validated. An
// invalid day the frequency does not use is ignored and the cadence carries its default; a
// valid unused day is kept, so switching frequency keeps it.
func TestParseCadenceIgnoresAnInvalidUnusedDay(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		freq, weekday, monthday, hhmm string
		want                          Cadence
	}{
		{"weekly ignores the monthday", "weekly", "fri", "31", "03:30",
			Cadence{Frequency: FrequencyWeekly, Weekday: time.Friday, MonthDay: DefaultMonthDay, Time: "03:30"}},
		{"monthly ignores the weekday", "monthly", "someday", "15", "03:30",
			Cadence{Frequency: FrequencyMonthly, Weekday: DefaultWeekday, MonthDay: 15, Time: "03:30"}},
		{"daily ignores both days", "daily", "someday", "x", "03:30",
			Cadence{Frequency: FrequencyDaily, Weekday: DefaultWeekday, MonthDay: DefaultMonthDay, Time: "03:30"}},
		{"empty frequency is daily and ignores both days", "", "7", "0", "",
			Cadence{Frequency: FrequencyDaily, Weekday: DefaultWeekday, MonthDay: DefaultMonthDay, Time: DefaultTime}},
		{"a valid unused day is kept", "daily", "sat", "20", "03:30",
			Cadence{Frequency: FrequencyDaily, Weekday: time.Saturday, MonthDay: 20, Time: "03:30"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCadence(tc.freq, tc.weekday, tc.monthday, tc.hhmm)
			if err != nil {
				t.Fatalf("ParseCadence(%q, %q, %q, %q) = %v; an unused day must not be validated",
					tc.freq, tc.weekday, tc.monthday, tc.hhmm, err)
			}
			if got != tc.want {
				t.Fatalf("ParseCadence(%q, %q, %q, %q) = %+v, want %+v", tc.freq, tc.weekday, tc.monthday, tc.hhmm, got, tc.want)
			}
		})
	}

	// The day the frequency uses is still validated, whatever the other day holds.
	for _, tc := range []struct{ freq, weekday, monthday string }{
		{"weekly", "someday", "15"},
		{"monthly", "fri", "31"},
	} {
		if _, err := ParseCadence(tc.freq, tc.weekday, tc.monthday, "02:00"); err == nil {
			t.Errorf("ParseCadence(%q, %q, %q) accepted an invalid day the frequency uses", tc.freq, tc.weekday, tc.monthday)
		}
	}
}

func TestCadenceNext(t *testing.T) {
	loc := time.UTC
	// 2026-09-30 is a Wednesday.
	now := time.Date(2026, 9, 30, 11, 58, 32, 0, loc)
	tests := []struct {
		name string
		c    Cadence
		now  time.Time
		want time.Time
	}{
		{"daily later today", Cadence{Frequency: FrequencyDaily, Time: "14:00"}, now, time.Date(2026, 9, 30, 14, 0, 0, 0, loc)},
		{"daily tomorrow", Cadence{Frequency: FrequencyDaily, Time: "02:00"}, now, time.Date(2026, 10, 1, 2, 0, 0, 0, loc)},
		{"weekly next monday", Cadence{Frequency: FrequencyWeekly, Weekday: time.Monday, Time: "02:00"}, now, time.Date(2026, 10, 5, 2, 0, 0, 0, loc)},
		{"weekly same day later", Cadence{Frequency: FrequencyWeekly, Weekday: time.Wednesday, Time: "14:00"}, now, time.Date(2026, 9, 30, 14, 0, 0, 0, loc)},
		{"weekly same day passed", Cadence{Frequency: FrequencyWeekly, Weekday: time.Wednesday, Time: "02:00"}, now, time.Date(2026, 10, 7, 2, 0, 0, 0, loc)},
		{"weekly exactly now", Cadence{Frequency: FrequencyWeekly, Weekday: time.Wednesday, Time: "11:58"}, time.Date(2026, 9, 30, 11, 58, 0, 0, loc), time.Date(2026, 10, 7, 11, 58, 0, 0, loc)},
		{"monthly later this month", Cadence{Frequency: FrequencyMonthly, MonthDay: 15, Time: "02:00"}, time.Date(2026, 9, 10, 0, 0, 0, 0, loc), time.Date(2026, 9, 15, 2, 0, 0, 0, loc)},
		{"monthly next month", Cadence{Frequency: FrequencyMonthly, MonthDay: 15, Time: "02:00"}, now, time.Date(2026, 10, 15, 2, 0, 0, 0, loc)},
		{"monthly across the year", Cadence{Frequency: FrequencyMonthly, MonthDay: 1, Time: "02:00"}, time.Date(2026, 12, 20, 0, 0, 0, 0, loc), time.Date(2027, 1, 1, 2, 0, 0, 0, loc)},
		{"monthly 28 in february", Cadence{Frequency: FrequencyMonthly, MonthDay: 28, Time: "02:00"}, time.Date(2027, 1, 30, 0, 0, 0, 0, loc), time.Date(2027, 2, 28, 2, 0, 0, 0, loc)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.c.Next(tt.now)
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("Next(%s) = %s; want %s", tt.now, got, tt.want)
			}
		})
	}
}

// A weekly run keeps its wall-clock time across a DST change in the host's zone.
func TestCadenceNextAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// 2026-10-25 03:00 CEST -> 02:00 CET. Friday 23 October, weekly on Monday at 02:00.
	c := Cadence{Frequency: FrequencyWeekly, Weekday: time.Monday, Time: "02:00"}
	got, err := c.Next(time.Date(2026, 10, 23, 12, 0, 0, 0, loc))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 26, 2, 0, 0, 0, loc)
	if !got.Equal(want) || got.Hour() != 2 {
		t.Fatalf("Next across DST = %s; want %s", got, want)
	}
}

func TestCadenceNextRejectsInvalid(t *testing.T) {
	for _, c := range []Cadence{
		{Frequency: "hourly", Time: "02:00"},
		{Frequency: FrequencyDaily, Time: "2am"},
		{Frequency: FrequencyMonthly, MonthDay: 31, Time: "02:00"},
		{Frequency: FrequencyWeekly, Weekday: 9, Time: "02:00"},
	} {
		if _, err := c.Next(time.Now()); err == nil {
			t.Errorf("Next accepted %+v", c)
		}
	}
}

func TestCadenceSchedule(t *testing.T) {
	tests := []struct {
		c    Cadence
		want string
	}{
		{Cadence{Frequency: FrequencyDaily, Time: "02:00"}, "00 02 * * *"},
		{Cadence{Frequency: FrequencyWeekly, Weekday: time.Monday, Time: "03:30"}, "30 03 * * 1"},
		{Cadence{Frequency: FrequencyWeekly, Weekday: time.Sunday, Time: "03:30"}, "30 03 * * 0"},
		{Cadence{Frequency: FrequencyMonthly, MonthDay: 15, Time: "23:05"}, "05 23 15 * *"},
		{Cadence{Frequency: FrequencyMonthly, MonthDay: 29, Time: "23:05"}, ""},
		{Cadence{Frequency: FrequencyDaily, Time: "bad"}, ""},
	}
	for _, tt := range tests {
		if got := tt.c.Schedule(); got != tt.want {
			t.Errorf("Schedule(%+v) = %q; want %q", tt.c, got, tt.want)
		}
	}
}

func TestParseSchedule(t *testing.T) {
	ok := []struct {
		in   string
		want Cadence
	}{
		{"0 3 * * * /usr/local/bin/proxsave", Cadence{Frequency: FrequencyDaily, Time: "03:00"}},
		{"0 3 * * 1 /usr/local/bin/proxsave", Cadence{Frequency: FrequencyWeekly, Weekday: time.Monday, Time: "03:00"}},
		{"0 3 * * 7", Cadence{Frequency: FrequencyWeekly, Weekday: time.Sunday, Time: "03:00"}},
		{"0 3 * * 0", Cadence{Frequency: FrequencyWeekly, Weekday: time.Sunday, Time: "03:00"}},
		{"15 4 28 * *", Cadence{Frequency: FrequencyMonthly, MonthDay: 28, Time: "04:15"}},
		{"@daily /usr/local/bin/proxsave", Cadence{Frequency: FrequencyDaily, Time: "00:00"}},
		{"@midnight", Cadence{Frequency: FrequencyDaily, Time: "00:00"}},
		{"@weekly", Cadence{Frequency: FrequencyWeekly, Weekday: time.Sunday, Time: "00:00"}},
		{"@monthly", Cadence{Frequency: FrequencyMonthly, MonthDay: 1, Time: "00:00"}},
	}
	for _, tt := range ok {
		got, err := ParseSchedule(tt.in)
		if err != nil {
			t.Errorf("ParseSchedule(%q): %v", tt.in, err)
			continue
		}
		// Fields that do not apply to the frequency carry the defaults.
		want := tt.want
		if want.Weekday == 0 && want.Frequency != FrequencyWeekly {
			want.Weekday = DefaultWeekday
		}
		if want.MonthDay == 0 {
			want.MonthDay = DefaultMonthDay
		}
		if got != want {
			t.Errorf("ParseSchedule(%q) = %+v; want %+v", tt.in, got, want)
		}
	}

	bad := []struct {
		in     string
		reason string
	}{
		{"", ReasonNotFiveFields},
		{"0 3 * *", ReasonNotFiveFields},
		{"@hourly", ReasonUnsupportedShortcut},
		{"@reboot", ReasonUnsupportedShortcut},
		{"*/15 * * * *", ReasonMinuteNotLiteral},
		{"0 3,15 * * *", ReasonHourNotLiteral},
		{"0 3 * 1 *", ReasonMonthRestricted},
		{"0 3 31 * *", ReasonMonthDayOutOfRange},
		{"0 3 29 * *", ReasonMonthDayOutOfRange},
		{"0 3 1-5 * *", ReasonMonthDayNotLiteral},
		{"0 3 * * 1-5", ReasonWeekdayNotLiteral},
		{"0 3 * * mon", ReasonWeekdayNotLiteral},
		{"0 3 1 * 1", ReasonBothDayFields},
	}
	for _, tt := range bad {
		_, err := ParseSchedule(tt.in)
		var se *ScheduleError
		if !errors.As(err, &se) {
			t.Errorf("ParseSchedule(%q) error = %v; want a *ScheduleError", tt.in, err)
			continue
		}
		if se.Reason != tt.reason {
			t.Errorf("ParseSchedule(%q) reason = %q; want %q", tt.in, se.Reason, tt.reason)
		}
	}
}

func TestWaitLabel(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{59*time.Minute + 3*time.Second, "59m3s"},
		{14*time.Hour + time.Minute + 28*time.Second, "14h1m28s"},
		{23*time.Hour + 59*time.Minute + 59*time.Second, "23h59m59s"},
		{24 * time.Hour, "1d"},
		{48*time.Hour + 5*time.Minute + 3*time.Second, "2d 5m3s"},
		{110*time.Hour + time.Minute + 28*time.Second, "4d 14h1m28s"},
		{168 * time.Hour, "7d"},
		{671*time.Hour + 59*time.Minute + 59*time.Second, "27d 23h59m59s"},
		{1500 * time.Millisecond, "2s"},
		{-time.Second, "0s"},
	}
	for _, tt := range tests {
		if got := WaitLabel(tt.in); got != tt.want {
			t.Errorf("WaitLabel(%s) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func TestWeekdayNames(t *testing.T) {
	for d := time.Sunday; d <= time.Saturday; d++ {
		name := WeekdayName(d)
		back, err := ParseWeekday(name)
		if err != nil || back != d {
			t.Errorf("WeekdayName(%s) = %q, ParseWeekday = %s, %v", d, name, back, err)
		}
	}
	if WeekdayName(time.Monday) != "mon" {
		t.Fatalf("WeekdayName(Monday) = %q; want mon", WeekdayName(time.Monday))
	}
}
