package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Frequency is how often the backup runs: SCHEDULER_FREQUENCY.
type Frequency string

const (
	FrequencyDaily   Frequency = "daily"
	FrequencyWeekly  Frequency = "weekly"
	FrequencyMonthly Frequency = "monthly"
)

const (
	// DefaultWeekday is SCHEDULER_WEEKDAY when it is absent.
	DefaultWeekday = time.Monday
	// DefaultMonthDay is SCHEDULER_MONTHDAY when it is absent.
	DefaultMonthDay = 1
	// MaxMonthDay caps the monthly day at 28 so that no month skips the backup, and so the
	// crontab line and the daemon describe the same run: Debian's cron has no "last day".
	MaxMonthDay = 28
)

// Cadence is when the backup runs: SCHEDULER_FREQUENCY, SCHEDULER_WEEKDAY, SCHEDULER_MONTHDAY
// and SCHEDULER_TIME together. Weekday matters only for weekly and MonthDay only for monthly;
// both always carry a valid value (their default when unused), so switching frequency keeps
// the other day.
type Cadence struct {
	Frequency Frequency
	Weekday   time.Weekday
	MonthDay  int
	Time      string // HH:MM, normalized
}

var weekdayNames = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// WeekdayName is the SCHEDULER_WEEKDAY spelling of d: mon, tue, ... sun.
func WeekdayName(d time.Weekday) string {
	if d < time.Sunday || d > time.Saturday {
		return ""
	}
	return weekdayNames[d]
}

// ParseFrequency reads SCHEDULER_FREQUENCY; empty means daily.
func ParseFrequency(s string) (Frequency, error) {
	switch v := Frequency(strings.ToLower(strings.TrimSpace(s))); v {
	case "":
		return FrequencyDaily, nil
	case FrequencyDaily, FrequencyWeekly, FrequencyMonthly:
		return v, nil
	default:
		return "", fmt.Errorf("frequency must be daily, weekly, or monthly")
	}
}

// ParseWeekday reads SCHEDULER_WEEKDAY (mon ... sun); empty means DefaultWeekday.
func ParseWeekday(s string) (time.Weekday, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "" {
		return DefaultWeekday, nil
	}
	for i, name := range weekdayNames {
		if v == name {
			return time.Weekday(i), nil
		}
	}
	return 0, fmt.Errorf("weekday must be one of mon, tue, wed, thu, fri, sat, sun")
}

// ParseMonthDay reads SCHEDULER_MONTHDAY (1-28); empty means DefaultMonthDay.
func ParseMonthDay(s string) (int, error) {
	v := strings.TrimSpace(s)
	if v == "" {
		return DefaultMonthDay, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > MaxMonthDay {
		return 0, fmt.Errorf("day of month must be between 1 and %d", MaxMonthDay)
	}
	return n, nil
}

// ParseCadence reads the four SCHEDULER_* values. Empty values take their defaults, so a
// config that predates SCHEDULER_FREQUENCY stays daily at SCHEDULER_TIME. Only the day the
// frequency uses is validated: the weekday for weekly, the day of the month for monthly,
// neither for daily. A day the frequency does not use cannot move the backup, so an invalid
// one is ignored and the Cadence carries its default (DefaultWeekday, DefaultMonthDay).
func ParseCadence(frequency, weekday, monthDay, hhmm string) (Cadence, error) {
	freq, err := ParseFrequency(frequency)
	if err != nil {
		return Cadence{}, err
	}
	day, err := ParseWeekday(weekday)
	if err != nil {
		if freq == FrequencyWeekly {
			return Cadence{}, err
		}
		day = DefaultWeekday
	}
	mday, err := ParseMonthDay(monthDay)
	if err != nil {
		if freq == FrequencyMonthly {
			return Cadence{}, err
		}
		mday = DefaultMonthDay
	}
	norm, err := NormalizeTime(hhmm, DefaultTime)
	if err != nil {
		return Cadence{}, err
	}
	return Cadence{Frequency: freq, Weekday: day, MonthDay: mday, Time: norm}, nil
}

func (c Cadence) validate() (hour, minute int, err error) {
	hour, minute, err = parseTime(strings.TrimSpace(c.Time))
	if err != nil {
		return 0, 0, err
	}
	switch c.Frequency {
	case FrequencyDaily:
	case FrequencyWeekly:
		if c.Weekday < time.Sunday || c.Weekday > time.Saturday {
			return 0, 0, fmt.Errorf("invalid weekday %d", c.Weekday)
		}
	case FrequencyMonthly:
		if c.MonthDay < 1 || c.MonthDay > MaxMonthDay {
			return 0, 0, fmt.Errorf("day of month must be between 1 and %d", MaxMonthDay)
		}
	default:
		return 0, 0, fmt.Errorf("frequency must be daily, weekly, or monthly")
	}
	return hour, minute, nil
}

// Next returns the next run strictly after now, in now's location. Days are added with
// AddDate on the calendar date, so the run keeps its wall-clock time across a DST change.
func (c Cadence) Next(now time.Time) (time.Time, error) {
	hour, minute, err := c.validate()
	if err != nil {
		return time.Time{}, err
	}
	switch c.Frequency {
	case FrequencyWeekly:
		ahead := (int(c.Weekday) - int(now.Weekday()) + 7) % 7
		next := time.Date(now.Year(), now.Month(), now.Day()+ahead, hour, minute, 0, 0, now.Location())
		if !next.After(now) {
			next = time.Date(now.Year(), now.Month(), now.Day()+ahead+7, hour, minute, 0, 0, now.Location())
		}
		return next, nil
	case FrequencyMonthly:
		next := time.Date(now.Year(), now.Month(), c.MonthDay, hour, minute, 0, 0, now.Location())
		if !next.After(now) {
			next = time.Date(now.Year(), now.Month()+1, c.MonthDay, hour, minute, 0, 0, now.Location())
		}
		return next, nil
	default:
		return NextDaily(now, c.Time)
	}
}

// Schedule returns the crontab time fields for c ("MM HH * * *", "MM HH * * D" or
// "MM HH N * *"), or "" when c is invalid.
func (c Cadence) Schedule() string {
	hour, minute, err := c.validate()
	if err != nil {
		return ""
	}
	switch c.Frequency {
	case FrequencyWeekly:
		return fmt.Sprintf("%02d %02d * * %d", minute, hour, int(c.Weekday))
	case FrequencyMonthly:
		return fmt.Sprintf("%02d %02d %d * *", minute, hour, c.MonthDay)
	default:
		return fmt.Sprintf("%02d %02d * * *", minute, hour)
	}
}

// Reasons a crontab schedule is not a cadence. They are machine codes for DEBUG lines.
const (
	ReasonNotFiveFields       = "not_five_fields"
	ReasonUnsupportedShortcut = "unsupported_shortcut"
	ReasonMinuteNotLiteral    = "minute_not_literal"
	ReasonHourNotLiteral      = "hour_not_literal"
	ReasonMonthRestricted     = "month_restricted"
	ReasonMonthDayNotLiteral  = "monthday_not_literal"
	ReasonMonthDayOutOfRange  = "monthday_out_of_range"
	ReasonWeekdayNotLiteral   = "weekday_not_literal"
	ReasonBothDayFields       = "both_day_fields_restricted"
)

// ScheduleError says why a crontab schedule cannot be expressed as a Cadence.
type ScheduleError struct {
	Reason string
}

func (e *ScheduleError) Error() string { return "crontab schedule not supported: " + e.Reason }

// ParseSchedule is the inverse of Cadence.Schedule: it reads a crontab schedule (a whole
// crontab line may be passed; trailing fields are ignored) into the cadence it describes.
// It accepts only what a cadence expresses exactly: literal minute and hour, month "*",
// and either both day fields "*" (daily), one literal day of week 0-7 (weekly, 0 and 7 are
// Sunday) or one literal day of month 1-28 (monthly), plus @daily, @midnight, @weekly and
// @monthly. Anything else returns a *ScheduleError, because rounding a schedule the operator
// wrote to one they did not choose would move the backup silently.
func ParseSchedule(schedule string) (Cadence, error) {
	fields := strings.Fields(strings.TrimSpace(schedule))
	if len(fields) == 0 {
		return Cadence{}, &ScheduleError{Reason: ReasonNotFiveFields}
	}
	base := Cadence{Weekday: DefaultWeekday, MonthDay: DefaultMonthDay, Time: "00:00"}
	if strings.HasPrefix(fields[0], "@") {
		switch strings.ToLower(fields[0]) {
		case "@daily", "@midnight":
			base.Frequency = FrequencyDaily
		case "@weekly":
			base.Frequency, base.Weekday = FrequencyWeekly, time.Sunday
		case "@monthly":
			base.Frequency, base.MonthDay = FrequencyMonthly, 1
		default:
			return Cadence{}, &ScheduleError{Reason: ReasonUnsupportedShortcut}
		}
		return base, nil
	}
	if len(fields) < 5 {
		return Cadence{}, &ScheduleError{Reason: ReasonNotFiveFields}
	}
	minute, ok := cronLiteralField(fields[0], 59)
	if !ok {
		return Cadence{}, &ScheduleError{Reason: ReasonMinuteNotLiteral}
	}
	hour, ok := cronLiteralField(fields[1], 23)
	if !ok {
		return Cadence{}, &ScheduleError{Reason: ReasonHourNotLiteral}
	}
	if fields[3] != "*" {
		return Cadence{}, &ScheduleError{Reason: ReasonMonthRestricted}
	}
	base.Time = fmt.Sprintf("%02d:%02d", hour, minute)
	dom, dow := fields[2], fields[4]
	switch {
	case dom == "*" && dow == "*":
		base.Frequency = FrequencyDaily
	case dom == "*":
		d, ok := cronLiteralField(dow, 7)
		if !ok {
			return Cadence{}, &ScheduleError{Reason: ReasonWeekdayNotLiteral}
		}
		base.Frequency, base.Weekday = FrequencyWeekly, time.Weekday(d%7)
	case dow == "*":
		d, ok := cronLiteralField(dom, 31)
		if !ok || d < 1 {
			return Cadence{}, &ScheduleError{Reason: ReasonMonthDayNotLiteral}
		}
		if d > MaxMonthDay {
			return Cadence{}, &ScheduleError{Reason: ReasonMonthDayOutOfRange}
		}
		base.Frequency, base.MonthDay = FrequencyMonthly, d
	default:
		return Cadence{}, &ScheduleError{Reason: ReasonBothDayFields}
	}
	return base, nil
}

// WaitLabel formats the wait until the next backup. Under 24 hours it is Go's duration
// format, rounded to the second; from 24 hours on it is whole days, a space, then the rest in
// the same format, or the days alone when nothing is left ("4d 14h1m28s", "7d").
func WaitLabel(d time.Duration) string {
	d = max(d.Round(time.Second), 0)
	const day = 24 * time.Hour
	if d < day {
		return d.String()
	}
	days, rest := d/day, d%day
	if rest == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %s", days, rest)
}
