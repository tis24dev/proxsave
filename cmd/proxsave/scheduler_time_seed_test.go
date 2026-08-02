// Package main contains the proxsave command entrypoint.
package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cronutil "github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
)

// stubCrontabLines swaps the crontab read seam so the SCHEDULER_TIME seeding can be
// driven without touching the host's real crontab.
func stubCrontabLines(t *testing.T, lines []string, err error) {
	t.Helper()
	orig := crontabReadLinesFn
	t.Cleanup(func() { crontabReadLinesFn = orig })
	crontabReadLinesFn = func(context.Context) ([]string, error) { return lines, err }
}

func TestSchedulerTimeFromCronLines(t *testing.T) {
	tests := []struct {
		name   string
		lines  []string
		want   string
		wantOK bool
	}{
		{
			name: "single daily proxsave line among unrelated jobs",
			lines: []string{
				"MAILTO=root",
				"# a comment",
				"30 4 * * * /usr/bin/other-job",
				"0 21 * * * /usr/local/bin/proxsave --backup",
			},
			want: "21:00", wantOK: true,
		},
		{
			name:  "legacy proxmox-backup entrypoint still counts",
			lines: []string{"0 21 * * * /usr/local/bin/proxmox-backup --backup"},
			want:  "21:00", wantOK: true,
		},
		{
			name:  "commented out proxsave line is not a schedule",
			lines: []string{"#0 21 * * * /usr/local/bin/proxsave --backup"},
			want:  "", wantOK: false,
		},
		{
			name:  "proxsave only as an argument is not a proxsave job",
			lines: []string{"0 21 * * * /bin/cp /usr/local/bin/proxsave /tmp/"},
			want:  "", wantOK: false,
		},
		{
			name: "two proxsave lines at different times are ambiguous",
			lines: []string{
				"0 21 * * * /usr/local/bin/proxsave --backup",
				"0 6 * * * /usr/local/bin/proxsave --backup",
			},
			want: "", wantOK: false,
		},
		{
			name: "two proxsave lines at the same time agree",
			lines: []string{
				"0 21 * * * /usr/local/bin/proxsave --backup",
				"00 21 * * * /usr/local/bin/proxsave --backup",
			},
			want: "21:00", wantOK: true,
		},
		{
			name:  "sub-daily schedule the daemon cannot express",
			lines: []string{"*/15 * * * * /usr/local/bin/proxsave --backup"},
			want:  "", wantOK: false,
		},
		{
			name:  "daily shortcut",
			lines: []string{"@daily /usr/local/bin/proxsave --backup"},
			want:  "00:00", wantOK: true,
		},
		{name: "no lines at all", lines: nil, want: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := schedulerTimeFromCronLines(tt.lines)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("schedulerTimeFromCronLines = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestSeedSchedulerTimeFromCrontab(t *testing.T) {
	writeCfg := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "backup.env")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("seed config: %v", err)
		}
		return p
	}
	read := func(t *testing.T, path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		return string(data)
	}

	t.Run("pre-0.30 config adopts the crontab run time", func(t *testing.T) {
		cfg := writeCfg(t, "SCHEDULER_MODE=cron\nBACKUP_PATH=/data\n")
		stubCrontabLines(t, []string{"0 21 * * * /usr/local/bin/proxsave --backup"}, nil)

		seed := seedSchedulerTimeFromCrontab(context.Background(), cfg)
		if seed.Time != "21:00" {
			t.Fatalf("seed.Time = %q, want 21:00", seed.Time)
		}
		if seed.Note == "" {
			t.Error("expected an operator-facing note")
		}
		if content := read(t, cfg); !strings.Contains(content, "SCHEDULER_TIME=21:00") {
			t.Fatalf("SCHEDULER_TIME not written:\n%s", content)
		}
	})

	t.Run("explicit operator value is never overridden", func(t *testing.T) {
		cfg := writeCfg(t, "SCHEDULER_MODE=cron\nSCHEDULER_TIME=07:30\n")
		before := read(t, cfg)
		stubCrontabLines(t, []string{"0 21 * * * /usr/local/bin/proxsave --backup"}, nil)

		if seed := seedSchedulerTimeFromCrontab(context.Background(), cfg); seed != (schedulerTimeSeed{}) {
			t.Fatalf("expected a no-op seed, got %+v", seed)
		}
		if after := read(t, cfg); after != before {
			t.Fatalf("config was modified:\n%s", after)
		}
	})

	t.Run("an explicitly stored default still wins", func(t *testing.T) {
		cfg := writeCfg(t, "SCHEDULER_MODE=cron\nSCHEDULER_TIME="+cronutil.DefaultTime+"\n")
		before := read(t, cfg)
		stubCrontabLines(t, []string{"0 21 * * * /usr/local/bin/proxsave --backup"}, nil)

		if seed := seedSchedulerTimeFromCrontab(context.Background(), cfg); seed != (schedulerTimeSeed{}) {
			t.Fatalf("expected a no-op seed, got %+v", seed)
		}
		if after := read(t, cfg); after != before {
			t.Fatalf("config was modified:\n%s", after)
		}
	})

	t.Run("second call is a no-op", func(t *testing.T) {
		cfg := writeCfg(t, "SCHEDULER_MODE=cron\n")
		stubCrontabLines(t, []string{"0 21 * * * /usr/local/bin/proxsave --backup"}, nil)

		if seed := seedSchedulerTimeFromCrontab(context.Background(), cfg); seed.Time != "21:00" {
			t.Fatalf("first call seed.Time = %q, want 21:00", seed.Time)
		}
		after := read(t, cfg)
		if seed := seedSchedulerTimeFromCrontab(context.Background(), cfg); seed != (schedulerTimeSeed{}) {
			t.Fatalf("second call should be a no-op, got %+v", seed)
		}
		if again := read(t, cfg); again != after {
			t.Fatalf("second call rewrote the config:\n%s", again)
		}
	})

	t.Run("a schedule the daemon cannot express only warns", func(t *testing.T) {
		cfg := writeCfg(t, "SCHEDULER_MODE=cron\n")
		before := read(t, cfg)
		stubCrontabLines(t, []string{"*/15 * * * * /usr/local/bin/proxsave --backup"}, nil)

		seed := seedSchedulerTimeFromCrontab(context.Background(), cfg)
		if seed.Time != "" {
			t.Fatalf("seed.Time = %q, want empty", seed.Time)
		}
		if !strings.Contains(seed.Note, cronutil.DefaultTime) {
			t.Fatalf("note should name the %s default, got %q", cronutil.DefaultTime, seed.Note)
		}
		if after := read(t, cfg); after != before {
			t.Fatalf("config was modified:\n%s", after)
		}
	})

	t.Run("no proxsave cron line is silent", func(t *testing.T) {
		cfg := writeCfg(t, "SCHEDULER_MODE=cron\n")
		stubCrontabLines(t, []string{"30 4 * * * /usr/bin/other-job"}, nil)

		if seed := seedSchedulerTimeFromCrontab(context.Background(), cfg); seed != (schedulerTimeSeed{}) {
			t.Fatalf("expected a zero seed, got %+v", seed)
		}
	})

	t.Run("a crontab read failure is best-effort", func(t *testing.T) {
		cfg := writeCfg(t, "SCHEDULER_MODE=cron\n")
		before := read(t, cfg)
		stubCrontabLines(t, nil, os.ErrPermission)

		if seed := seedSchedulerTimeFromCrontab(context.Background(), cfg); seed != (schedulerTimeSeed{}) {
			t.Fatalf("expected a zero seed, got %+v", seed)
		}
		if after := read(t, cfg); after != before {
			t.Fatalf("config was modified:\n%s", after)
		}
	})

	t.Run("missing or empty config path", func(t *testing.T) {
		stubCrontabLines(t, []string{"0 21 * * * /usr/local/bin/proxsave --backup"}, nil)
		if seed := seedSchedulerTimeFromCrontab(context.Background(), ""); seed != (schedulerTimeSeed{}) {
			t.Fatalf("empty path -> %+v, want a zero seed", seed)
		}
		missing := filepath.Join(t.TempDir(), "absent.env")
		if seed := seedSchedulerTimeFromCrontab(context.Background(), missing); seed != (schedulerTimeSeed{}) {
			t.Fatalf("missing file -> %+v, want a zero seed", seed)
		}
	})
}

// TestSeededTimeDrivesInstallSchedule pins the install-path consequence: once the
// crontab time has been adopted, the keep-config reinstall rewrites cron at the
// operator's time instead of resetting it to the 02:00 default, and the CLI wizard
// offers that same time as the "Run at" prefill.
func TestSeededTimeDrivesInstallSchedule(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(cfg, []byte("SCHEDULER_MODE=cron\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	stubCrontabLines(t, []string{"0 21 * * * /usr/local/bin/proxsave --backup"}, nil)

	// Before seeding: the pre-0.30 config has no SCHEDULER_TIME, so both the cron
	// rewrite and the prompt fall back to 02:00 -- the bug.
	if got := buildInstallCronSchedule(true, "", cfg); got != cronutil.TimeToSchedule(cronutil.DefaultTime) {
		t.Fatalf("pre-seed schedule = %q, want the %s default", got, cronutil.DefaultTime)
	}

	if seed := seedSchedulerTimeFromCrontab(context.Background(), cfg); seed.Time != "21:00" {
		t.Fatalf("seed.Time = %q, want 21:00", seed.Time)
	}

	if got := buildInstallCronSchedule(true, "", cfg); got != "00 21 * * *" {
		t.Fatalf("post-seed schedule = %q, want \"00 21 * * *\"", got)
	}
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := cronTimeDefault(true, string(data)); got != "21:00" {
		t.Fatalf("cronTimeDefault = %q, want 21:00", got)
	}
}

// TestPrepareBaseTemplateNeverWritesDuringTheInteractivePhase pins where the adopted
// time may land. The seeding PERSISTS SCHEDULER_TIME, so no answer may write
// backup.env while the operator can still walk away: cancelling at the prompt, or
// several screens into the wizard, must leave the host byte-identical.
//
// Edit still has to SEE the adopted time - the wizard prefills "Run at" from the
// template it is handed - so it travels in memory and reaches disk only when the
// wizard rewrites the file at the end.
func TestPrepareBaseTemplateNeverWritesDuringTheInteractivePhase(t *testing.T) {
	const preThirty = "SCHEDULER_MODE=cron\n"

	tests := []struct {
		name        string
		answer      string
		wantPrefill bool
		wantAbort   bool
	}{
		{name: "cancel", answer: "0\n", wantAbort: true},
		{name: "overwrite does not adopt", answer: "1\n"},
		{name: "edit adopts in memory only", answer: "2\n", wantPrefill: true},
		{name: "keep existing defers its write", answer: "3\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := createTempFile(t, preThirty)
			stubCrontabLines(t, []string{"0 21 * * * /usr/local/bin/proxsave --backup"}, nil)

			var tmpl string
			var err error
			captureStdout(t, func() {
				tmpl, _, _, err = prepareBaseTemplate(context.Background(), bufio.NewReader(strings.NewReader(tt.answer)), cfg, nil)
			})
			if tt.wantAbort {
				if !errors.Is(err, errInteractiveAborted) {
					t.Fatalf("expected an interactive abort, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("prepareBaseTemplate: %v", err)
			}

			data, readErr := os.ReadFile(cfg)
			if readErr != nil {
				t.Fatalf("read back: %v", readErr)
			}
			if string(data) != preThirty {
				t.Fatalf("the interactive phase must not write backup.env:\n%s", data)
			}

			if got := cronTimeDefault(true, tmpl) == "21:00"; got != tt.wantPrefill {
				t.Fatalf("template prefill = %v, want %v (tmpl=%q)", got, tt.wantPrefill, tmpl)
			}
		})
	}
}

// TestAdoptCronRunTimeIntoBase pins the ONE adoption helper both front-ends now
// share: which answers adopt the crontab time into the wizard's in-memory base,
// and the rule that the interactive phase must not write backup.env. The adopted
// value is carried in the returned base, and ApplyInstallData persists it only if
// the wizard finishes.
//
// S2: the gate is decision.FromExistingFile, i.e. EDIT ONLY - the CLI's behavior,
// which wins over the TUI's former Keep-OR-Edit gate. "keep existing" therefore
// adopts NOTHING here; that is the signed-off unification, not a weakened test.
// Keep still gets its SCHEDULER_TIME, but at the commit point via
// seedSchedulerTimeFromCrontabFn (install.go / install_tui.go), which is unchanged
// and is now the single place the Keep-path note is logged instead of twice.
func TestAdoptCronRunTimeIntoBase(t *testing.T) {
	const preThirty = "SCHEDULER_MODE=cron\n"

	tests := []struct {
		name       string
		action     installer.ExistingConfigAction
		wantAdopts bool
	}{
		{name: "cancel adopts nothing", action: installer.ExistingConfigCancel},
		{name: "overwrite adopts nothing", action: installer.ExistingConfigOverwrite},
		{name: "edit adopts", action: installer.ExistingConfigEdit, wantAdopts: true},
		{name: "keep existing adopts nothing (S2)", action: installer.ExistingConfigKeepContinue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := filepath.Join(t.TempDir(), "backup.env")
			if err := os.WriteFile(cfg, []byte(preThirty), 0o600); err != nil {
				t.Fatalf("seed config: %v", err)
			}
			stubCrontabLines(t, []string{"0 21 * * * /usr/local/bin/proxsave --backup"}, nil)

			decision, err := installer.ResolveExistingConfigDecision(tt.action, cfg)
			if err != nil {
				t.Fatalf("ResolveExistingConfigDecision error: %v", err)
			}

			// The resolver hands a RAW "" base to every answer except Edit, and
			// ApplySchedulerTimeSeed is a no-op on "" -- so asserting on the returned
			// base alone would be VACUOUS for three of the four rows: it reads false
			// whether the gate is Edit-only or the TUI's former Keep-OR-Edit. Count
			// the derive instead: it is the observable the gate actually controls, and
			// it fails if the gate is removed.
			derives := 0
			origDerive := deriveSchedulerTimeFromCrontabFn
			deriveSchedulerTimeFromCrontabFn = func(ctx context.Context, path string) schedulerTimeSeed {
				derives++
				return origDerive(ctx, path)
			}
			t.Cleanup(func() { deriveSchedulerTimeFromCrontabFn = origDerive })

			base := adoptCronRunTimeIntoBase(context.Background(), decision, cfg, nil)
			if got := strings.Contains(base, "SCHEDULER_TIME=21:00"); got != tt.wantAdopts {
				t.Fatalf("adopted = %v, want %v (base=%q)", got, tt.wantAdopts, base)
			}
			wantDerives := 0
			if tt.wantAdopts {
				wantDerives = 1
			}
			if derives != wantDerives {
				t.Fatalf("crontab derive calls = %d, want %d: only Edit may consult the crontab (S2)", derives, wantDerives)
			}

			data, err := os.ReadFile(cfg)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if string(data) != preThirty {
				t.Fatalf("deriving must not write backup.env:\n%s", data)
			}
		})
	}
}

// TestApplyConfigUpgradeAdoptsCrontabRunTime is the end-to-end regression pin for
// the reported bug: the 0.30 template merge must not invent SCHEDULER_TIME=02:00
// on a host whose crontab says 21:00, because the daemon auto-migration deletes
// that cron line moments later.
func TestApplyConfigUpgradeAdoptsCrontabRunTime(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "backup.env")
	// A pre-0.30 shape: no SCHEDULER_TIME, and the run time recorded only in cron.
	if err := os.WriteFile(cfg, []byte("BACKUP_PATH="+dir+"\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	stubCrontabLines(t, []string{"0 21 * * * /usr/local/bin/proxsave --backup"}, nil)

	result, err := applyConfigUpgrade(context.Background(), cfg, dir)
	if err != nil {
		t.Fatalf("applyConfigUpgrade: %v", err)
	}
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "SCHEDULER_TIME=21:00") {
		t.Fatalf("merged config lost the operator run time:\n%s", content)
	}
	if strings.Contains(content, "SCHEDULER_TIME="+cronutil.DefaultTime) {
		t.Fatalf("merged config carries the template default:\n%s", content)
	}
	for _, key := range result.MissingKeys {
		if key == "SCHEDULER_TIME" {
			t.Error("SCHEDULER_TIME was reported as added; the seeded value should have been preserved instead")
		}
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "21:00") {
		t.Errorf("adoption note missing from the upgrade warnings: %v", result.Warnings)
	}
}

// TestApplyConfigUpgradeKeepsExplicitSchedulerTime pins the precedence rule end to
// end: a time the operator actually set is never replaced by the crontab.
func TestApplyConfigUpgradeKeepsExplicitSchedulerTime(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "backup.env")
	if err := os.WriteFile(cfg, []byte("BACKUP_PATH="+dir+"\nSCHEDULER_TIME=07:30\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	stubCrontabLines(t, []string{"0 21 * * * /usr/local/bin/proxsave --backup"}, nil)

	result, err := applyConfigUpgrade(context.Background(), cfg, dir)
	if err != nil {
		t.Fatalf("applyConfigUpgrade: %v", err)
	}
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if content := string(data); !strings.Contains(content, "SCHEDULER_TIME=07:30") {
		t.Fatalf("explicit SCHEDULER_TIME was overridden:\n%s", content)
	}
	if strings.Contains(strings.Join(result.Warnings, "\n"), "21:00") {
		t.Errorf("no adoption note expected when the operator set the time: %v", result.Warnings)
	}
}

// TestAdoptCronRunTimeIntoBaseNoteMatchesReality pins that the adoption note is
// only emitted when the value actually reached the base. The note promises "the
// daily run time does not change", but ApplySchedulerTimeSeed discards the seed on
// a blank base -- so logging unconditionally told the operator their 21:00 was kept
// while the wizard went on to offer the 02:00 default. A note the code contradicts
// is worse than silence.
func TestAdoptCronRunTimeIntoBaseNoteMatchesReality(t *testing.T) {
	for _, tt := range []struct {
		name       string
		content    string
		wantSeeded bool
	}{
		{name: "blank base discards the seed, so no note", content: "", wantSeeded: false},
		{name: "real base keeps the seed and the note", content: "SCHEDULER_MODE=cron\n", wantSeeded: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := filepath.Join(t.TempDir(), "backup.env")
			if err := os.WriteFile(cfg, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("seed config: %v", err)
			}
			stubCrontabLines(t, []string{"0 21 * * * /usr/local/bin/proxsave --backup"}, nil)

			decision, err := installer.ResolveExistingConfigDecision(installer.ExistingConfigEdit, cfg)
			if err != nil {
				t.Fatalf("ResolveExistingConfigDecision error: %v", err)
			}
			bootstrap := logging.NewBootstrapLogger()
			base := adoptCronRunTimeIntoBase(context.Background(), decision, cfg, bootstrap)

			if got := strings.Contains(base, "SCHEDULER_TIME=21:00"); got != tt.wantSeeded {
				t.Fatalf("seeded = %v, want %v (base=%q)", got, tt.wantSeeded, base)
			}
			wantEntries := 0
			if tt.wantSeeded {
				wantEntries = 1
			}
			if got := bootstrap.EntryCount(); got != wantEntries {
				t.Fatalf("bootstrap entries = %d, want %d: the adoption note must not outlive the value it describes", got, wantEntries)
			}
		})
	}
}
