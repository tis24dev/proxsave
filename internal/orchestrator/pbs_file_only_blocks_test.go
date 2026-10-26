package orchestrator

import (
	"errors"
	"strings"
	"testing"
)

// pbsBlockFrom returns the lines of a log from the one equal to first through
// the first one after it that contains outcome.
func pbsBlockFrom(t *testing.T, log, first, outcome string) []string {
	t.Helper()
	lines := pbsBlockLogLines(pbsLogAtomicTemp.ReplaceAllString(log, ".proxsave.tmp.<n>"))
	for i, line := range lines {
		if line != first {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.Contains(lines[j], outcome) {
				return lines[i : j+1]
			}
		}
	}
	t.Fatalf("no block from %q to %q in:\n%s", first, outcome, strings.Join(lines, "\n"))
	return nil
}

// runPBSHostFileStep runs the pbs_host step in Merge mode with the given staged
// files and prepare funcs, and checks what every case of these blocks shares:
// the caller adds no warning and no failed item for them.
func runPBSHostFileStep(t *testing.T, files map[string]string, prepare ...func()) (string, bool, *FakeFS) {
	t.Helper()
	log, warned, fakeFS := runPBSSectionRestoreStep(t, PBSRestoreBehaviorMerge, []string{"pbs_host"}, files, &FakeCommandRunner{}, prepare...)
	for _, unwanted := range []string{"WARNING  PBS staged apply:", "failed to apply", "is empty; removing", "does not look like a valid PBS config file (missing section header); skipping apply"} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("unexpected %q in log:\n%s", unwanted, log)
		}
	}
	return log, warned, fakeFS
}

// The metric server and proxy configuration, each as one block: applied, the
// staged file unreadable, the write refused, absent, empty or only comments
// (target removed), and without a section header (not written).
func TestPBSConfigFileBlocks(t *testing.T) {
	for _, k := range []struct {
		name, rel, prefix, opening, config, outcome string
	}{
		{"metricserver", "etc/proxmox-backup/metricserver.cfg", "PBS metric server restore: ", "Restoring PBS metric server configuration...", "The metric server configuration", "PBS metric server configuration"},
		{"proxy", "etc/proxmox-backup/proxy.cfg", "PBS proxy restore: ", "Restoring PBS proxy configuration...", "The proxy configuration", "PBS proxy configuration"},
	} {
		target := "/" + k.rel
		staged := "x: one\n\tkey value\n"
		evidence := "DEBUG    " + k.prefix + "staged=/stage/" + k.rel + " target=" + target
		current := func() {
			if err := restoreFS.WriteFile(target, []byte("x: current\n"), 0o640); err != nil {
				t.Fatal(err)
			}
		}
		cases := []struct {
			name    string
			staged  string
			prepare []func()
			want    []string
			warned  bool
			target  *string // nil: absent after the step
		}{
			{name: "applied", staged: staged, prepare: []func(){current},
				want:   []string{"INFO     " + k.opening, evidence, "DEBUG    " + k.prefix + "written", "INFO     ✓ " + k.outcome + ": applied"},
				target: new(strings.TrimSpace(staged) + "\n")},
			{name: "staged-unreadable", staged: pbsStagedUnreadable, prepare: []func(){current},
				want: []string{"INFO     " + k.opening, evidence,
					`DEBUG    ` + k.prefix + `staged read failed error="read staged ` + k.rel + `: read <fakefs>/stage/` + k.rel + `: is a directory"`,
					`INFO     ` + k.config + ` in the backup could not be read: "read staged ` + k.rel + `: read <fakefs>/stage/` + k.rel + `: is a directory"`,
					"WARNING  ⚠ " + k.outcome + ": not applied"},
				warned: true, target: new("x: current\n")},
			{name: "write-refused", staged: staged, prepare: []func(){func() {
				if err := restoreFS.MkdirAll(target+"/x", 0o755); err != nil {
					t.Fatal(err)
				}
			}},
				want: []string{"INFO     " + k.opening, evidence,
					`DEBUG    ` + k.prefix + `write failed error="write ` + target + `: rename <fakefs>` + target + `.proxsave.tmp.<n> <fakefs>` + target + `: file exists"`,
					`INFO     ` + k.config + ` could not be written to this PBS: "write ` + target + `: rename <fakefs>` + target + `.proxsave.tmp.<n> <fakefs>` + target + `: file exists"`,
					"WARNING  ⚠ " + k.outcome + ": not applied"},
				warned: true},
			{name: "empty", staged: "\n  \n", prepare: []func(){current},
				want: []string{"INFO     " + k.opening, evidence, "DEBUG    " + k.prefix + "staged file has no configuration comments_only=false, target removed", "INFO     ✓ " + k.outcome + ": applied"}},
			{name: "comments-only", staged: "# nothing here\n", prepare: []func(){current},
				want: []string{"INFO     " + k.opening, evidence, "DEBUG    " + k.prefix + "staged file has no configuration comments_only=true, target removed", "INFO     ✓ " + k.outcome + ": applied"}},
			{name: "no-header", staged: "key value without a section\n", prepare: []func(){current},
				want: []string{"INFO     " + k.opening, evidence,
					`DEBUG    ` + k.prefix + `write failed error="` + k.rel + ` does not look like a valid PBS config file (missing section header)"`,
					`INFO     ` + k.config + ` could not be written to this PBS: "` + k.rel + ` does not look like a valid PBS config file (missing section header)"`,
					"WARNING  ⚠ " + k.outcome + ": not applied"},
				warned: true, target: new("x: current\n")},
		}
		for _, tc := range cases {
			t.Run(k.name+"/"+tc.name, func(t *testing.T) {
				log, warned, fakeFS := runPBSHostFileStep(t, map[string]string{k.rel: tc.staged}, tc.prepare...)
				got := pbsBlockFrom(t, log, tc.want[0], k.outcome+": ")
				if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
					t.Fatalf("block\n--- got ---\n%s\n--- want ---\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
				}
				if warned != tc.warned {
					t.Fatalf("warned=%v want %v", warned, tc.warned)
				}
				if tc.name == "write-refused" {
					return
				}
				data, err := fakeFS.ReadFile(target)
				if tc.target == nil {
					if err == nil {
						t.Fatalf("target must be removed, got %q", data)
					}
					return
				}
				if err != nil || string(data) != *tc.target {
					t.Fatalf("target %q (err %v), want %q", data, err, *tc.target)
				}
			})
		}
		t.Run(k.name+"/absent", func(t *testing.T) {
			log, warned, _ := runPBSHostFileStep(t, nil)
			if strings.Contains(log, k.opening) || warned {
				t.Fatalf("an absent file shows nothing, warned=%v log:\n%s", warned, log)
			}
			if !strings.Contains(log, "DEBUG    "+k.prefix+"staged=/stage/"+k.rel+" not in the backup action=skip\n") {
				t.Fatalf("missing the DEBUG skip line:\n%s", log)
			}
		})
	}
}

// The ACME accounts as one block: applied, the backup's accounts not readable,
// the write refused, and absent.
func TestPBSAcmeAccountsBlock(t *testing.T) {
	const (
		opening  = "INFO     Restoring PBS ACME accounts..."
		evidence = "DEBUG    PBS ACME accounts restore: staged=/stage/etc/proxmox-backup/acme/accounts target=/etc/proxmox-backup/acme/accounts"
	)
	t.Run("applied", func(t *testing.T) {
		log, warned, fakeFS := runPBSHostFileStep(t, map[string]string{"etc/proxmox-backup/acme/accounts/default": `{"location":"x"}`})
		got := pbsBlockFrom(t, log, opening, "PBS ACME accounts: ")
		want := []string{opening, evidence,
			"DEBUG    pbs staged apply acme accounts: Applied 1 account(s) to /etc/proxmox-backup/acme/accounts, removed 0 not present in the backup",
			"INFO     ✓ PBS ACME accounts: applied"}
		if strings.Join(got, "\n") != strings.Join(want, "\n") || warned {
			t.Fatalf("warned=%v block\n%s\nwant\n%s", warned, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if data, err := fakeFS.ReadFile("/etc/proxmox-backup/acme/accounts/default"); err != nil || string(data) != `{"location":"x"}` {
			t.Fatalf("account %q (err %v)", data, err)
		}
	})
	t.Run("backup-unreadable", func(t *testing.T) {
		// The staged accounts path is a file, not the directory PBS writes.
		log, warned, _ := runPBSHostFileStep(t, map[string]string{"etc/proxmox-backup/acme/accounts": "not a directory"})
		got := pbsBlockFrom(t, log, opening, "PBS ACME accounts: ")
		want := []string{opening, evidence,
			`DEBUG    PBS ACME accounts restore: staged read failed error="staged etc/proxmox-backup/acme/accounts is not a directory"`,
			`INFO     The ACME accounts in the backup could not be read: "staged etc/proxmox-backup/acme/accounts is not a directory"`,
			"WARNING  ⚠ PBS ACME accounts: not applied"}
		if strings.Join(got, "\n") != strings.Join(want, "\n") || !warned {
			t.Fatalf("warned=%v block\n%s\nwant\n%s", warned, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})
	t.Run("write-refused", func(t *testing.T) {
		// A file where the accounts directory of this PBS goes: it cannot be created.
		log, warned, _ := runPBSHostFileStep(t, map[string]string{"etc/proxmox-backup/acme/accounts/default": `{"location":"x"}`}, func() {
			if err := restoreFS.WriteFile("/etc/proxmox-backup/acme/accounts", []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		})
		got := pbsBlockFrom(t, log, opening, "PBS ACME accounts: ")
		if len(got) != 5 || got[1] != evidence ||
			!strings.HasPrefix(got[2], `DEBUG    PBS ACME accounts restore: write failed error="mkdir /etc/proxmox-backup/acme/accounts: `) ||
			!strings.HasPrefix(got[3], `INFO     The ACME accounts could not be written to this PBS: "mkdir /etc/proxmox-backup/acme/accounts: `) ||
			got[4] != "WARNING  ⚠ PBS ACME accounts: not applied" || !warned {
			t.Fatalf("warned=%v block:\n%s", warned, strings.Join(got, "\n"))
		}
	})
	t.Run("absent", func(t *testing.T) {
		log, warned, _ := runPBSHostFileStep(t, nil)
		if strings.Contains(log, opening) || warned {
			t.Fatalf("absent accounts show nothing, warned=%v log:\n%s", warned, log)
		}
		if !strings.Contains(log, "DEBUG    PBS ACME accounts restore: staged=/stage/etc/proxmox-backup/acme/accounts not in the backup action=skip\n") {
			t.Fatalf("missing the DEBUG skip line:\n%s", log)
		}
	})
	current := func(names ...string) func() {
		return func() {
			for _, name := range names {
				if err := restoreFS.WriteFile("/etc/proxmox-backup/acme/accounts/"+name, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	failRemove := func() {
		restoreFS = &pbsRemoveFailFS{FakeFS: restoreFS.(*FakeFS), fail: "/etc/proxmox-backup/acme/accounts/old"}
	}
	removeOld := []string{
		"DEBUG    PBS ACME accounts restore: account=old not in backup action=remove",
		`DEBUG    PBS ACME accounts restore: account=old remove failed error="remove /etc/proxmox-backup/acme/accounts/old: injected remove failure"`,
		"INFO     ACME account old is configured on this PBS but not present in the backup",
		"WARNING  ⚠ PBS ACME account old: not removed",
	}
	for _, tc := range []struct {
		name    string
		files   map[string]string
		prepare []func()
		want    []string
		warned  bool
		stepErr string
	}{
		{
			name: "removes-an-account-not-in-the-backup", files: map[string]string{"etc/proxmox-backup/acme/accounts/default": `{"location":"x"}`},
			prepare: []func(){current("old")},
			want: []string{opening, evidence,
				"DEBUG    pbs staged apply acme accounts: Applied 1 account(s) to /etc/proxmox-backup/acme/accounts, removed 1 not present in the backup",
				"DEBUG    PBS ACME accounts restore: account=old not in backup action=remove",
				"INFO     ACME account old is configured on this PBS but not present in the backup",
				"INFO     ✓ PBS ACME account old: removed",
				"INFO     ✓ PBS ACME accounts: applied"},
		},
		{
			name: "removal-refused-partially-applied", files: map[string]string{"etc/proxmox-backup/acme/accounts/default": `{"location":"x"}`},
			prepare: []func(){current("old", "other"), failRemove},
			want: append(append([]string{opening, evidence,
				"DEBUG    pbs staged apply acme accounts: Applied 1 account(s) to /etc/proxmox-backup/acme/accounts, removed 1 not present in the backup"},
				removeOld...),
				"DEBUG    PBS ACME accounts restore: account=other not in backup action=remove",
				"INFO     ACME account other is configured on this PBS but not present in the backup",
				"INFO     ✓ PBS ACME account other: removed",
				"WARNING  ⚠ PBS ACME accounts: partially applied"),
			warned: true, stepErr: "1 ACME account(s) not removed [old]",
		},
		{
			// The backup has no accounts and the only removal is refused.
			name: "removal-refused-not-applied", files: nil,
			prepare: []func(){func() {
				if err := restoreFS.MkdirAll("/stage/etc/proxmox-backup/acme/accounts", 0o700); err != nil {
					t.Fatal(err)
				}
			}, current("old"), failRemove},
			want: append(append([]string{opening, evidence,
				"DEBUG    pbs staged apply acme accounts: Applied 0 account(s) to /etc/proxmox-backup/acme/accounts, removed 0 not present in the backup"},
				removeOld...),
				"WARNING  ⚠ PBS ACME accounts: not applied"),
			warned: true, stepErr: "1 ACME account(s) not removed [old]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log, warned, fakeFS := runPBSHostFileStep(t, tc.files, tc.prepare...)
			got := pbsBlockFrom(t, log, opening, "PBS ACME accounts: ")
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") || warned != tc.warned {
				t.Fatalf("warned=%v (want %v) block\n--- got ---\n%s\n--- want ---\n%s", warned, tc.warned, strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
			if tc.stepErr != "" && !strings.Contains(log, "DEBUG    PBS staged config apply: outcome already reported: "+tc.stepErr+"\n") {
				t.Fatalf("the step must record the outcome as already reported, log:\n%s", log)
			}
			if strings.Contains(log, "PBS staged apply: removed ACME account") {
				t.Fatalf("the old removal line must not appear in the block:\n%s", log)
			}
			if _, err := fakeFS.Stat("/etc/proxmox-backup/acme/accounts/old"); (err == nil) != tc.warned {
				t.Fatalf("old present=%v, want %v", err == nil, tc.warned)
			}
		})
	}
}

// pbsRemoveFailFS is the FakeFS with the removal of one path failing.
type pbsRemoveFailFS struct {
	*FakeFS
	fail string
}

func (f *pbsRemoveFailFS) RemoveAll(path string) error {
	if path == f.fail {
		return errors.New("injected remove failure")
	}
	return f.FakeFS.RemoveAll(path)
}
