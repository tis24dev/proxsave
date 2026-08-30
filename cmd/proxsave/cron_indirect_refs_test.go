// Package main contains the proxsave command entrypoint.
package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// The detector's verdict on a NAMED command depends on whether that command can be READ: the
// content probe decides whenever it can, and only when it cannot does the name get a vote
// (TestNameRuleOnlyFiresWhenTheScriptCannotBeRead). A fixture that hardcodes an absolute host
// path therefore hands the verdict to the machine running the suite. Seven tests did, and they
// passed only because no /usr/local/sbin/proxsave-nas-guard happens to exist here: the same
// cron line classifies the other way the moment a host has one, and the suite would then fail
// on a real operator's machine for a reason that has nothing to do with the code.
//
// absentWrapper puts that wrapper under the test's own temp dir, which the framework
// guarantees is fresh and empty, so "could not be read" is a property of the fixture.
func absentWrapper(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "proxsave-nas-guard")
}

// unrelatedCommand is the ordinary operator job no rule may fire on. It lives under the temp
// dir for the same reason: the probe opens whatever the cron line names, and /usr/bin/rsync is
// the host's file, not the test's.
func unrelatedCommand(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rsync")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexec rsync \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

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
	wrapper := "00 02 * * * " + absentWrapper(t)
	got := wrapperCronLines([]string{"0 6 * * * " + unrelatedCommand(t) + " /a /b", wrapper, "0 2 * * * /usr/local/bin/proxsave --backup"})
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
	origPaths := systemCronPaths
	t.Cleanup(func() {
		crontabReadLinesFn = origRead
		applyDaemonModeFn = origApply
		systemCronPaths = origPaths
	})
	// detectIndirectProxsaveCron unions the root crontab with the SYSTEM habitat, and
	// systemCronPaths points at the real /etc. Without this the verdict of the test that
	// decides whether an UPGRADE IS BLOCKED depends on the cron.d of whatever machine runs
	// the suite: plant a proxsave-named entry there and this test fails for a reason that
	// has nothing to do with the code. An empty tree is the ordinary host.
	systemCronPaths = []string{filepath.Join(t.TempDir(), "absent")}

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
	wrapperLine := "00 02 * * * " + absentWrapper(t)
	crontabReadLinesFn = func(context.Context) ([]string, error) {
		return []string{wrapperLine}, nil
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
	wrapperCmd := absentWrapper(t)
	systemCrontab := filepath.Join(dir, "crontab")
	write(systemCrontab, "SHELL=/bin/sh\n# comment\n0 6 * * * root "+unrelatedCommand(t)+" /a /b\n")
	write(filepath.Join(cronD, "proxsave-guard"), "17 02 * * * root "+wrapperCmd+"\n")
	write(filepath.Join(cronD, "ignored.bak"), "17 03 * * * root "+wrapperCmd+"\n")

	orig := systemCronPaths
	t.Cleanup(func() { systemCronPaths = orig })
	systemCronPaths = []string{systemCrontab, cronD}

	refs := indirectProxsaveSystemCronRefs()
	if len(refs) != 1 {
		t.Fatalf("want exactly the one active cron.d wrapper, got %d: %+v", len(refs), refs)
	}
	if refs[0].Command != wrapperCmd {
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

// The advisory is the entire remedy on this path, so its text is the deliverable. Its
// hardest constraint is NEGATIVE: it may not assert anything it cannot check from here.
// It used to claim the /etc entry stood "alongside the cron line just written at
// SCHEDULER_TIME" and to tell the operator to remove one of the two, and neither was
// knowable - migrateLegacyCronEntries writes nothing on any of four early returns, one of
// which is reached deterministically when `crontab -l` fails, because it re-runs the very
// read that already failed. The notice then described a duplicate that did not exist and
// pointed the operator at the only schedule the host had left.
func TestSystemCronScheduleAdvisory(t *testing.T) {
	if got := systemCronScheduleAdvisory(nil); got != nil {
		t.Fatalf("no finding must produce no output at all, got %v", got)
	}

	refs := []indirectCronRef{{
		Line:    "17 02 * * * root /usr/local/sbin/proxsave-nas-guard",
		Command: "/usr/local/sbin/proxsave-nas-guard",
		Reason:  "its command \"proxsave-nas-guard\" is named after proxsave",
		Source:  "/etc/cron.d/proxsave-guard",
	}}
	joined := strings.Join(systemCronScheduleAdvisory(refs), "\n")
	for _, want := range []string{
		"/etc/cron.d/proxsave-guard",                          // WHERE: a file, findable
		"17 02 * * * root /usr/local/sbin/proxsave-nas-guard", // WHAT: the line verbatim
		"named after proxsave",                                // WHY: the rule that fired
		"never edits files it did not place",                  // WHAT ProxSave did not do
		"/etc unchanged",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the advisory must contain %q:\n%s", want, joined)
		}
	}

	// Everything below is a claim this function cannot support. Each of these strings
	// appeared in the version that shipped a lie; none may come back.
	for _, forbidden := range []string{
		"just written",   // no way to know migrateLegacyCronEntries wrote anything
		"runs twice",     // no way to know the /etc entry runs a backup at all
		"one of the two", // the instruction that could unschedule the host
		"was removed",    // ProxSave never edits /etc
		"were removed",
		"disabled it",
	} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("the advisory may not claim %q, which it cannot check from here:\n%s", forbidden, joined)
		}
	}
}

// The shape the detector deliberately drops, in the one habitat where dropping it is
// wrong. indirectProxsaveCronRefsWithToken skips a line whose command token IS the binary
// as "canonical, dropCanonicalCronLines owns it" - true of the root crontab, false of
// /etc, which dropCanonicalCronLines never reads and by design never will. So
//
//	0 2 * * * root /usr/local/bin/proxsave --backup   [/etc/cron.d/proxsave]
//
// was a live ProxSave backup schedule no code path in this package could see, remove or
// report, and it is the likeliest way a host is scheduled from /etc at all: the installed
// line moved into a file the operator or a config-management tool manages.
//
// systemCronProxsaveRefs reports it, and detectIndirectProxsaveCron carries it into the
// unattended --upgrade refusal. indirectProxsaveSystemCronRefs must NOT: keeping the two
// views distinct is what makes it visible, the day the heuristics are widened, which of
// the two blast radiuses grew.
func TestSystemCronProxsaveRefsSeesADirectProxsaveLine(t *testing.T) {
	dir := t.TempDir()
	cronD := filepath.Join(dir, "cron.d")
	if err := os.MkdirAll(cronD, 0o755); err != nil {
		t.Fatal(err)
	}
	systemCrontab := filepath.Join(dir, "crontab")
	if err := os.WriteFile(systemCrontab, []byte("0 6 * * * root "+unrelatedCommand(t)+" /a /b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cronD, "proxsave"), []byte("0 2 * * * root /usr/local/bin/proxsave --backup\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := systemCronPaths
	t.Cleanup(func() { systemCronPaths = orig })
	systemCronPaths = []string{systemCrontab, cronD}

	refs := systemCronProxsaveRefs()
	if len(refs) != 1 {
		t.Fatalf("the revert advisory must see the direct proxsave line, got %d: %+v", len(refs), refs)
	}
	if refs[0].Command != "/usr/local/bin/proxsave" {
		t.Errorf("Command = %q, want the binary itself", refs[0].Command)
	}
	if refs[0].Source != filepath.Join(cronD, "proxsave") {
		t.Errorf("Source = %q, want the file the line came from", refs[0].Source)
	}
	if refs[0].Reason == "" {
		t.Error("every finding must carry an operator-facing reason")
	}

	if indirect := indirectProxsaveSystemCronRefs(); len(indirect) != 0 {
		t.Fatalf("the --upgrade refusal predicate must be unchanged by this: a direct line is not an INDIRECT reference, got %+v", indirect)
	}

	// And the unrelated operator line in the system crontab stays unflagged under both
	// views: the direct rule is commandTokenMatchesTarget, not a substring scan.
	systemCronPaths = []string{systemCrontab}
	if refs := systemCronProxsaveRefs(); len(refs) != 0 {
		t.Fatalf("an unrelated system-cron job must not be reported, got %+v", refs)
	}
}

// A symlinked /etc/cron.d entry was silently invisible: os.Stat follows the link so the
// entry passed the regular-file and size gates, then safefs.OpenFileUnderRoot refused the
// absolute-symlink final component and the error was swallowed by the same fail-quiet rule
// that covers a missing /etc/cron.d. Cron loads that entry regardless, and a
// config-management tool is both the named cause of a ProxSave schedule under /etc and the
// thing most likely to place its files there as links into a package tree.
func TestSystemCronFollowsSymlinkedEntries(t *testing.T) {
	dir := t.TempDir()
	cronD := filepath.Join(dir, "cron.d")
	pkg := filepath.Join(dir, "pkg")
	for _, d := range []string{cronD, pkg} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wrapperCmd := absentWrapper(t)
	target := filepath.Join(pkg, "proxsave.cron")
	if err := os.WriteFile(target, []byte("17 02 * * * root "+wrapperCmd+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An ABSOLUTE symlink, which is precisely the shape OpenFileUnderRoot refuses.
	if err := os.Symlink(target, filepath.Join(cronD, "proxsave-guard")); err != nil {
		t.Skipf("symlinks unavailable on this filesystem: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "crontab"), []byte("0 6 * * * root "+unrelatedCommand(t)+" /a /b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := systemCronPaths
	t.Cleanup(func() { systemCronPaths = orig })
	systemCronPaths = []string{filepath.Join(dir, "crontab"), cronD}

	refs := indirectProxsaveSystemCronRefs()
	if len(refs) != 1 {
		t.Fatalf("a symlinked cron.d entry must be scanned like any other, got %d: %+v", len(refs), refs)
	}
	if refs[0].Command != wrapperCmd {
		t.Errorf("Command = %q", refs[0].Command)
	}
	// The finding is reported at the path CRON knows it by, not at the link target: that is
	// the name the operator has to edit, and the one that appears in /etc/cron.d.
	if want := filepath.Join(cronD, "proxsave-guard"); refs[0].Source != want {
		t.Errorf("Source = %q, want the cron.d path %q", refs[0].Source, want)
	}
}

// resolveSystemCronPath must widen nothing on its own: a plain file, a broken link and a
// link to something that is not an ordinary small file all fall back to the original path
// so the caller's existing guards decide.
func TestResolveSystemCronPathOnlyFollowsRealFiles(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveSystemCronPath(plain); got != plain {
		t.Errorf("a regular file must be returned unchanged, got %q", got)
	}
	if got := resolveSystemCronPath(filepath.Join(dir, "absent")); got != filepath.Join(dir, "absent") {
		t.Errorf("a missing path must be returned unchanged, got %q", got)
	}
	broken := filepath.Join(dir, "broken")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), broken); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := resolveSystemCronPath(broken); got != broken {
		t.Errorf("a broken link must be returned unchanged, got %q", got)
	}
	toDir := filepath.Join(dir, "todir")
	if err := os.Symlink(dir, toDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := resolveSystemCronPath(toDir); got != toDir {
		t.Errorf("a link to a directory must be returned unchanged, got %q", got)
	}
}

// The unattended --upgrade refusal must see a DIRECT proxsave line under /etc. That shape
// is the likeliest way a host ends up scheduled from there, dropCanonicalCronLines never
// reads those files, and installing the daemon on top of it is issue #298 again on the same
// path. It was deliberately withheld from this predicate at first; this pins that it is in.
func TestDetectIndirectProxsaveCronSeesADirectSystemCronLine(t *testing.T) {
	origRead := crontabReadLinesFn
	origPaths := systemCronPaths
	t.Cleanup(func() {
		crontabReadLinesFn = origRead
		systemCronPaths = origPaths
	})
	crontabReadLinesFn = func(context.Context) ([]string, error) { return nil, nil }

	dir := t.TempDir()
	cronD := filepath.Join(dir, "cron.d")
	if err := os.MkdirAll(cronD, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cronD, "proxsave"), []byte("0 2 * * * root /usr/local/bin/proxsave --backup\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	systemCronPaths = []string{filepath.Join(dir, "absent-crontab"), cronD}

	refs, err := detectIndirectProxsaveCron(context.Background())
	if err != nil {
		t.Fatalf("detectIndirectProxsaveCron: %v", err)
	}
	if len(refs) != 1 || refs[0].Command != "/usr/local/bin/proxsave" {
		t.Fatalf("the refusal predicate must see a direct proxsave line under /etc, got %+v", refs)
	}
}

// A host scheduled from /etc/cron.d used to lose its run time: SCHEDULER_TIME stayed at the
// 02:00 default, so a backup that ran at 05:00 for years silently moved to 02:00 the moment
// the daemon took over. The adoption reads that habitat now.
//
// Direct lines ONLY. A run time is written into backup.env as the host's schedule, so it may
// only be inherited from a line whose command IS the proxsave binary and whose schedule is an
// unambiguous single daily time. A wrapper's schedule belongs to a script this code did not
// write and cannot interpret, so it is reported and never adopted.
func TestSchedulerTimeAdoptedFromSystemCron(t *testing.T) {
	write := func(t *testing.T, dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pin := func(t *testing.T, cronD string) {
		t.Helper()
		orig := systemCronPaths
		t.Cleanup(func() { systemCronPaths = orig })
		systemCronPaths = []string{filepath.Join(cronD, "..", "absent-crontab"), cronD}
	}

	t.Run("direct proxsave line: adopted", func(t *testing.T) {
		d := t.TempDir()
		write(t, d, "proxsave", "0 5 * * * root /usr/local/bin/proxsave --backup\n")
		pin(t, d)
		refs := systemCronDirectProxsaveLines()
		if len(refs) != 1 {
			t.Fatalf("want the direct line, got %+v", refs)
		}
		if got := cron.ScheduleToTime(refs[0].Line); got != "05:00" {
			t.Fatalf("the adopted time must be 05:00, got %q", got)
		}
	})

	t.Run("wrapper line: reported elsewhere, never adopted here", func(t *testing.T) {
		d := t.TempDir()
		write(t, d, "guard", "17 02 * * * root /usr/local/sbin/proxsave-nas-guard\n")
		pin(t, d)
		if refs := systemCronDirectProxsaveLines(); len(refs) != 0 {
			t.Fatalf("a wrapper schedule must never be adopted, got %+v", refs)
		}
	})

	t.Run("no script probe: a neutrally named script is not read", func(t *testing.T) {
		d := t.TempDir()
		script := filepath.Join(d, "nas-guard")
		if err := os.WriteFile(script, []byte("#!/bin/sh\n/usr/local/bin/proxsave --backup\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, d, "maint", "0 3 * * * root "+script+"\n")
		pin(t, d)
		if refs := systemCronDirectProxsaveLines(); len(refs) != 0 {
			t.Fatalf("the adoption must not probe script contents, got %+v", refs)
		}
	})
}

// The adoption itself, through the function the install and upgrade paths call. The user
// crontab keeps priority: it is the table ProxSave owns and rewrites, so a time found there
// is the one it is about to reinstate. /etc is consulted only when that yields nothing.
func TestDeriveSchedulerTimeReadsSystemCron(t *testing.T) {
	setup := func(t *testing.T, sysLine string, userLines []string) (string, string) {
		t.Helper()
		d := t.TempDir()
		cronD := filepath.Join(d, "cron.d")
		if err := os.MkdirAll(cronD, 0o755); err != nil {
			t.Fatal(err)
		}
		if sysLine != "" {
			if err := os.WriteFile(filepath.Join(cronD, "proxsave"), []byte(sysLine+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cp := filepath.Join(d, "backup.env")
		if err := os.WriteFile(cp, []byte("BACKUP_PATH=/x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		origPaths, origRead := systemCronPaths, crontabReadLinesFn
		t.Cleanup(func() { systemCronPaths, crontabReadLinesFn = origPaths, origRead })
		systemCronPaths = []string{filepath.Join(d, "absent-crontab"), cronD}
		crontabReadLinesFn = func(context.Context) ([]string, error) { return userLines, nil }
		return cp, cronD
	}

	t.Run("empty user crontab: time comes from /etc", func(t *testing.T) {
		cp, cronD := setup(t, "0 5 * * * root /usr/local/bin/proxsave --backup", nil)
		seed := deriveSchedulerTimeFromCrontab(context.Background(), cp)
		if seed.Time != "05:00" {
			t.Fatalf("want 05:00 adopted from /etc, got %q (note %q)", seed.Time, seed.Note)
		}
		if !strings.Contains(seed.Note, cronD) {
			t.Errorf("the note must name the file the time came from, got %q", seed.Note)
		}
	})

	t.Run("user crontab wins over /etc", func(t *testing.T) {
		cp, _ := setup(t, "0 5 * * * root /usr/local/bin/proxsave --backup",
			[]string{"30 21 * * * /usr/local/bin/proxsave --backup"})
		seed := deriveSchedulerTimeFromCrontab(context.Background(), cp)
		if seed.Time != "21:30" {
			t.Fatalf("the table ProxSave owns must win, got %q", seed.Time)
		}
	})

	t.Run("two /etc lines at different times: adopt nothing", func(t *testing.T) {
		d := t.TempDir()
		cronD := filepath.Join(d, "cron.d")
		if err := os.MkdirAll(cronD, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, line := range map[string]string{
			"proxsave-a": "0 5 * * * root /usr/local/bin/proxsave --backup",
			"proxsave-b": "0 6 * * * root /usr/local/bin/proxsave --backup",
		} {
			if err := os.WriteFile(filepath.Join(cronD, name), []byte(line+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cp := filepath.Join(d, "backup.env")
		if err := os.WriteFile(cp, []byte("BACKUP_PATH=/x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		origPaths, origRead := systemCronPaths, crontabReadLinesFn
		t.Cleanup(func() { systemCronPaths, crontabReadLinesFn = origPaths, origRead })
		systemCronPaths = []string{filepath.Join(d, "absent-crontab"), cronD}
		crontabReadLinesFn = func(context.Context) ([]string, error) { return nil, nil }

		seed := deriveSchedulerTimeFromCrontab(context.Background(), cp)
		if seed.Time != "" {
			t.Fatalf("two different times are ambiguous; nothing may be adopted, got %q", seed.Time)
		}
	})

	t.Run("explicit SCHEDULER_TIME is never overridden", func(t *testing.T) {
		cp, _ := setup(t, "0 5 * * * root /usr/local/bin/proxsave --backup", nil)
		if err := os.WriteFile(cp, []byte("SCHEDULER_TIME=23:15\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if seed := deriveSchedulerTimeFromCrontab(context.Background(), cp); seed.Time != "" {
			t.Fatalf("an operator value must win over every habitat, got %q", seed.Time)
		}
	})
}

// The --daemon-setup warning states what was found and what ProxSave did not do, and stops
// there. It used to close with "Remove/disable entries for daemon-only scheduling: ...",
// which tells the operator what to do with their own host: if they want the wrapper and the
// daemon both, that is their call and ProxSave's job ended at saying the two coexist.
func TestWarnIndirectProxsaveCronOnDaemonInstallStatesFactsOnly(t *testing.T) {
	origRead, origPaths := crontabReadLinesFn, systemCronPaths
	t.Cleanup(func() { crontabReadLinesFn, systemCronPaths = origRead, origPaths })
	wrapperLine := "30 02 * * * " + absentWrapper(t)
	crontabReadLinesFn = func(context.Context) ([]string, error) {
		return []string{wrapperLine}, nil
	}
	systemCronPaths = []string{filepath.Join(t.TempDir(), "absent")}

	orig := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(orig) })
	var buf bytes.Buffer
	def := logging.New(types.LogLevelDebug, false)
	def.SetOutput(&buf)
	logging.SetDefaultLogger(def)

	warnIndirectProxsaveCronOnDaemonInstall(context.Background(), nil)
	out := buf.String()

	for _, want := range []string{
		"unmanaged cron line(s) still schedule ProxSave", // WHAT was found
		wrapperLine,            // the line verbatim
		"named after proxsave", // WHY it matched
		"NOT removed",          // what ProxSave did not do
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the warning must state %q, out=%q", want, out)
		}
	}

	// ONE problem, ONE warning. Three WARNING lines for a single fact read as three problems
	// in the run's "WARNINGS/ERRORS DURING RUN (warnings=N)" recap, and that count is what an
	// operator scans. The header and the findings belong below it.
	if got := strings.Count(out, "WARNING"); got != 1 {
		t.Errorf("want exactly one WARNING line, got %d, out=%q", got, out)
	}
	if got := strings.Count(out, "INFO"); got != 2 {
		t.Errorf("the header and the finding must be INFO, got %d such lines, out=%q", got, out)
	}
	// The WARNING has to stand alone: DEBUG_LEVEL=warning hides the two INFO lines, so it
	// carries the count itself rather than leaning on a header the operator may not see.
	warnLine := warningLine(t, out)
	if !strings.Contains(warnLine, "1 unmanaged cron line(s)") {
		t.Errorf("the WARNING must repeat the count so it survives DEBUG_LEVEL=warning, got %q", warnLine)
	}
	for _, forbidden := range []string{
		"Remove/disable",
		"crontab -e",
		"an editor for the files",
	} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the warning must not instruct the operator (%q), out=%q", forbidden, out)
		}
	}
}

// The name rule is a FALLBACK, not evidence. ProxSave only ever installs a binary called
// exactly "proxsave", so "proxsave-anything" is by definition the operator's own file and
// its name says nothing about what it does. The three behavioural rules - install tree,
// runner, script content - are what actually observe a ProxSave invocation.
//
// So when the script can be read, the reading decides: a wrapper that calls the binary is
// flagged by content, and one that does not is left alone however it is named. The name only
// gets a vote when nothing could be read - an unreadable file, a compiled binary, a command
// on a stalled mount - where it is the last thing standing between us and issue #298.
func TestNameRuleOnlyFiresWhenTheScriptCannotBeRead(t *testing.T) {
	d := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		p := filepath.Join(d, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	wrapper := write("proxsave-nas-guard", "#!/bin/sh\nmountpoint -q /mnt/nas || exit 1\n/usr/local/bin/proxsave --backup\n")
	exporter := write("proxsave-metrics-exporter", "#!/bin/sh\ncurl -s localhost:9100/metrics > /var/lib/x.prom\n")
	missing := filepath.Join(d, "proxsave-compiled-thing")

	flagged := func(path string, probe bool) (bool, string) {
		refs := indirectProxsaveCronRefs([]string{"30 02 * * * " + path}, probe)
		if len(refs) == 0 {
			return false, ""
		}
		return true, refs[0].Reason
	}

	t.Run("readable and calls proxsave: flagged, by content", func(t *testing.T) {
		ok, reason := flagged(wrapper, cronProbeReadScripts)
		if !ok {
			t.Fatal("the #298 wrapper must still be flagged")
		}
		if !strings.Contains(reason, "calls the proxsave binary") {
			t.Fatalf("the reason must be the content probe, not the name: %q", reason)
		}
	})

	t.Run("readable and does NOT call proxsave: not flagged", func(t *testing.T) {
		if ok, reason := flagged(exporter, cronProbeReadScripts); ok {
			t.Fatalf("a readable script that never calls proxsave must not be flagged on its name alone: %q", reason)
		}
	})

	t.Run("cannot be read: the name is the fallback", func(t *testing.T) {
		ok, reason := flagged(missing, cronProbeReadScripts)
		if !ok {
			t.Fatal("an unreadable proxsave-named command must still be flagged")
		}
		if !strings.Contains(reason, "named after proxsave") {
			t.Fatalf("the reason must be the name fallback: %q", reason)
		}
	})

	t.Run("lexical-only mode keeps the name rule", func(t *testing.T) {
		if ok, _ := flagged(exporter, cronProbeNamesOnly); !ok {
			t.Fatal("with no probe available the name is all there is, so it must still fire")
		}
	})
}

// One refusal, one WARNING. Five warning lines for a single decision read as five problems
// in the run's "WARNINGS/ERRORS DURING RUN (warnings=N)" recap, and that count is what an
// operator scans. The findings and the way forward sit below at INFO.
//
// warningLine returns the ONE WARNING line from captured log output, without the lines that
// follow it.
//
// Slicing from strings.Index(out, "WARNING") to the end of the buffer is not the same thing
// and was the defect: it hands back the WARNING plus every INFO line printed after it, so an
// assertion meant to prove the verdict stands alone under DEBUG_LEVEL=warning passes just as
// happily when the words it looks for have moved down into a line that level hides.
func warningLine(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "WARNING") {
			return line
		}
	}
	t.Fatalf("no WARNING line in output: %q", out)
	return ""
}

// The verdict line has to stand alone, because DEBUG_LEVEL=warning (internal/cli/args.go)
// hides the INFO lines: it carries REFUSED, the consequence, and the fact that nothing
// changed, so an operator reading only warnings still learns the migration did not happen.
func TestMaybeAutoMigrateDaemonRefusalUsesOneWarning(t *testing.T) {
	origRead, origApply, origPaths := crontabReadLinesFn, applyDaemonModeFn, systemCronPaths
	t.Cleanup(func() {
		crontabReadLinesFn, applyDaemonModeFn, systemCronPaths = origRead, origApply, origPaths
	})
	wrapperLine := "30 02 * * * " + absentWrapper(t)
	crontabReadLinesFn = func(context.Context) ([]string, error) {
		return []string{wrapperLine}, nil
	}
	systemCronPaths = []string{filepath.Join(t.TempDir(), "absent")}
	applyDaemonModeFn = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRemovalOutcome, error) {
		t.Fatal("the migration must not run")
		return cronRemovalOutcome{}, nil
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "backup.env")
	if err := os.WriteFile(configPath, []byte("SCHEDULER_MODE=cron\nDAEMON_OPT_OUT=false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	orig := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(orig) })
	var buf bytes.Buffer
	def := logging.New(types.LogLevelDebug, false)
	def.SetOutput(&buf)
	logging.SetDefaultLogger(def)

	maybeAutoMigrateDaemon(context.Background(), configPath, dir, "/usr/local/bin/proxsave", nil)
	out := buf.String()

	if got := strings.Count(out, "WARNING"); got != 1 {
		t.Errorf("want exactly one WARNING, got %d, out=%q", got, out)
	}
	warnLine := warningLine(t, out)
	for _, want := range []string{"REFUSED", "duplicate backups", "No changes"} {
		if !strings.Contains(warnLine, want) {
			t.Errorf("the WARNING must carry %q so it survives DEBUG_LEVEL=warning, got %q", want, warnLine)
		}
	}
	for _, want := range []string{
		"unmanaged cron line(s) schedule ProxSave", // the header, below the verdict
		wrapperLine,               // the finding verbatim
		"proxsave --daemon-setup", // the way forward
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the block must still state %q, out=%q", want, out)
		}
	}
}
