// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"go/ast"
	gotypes "go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
)

// The maintainer's decisions on the schedule adoption and the cron removal (todo A1-A19,
// combinations 4 and 6, D6), each pinned by the exact lines the operator gets.

const adoptBin = " /usr/local/bin/proxsave --backup"

// logBlock turns captured log output into "LEVEL message" lines, the shape seedLines renders,
// so a logged block can be compared line by line.
func logBlock(out string) []string {
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		i := strings.Index(l, "] ")
		if i < 0 {
			continue
		}
		rest := l[i+2:]
		if len(rest) < 9 {
			lines = append(lines, strings.TrimSpace(rest))
			continue
		}
		lines = append(lines, strings.TrimSpace(rest[:8])+" "+rest[9:])
	}
	return lines
}

// blockFrom returns the lines of got from the first one equal to first through the next one
// equal to last, both included, or nil.
func blockFrom(got []string, first, last string) []string {
	for i, l := range got {
		if l != first {
			continue
		}
		for j := i; j < len(got); j++ {
			if got[j] == last {
				return got[i : j+1]
			}
		}
	}
	return nil
}

// noteLines renders UpgradeResult.Notes as "LEVEL text" lines.
func noteLines(notes []config.UpgradeNote) []string {
	var out []string
	for _, n := range notes {
		level := "INFO"
		if n.Level == config.UpgradeNoteDebug {
			level = "DEBUG"
		}
		out = append(out, level+" "+n.Text)
	}
	return out
}

func withCfg(lines []string, cfg string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.ReplaceAll(l, "CFG", cfg)
	}
	return out
}

func writeCronFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// etcTree points systemCronPaths at a temp /etc/crontab file and /etc/cron.d dir, in that
// order. Call it after stubCrontabLines, which pins the paths at an empty tree.
func etcTree(t *testing.T) (crontab, cronD string) {
	t.Helper()
	orig := systemCronPaths
	t.Cleanup(func() { systemCronPaths = orig })
	root := t.TempDir()
	crontab, cronD = filepath.Join(root, "crontab"), filepath.Join(root, "cron.d")
	if err := os.MkdirAll(cronD, 0o755); err != nil {
		t.Fatal(err)
	}
	systemCronPaths = []string{crontab, cronD}
	return crontab, cronD
}

func upgradeCfg(t *testing.T, extra string) (dir, cfg string) {
	t.Helper()
	dir = t.TempDir()
	cfg = filepath.Join(dir, "backup.env")
	if err := os.WriteFile(cfg, []byte("BACKUP_PATH="+dir+"\n"+extra), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, cfg
}

// A2: three or more lines that disagree are named by their first two DIFFERENT schedules.
func TestSchedulerCadenceFromCronLinesNamesTheFirstTwoDifferentSchedules(t *testing.T) {
	r := schedulerCadenceFromCronLines([]string{"0 3 * * 0" + adoptBin, "00 03 * * 7" + adoptBin, "0 4 * * 0" + adoptBin, "0 5 * * 0" + adoptBin})
	if r.Reason != reasonLinesDisagree || r.First != "0 3 * * 0"+adoptBin || r.Line != "0 4 * * 0"+adoptBin {
		t.Fatalf("got reason=%q first=%q line=%q", r.Reason, r.First, r.Line)
	}
}

// A2, install CLI: a line that is not daily, weekly or monthly, and lines that disagree.
func TestUnadoptableLineInstallBlock(t *testing.T) {
	listBlock := []string{
		"INFO Adopting backup schedule from cron entry...",
		`DEBUG schedule adopt: cron line="0 3,15 * * *` + adoptBin + `" not adoptable reason=hour_not_literal`,
		"DEBUG schedule adopt: kept frequency=daily time=02:00 source=CFG",
		"INFO   Frequency: daily",
		"INFO   Time: 02:00",
		`INFO "0 3,15 * * *" is not daily, weekly or monthly`,
		`WARNING ⚠ Backup schedule: cron entry "0 3,15 * * *" not adopted`,
	}

	t.Run("a list", func(t *testing.T) {
		cfg := writeScheduleCfg(t, "")
		stubCrontabLines(t, []string{"0 3,15 * * *" + adoptBin}, nil)
		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		assertLines(t, seedLines(seed), withCfg(listBlock, cfg))
		if want := `⚠ Backup schedule: cron entry "0 3,15 * * *" not adopted, not daily, weekly or monthly, default daily at 02:00`; seed.Item != want {
			t.Fatalf("item = %q, want %q", seed.Item, want)
		}
	})

	t.Run("three lines, two schedules", func(t *testing.T) {
		cfg := writeScheduleCfg(t, "")
		stubCrontabLines(t, []string{"0 3 * * 0" + adoptBin, "0 3 * * 7" + adoptBin, "0 4 * * 0" + adoptBin}, nil)
		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		assertLines(t, seedLines(seed), withCfg([]string{
			"INFO Adopting backup schedule from cron entry...",
			`DEBUG schedule adopt: cron line="0 3 * * 0` + adoptBin + `" parsed frequency=weekly weekday=sun time=03:00`,
			`DEBUG schedule adopt: cron line="0 4 * * 0` + adoptBin + `" parsed frequency=weekly weekday=sun time=04:00, not adoptable reason=lines_disagree`,
			"DEBUG schedule adopt: kept frequency=daily time=02:00 source=CFG",
			"INFO   Frequency: daily",
			"INFO   Time: 02:00",
			"INFO Two schedules in the crontab",
			`WARNING ⚠ Backup schedule: cron entries "0 3 * * 0" and "0 4 * * 0" not adopted`,
		}, cfg))
		if want := `⚠ Backup schedule: cron entries "0 3 * * 0" and "0 4 * * 0" not adopted, two schedules, default daily at 02:00`; seed.Item != want {
			t.Fatalf("item = %q, want %q", seed.Item, want)
		}
	})

	// The install CLI (Keep existing) renders it through the bootstrap logger, levels intact.
	t.Run("logged by the install front-end", func(t *testing.T) {
		cfg := writeScheduleCfg(t, "SCHEDULER_MODE=cron\n")
		stubCrontabLines(t, []string{"0 3,15 * * *" + adoptBin}, nil)
		bootstrap, buf := captureBootstrapLog(t)
		logScheduleLines(bootstrap, seedSchedulerTimeFromCrontabFn(context.Background(), cfg).Block.lines())
		assertLines(t, logBlock(buf.String()), withCfg(listBlock, cfg))
	})

	// The old note is gone from every path.
	t.Run("no old note", func(t *testing.T) {
		cfg := writeScheduleCfg(t, "")
		stubCrontabLines(t, []string{"*/15 * * * *" + adoptBin}, nil)
		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		if got := strings.Join(append(seedLines(seed), seed.Item), "\n"); strings.Contains(got, "not a single daily time") {
			t.Fatalf("the old note is still there:\n%s", got)
		}
	})
}

// A2 + A5 in the upgrade: one item in place of the old note; with SCHEDULER_TIME present and
// SCHEDULER_FREQUENCY absent it is reported the same way, against the stored time.
func TestUnadoptableLineUpgradeItem(t *testing.T) {
	for _, tc := range []struct {
		name, stored string
		lines        []string
		want         string
	}{
		{"a step, nothing stored", "", []string{"*/15 * * * *" + adoptBin},
			`⚠ Backup schedule: cron entry "*/15 * * * *" not adopted, not daily, weekly or monthly, default daily at 02:00`},
		{"a step, time present", "SCHEDULER_TIME=04:30\n", []string{"*/15 * * * *" + adoptBin},
			`⚠ Backup schedule: cron entry "*/15 * * * *" not adopted, not daily, weekly or monthly, daily at 04:30`},
		{"two schedules, three lines", "", []string{"0 3 * * 0" + adoptBin, "0 3 * * 0" + adoptBin, "0 4 * * 0" + adoptBin},
			`⚠ Backup schedule: cron entries "0 3 * * 0" and "0 4 * * 0" not adopted, two schedules, default daily at 02:00`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, cfg := upgradeCfg(t, tc.stored)
			stubCrontabLines(t, tc.lines, nil)
			result, err := applyConfigUpgrade(context.Background(), cfg, dir)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(result.Warnings, "\n") != tc.want {
				t.Fatalf("warnings = %q, want exactly %q", result.Warnings, tc.want)
			}
			for _, n := range result.Notes {
				if n.Level != config.UpgradeNoteDebug {
					t.Fatalf("only debug notes may accompany a warning item, got %+v", n)
				}
			}
		})
	}
}

// A2, the cron -> daemon switch: the same WARNING block the install logs (it was silent).
func TestAdoptSchedulerTimeForDaemonReportsUnadoptableLines(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		want  []string
	}{
		{"a step", []string{"*/30 * * * *" + adoptBin}, []string{
			"INFO Adopting backup schedule from cron entry...",
			`DEBUG schedule adopt: cron line="*/30 * * * *` + adoptBin + `" not adoptable reason=minute_not_literal`,
			"DEBUG schedule adopt: kept frequency=daily time=03:15 source=CFG",
			"INFO   Frequency: daily",
			"INFO   Time: 03:15",
			`INFO "*/30 * * * *" is not daily, weekly or monthly`,
			`WARNING ⚠ Backup schedule: cron entry "*/30 * * * *" not adopted`,
		}},
		{"two schedules", []string{"0 21 * * *" + adoptBin, "0 5 * * *" + adoptBin, "0 6 * * *" + adoptBin}, []string{
			"INFO Adopting backup schedule from cron entry...",
			`DEBUG schedule adopt: cron line="0 21 * * *` + adoptBin + `" parsed frequency=daily time=21:00`,
			`DEBUG schedule adopt: cron line="0 5 * * *` + adoptBin + `" parsed frequency=daily time=05:00, not adoptable reason=lines_disagree`,
			"DEBUG schedule adopt: kept frequency=daily time=03:15 source=CFG",
			"INFO   Frequency: daily",
			"INFO   Time: 03:15",
			"INFO Two schedules in the crontab",
			`WARNING ⚠ Backup schedule: cron entries "0 21 * * *" and "0 5 * * *" not adopted`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const stored = "BACKUP_PATH=/data\nSCHEDULER_TIME=03:15\n"
			cfg := writeScheduleCfg(t, stored)
			def, buf := captureDefaultLog(t)

			adoptSchedulerTimeForDaemon(cfg, tc.lines, nil)

			assertLines(t, logBlock(buf.String()), withCfg(tc.want, cfg))
			if def.WarningCount() != 1 {
				t.Errorf("warnings = %d, want 1", def.WarningCount())
			}
			if data, _ := os.ReadFile(cfg); string(data) != stored {
				t.Errorf("nothing may be written, got:\n%s", data)
			}
		})
	}
}

// A3: the post-merge write fails. The upgrade shows one item; the error is DEBUG only.
func TestApplyConfigUpgradeWriteFailureItem(t *testing.T) {
	dir, cfg := upgradeCfg(t, "")
	// The merge writes through backup.env.tmp; only the adoption's temp file is blocked.
	if err := os.MkdirAll(filepath.Join(cfg+".daemon.tmp", "blocker"), 0o700); err != nil {
		t.Fatal(err)
	}
	stubCrontabLines(t, []string{"0 3 * * 1" + adoptBin}, nil)

	result, err := applyConfigUpgrade(context.Background(), cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	const want = "⚠ Backup schedule: not adopted from cron entry, backup.env could not be written, default daily at 02:00"
	if strings.Join(result.Warnings, "\n") != want {
		t.Fatalf("warnings = %q, want exactly %q", result.Warnings, want)
	}
	notes := noteLines(result.Notes)
	failed := "DEBUG schedule adopt: write failed file=" + cfg + " error=failed to write configuration file: "
	if len(notes) != 4 || !strings.HasPrefix(notes[2], failed) {
		t.Fatalf("notes = %q, want four DEBUG lines with the error third", notes)
	}
	assertLines(t, notes, []string{
		`DEBUG schedule adopt: cron line="0 3 * * 1` + adoptBin + `" parsed frequency=weekly weekday=mon time=03:00`,
		"DEBUG schedule adopt: SCHEDULER_TIME=absent SCHEDULER_FREQUENCY=absent source=" + cfg,
		notes[2],
		"DEBUG schedule adopt: kept frequency=daily time=02:00 source=" + cfg,
	})
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "SCHEDULER_FREQUENCY=daily") || !strings.Contains(string(data), "SCHEDULER_TIME=02:00") {
		t.Fatalf("the merge result stands, with the template defaults:\n%s", data)
	}
}

// A7, A7 bis, combination 4: /etc lines that are not one agreed daily cadence.
func TestEtcLinesThatAreNotOneDailyCadence(t *testing.T) {
	t.Run("one weekly line", func(t *testing.T) {
		cfg := writeScheduleCfg(t, "")
		stubCrontabLines(t, nil, nil)
		_, cronD := etcTree(t)
		src := filepath.Join(cronD, "proxsave")
		writeCronFile(t, src, "0 3 * * 1 root /usr/local/bin/proxsave --backup\n")

		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		assertLines(t, seedLines(seed), []string{
			"INFO Adopting backup schedule from cron entry...",
			"DEBUG schedule adopt: root crontab has no proxsave line",
			"DEBUG schedule adopt: " + src + ` line="0 3 * * 1 root /usr/local/bin/proxsave --backup" parsed frequency=weekly weekday=mon time=03:00, not adopted reason=file_not_managed`,
			"INFO   Frequency: daily",
			"INFO   Time: 02:00",
			"INFO   Kept: " + src + ", weekly, Monday at 03:00",
			"INFO Two schedules: " + src + " is not edited by ProxSave",
			"WARNING ⚠ Backup schedule: cron entry in " + src + " not adopted",
		})
		if want := "⚠ Backup schedule: cron entry in " + src + " not adopted, two schedules, default daily at 02:00"; seed.Item != want {
			t.Fatalf("item = %q, want %q", seed.Item, want)
		}
	})

	t.Run("a line whose schedule does not parse", func(t *testing.T) {
		cfg := writeScheduleCfg(t, "")
		stubCrontabLines(t, nil, nil)
		_, cronD := etcTree(t)
		src := filepath.Join(cronD, "proxsave")
		writeCronFile(t, src, "*/30 * * * * root /usr/local/bin/proxsave --backup\n")

		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		assertLines(t, seedLines(seed), []string{
			"INFO Adopting backup schedule from cron entry...",
			"DEBUG schedule adopt: root crontab has no proxsave line",
			"DEBUG schedule adopt: " + src + ` line="*/30 * * * * root /usr/local/bin/proxsave --backup" not parsed reason=minute_not_literal, not adopted reason=file_not_managed`,
			"INFO   Frequency: daily",
			"INFO   Time: 02:00",
			"INFO   Kept: " + src + `, "*/30 * * * *"`,
			"INFO Two schedules: " + src + " is not edited by ProxSave",
			"WARNING ⚠ Backup schedule: cron entry in " + src + " not adopted",
		})
	})

	t.Run("two files, the upgrade item names the first", func(t *testing.T) {
		dir, cfg := upgradeCfg(t, "")
		stubCrontabLines(t, nil, nil)
		crontab, cronD := etcTree(t)
		src := filepath.Join(cronD, "proxsave")
		writeCronFile(t, crontab, "0 5 * * * root /usr/local/bin/proxsave --backup\n")
		writeCronFile(t, src, "0 3 * * 0 root /usr/local/bin/proxsave --backup\n")

		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		assertLines(t, seedLines(seed), []string{
			"INFO Adopting backup schedule from cron entry...",
			"DEBUG schedule adopt: root crontab has no proxsave line",
			"DEBUG schedule adopt: " + crontab + ` line="0 5 * * * root /usr/local/bin/proxsave --backup" parsed frequency=daily time=05:00, not adopted reason=file_not_managed`,
			"DEBUG schedule adopt: " + src + ` line="0 3 * * 0 root /usr/local/bin/proxsave --backup" parsed frequency=weekly weekday=sun time=03:00, not adopted reason=file_not_managed`,
			"INFO   Frequency: daily",
			"INFO   Time: 02:00",
			"INFO   Kept: " + crontab + ", daily at 05:00",
			"INFO   Kept: " + src + ", weekly, Sunday at 03:00",
			"INFO Two schedules: " + crontab + " is not edited by ProxSave",
			"WARNING ⚠ Backup schedule: cron entry in " + crontab + " not adopted",
		})

		result, err := applyConfigUpgrade(context.Background(), cfg, dir)
		if err != nil {
			t.Fatal(err)
		}
		if want := "⚠ Backup schedule: cron entry in " + crontab + " not adopted, two schedules, default daily at 02:00"; strings.Join(result.Warnings, "\n") != want {
			t.Fatalf("warnings = %q, want exactly %q", result.Warnings, want)
		}
	})

	// A single agreed daily cadence keeps the approved "two backups a day".
	t.Run("two files, one daily cadence", func(t *testing.T) {
		cfg := writeScheduleCfg(t, "")
		stubCrontabLines(t, nil, nil)
		crontab, cronD := etcTree(t)
		src := filepath.Join(cronD, "proxsave")
		writeCronFile(t, crontab, "0 5 * * * root /usr/local/bin/proxsave --backup\n")
		writeCronFile(t, src, "00 05 * * * root /usr/local/bin/proxsave --backup\n")

		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		if want := "⚠ Backup schedule: cron entry in " + src + " not adopted, two backups a day, default daily at 02:00"; seed.Item != want {
			t.Fatalf("item = %q, want %q", seed.Item, want)
		}
	})
}

// A8 (and A5): a wrapper whose cadence is not daily, or does not parse.
func TestWrapperThatIsNotDaily(t *testing.T) {
	t.Run("weekly", func(t *testing.T) {
		wrapper := absentWrapper(t)
		line := "0 3 * * 1 " + wrapper
		cfg := writeScheduleCfg(t, "")
		stubCrontabLines(t, []string{line}, nil)
		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		assertLines(t, seedLines(seed), []string{
			"INFO Adopting backup schedule from cron entry...",
			"DEBUG schedule adopt: root crontab has no proxsave line",
			"DEBUG schedule adopt: indirect command=" + wrapper + ` line="` + line + `" parsed frequency=weekly weekday=mon time=03:00, not adopted reason=wrapper_not_interpreted`,
			"INFO   Frequency: daily",
			"INFO   Time: 02:00",
			"INFO   Kept: " + wrapper + ", weekly, Monday at 03:00",
			"INFO Two schedules: " + wrapper + " appears to run ProxSave",
			"WARNING ⚠ Backup schedule: cron entry for " + wrapper + " not adopted",
		})
		if want := "⚠ Backup schedule: cron entry for " + wrapper + " not adopted, two schedules, default daily at 02:00"; seed.Item != want {
			t.Fatalf("item = %q, want %q", seed.Item, want)
		}
	})

	t.Run("does not parse", func(t *testing.T) {
		wrapper := absentWrapper(t)
		line := "*/15 * * * * " + wrapper
		cfg := writeScheduleCfg(t, "")
		stubCrontabLines(t, []string{line}, nil)
		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		assertLines(t, seedLines(seed), []string{
			"INFO Adopting backup schedule from cron entry...",
			"DEBUG schedule adopt: root crontab has no proxsave line",
			"DEBUG schedule adopt: indirect command=" + wrapper + ` line="` + line + `" not parsed reason=minute_not_literal, not adopted reason=wrapper_not_interpreted`,
			"INFO   Frequency: daily",
			"INFO   Time: 02:00",
			"INFO   Kept: " + wrapper,
			"INFO Two schedules: " + wrapper + " appears to run ProxSave",
			"WARNING ⚠ Backup schedule: cron entry for " + wrapper + " not adopted",
		})
		if want := "⚠ Backup schedule: cron entry for " + wrapper + " not adopted, two schedules, default daily at 02:00"; seed.Item != want {
			t.Fatalf("item = %q, want %q", seed.Item, want)
		}
	})

	t.Run("daily, time present", func(t *testing.T) {
		wrapper := absentWrapper(t)
		cfg := writeScheduleCfg(t, "SCHEDULER_TIME=04:30\n")
		stubCrontabLines(t, []string{"0 3 * * * " + wrapper}, nil)
		seed := deriveSchedulerTimeFromCrontab(context.Background(), cfg)
		if want := "⚠ Backup schedule: cron entry for " + wrapper + " not adopted, two backups a day, daily at 04:30"; seed.Item != want {
			t.Fatalf("item = %q, want %q", seed.Item, want)
		}
		if got := strings.Join(seedLines(seed), "\n"); !strings.Contains(got, "INFO Two backups a day: "+wrapper+" appears to run ProxSave") {
			t.Fatalf("a daily wrapper keeps the approved reason:\n%s", got)
		}
	})
}

// removalBlock is the removal block of a switch, from its header to its WARNING outcome.
func removalBlock(out string) []string {
	return blockFrom(logBlock(out), "INFO Removing proxsave cron entry...",
		"WARNING ⚠ Proxsave cron entry: not removed, remove it by hand to avoid a double backup")
}

// A10: a failed removal names the surviving entries by their fields when they do not give one
// readable cadence.
func TestCronRemovalNamesUnreadableEntries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		lines   []string
		details []string
	}{
		{"day 31", []string{"0 3 31 * *" + adoptBin}, []string{`INFO   Cron entry: "0 3 31 * *"`}},
		{"two schedules", []string{"0 3 * * 1" + adoptBin, "0 4 * * 1" + adoptBin},
			[]string{`INFO   Cron entry: "0 3 * * 1"`, `INFO   Cron entry: "0 4 * * 1"`}},
		{"one readable cadence", []string{"0 3 * * 1" + adoptBin, "00 03 * * 1" + adoptBin},
			[]string{"INFO   Cron entry: weekly, Monday at 03:00"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configPath, _ := handoverFixture(t, tc.lines)
			crontabWriteLinesFn = func(context.Context, []string) error {
				return errors.New("crontab update failed: read-only file system")
			}
			_, buf := captureDefaultLog(t)

			prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

			want := []string{
				"INFO Removing proxsave cron entry...",
				"DEBUG cron removal: crontab write failed error=crontab update failed: read-only file system",
			}
			for _, l := range tc.lines {
				want = append(want, `DEBUG cron removal: still present line="`+l+`", per-run lock prevents overlap`)
			}
			want = append(want, tc.details...)
			want = append(want, "INFO Crontab could not be written",
				"WARNING ⚠ Proxsave cron entry: not removed, remove it by hand to avoid a double backup")
			assertLines(t, removalBlock(buf.String()), want)
		})
	}
}

// A11: the line was in the first read and is gone in the second: DEBUG only, no block, no
// WARNING.
func TestCronRemovalRaceIsDebugOnly(t *testing.T) {
	const line = "0 3 * * 1" + adoptBin

	t.Run("reported alone", func(t *testing.T) {
		def, buf := captureDefaultLog(t)
		reportCronRemoval([]string{line}, cronRemovalOutcome{Verified: true}, nil, nil)
		assertLines(t, logBlock(buf.String()), []string{
			`DEBUG cron removal: line="` + line + `" in the first read, absent in the second, nothing to remove`,
		})
		if def.WarningCount() != 0 {
			t.Fatalf("warnings = %d, want 0", def.WarningCount())
		}
	})

	t.Run("through the switch", func(t *testing.T) {
		configPath, _ := handoverFixture(t, nil)
		reads := 0
		crontabReadLinesFn = func(context.Context) ([]string, error) {
			reads++
			if reads == 1 {
				return []string{line}, nil
			}
			return nil, nil
		}
		crontabWriteLinesFn = func(context.Context, []string) error {
			t.Error("nothing matched the second read, so nothing may be written")
			return nil
		}
		def, buf := captureDefaultLog(t)

		prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

		out := buf.String()
		if strings.Contains(out, cronRemovalHeader) || strings.Contains(out, "not removed") {
			t.Fatalf("no removal block may be printed, out=%q", out)
		}
		if !strings.Contains(out, `DEBUG    cron removal: line="`+line+`" in the first read, absent in the second, nothing to remove`) {
			t.Fatalf("the race must leave its DEBUG evidence, out=%q", out)
		}
		if def.WarningCount() != 0 {
			t.Fatalf("warnings = %d, want 0, out=%q", def.WarningCount(), out)
		}
	})
}

// A17: the removal failed while the first read had no proxsave line: the WARNING block, with
// no "Cron entry:" detail.
func TestCronRemovalFailureWithNoLineInTheFirstRead(t *testing.T) {
	want := []string{
		"INFO Removing proxsave cron entry...",
		"DEBUG cron removal: no proxsave line in the first read, crontab write failed error=crontab update failed: read-only file system",
		"INFO Crontab could not be written",
		"WARNING ⚠ Proxsave cron entry: not removed, remove it by hand to avoid a double backup",
	}

	t.Run("reported alone", func(t *testing.T) {
		def, buf := captureDefaultLog(t)
		reportCronRemoval([]string{"0 6 * * * /usr/bin/rsync /a /b"}, cronRemovalOutcome{}, errors.New("crontab update failed: read-only file system"), nil)
		assertLines(t, logBlock(buf.String()), want)
		if def.WarningCount() != 1 {
			t.Fatalf("warnings = %d, want 1", def.WarningCount())
		}
	})

	t.Run("through the switch", func(t *testing.T) {
		configPath, _ := handoverFixture(t, nil)
		reads := 0
		crontabReadLinesFn = func(context.Context) ([]string, error) {
			reads++
			if reads == 1 {
				return []string{"0 6 * * * /usr/bin/rsync /a /b"}, nil
			}
			return []string{"0 6 * * * /usr/bin/rsync /a /b", "0 3 * * 1" + adoptBin}, nil
		}
		crontabWriteLinesFn = func(context.Context, []string) error {
			return errors.New("crontab update failed: read-only file system")
		}
		_, buf := captureDefaultLog(t)

		prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

		assertLines(t, removalBlock(buf.String()), want)
	})
}

// A13 in the switch: only the frequency, the time and the day the cadence uses are written.
func TestAdoptSchedulerTimeForDaemonWritesOnlyTheCadenceVariables(t *testing.T) {
	const stored = "BACKUP_PATH=/data\nSCHEDULER_TIME=03:15\nSCHEDULER_WEEKDAY=someday\nSCHEDULER_MONTHDAY=31\n"

	t.Run("daily", func(t *testing.T) {
		cfg := writeScheduleCfg(t, stored)
		_, buf := captureDefaultLog(t)
		adoptSchedulerTimeForDaemon(cfg, []string{"0 21 * * *" + adoptBin}, nil)
		data, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if want := "BACKUP_PATH=/data\nSCHEDULER_TIME=21:00\nSCHEDULER_WEEKDAY=someday\nSCHEDULER_MONTHDAY=31\n\nSCHEDULER_FREQUENCY=daily"; string(data) != want {
			t.Fatalf("config =\n%q\nwant\n%q", data, want)
		}
		assertLines(t, logBlock(buf.String()), withCfg([]string{
			"INFO Adopting backup schedule from cron entry...",
			`DEBUG schedule adopt: cron line="0 21 * * *` + adoptBin + `" parsed frequency=daily time=21:00`,
			"DEBUG schedule adopt: before frequency=daily time=03:15 source=CFG",
			"DEBUG schedule adopt: wrote SCHEDULER_FREQUENCY=daily SCHEDULER_TIME=21:00 file=CFG",
			"INFO   Frequency: daily",
			"INFO   Time: 21:00",
			"INFO ✓ Backup schedule: adopted from cron entry",
		}, cfg))
	})

	t.Run("monthly", func(t *testing.T) {
		cfg := writeScheduleCfg(t, stored)
		captureDefaultLog(t)
		adoptSchedulerTimeForDaemon(cfg, []string{"0 4 15 * *" + adoptBin}, nil)
		data, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if want := "BACKUP_PATH=/data\nSCHEDULER_TIME=04:00\nSCHEDULER_WEEKDAY=someday\nSCHEDULER_MONTHDAY=15\n\nSCHEDULER_FREQUENCY=monthly"; string(data) != want {
			t.Fatalf("config =\n%q\nwant\n%q", data, want)
		}
	})
}

// A18 (+A13): the merge puts every missing SCHEDULER_* variable at its template place, with its
// inline comment, and reports it as added; the adoption then writes only its values.
func TestApplyConfigUpgradeWritesTheAdoptionAfterTheMerge(t *testing.T) {
	dir, cfg := upgradeCfg(t, "")
	stubCrontabLines(t, []string{"0 3 * * 1" + adoptBin}, nil)

	result, err := applyConfigUpgrade(context.Background(), cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	template := strings.Split(config.DefaultEnvTemplate(), "\n")
	templateLine := func(key string) string {
		for _, l := range template {
			if strings.HasPrefix(l, key+"=") {
				return l
			}
		}
		t.Fatalf("%s not in the template", key)
		return ""
	}
	want := []string{
		"SCHEDULER_MODE=cron",
		strings.Replace(templateLine("SCHEDULER_FREQUENCY"), "=daily ", "=weekly ", 1),
		templateLine("SCHEDULER_WEEKDAY"),
		templateLine("SCHEDULER_MONTHDAY"),
		strings.Replace(templateLine("SCHEDULER_TIME"), "=02:00 ", "=03:00 ", 1),
	}
	at := -1
	for i, l := range lines {
		if l == "SCHEDULER_MODE=cron" {
			at = i
		}
	}
	if at < 1 || at+len(want) > len(lines) {
		t.Fatalf("SCHEDULER_MODE not found in its place:\n%s", data)
	}
	// The inline comments are the template's; the merge does not add section comments (F4).
	assertLines(t, lines[at:at+len(want)], want)
	for _, key := range []string{"SCHEDULER_FREQUENCY=", "SCHEDULER_WEEKDAY=", "SCHEDULER_MONTHDAY=", "SCHEDULER_TIME="} {
		if n := strings.Count(string(data), "\n"+key); n != 1 {
			t.Errorf("%s appears %d times, want once, in its template place", key, n)
		}
	}
	added := map[string]bool{}
	for _, k := range result.MissingKeys {
		added[k] = true
	}
	for _, key := range []string{"SCHEDULER_FREQUENCY", "SCHEDULER_WEEKDAY", "SCHEDULER_MONTHDAY", "SCHEDULER_TIME"} {
		if !added[key] {
			t.Errorf("%s must be reported as an added key, got %v", key, result.MissingKeys)
		}
	}
	assertLines(t, noteLines(result.Notes), []string{
		"INFO Adopting backup schedule from cron entry...",
		`DEBUG schedule adopt: cron line="0 3 * * 1` + adoptBin + `" parsed frequency=weekly weekday=mon time=03:00`,
		"DEBUG schedule adopt: SCHEDULER_TIME=absent SCHEDULER_FREQUENCY=absent source=" + cfg,
		"DEBUG schedule adopt: wrote SCHEDULER_FREQUENCY=weekly SCHEDULER_TIME=03:00 SCHEDULER_WEEKDAY=mon file=" + cfg,
		"INFO   Frequency: weekly",
		"INFO   Weekday: Monday",
		"INFO   Time: 03:00",
		"INFO ✓ Backup schedule: adopted from cron entry",
	})
	if len(result.Warnings) != 0 {
		t.Errorf("an adoption is not a warning, got %q", result.Warnings)
	}
}

// A16 + A19 in the upgrade: both variables present and empty is an explicit schedule.
func TestApplyConfigUpgradeEmptyVariablesAreStated(t *testing.T) {
	dir, cfg := upgradeCfg(t, "SCHEDULER_TIME=\nSCHEDULER_FREQUENCY=\n")
	stubCrontabLines(t, []string{"0 3 * * 1" + adoptBin}, nil)

	result, err := applyConfigUpgrade(context.Background(), cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Notes) != 0 || len(result.Warnings) != 0 {
		t.Fatalf("nothing may be adopted or reported: notes=%v warnings=%q", result.Notes, result.Warnings)
	}
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\nSCHEDULER_TIME=\n") || !strings.Contains(string(data), "\nSCHEDULER_FREQUENCY=\n") {
		t.Fatalf("the empty variables must stay as the operator left them:\n%s", data)
	}
}

// D6: a present but empty SCHEDULER_TIME is 02:00 and keeps the stored frequency and day.
func TestKeptCronScheduleWithAnEmptyTime(t *testing.T) {
	for _, tc := range []struct {
		name, content, want string
	}{
		{"empty time, weekly", "SCHEDULER_TIME=\nSCHEDULER_FREQUENCY=weekly\nSCHEDULER_WEEKDAY=fri\n", "00 02 * * 5"},
		{"empty time, monthly", "SCHEDULER_TIME=\nSCHEDULER_FREQUENCY=monthly\nSCHEDULER_MONTHDAY=15\n", "00 02 15 * *"},
		{"empty time, invalid frequency", "SCHEDULER_TIME=\nSCHEDULER_FREQUENCY=hourly\n", "00 02 * * *"},
		{"no time at all", "SCHEDULER_FREQUENCY=weekly\nSCHEDULER_WEEKDAY=fri\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := writeScheduleCfg(t, tc.content)
			if got := keptCronScheduleFromConfig(cfg); got != tc.want {
				t.Fatalf("keptCronScheduleFromConfig = %q, want %q", got, tc.want)
			}
		})
	}
	cfg := writeScheduleCfg(t, "SCHEDULER_TIME=\nSCHEDULER_FREQUENCY=weekly\nSCHEDULER_WEEKDAY=fri\n")
	if got := buildInstallCronSchedule(true, "", cfg); got != "00 02 * * 5" {
		t.Fatalf("the keep-config reinstall writes %q, want the weekly line at 02:00", got)
	}
}

// Combination 6: the dashboard result adds the adoption's details and outcome after the backup
// line, never the header or the DEBUG evidence.
func TestConfigApplyDescriptionCarriesTheAdoption(t *testing.T) {
	notes := []config.UpgradeNote{
		{Level: config.UpgradeNoteInfo, Text: "Adopting backup schedule from cron entry..."},
		{Level: config.UpgradeNoteDebug, Text: "schedule adopt: evidence"},
		{Level: config.UpgradeNoteInfo, Text: "  Frequency: weekly"},
		{Level: config.UpgradeNoteInfo, Text: "  Weekday: Sunday"},
		{Level: config.UpgradeNoteInfo, Text: "  Time: 03:00"},
		{Level: config.UpgradeNoteInfo, Text: "✓ Backup schedule: adopted from cron entry"},
	}
	got := describeConfigApply(&config.UpgradeResult{Changed: true, MissingKeys: []string{"A", "B", "C", "D"}, BackupPath: "/x.backup", Notes: notes})
	want := "Updated the configuration: added 4 key(s).\nBackup saved to /x.backup.\n  Frequency: weekly\n  Weekday: Sunday\n  Time: 03:00\n✓ Backup schedule: adopted from cron entry"
	if got != want {
		t.Fatalf("describeConfigApply =\n%q\nwant\n%q", got, want)
	}

	// A warning item comes with DEBUG notes only: nothing but the item is added.
	item := `⚠ Backup schedule: cron entry "0 3 31 * *" not adopted, day 31 is outside 1-28, default daily at 02:00`
	got = describeConfigApply(&config.UpgradeResult{Changed: true, MissingKeys: []string{"A"}, BackupPath: "/x.backup",
		Notes: []config.UpgradeNote{{Level: config.UpgradeNoteDebug, Text: "schedule adopt: evidence"}}, Warnings: []string{item}})
	if want := "Updated the configuration: added 1 key(s).\nBackup saved to /x.backup.\n" + item; got != want {
		t.Fatalf("describeConfigApply =\n%q\nwant\n%q", got, want)
	}

	// End to end, from the real upgrade.
	dir, cfg := upgradeCfg(t, "")
	stubCrontabLines(t, []string{"0 3 * * 1" + adoptBin}, nil)
	result, err := applyConfigUpgrade(context.Background(), cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	tail := "\nBackup saved to " + result.BackupPath + ".\n  Frequency: weekly\n  Weekday: Monday\n  Time: 03:00\n✓ Backup schedule: adopted from cron entry"
	if msg := describeConfigApply(result); !strings.HasSuffix(msg, tail) {
		t.Fatalf("describeConfigApply =\n%q\nwant it to end with\n%q", msg, tail)
	}
}

// A1: --upgrade-config logs the Notes right after "Upgrading configuration file:", before the
// warnings list and before the merge summary.
func TestRunUpgradeConfigModeLogsTheNotesFirst(t *testing.T) {
	t.Run("an adoption", func(t *testing.T) {
		_, cfg := upgradeCfg(t, "")
		stubCrontabLines(t, []string{"0 3 * * 1" + adoptBin}, nil)
		bootstrap, buf := captureBootstrapLog(t)

		var code int
		var handled bool
		captureStdout(t, func() {
			code, handled = runUpgradeConfigMode(context.Background(), &cli.Args{UpgradeConfig: true, ConfigPath: cfg}, bootstrap)
		})
		if !handled || code != 0 {
			t.Fatalf("code=%d handled=%v, out=%s", code, handled, buf.String())
		}
		got := logBlock(buf.String())
		block := blockFrom(got, "INFO Upgrading configuration file: "+cfg, "INFO ✓ Backup schedule: adopted from cron entry")
		assertLines(t, block, withCfg([]string{
			"INFO Upgrading configuration file: CFG",
			"INFO Adopting backup schedule from cron entry...",
			`DEBUG schedule adopt: cron line="0 3 * * 1` + adoptBin + `" parsed frequency=weekly weekday=mon time=03:00`,
			"DEBUG schedule adopt: SCHEDULER_TIME=absent SCHEDULER_FREQUENCY=absent source=CFG",
			"DEBUG schedule adopt: wrote SCHEDULER_FREQUENCY=weekly SCHEDULER_TIME=03:00 SCHEDULER_WEEKDAY=mon file=CFG",
			"INFO   Frequency: weekly",
			"INFO   Weekday: Monday",
			"INFO   Time: 03:00",
			"INFO ✓ Backup schedule: adopted from cron entry",
		}, cfg))
		out := strings.Join(got, "\n")
		assertInOrder(t, out, "INFO ✓ Backup schedule: adopted from cron entry", "INFO - Added ", "INFO ✓ Configuration upgrade completed successfully.")
	})

	t.Run("a warning item", func(t *testing.T) {
		_, cfg := upgradeCfg(t, "")
		stubCrontabLines(t, []string{"*/15 * * * *" + adoptBin}, nil)
		bootstrap, buf := captureBootstrapLog(t)

		captureStdout(t, func() {
			runUpgradeConfigMode(context.Background(), &cli.Args{UpgradeConfig: true, ConfigPath: cfg}, bootstrap)
		})
		got := logBlock(buf.String())
		last := `WARNING   - ⚠ Backup schedule: cron entry "*/15 * * * *" not adopted, not daily, weekly or monthly, default daily at 02:00`
		assertLines(t, blockFrom(got, "INFO Upgrading configuration file: "+cfg, last), withCfg([]string{
			"INFO Upgrading configuration file: CFG",
			`DEBUG schedule adopt: cron line="*/15 * * * *` + adoptBin + `" not adoptable reason=minute_not_literal`,
			"DEBUG schedule adopt: kept frequency=daily time=02:00 source=CFG",
			"WARNING Config upgrade warnings (1):",
			last,
		}, cfg))
		assertInOrder(t, strings.Join(got, "\n"), last, "INFO - Added ")
	})
}

// A1: upgradeFinalizePhase execs the installed binary and writes outside a temp dir, so it is
// pinned by its source, like the other wiring guards: the Notes the child returned are logged
// after the merge and before the footer that lists the configuration warnings.
func TestUpgradeFinalizePhaseLogsTheNotesBeforeTheFooter(t *testing.T) {
	fn := findFuncDecl(t, "upgrade.go", "upgradeFinalizePhase")
	calls := map[string][]*ast.CallExpr{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			name := gotypes.ExprString(call.Fun)
			calls[name] = append(calls[name], call)
		}
		return true
	})
	notes, merge, footer := calls["logConfigUpgradeNotes"], calls["upgradeConfigWithBinary"], calls["printUpgradeFooter"]
	if len(notes) != 1 || len(merge) != 1 || len(footer) != 1 {
		t.Fatalf("calls: logConfigUpgradeNotes=%d upgradeConfigWithBinary=%d printUpgradeFooter=%d, want one each", len(notes), len(merge), len(footer))
	}
	if got := gotypes.ExprString(notes[0].Args[1]); got != "cfgUpgradeResult.Notes" {
		t.Fatalf("logConfigUpgradeNotes is passed %q, want cfgUpgradeResult.Notes", got)
	}
	if merge[0].Pos() >= notes[0].Pos() || notes[0].Pos() >= footer[0].Pos() {
		t.Fatal("upgradeFinalizePhase must log the Notes after the merge and before the footer that lists the warnings")
	}
	footerArgs := make([]string, 0, len(footer[0].Args))
	for _, a := range footer[0].Args {
		footerArgs = append(footerArgs, gotypes.ExprString(a))
	}
	if !strings.Contains(strings.Join(footerArgs, ","), "cfgUpgradeResult") {
		t.Fatalf("the footer no longer renders the merge result, args=%v", footerArgs)
	}
}
