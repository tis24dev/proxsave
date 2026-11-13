package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Switching an install to cron removed the daemon unit without reading the crontab
// back, and the cron write reports its failures only as WARNINGs: with a crontab that
// refused the new table the host ended with no scheduler under "Installation
// completed". The crontab is now read back: without the line the daemon is kept and
// the install fails; a first cron install without the line fails too. The line read back
// must be the one written: an older proxsave line left by a refused write, or a line with
// another --config, does not count.
func TestInstallCronSwitchReadsTheCrontabBack(t *testing.T) {
	const olderLine = "0 5 * * * /usr/local/bin/proxsave --backup\n"
	const otherConfig = "0 2 * * * /usr/local/bin/proxsave --backup --config /etc/other/backup.env\n"
	for _, tc := range []struct {
		name     string
		daemon   bool
		writeOK  bool
		wantErr  string
		wantUnit bool
		wantWarn bool
		before   string // proxsave lines already in the crontab
		readBack string // when set, the table crontab installs whatever it was given
	}{
		{"daemon host, cron refused", true, false, "the cron entry could not be written", true, true, "", ""},
		{"first cron install, cron refused", false, false, "the cron entry could not be written, so no scheduler is installed", false, false, "", ""},
		{"daemon host, cron written", true, true, "", false, false, "", ""},
		{"daemon host, cron refused, an older proxsave line left", true, false, "the cron entry could not be written", true, true, olderLine, ""},
		{"daemon host, another --config read back", true, true, "the cron entry could not be written", true, true, "", otherConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "fakebin")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			table := filepath.Join(dir, "crontab-table")
			if err := os.WriteFile(table, []byte("0 4 * * * /root/scripts/other.sh\n"+tc.before), 0o600); err != nil {
				t.Fatal(err)
			}
			write := "cat > /dev/null\necho \"errors in crontab file, can't install.\" >&2\nexit 1\n"
			if tc.writeOK {
				write = "cat > \"" + table + "\"\nexit 0\n"
			}
			if tc.readBack != "" {
				write = "cat > /dev/null\nprintf '%s' '" + tc.readBack + "' > \"" + table + "\"\nexit 0\n"
			}
			crontab := "#!/bin/sh\nif [ \"$1\" = \"-l\" ]; then cat \"" + table + "\"; exit 0; fi\n" + write
			sysLog := filepath.Join(dir, "systemctl.log")
			systemctl := "#!/bin/sh\necho \"$@\" >> \"" + sysLog + "\"\nexit 0\n"
			if err := os.WriteFile(filepath.Join(bin, "crontab"), []byte(crontab), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(systemctl), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")

			globals := filepath.Join(dir, "global")
			if err := os.MkdirAll(globals, 0o755); err != nil {
				t.Fatal(err)
			}
			prevGlobals := globalEntrypointDirs
			globalEntrypointDirs = []string{globals}
			t.Cleanup(func() { globalEntrypointDirs = prevGlobals })

			unit := filepath.Join(dir, "proxsave.service")
			if tc.daemon {
				if err := os.WriteFile(unit, []byte("[Unit]\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			prevUnit := daemonUnitPath
			daemonUnitPath = unit
			t.Cleanup(func() { daemonUnitPath = prevUnit })

			executable := filepath.Join(dir, "proxsave")
			if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
				t.Fatal(err)
			}
			cfgPath := filepath.Join(dir, "backup.env")
			if err := os.WriteFile(cfgPath, []byte("BASE_DIR="+dir+"\nSCHEDULER_MODE=cron\nHEALTHCHECK_ENABLED=false\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			prevLogger := logging.GetDefaultLogger()
			var out bytes.Buffer
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(&out)
			logging.SetDefaultLogger(logger)
			t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

			p := newInstallRuntimePreparation(cfgPath, dir, ExecInfo{ExecPath: executable}, logging.NewBootstrapLogger())
			p.cronSchedule = "0 2 * * *"
			err := p.Finalize(context.Background())

			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if gotErr != tc.wantErr {
				t.Fatalf("Finalize error %q, want %q; log:\n%s", gotErr, tc.wantErr, out.String())
			}
			_, unitErr := os.Stat(unit)
			if (unitErr == nil) != tc.wantUnit {
				t.Fatalf("daemon unit present=%v, want %v; log:\n%s", unitErr == nil, tc.wantUnit, out.String())
			}
			warn := "WARNING  daemon: the cron entry could not be written, so the daemon service was left in place and keeps scheduling the backup."
			if strings.Contains(out.String(), warn) != tc.wantWarn {
				t.Fatalf("WARNING shown=%v, want %v; log:\n%s", !tc.wantWarn, tc.wantWarn, out.String())
			}
		})
	}
}
