package cron

import (
	"fmt"
	"strconv"
	"strings"
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
