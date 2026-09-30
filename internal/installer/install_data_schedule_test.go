package installer

import (
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/cron"
)

func TestApplyInstallDataWritesSchedule(t *testing.T) {
	data := &InstallWizardData{
		SchedulerMode:     "daemon",
		CronTime:          "03:30",
		ScheduleFrequency: "weekly",
		ScheduleWeekday:   "fri",
		ScheduleMonthDay:  "15",
	}
	out, err := ApplyInstallData("", data)
	if err != nil {
		t.Fatalf("ApplyInstallData: %v", err)
	}
	for _, want := range []string{"SCHEDULER_FREQUENCY=weekly", "SCHEDULER_WEEKDAY=fri", "SCHEDULER_MONTHDAY=15", "SCHEDULER_TIME=03:30"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	p := DeriveInstallWizardPrefill(out)
	if p.SchedulerFrequency != "weekly" || p.SchedulerWeekday != "fri" || p.SchedulerMonthDay != "15" {
		t.Fatalf("prefill = %q %q %q; want weekly fri 15", p.SchedulerFrequency, p.SchedulerWeekday, p.SchedulerMonthDay)
	}
}

// A front-end that did not ask leaves the stored schedule alone.
func TestApplyInstallDataKeepsStoredScheduleWhenNotAsked(t *testing.T) {
	base := "SCHEDULER_MODE=cron\nSCHEDULER_FREQUENCY=monthly\nSCHEDULER_MONTHDAY=20\nSCHEDULER_TIME=04:00\n"
	out, err := ApplyInstallData(base, &InstallWizardData{SchedulerMode: "cron"})
	if err != nil {
		t.Fatalf("ApplyInstallData: %v", err)
	}
	for _, want := range []string{"SCHEDULER_FREQUENCY=monthly", "SCHEDULER_MONTHDAY=20", "SCHEDULER_TIME=04:00"} {
		if !strings.Contains(out, want) {
			t.Errorf("stored value lost: missing %q", want)
		}
	}
}

func TestInstallWizardDataCadence(t *testing.T) {
	c, err := (&InstallWizardData{CronTime: "03:30", ScheduleFrequency: "monthly", ScheduleMonthDay: "15"}).Cadence()
	if err != nil {
		t.Fatalf("Cadence: %v", err)
	}
	if c != (cron.Cadence{Frequency: cron.FrequencyMonthly, Weekday: time.Monday, MonthDay: 15, Time: "03:30"}) {
		t.Fatalf("Cadence = %+v", c)
	}
	if c.Schedule() != "30 03 15 * *" {
		t.Fatalf("Schedule = %q", c.Schedule())
	}
}
