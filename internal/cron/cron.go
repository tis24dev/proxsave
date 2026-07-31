package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const DefaultTime = "02:00"

// NormalizeTime validates a cron time in HH:MM form and returns a normalized,
// zero-padded value. Empty input falls back to defaultValue.
func NormalizeTime(input string, defaultValue string) (string, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		value = strings.TrimSpace(defaultValue)
	}
	hour, minute, err := parseTime(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%02d:%02d", hour, minute), nil
}

// TimeToSchedule converts HH:MM into "MM HH * * *". Invalid input returns "".
func TimeToSchedule(cronTime string) string {
	hour, minute, err := parseTime(strings.TrimSpace(cronTime))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%02d %02d * * *", minute, hour)
}

// ScheduleToTime is the inverse of TimeToSchedule: it converts a crontab schedule
// into the daily HH:MM the daemon can express. It accepts ONLY an unambiguous
// single daily run - literal numeric minute and hour with day-of-month, month and
// day-of-week all "*" - plus the @daily/@midnight shortcuts, which are exactly
// 00:00. Steps ("*/15"), lists ("0,30"), ranges ("1-5"), names, "*" in the minute
// or hour field and every other @-shortcut return "": the daemon runs once a day,
// so a schedule it cannot express must fall back to DefaultTime instead of being
// silently rounded to a time the operator never chose. Trailing fields (the cron
// command) are ignored, so a whole crontab line may be passed.
func ScheduleToTime(schedule string) string {
	fields := strings.Fields(strings.TrimSpace(schedule))
	if len(fields) == 0 {
		return ""
	}
	if strings.HasPrefix(fields[0], "@") {
		switch strings.ToLower(fields[0]) {
		case "@daily", "@midnight":
			return "00:00"
		}
		return ""
	}
	if len(fields) < 5 {
		return ""
	}
	// Day-of-month, month and day-of-week must all be unrestricted: anything else
	// is not a plain daily run.
	for _, field := range fields[2:5] {
		if field != "*" {
			return ""
		}
	}
	minute, ok := cronLiteralField(fields[0], 59)
	if !ok {
		return ""
	}
	hour, ok := cronLiteralField(fields[1], 23)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

// cronLiteralField parses ONE literal cron field (digits only: no "*", step, list,
// range or sign) into an int within [0,max].
func cronLiteralField(field string, max int) (int, bool) {
	if field == "" {
		return 0, false
	}
	for _, r := range field {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	value, err := strconv.Atoi(field)
	if err != nil || value < 0 || value > max {
		return 0, false
	}
	return value, true
}

// NextDaily returns the next occurrence of the daily HH:MM time strictly after
// now (today if still ahead, otherwise tomorrow), in now's location. Used by the
// resident daemon scheduler. Invalid HH:MM returns an error.
func NextDaily(now time.Time, hhmm string) (time.Time, error) {
	hour, minute, err := parseTime(strings.TrimSpace(hhmm))
	if err != nil {
		return time.Time{}, err
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next, nil
}

func parseTime(value string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("cron time must be in HH:MM format")
	}

	hour, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("cron hour must be between 00 and 23")
	}

	minute, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("cron minute must be between 00 and 59")
	}

	return hour, minute, nil
}
