// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

// #298: a cron line that invokes ProxSave INDIRECTLY (an operator wrapper script,
// a shell -c, a runner like flock/sudo) must be visible to the daemon migration,
// while every line the narrow command-token matcher deliberately protects must stay
// invisible. The negatives here are the contract: commandTokenMatchesTarget keeps
// its exact semantics and this detector must not become a substring scan by proxy.
//
// The table runs in cronProbeNamesOnly mode so the verdicts are purely lexical and
// cannot depend on what happens to exist on the machine running the test; the
// content probe has its own test below.
func TestIndirectProxsaveCronRefs(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		// The reported host (#298): a wrapper that checks the NAS mount is CIFS
		// before invoking ProxSave. Basename "proxsave-nas-guard" -> not canonical.
		{"wrapper named after proxsave", "00 02 * * * /usr/local/sbin/proxsave-nas-guard", true},
		{"wrapper with underscore", "@daily /usr/local/sbin/proxsave_wrapper.sh", true},
		{"wrapper with proxsave as a trailing component", "@daily /usr/local/sbin/wrap-proxsave.sh", true},
		{"script inside the install tree", "0 1 * * * /opt/proxsave/script/proxmox-backup.sh", true},
		{"shell -c naming the binary", "0 3 * * * /bin/bash -c 'mountpoint -q /mnt/nas && /usr/local/bin/proxsave --backup'", true},
		{"flock wrapping the binary", "0 3 * * * /usr/bin/flock -n /var/lock/ps.lock /usr/local/bin/proxsave --backup", true},
		{"sudo running a non-canonical install", "0 3 * * * /usr/bin/sudo /opt/proxsave/proxsave --backup", true},

		// Canonical entries are NOT indirect: dropCanonicalCronLines owns them, and
		// reporting them would turn every ordinary host into a refusal.
		{"canonical proxsave entry", "0 2 * * * /usr/local/bin/proxsave --backup", false},
		{"canonical legacy entry", "0 2 * * * /usr/local/bin/proxmox-backup --backup", false},

		// Stock PBS binaries live on nearly every target host. Flagging them would
		// refuse the migration almost everywhere.
		{"PBS client", "0 1 * * * /usr/bin/proxmox-backup-client backup root.pxar:/etc", false},
		{"PBS proxy", "0 2 * * * /usr/sbin/proxmox-backup-proxy", false},
		{"PBS config directory", "0 1 * * * /etc/proxmox-backup/hook.sh", false},

		// Existing guarantees that must not regress (TestFilterCronLines pins them
		// for the removal path; they must not become refusals either).
		{"prefix-sharing binary", "0 2 * * * /usr/local/bin/proxsavex", false},
		{"proxmox-backup-new", "0 2 * * * /usr/local/bin/proxmox-backup-new", false},
		{"proxmox-backup-dog", "0 2 * * * /usr/bin/proxmox-backup-dog", false},
		{"binary passed only as an argument", "0 4 * * * /usr/bin/cp /usr/local/bin/proxsave /backup/proxsave.bak", false},
		{"legacy script passed only as an argument", "0 5 * * * /bin/echo /opt/proxsave/script/proxmox-backup.sh", false},
		{"operator PBS job", "0 12 * * * /mnt/pve/nas/scripts/proxmox/proxmox-backup-client/backup_folders-nightly.sh 1.2.3.4 h1 /mnt/pve/nas", false},
		{"unrelated job", "0 2 * * * /usr/bin/rsync /a /b", false},
		{"commented-out wrapper", "# 00 02 * * * /usr/local/sbin/proxsave-nas-guard", false},
		{"env assignment", "MAILTO=root", false},
		{"blank line", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refs := indirectProxsaveCronRefs([]string{tt.line}, cronProbeNamesOnly)
			if got := len(refs) > 0; got != tt.want {
				t.Fatalf("indirectProxsaveCronRefs(%q) flagged=%v, want %v (refs=%v)", tt.line, got, tt.want, refs)
			}
			if len(refs) == 1 {
				if refs[0].Line != strings.TrimSpace(tt.line) {
					t.Errorf("Line = %q, want the crontab line verbatim", refs[0].Line)
				}
				if refs[0].Reason == "" {
					t.Error("every finding must carry an operator-facing reason")
				}
			}
		})
	}

	// wrapperCronLines is the plain-lines adapter applyCronMode's fallback consults.
	// It must agree with the detector and hand back the line verbatim, because that is
	// what gets echoed to the operator on a revert.
	const wrapper = "00 02 * * * /usr/local/sbin/proxsave-nas-guard"
	got := wrapperCronLines([]string{"0 6 * * * /usr/bin/rsync /a /b", wrapper, "0 2 * * * /usr/local/bin/proxsave --backup"})
	if len(got) != 1 || got[0] != wrapper {
		t.Fatalf("wrapperCronLines must return exactly the wrapper line verbatim, got %v", got)
	}
}

// The content probe is the last resort for a wrapper whose NAME gives nothing away
// (the residual silent-duplicate case). It must fire on a small text script that
// calls the binary by path, and must stay off for everything it cannot read as a
// script - otherwise every ordinary cron command on the host would be "suspicious".
func TestIndirectProxsaveCronRefsContentProbe(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, content []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, content, 0o700); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}

	wrapper := write("nas-guard", []byte("#!/bin/sh\nmountpoint -q /mnt/nas || exit 0\nexec /usr/local/bin/proxsave --backup\n"))
	neutral := write("rotate-logs", []byte("#!/bin/sh\nlogrotate -f /etc/logrotate.conf\n"))
	mention := write("notes.sh", []byte("#!/bin/sh\n# we used to run proxsave here, now handled elsewhere\ntrue\n"))
	binary := write("compiled", append([]byte("\x7fELF\x00\x00/usr/local/bin/proxsave"), 0x00))
	oversized := write("huge.sh", append([]byte("#!/bin/sh\n/usr/local/bin/proxsave --backup\n"), make([]byte, maxCronWrapperProbeBytes)...))

	flagged := func(path string, probe bool) bool {
		return len(indirectProxsaveCronRefs([]string{"0 2 * * * " + path}, probe)) > 0
	}

	if !flagged(wrapper, cronProbeReadScripts) {
		t.Error("a neutrally named wrapper that calls the binary by path must be detected")
	}
	if flagged(wrapper, cronProbeNamesOnly) {
		t.Error("cronProbeNamesOnly must not read the script (the wizard and --upgrade-config-json rely on it)")
	}
	if flagged(neutral, cronProbeReadScripts) {
		t.Error("an unrelated script must not be flagged")
	}
	if flagged(mention, cronProbeReadScripts) {
		t.Error("a bare prose mention of proxsave must not block a migration; only a path reference counts")
	}
	if flagged(binary, cronProbeReadScripts) {
		t.Error("a compiled binary must be skipped, not scanned")
	}
	if flagged(oversized, cronProbeReadScripts) {
		t.Error("a file past maxCronWrapperProbeBytes must be left unread")
	}
	if flagged(filepath.Join(dir, "does-not-exist"), cronProbeReadScripts) {
		t.Error("an unreadable command must not be treated as suspicious")
	}
}

// #298 on the exact path that caused it: the unattended --upgrade retrofit must
// REFUSE to install the daemon while a wrapper cron entry is live, and the refusal
// must be a true no-op (host still on cron, backup.env untouched), never a half
// migration. The two controls below are as important as the refusal itself: an
// ordinary host must migrate exactly as it did before, and a host with no readable
// crontab at all (nothing to collide with) must not be blocked.
func TestMaybeAutoMigrateDaemonRefusesIndirectCronEntry(t *testing.T) {
	origRead := crontabReadLinesFn
	origApply := applyDaemonModeFn
	t.Cleanup(func() {
		crontabReadLinesFn = origRead
		applyDaemonModeFn = origApply
	})

	configPath := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(configPath, []byte("SCHEDULER_MODE=cron\nDAEMON_OPT_OUT=false\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	applied := false
	applyDaemonModeFn = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRemovalOutcome, error) {
		applied = true
		return cronRemovalOutcome{Verified: true}, nil
	}
	migrate := func() {
		maybeAutoMigrateDaemon(context.Background(), configPath, "/opt/proxsave", "/usr/local/bin/proxsave", nil)
	}

	// The reported crontab: a wrapper the canonical matcher cannot see.
	crontabReadLinesFn = func(context.Context) ([]string, error) {
		return []string{"00 02 * * * /usr/local/sbin/proxsave-nas-guard"}, nil
	}
	migrate()
	if applied {
		t.Fatal("REGRESSION (#298): the retrofit migrated on top of a wrapper cron entry; the host would run every backup twice")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "SCHEDULER_MODE=cron") || strings.Contains(string(data), "DAEMON_OPT_OUT=true") {
		t.Fatalf("a refusal must leave backup.env alone: no half migration, and no opt-out decided on the operator's behalf:\n%s", data)
	}

	// Control 1: the same host WITHOUT the wrapper still migrates exactly as before.
	crontabReadLinesFn = func(context.Context) ([]string, error) {
		return []string{"00 02 * * * /usr/local/bin/proxsave --backup"}, nil
	}
	migrate()
	if !applied {
		t.Fatal("a plain canonical cron entry must still migrate: the wrapper check must not change the ordinary host")
	}

	// Control 2: an unreadable crontab is not evidence of a wrapper. A host with no
	// cron installed has nothing to collide with and must still be retrofitted.
	applied = false
	crontabReadLinesFn = func(context.Context) ([]string, error) {
		return nil, errors.New("exec: \"crontab\": executable file not found in $PATH")
	}
	migrate()
	if !applied {
		t.Fatal("an unreadable crontab must not block the migration")
	}
}

// #298, second habitat. cronCommandToken reads field 6, which is the COMMAND in a user
// crontab but the USER in a system one, so a wrapper installed in /etc/cron.d parsed as a
// username and matched nothing at all. The detector reads those files now; nothing else
// does, and nothing writes to them.
func TestSystemCronCommandToken(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want string
	}{
		{"five fields plus user plus command", "17 02 * * * root /usr/local/sbin/proxsave-nas-guard", "/usr/local/sbin/proxsave-nas-guard"},
		{"shortcut carries a user too", "@daily root /usr/local/sbin/proxsave-nas-guard", "/usr/local/sbin/proxsave-nas-guard"},
		{"comment", "# 17 02 * * * root /usr/local/sbin/proxsave-nas-guard", ""},
		{"env assignment", "MAILTO=root", ""},
		{"user format is not a system line: the command lands on the user field", "17 02 * * * /usr/local/sbin/proxsave-nas-guard", ""},
		{"shortcut with no command", "@daily root", ""},
		{"blank", "   ", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := systemCronCommandToken(tc.line); got != tc.want {
				t.Fatalf("systemCronCommandToken(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

// cron itself ignores /etc/cron.d entries whose name is not [A-Za-z0-9_-]+, so reporting
// one would refuse a migration over a schedule that never fires.
func TestCronDNameIsActive(t *testing.T) {
	active := []string{"proxsave-guard", "backup_job", "job1"}
	inactive := []string{"", "proxsave.bak", "job.dpkg-dist", ".hidden", "job~"}
	for _, name := range active {
		if !cronDNameIsActive(name) {
			t.Errorf("cronDNameIsActive(%q) = false, want true", name)
		}
	}
	for _, name := range inactive {
		if cronDNameIsActive(name) {
			t.Errorf("cronDNameIsActive(%q) = true, want false", name)
		}
	}
}

// The end-to-end shape: a wrapper in /etc/cron.d is found, tagged with the file it came
// from, and reported with an edit hint that does NOT send the operator to `crontab -e`
// for a file crontab(1) will not touch.
func TestIndirectProxsaveSystemCronRefs(t *testing.T) {
	dir := t.TempDir()
	cronD := filepath.Join(dir, "cron.d")
	if err := os.MkdirAll(cronD, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	systemCrontab := filepath.Join(dir, "crontab")
	write(systemCrontab, "SHELL=/bin/sh\n# comment\n0 6 * * * root /usr/bin/rsync /a /b\n")
	write(filepath.Join(cronD, "proxsave-guard"), "17 02 * * * root /usr/local/sbin/proxsave-nas-guard\n")
	write(filepath.Join(cronD, "ignored.bak"), "17 03 * * * root /usr/local/sbin/proxsave-nas-guard\n")

	orig := systemCronPaths
	t.Cleanup(func() { systemCronPaths = orig })
	systemCronPaths = []string{systemCrontab, cronD}

	refs := indirectProxsaveSystemCronRefs()
	if len(refs) != 1 {
		t.Fatalf("want exactly the one active cron.d wrapper, got %d: %+v", len(refs), refs)
	}
	if refs[0].Command != "/usr/local/sbin/proxsave-nas-guard" {
		t.Errorf("Command = %q", refs[0].Command)
	}
	if refs[0].Source != filepath.Join(cronD, "proxsave-guard") {
		t.Errorf("Source = %q, want the file the line came from", refs[0].Source)
	}
	if desc := describeIndirectCronRefs(refs); len(desc) != 1 || !strings.Contains(desc[0], cronD) {
		t.Errorf("the description must name the file: %v", desc)
	}
	if hint := cronRefEditHint(refs); strings.Contains(hint, "crontab -e") {
		t.Errorf("a system-cron finding must not be pointed at 'crontab -e': %q", hint)
	}
	if hint := cronRefEditHint([]indirectCronRef{{}}); !strings.Contains(hint, "crontab -e") {
		t.Errorf("a user-crontab finding must still be pointed at 'crontab -e': %q", hint)
	}
}

// The system files are READ, never written, and never fed to the removal or the seeding.
// This pins the boundary that keeps ProxSave from editing files it did not place.
func TestSystemCronIsReadOnlyAndOutsideRemovalAndSeeding(t *testing.T) {
	line := "17 02 * * * root /usr/local/sbin/proxsave-nas-guard"
	if kept := dropCanonicalCronLines([]string{line}, cronCorrectPaths("/usr/local/bin/proxsave")); len(kept) != 1 {
		t.Fatal("a system-format line must never be dropped by the user-crontab remover")
	}
	if _, ok := schedulerTimeFromCronLines([]string{line}); ok {
		t.Fatal("SCHEDULER_TIME must not be seeded from a system-format line")
	}
	if got := wrapperCronLines([]string{line}); len(got) != 0 {
		t.Fatalf("wrapperCronLines reads the USER crontab format only, got %v", got)
	}
}
