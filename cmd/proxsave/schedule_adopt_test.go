// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	cronutil "github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/installer"
)

// seedLines renders a seed's block as "LEVEL text" strings, the way it reaches the log.
func seedLines(s schedulerTimeSeed) []string {
	var out []string
	for _, l := range s.Block.lines() {
		out = append(out, scheduleLevelName(l.Level)+" "+l.Text)
	}
	return out
}

func scheduleLevelName(level scheduleLineLevel) string {
	switch level {
	case scheduleLineDebug:
		return "DEBUG"
	case scheduleLineWarning:
		return "WARNING"
	default:
		return "INFO"
	}
}

func assertLines(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("block mismatch\n got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// logLineLevel returns the level column of the first log line that contains msg, or "".
func logLineLevel(out, msg string) string {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, msg) {
			continue
		}
		for _, level := range []string{"DEBUG", "WARNING", "INFO", "ERROR"} {
			if strings.Contains(line, "] "+level+" ") {
				return level
			}
		}
	}
	return ""
}

// assertInOrder checks every part appears in out, each after the previous one.
func assertInOrder(t *testing.T, out string, parts ...string) {
	t.Helper()
	at := 0
	for _, p := range parts {
		i := strings.Index(out[at:], p)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", p, out)
		}
		at += i + len(p)
	}
}

func writeScheduleCfg(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSchedulerCadenceFromCronLines(t *testing.T) {
	const bin = " /usr/local/bin/proxsave --backup"
	for _, tc := range []struct {
		name       string
		lines      []string
		wantFound  bool
		wantReason string
		wantSched  string // Cadence.Schedule() when adoptable
		wantLine   string // the line the verdict names
	}{
		{name: "no lines", lines: nil},
		{name: "commented line", lines: []string{"#0 21 * * *" + bin}},
		{name: "proxsave only as an argument", lines: []string{"0 21 * * * /bin/cp /usr/local/bin/proxsave /tmp/"}},
		{name: "daily among unrelated jobs", lines: []string{"MAILTO=root", "30 4 * * * /usr/bin/other", "0 21 * * *" + bin},
			wantFound: true, wantSched: "00 21 * * *", wantLine: "0 21 * * *" + bin},
		{name: "weekly", lines: []string{"0 3 * * 1" + bin}, wantFound: true, wantSched: "00 03 * * 1", wantLine: "0 3 * * 1" + bin},
		{name: "monthly", lines: []string{"30 4 15 * *" + bin}, wantFound: true, wantSched: "30 04 15 * *", wantLine: "30 4 15 * *" + bin},
		{name: "weekly shortcut", lines: []string{"@weekly" + bin}, wantFound: true, wantSched: "00 00 * * 0", wantLine: "@weekly" + bin},
		{name: "legacy entrypoint", lines: []string{"0 21 * * * /usr/local/bin/proxmox-backup --backup"},
			wantFound: true, wantSched: "00 21 * * *", wantLine: "0 21 * * * /usr/local/bin/proxmox-backup --backup"},
		{name: "two lines, same cadence spelled twice", lines: []string{"0 3 * * 0" + bin, "00 03 * * 7" + bin},
			wantFound: true, wantSched: "00 03 * * 0", wantLine: "0 3 * * 0" + bin},
		{name: "two lines at different times", lines: []string{"0 21 * * *" + bin, "0 6 * * *" + bin},
			wantFound: true, wantReason: reasonLinesDisagree, wantLine: "0 6 * * *" + bin},
		{name: "a step", lines: []string{"*/15 * * * *" + bin},
			wantFound: true, wantReason: cronutil.ReasonMinuteNotLiteral, wantLine: "*/15 * * * *" + bin},
		{name: "day 31", lines: []string{"0 3 31 * *" + bin},
			wantFound: true, wantReason: cronutil.ReasonMonthDayOutOfRange, wantLine: "0 3 31 * *" + bin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := schedulerCadenceFromCronLines(tc.lines)
			if got.Found != tc.wantFound || got.Reason != tc.wantReason || got.Line != tc.wantLine {
				t.Fatalf("got found=%v reason=%q line=%q, want found=%v reason=%q line=%q",
					got.Found, got.Reason, got.Line, tc.wantFound, tc.wantReason, tc.wantLine)
			}
			if tc.wantSched != "" && got.Cadence.Schedule() != tc.wantSched {
				t.Fatalf("schedule = %q, want %q", got.Cadence.Schedule(), tc.wantSched)
			}
		})
	}
}

// Point 15: SCHEDULER_TIME and SCHEDULER_FREQUENCY both present is an explicit schedule and
// nothing is adopted; SCHEDULER_TIME alone still leaves the frequency unstated, so a weekly or
// monthly line is adopted whole, time included; with no SCHEDULER_TIME any line is adopted.
func TestDeriveScheduleGate(t *testing.T) {
	const bin = " /usr/local/bin/proxsave --backup"
	for _, tc := range []struct {
		name    string
		stored  string
		line    string
		want    string // Cadence.Schedule() adopted, "" for nothing
		silence bool   // nothing at all to say
	}{
		{name: "time and frequency present: explicit", stored: "SCHEDULER_TIME=02:00\nSCHEDULER_FREQUENCY=daily\n", line: "0 3 * * 1" + bin, silence: true},
		{name: "time present, weekly line: whole cadence adopted", stored: "SCHEDULER_TIME=02:00\n", line: "0 3 * * 1" + bin, want: "00 03 * * 1"},
		{name: "time present, monthly line: whole cadence adopted", stored: "SCHEDULER_TIME=02:00\n", line: "0 3 15 * *" + bin, want: "00 03 15 * *"},
		{name: "time present, daily line: the explicit time wins", stored: "SCHEDULER_TIME=02:00\n", line: "0 21 * * *" + bin, silence: true},
		{name: "time present, a step: silent as before", stored: "SCHEDULER_TIME=02:00\n", line: "*/15 * * * *" + bin, silence: true},
		{name: "nothing stored, daily line", stored: "", line: "0 21 * * *" + bin, want: "00 21 * * *"},
		{name: "nothing stored, weekly line", stored: "", line: "0 21 * * 5" + bin, want: "00 21 * * 5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := writeScheduleCfg(t, "BACKUP_PATH=/data\n"+tc.stored)
			stubCrontabLines(t, []string{tc.line}, nil)

			seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
			if tc.silence {
				if !seed.empty() {
					t.Fatalf("want a silent zero seed, got %+v", seed)
				}
				return
			}
			if got := seed.Cadence.Schedule(); got != tc.want {
				t.Fatalf("adopted %q, want %q", got, tc.want)
			}
			if seed.Item != "" {
				t.Fatalf("an adoption is no warning, got item %q", seed.Item)
			}
		})
	}
}

func TestAdoptionBlocks(t *testing.T) {
	const bin = " /usr/local/bin/proxsave --backup"

	t.Run("weekly adopted over an explicit time", func(t *testing.T) {
		cfg := writeScheduleCfg(t, "SCHEDULER_TIME=02:00\n")
		stubCrontabLines(t, []string{"0 3 * * 1" + bin}, nil)
		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		assertLines(t, seedLines(seed), []string{
			"INFO Adopting backup schedule from cron entry...",
			`DEBUG schedule adopt: cron line="0 3 * * 1` + bin + `" parsed frequency=weekly weekday=mon time=03:00`,
			"DEBUG schedule adopt: SCHEDULER_TIME=02:00 SCHEDULER_FREQUENCY=absent source=" + cfg,
			"INFO   Frequency: weekly",
			"INFO   Weekday: Monday",
			"INFO   Time: 03:00",
			"INFO ✓ Backup schedule: adopted from cron entry",
		})
	})

	t.Run("monthly names the day, daily names none", func(t *testing.T) {
		cfg := writeScheduleCfg(t, "")
		stubCrontabLines(t, []string{"0 3 15 * *" + bin}, nil)
		got := strings.Join(seedLines(deriveSchedulerTimeFromCrontab(context.Background(), cfg)), "\n")
		assertInOrder(t, got, "INFO   Frequency: monthly", "INFO   Day of month: 15", "INFO   Time: 03:00")

		stubCrontabLines(t, []string{"0 21 * * *" + bin}, nil)
		got = strings.Join(seedLines(deriveSchedulerTimeFromCrontab(context.Background(), cfg)), "\n")
		if strings.Contains(got, "Weekday") || strings.Contains(got, "Day of month") {
			t.Fatalf("a daily adoption names no day:\n%s", got)
		}
	})

	t.Run("day 31 with no stored time", func(t *testing.T) {
		cfg := writeScheduleCfg(t, "")
		stubCrontabLines(t, []string{"0 3 31 * *" + bin}, nil)
		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		if seed.adopted() {
			t.Fatalf("day 31 cannot be adopted, got %+v", seed.Cadence)
		}
		assertLines(t, seedLines(seed), []string{
			"INFO Adopting backup schedule from cron entry...",
			`DEBUG schedule adopt: cron line="0 3 31 * *` + bin + `" not adoptable reason=monthday_out_of_range`,
			"DEBUG schedule adopt: kept frequency=daily time=02:00 source=" + cfg,
			"INFO   Frequency: daily",
			"INFO   Time: 02:00",
			"INFO Day 31 is outside 1-28",
			`WARNING ⚠ Backup schedule: cron entry "0 3 31 * *" not adopted`,
		})
		if want := `⚠ Backup schedule: cron entry "0 3 31 * *" not adopted, day 31 is outside 1-28, default daily at 02:00`; seed.Item != want {
			t.Fatalf("item = %q, want %q", seed.Item, want)
		}
	})

	// "default" only when the time in effect really is the 02:00 default.
	t.Run("day 30 with a stored time", func(t *testing.T) {
		cfg := writeScheduleCfg(t, "SCHEDULER_TIME=04:30\n")
		stubCrontabLines(t, []string{"0 3 30 * *" + bin}, nil)
		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		got := strings.Join(seedLines(seed), "\n")
		assertInOrder(t, got, "INFO   Time: 04:30", "INFO Day 30 is outside 1-28", `WARNING ⚠ Backup schedule: cron entry "0 3 30 * *" not adopted`)
		if want := `⚠ Backup schedule: cron entry "0 3 30 * *" not adopted, day 30 is outside 1-28, daily at 04:30`; seed.Item != want {
			t.Fatalf("item = %q, want %q", seed.Item, want)
		}
	})

	// No approved text for these yet: the note of before stays, word for word.
	t.Run("a step keeps the old note", func(t *testing.T) {
		cfg := writeScheduleCfg(t, "")
		stubCrontabLines(t, []string{"*/15 * * * *" + bin}, nil)
		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		const note = "The existing proxsave cron entry is not a single daily time; SCHEDULER_TIME stays at the 02:00 default - set it in backup.env if the backup must run at another time."
		assertLines(t, seedLines(seed), []string{
			`DEBUG schedule adopt: cron line="*/15 * * * *` + bin + `" not adoptable reason=minute_not_literal`,
			"INFO " + note,
		})
		if seed.Item != note {
			t.Fatalf("item = %q, want the old note", seed.Item)
		}
	})

	t.Run("an /etc line", func(t *testing.T) {
		cronD := filepath.Join(t.TempDir(), "cron.d")
		if err := os.MkdirAll(cronD, 0o755); err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(cronD, "proxsave")
		const etcLine = "0 3 * * * root /usr/local/bin/proxsave --backup"
		if err := os.WriteFile(src, []byte(etcLine+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := writeScheduleCfg(t, "")
		stubCrontabLines(t, nil, nil)
		systemCronPaths = []string{filepath.Join(t.TempDir(), "absent-crontab"), cronD}

		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		assertLines(t, seedLines(seed), []string{
			"INFO Adopting backup schedule from cron entry...",
			"DEBUG schedule adopt: root crontab has no proxsave line",
			"DEBUG schedule adopt: " + src + ` line="` + etcLine + `" parsed frequency=daily time=03:00, not adopted reason=file_not_managed`,
			"INFO   Frequency: daily",
			"INFO   Time: 02:00",
			"INFO   Kept: " + src + ", daily at 03:00",
			"INFO Two backups a day: " + src + " is not edited by ProxSave",
			"WARNING ⚠ Backup schedule: cron entry in " + src + " not adopted",
		})
		if want := "⚠ Backup schedule: cron entry in " + src + " not adopted, two backups a day, default daily at 02:00"; seed.Item != want {
			t.Fatalf("item = %q, want %q", seed.Item, want)
		}
	})

	t.Run("an indirect wrapper", func(t *testing.T) {
		wrapper := absentWrapper(t)
		line := "0 3 * * * " + wrapper
		cfg := writeScheduleCfg(t, "")
		stubCrontabLines(t, []string{line}, nil)

		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		assertLines(t, seedLines(seed), []string{
			"INFO Adopting backup schedule from cron entry...",
			"DEBUG schedule adopt: root crontab has no proxsave line",
			"DEBUG schedule adopt: indirect command=" + wrapper + ` line="` + line + `" parsed frequency=daily time=03:00, not adopted reason=wrapper_not_interpreted`,
			"INFO   Frequency: daily",
			"INFO   Time: 02:00",
			"INFO   Kept: " + wrapper + ", daily at 03:00",
			"INFO Two backups a day: " + wrapper + " appears to run ProxSave",
			"WARNING ⚠ Backup schedule: cron entry for " + wrapper + " not adopted",
		})
		if want := "⚠ Backup schedule: cron entry for " + wrapper + " not adopted, two backups a day, default daily at 02:00"; seed.Item != want {
			t.Fatalf("item = %q, want %q", seed.Item, want)
		}
	})
}

// The seed writes all four variables, so the file states the whole schedule the cron line had.
func TestSeedWritesAllFourVariables(t *testing.T) {
	cfg := writeScheduleCfg(t, "BACKUP_PATH=/data\nSCHEDULER_TIME=02:00\n")
	stubCrontabLines(t, []string{"0 3 * * 1 /usr/local/bin/proxsave --backup"}, nil)

	seed := seedSchedulerTimeFromCrontab(context.Background(), cfg)
	if !seed.adopted() {
		t.Fatalf("expected an adoption, got %+v", seed)
	}
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SCHEDULER_FREQUENCY=weekly", "SCHEDULER_WEEKDAY=mon", "SCHEDULER_MONTHDAY=1", "SCHEDULER_TIME=03:00", "BACKUP_PATH=/data"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%q missing from:\n%s", want, data)
		}
	}
	got := strings.Join(seedLines(seed), "\n")
	assertInOrder(t, got, "DEBUG schedule adopt: wrote ", "file="+cfg, "INFO   Frequency: weekly", "✓ Backup schedule: adopted from cron entry")

	// And a second call finds both variables present: explicit, nothing to do.
	if again := seedSchedulerTimeFromCrontab(context.Background(), cfg); !again.empty() {
		t.Fatalf("second call must be a no-op, got %+v", again)
	}
}

func TestApplyConfigUpgradeRoutesTheScheduleBlock(t *testing.T) {
	t.Run("an adoption travels as notes, not as a warning", func(t *testing.T) {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "backup.env")
		if err := os.WriteFile(cfg, []byte("BACKUP_PATH="+dir+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stubCrontabLines(t, []string{"0 3 * * 1 /usr/local/bin/proxsave --backup"}, nil)

		result, err := applyConfigUpgrade(context.Background(), cfg, dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range result.Warnings {
			if strings.Contains(w, "Backup schedule") {
				t.Fatalf("an adoption is not a warning: %q", w)
			}
		}
		var infos []string
		sawDebug := false
		for _, n := range result.Notes {
			switch n.Level {
			case config.UpgradeNoteInfo:
				infos = append(infos, n.Text)
			case config.UpgradeNoteDebug:
				sawDebug = true
			default:
				t.Fatalf("unexpected note level %q", n.Level)
			}
		}
		if !sawDebug {
			t.Error("the evidence must travel with the block")
		}
		assertLines(t, infos, []string{
			"Adopting backup schedule from cron entry...",
			"  Frequency: weekly",
			"  Weekday: Monday",
			"  Time: 03:00",
			"✓ Backup schedule: adopted from cron entry",
		})
		data, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "SCHEDULER_FREQUENCY=weekly") || !strings.Contains(string(data), "SCHEDULER_TIME=03:00") {
			t.Fatalf("merged config lost the adopted cadence:\n%s", data)
		}
	})

	t.Run("an unadoptable line is one warning item and debug notes", func(t *testing.T) {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "backup.env")
		if err := os.WriteFile(cfg, []byte("BACKUP_PATH="+dir+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stubCrontabLines(t, []string{"0 3 31 * * /usr/local/bin/proxsave --backup"}, nil)

		result, err := applyConfigUpgrade(context.Background(), cfg, dir)
		if err != nil {
			t.Fatal(err)
		}
		want := `⚠ Backup schedule: cron entry "0 3 31 * *" not adopted, day 31 is outside 1-28, default daily at 02:00`
		if strings.Join(result.Warnings, "\n") != want {
			t.Fatalf("warnings = %q, want exactly %q", result.Warnings, want)
		}
		if len(result.Notes) == 0 {
			t.Fatal("the evidence must travel as debug notes")
		}
		for _, n := range result.Notes {
			if n.Level != config.UpgradeNoteDebug {
				t.Fatalf("only debug notes may accompany a warning item, got %+v", n)
			}
		}
	})
}

func TestLogConfigUpgradeNotesKeepsLevels(t *testing.T) {
	bootstrap, buf := captureBootstrapLog(t)
	logConfigUpgradeNotes(bootstrap, []config.UpgradeNote{
		{Level: config.UpgradeNoteInfo, Text: "Adopting backup schedule from cron entry..."},
		{Level: config.UpgradeNoteDebug, Text: "schedule adopt: evidence"},
		{Level: config.UpgradeNoteInfo, Text: "✓ Backup schedule: adopted from cron entry"},
	})
	out := buf.String()
	assertInOrder(t, out, "Adopting backup schedule from cron entry...", "schedule adopt: evidence", "✓ Backup schedule: adopted from cron entry")
	if got := logLineLevel(out, "schedule adopt: evidence"); got != "DEBUG" {
		t.Errorf("evidence level = %q, want DEBUG", got)
	}
	if got := logLineLevel(out, "✓ Backup schedule"); got != "INFO" {
		t.Errorf("outcome level = %q, want INFO", got)
	}
}

// The install front-ends render the block through the bootstrap logger, levels intact.
func TestAdoptCronRunTimeIntoBaseLogsTheBlock(t *testing.T) {
	cfg := writeScheduleCfg(t, "SCHEDULER_MODE=cron\nSCHEDULER_TIME=02:00\n")
	stubCrontabLines(t, []string{"0 3 * * 1 /usr/local/bin/proxsave --backup"}, nil)
	decision, err := installer.ResolveExistingConfigDecision(installer.ExistingConfigEdit, cfg)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, buf := captureBootstrapLog(t)

	base := adoptCronRunTimeIntoBase(context.Background(), decision, cfg, bootstrap)

	for _, want := range []string{"SCHEDULER_FREQUENCY=weekly", "SCHEDULER_WEEKDAY=mon", "SCHEDULER_TIME=03:00"} {
		if !strings.Contains(base, want) {
			t.Errorf("%q missing from the wizard base:\n%s", want, base)
		}
	}
	out := buf.String()
	assertInOrder(t, out, "Adopting backup schedule from cron entry...", "schedule adopt: cron line=", "  Frequency: weekly", "  Weekday: Monday", "  Time: 03:00", "✓ Backup schedule: adopted from cron entry")
	if got := logLineLevel(out, "schedule adopt: cron line="); got != "DEBUG" {
		t.Errorf("evidence level = %q, want DEBUG", got)
	}
}

// The label a detail line and an upgrade item use for a cadence.
func TestCadenceLabel(t *testing.T) {
	for _, tc := range []struct {
		c    cronutil.Cadence
		want string
	}{
		{cronutil.Cadence{Frequency: cronutil.FrequencyDaily, Time: "03:00"}, "daily at 03:00"},
		{cronutil.Cadence{Frequency: cronutil.FrequencyWeekly, Weekday: time.Monday, Time: "03:00"}, "weekly, Monday at 03:00"},
		{cronutil.Cadence{Frequency: cronutil.FrequencyMonthly, MonthDay: 15, Time: "03:00"}, "monthly, day 15 at 03:00"},
	} {
		if got := cadenceLabel(tc.c); got != tc.want {
			t.Errorf("cadenceLabel(%+v) = %q, want %q", tc.c, got, tc.want)
		}
	}
}
