package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runPartsTree builds a temp stand-in for the run-parts habitats and points
// systemCronPaths at it. It returns the parent so a test can add a cron.d beside it.
func runPartsTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	orig := systemCronPaths
	t.Cleanup(func() { systemCronPaths = orig })
	paths := []string{filepath.Join(root, "crontab"), filepath.Join(root, "cron.d")}
	for name := range runPartsCronDirNames {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		paths = append(paths, dir)
	}
	systemCronPaths = paths
	return root
}

func writeScript(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

const runPartsWrapper = "#!/bin/sh\nif mountpoint -q /mnt/nas ; then /usr/local/bin/proxsave --backup ; fi\n"

// The two lists are one decision written twice: the habitats this detector walks, and
// which of them hold scripts rather than crontab lines. Nothing else relates them, so a
// key dropped from either side would leave a habitat either unwalked or walked with the
// wrong parser, silently.
func TestTheDefaultSystemCronHabitatsCoverTheRunPartsDirectories(t *testing.T) {
	inList := map[string]bool{}
	for _, path := range systemCronPaths {
		inList[filepath.Base(path)] = true
	}
	for name := range runPartsCronDirNames {
		if !inList[name] {
			t.Errorf("runPartsCronDirNames has %q but systemCronPaths does not walk /etc/%s, so that habitat is never opened", name, name)
		}
	}
	for _, path := range systemCronPaths {
		base := filepath.Base(path)
		if base == "cron.d" || !strings.HasPrefix(base, "cron.") {
			continue
		}
		if _, ok := runPartsCronDirNames[base]; !ok {
			t.Errorf("systemCronPaths walks %s but runPartsCronDirNames does not name it, so its scripts are parsed as system crontab lines", path)
		}
	}
	for _, want := range []string{"/etc/crontab", "/etc/cron.d", "/etc/cron.hourly", "/etc/cron.daily", "/etc/cron.weekly", "/etc/cron.monthly"} {
		found := false
		for _, path := range systemCronPaths {
			if path == want {
				found = true
			}
		}
		if !found {
			t.Errorf("systemCronPaths does not contain %q", want)
		}
	}
}

// One test per habitat, because a key lost from runPartsCronDirNames costs exactly one
// habitat and nothing else fails.
func TestAWrapperInAnyRunPartsDirectoryIsFound(t *testing.T) {
	for _, dirName := range []string{"cron.hourly", "cron.daily", "cron.weekly", "cron.monthly"} {
		t.Run(dirName, func(t *testing.T) {
			root := runPartsTree(t)
			script := filepath.Join(root, dirName, "nas-guard")
			writeScript(t, script, runPartsWrapper, 0o755)

			refs := systemCronProxsaveRefs()
			if len(refs) != 1 {
				t.Fatalf("systemCronProxsaveRefs() = %+v, want exactly one finding for %s", refs, script)
			}
			if refs[0].Line != script || refs[0].Source != filepath.Join(root, dirName) {
				t.Errorf("finding = {Line:%q Source:%q}, want {Line:%q Source:%q}", refs[0].Line, refs[0].Source, script, filepath.Join(root, dirName))
			}
			if !refs[0].RunParts {
				t.Errorf("finding is not marked RunParts, so every renderer will treat its path as a cron line")
			}
			if !strings.Contains(refs[0].Reason, "no cron time of its own") {
				t.Errorf("Reason = %q, want it to say the script has no cron time of its own", refs[0].Reason)
			}
		})
	}
}

// The reason the directories could not simply be appended to systemCronPaths. A script is
// not a crontab: read as one, this wrapper's seventh whitespace field is the binary, so it
// would be reported as a DIRECT proxsave cron line and its shell fragment handed to the
// schedule parser, which answers "" and silences the real entry in /etc/cron.d.
func TestARunPartsScriptIsNeverParsedAsACrontabLine(t *testing.T) {
	root := runPartsTree(t)
	writeScript(t, filepath.Join(root, "cron.daily", "nas-guard"), runPartsWrapper, 0o755)
	if err := os.MkdirAll(filepath.Join(root, "cron.d"), 0o755); err != nil {
		t.Fatalf("mkdir cron.d: %v", err)
	}
	writeScript(t, filepath.Join(root, "cron.d", "proxsave"), "0 5 * * * root /usr/local/bin/proxsave --backup\n", 0o644)

	for _, ref := range systemCronDirectProxsaveLines() {
		if strings.Contains(ref.Line, "mountpoint") || strings.Contains(ref.Source, "cron.daily") {
			t.Errorf("the run-parts script reached the direct-line view as %+v: its shell code is being read as a cron line", ref)
		}
	}

	got, source := schedulerTimeFromSystemCron()
	if got != "05:00" {
		t.Errorf("schedulerTimeFromSystemCron() = (%q, %q), want (\"05:00\", the cron.d file): a run-parts script has no time and must not be allowed to answer the question", got, source)
	}
}

// run-parts executes every entry that passes its own filter, and only those. Reporting a
// script it would never run refuses a migration over a schedule that does not exist.
func TestTheRunPartsWalkAcceptsExactlyWhatRunPartsRuns(t *testing.T) {
	cases := []struct {
		name  string
		file  string
		mode  os.FileMode
		found bool
	}{
		{"plain name, executable", "guard", 0o755, true},
		{"underscore and digits", "guard_2", 0o755, true},
		{"hyphen", "guard-b", 0o755, true},
		{"uppercase", "GUARD", 0o755, true},
		// run-parts as root executes on ANY execute bit, so a group-only bit still runs
		// nightly. Measured with `sudo run-parts --test`.
		{"group-only execute bit", "guard-group", 0o610, true},
		{"no execute bit at all", "guard-noexec", 0o644, false},
		{"dot in the name", "guard.sh", 0o755, false},
		{"package leftover", "guard.dpkg-dist", 0o755, false},
		{"editor backup", "guard~", 0o755, false},
		{"hidden", ".guard", 0o755, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := runPartsTree(t)
			writeScript(t, filepath.Join(root, "cron.daily", tc.file), runPartsWrapper, tc.mode)
			refs := systemCronProxsaveRefs()
			if found := len(refs) > 0; found != tc.found {
				t.Errorf("%s at mode %v: found = %v, want %v (refs %+v)", tc.file, tc.mode, found, tc.found, refs)
			}
		})
	}
}

// A run-parts script must never reach the view that writes SCHEDULER_TIME into
// backup.env: it has no time, and one unreadable time makes that helper say nothing at
// all, which drops the host to the 02:00 default.
func TestARunPartsScriptNeverReachesTheSchedulerTimeAdoption(t *testing.T) {
	root := runPartsTree(t)
	writeScript(t, filepath.Join(root, "cron.daily", "nas-guard"), runPartsWrapper, 0o755)

	if refs := systemCronDirectProxsaveLines(); len(refs) != 0 {
		t.Errorf("systemCronDirectProxsaveLines() = %+v, want none: the direct-line view must not see the run-parts habitat at all", refs)
	}
}

// A run-parts script is stopped with chmod -x or by removing it. "Edit the file" leaves an
// operator editing a script that keeps running every night.
func TestTheEditHintNamesTheRightToolForARunPartsScript(t *testing.T) {
	got := cronRefEditHint([]indirectCronRef{{Line: "/etc/cron.daily/nas-guard", Source: "/etc/cron.daily", RunParts: true}})
	want := "'chmod -x' or remove the run-parts script named above"
	if got != want {
		t.Errorf("cronRefEditHint = %q, want %q", got, want)
	}
	got = cronRefEditHint([]indirectCronRef{
		{Line: "0 2 * * * /usr/local/sbin/nas-guard"},
		{Line: "/etc/cron.daily/nas-guard", Source: "/etc/cron.daily", RunParts: true},
	})
	want = "'crontab -e' for the crontab entries, 'chmod -x' or removal for the run-parts scripts named above"
	if got != want {
		t.Errorf("cronRefEditHint (mixed) = %q, want %q", got, want)
	}
}
