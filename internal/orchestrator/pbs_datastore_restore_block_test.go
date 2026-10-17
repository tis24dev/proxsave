package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

const pbsDatastoreBlockListCmd = "proxmox-backup-manager datastore list --output-format=json"

// pbsStagedUnreadable, passed as the staged content, puts a directory where the
// staged datastore.cfg should be, so reading it fails.
const pbsStagedUnreadable = "<unreadable>"

// pbsLogTimestamp matches the timestamp the logger writes in front of each line,
// pbsLogDuration the elapsed time the DEBUG end line of a step carries.
var (
	pbsLogTimestamp = regexp.MustCompile(`^\[[^\]]*\] `)
	pbsLogDuration  = regexp.MustCompile(`duration=[^)]*\)`)
	pbsLogFakeFS    = regexp.MustCompile(`\S*/fakefs-[0-9]+`)
)

// pbsBlockLogLines returns the logged lines without their timestamps and durations.
func pbsBlockLogLines(log string) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(log, "\n"), "\n") {
		line = pbsLogTimestamp.ReplaceAllString(line, "")
		line = pbsLogFakeFS.ReplaceAllString(line, "<fakefs>")
		lines = append(lines, pbsLogDuration.ReplaceAllString(line, "duration=<d>)"))
	}
	return lines
}

// runPBSDatastoreRestoreStep runs the "PBS staged config apply" step through the
// staged-apply step runner with the real datastore.cfg API apply, and returns the
// full log (all levels) and whether the restore was marked with warnings. Each
// prepare func runs on the fake filesystem after the staged file is written.
func runPBSDatastoreRestoreStep(t *testing.T, behavior PBSRestoreBehavior, staged string, runner *FakeCommandRunner, prepare ...func(*FakeFS)) (string, bool) {
	t.Helper()
	origFS := restoreFS
	origCmd := restoreCmd
	origIsReal := pbsStagedApplyIsRealRestoreFSFn
	origGeteuid := pbsStagedApplyGeteuidFn
	origEnsure := pbsStagedApplyEnsurePBSServicesForAPIFn
	origDS := pbsStagedApplyDatastoreCfgViaAPIFn
	origRunDir := restoreRunDirPath
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
		pbsStagedApplyIsRealRestoreFSFn = origIsReal
		pbsStagedApplyGeteuidFn = origGeteuid
		pbsStagedApplyEnsurePBSServicesForAPIFn = origEnsure
		pbsStagedApplyDatastoreCfgViaAPIFn = origDS
		restoreRunDirPath = origRunDir
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreCmd = runner
	pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
	pbsStagedApplyGeteuidFn = func() int { return 0 }
	pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error { return nil }
	pbsStagedApplyDatastoreCfgViaAPIFn = applyPBSDatastoreCfgViaAPI

	stageRoot := "/stage"
	if staged == pbsStagedUnreadable {
		// A directory where the file should be: reading it fails.
		if err := fakeFS.MkdirAll(stageRoot+"/etc/proxmox-backup/datastore.cfg", 0o750); err != nil {
			t.Fatalf("mkdir staged datastore.cfg: %v", err)
		}
	} else if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/datastore.cfg", []byte(staged), 0o640); err != nil {
		t.Fatalf("write staged datastore.cfg: %v", err)
	}
	for _, p := range prepare {
		p(fakeFS)
	}

	var logBuf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&logBuf)

	plan := &RestorePlan{
		SystemType:         SystemTypePBS,
		PBSRestoreBehavior: behavior,
		NormalCategories:   []Category{{ID: "datastore_pbs"}},
	}
	w := &restoreUIWorkflowRun{ctx: context.Background(), logger: logger, plan: plan}
	step := restoreStageApplyStep{name: "PBS staged config apply", run: func() error {
		return maybeApplyPBSConfigsFromStage(w.ctx, w.logger, w.plan, stageRoot, false)
	}}
	if err := w.runStageApplyStep(step); err != nil {
		t.Fatalf("runStageApplyStep: %v", err)
	}
	return logBuf.String(), w.restoreHadWarnings
}

func assertPBSBlockLog(t *testing.T, log string, want []string) {
	t.Helper()
	got := pbsBlockLogLines(log)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("log mismatch\n--- got ---\n%s\n--- want ---\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The approved Clean scenario: stale-ds removed, dsa existing at the same path,
// dsb at a different path, dsc new, dsd without a path.
func TestPBSDatastoreRestoreBlock_CleanScenario(t *testing.T) {
	staged := "datastore: dsa\n\tpath /mnt/a\n\n" +
		"datastore: dsb\n\tpath /mnt/b\n\n" +
		"datastore: dsc\n\tpath /mnt/c\n\n" +
		"datastore: dsd\n\tcomment no-path\n"
	runner := &FakeCommandRunner{Outputs: map[string][]byte{
		pbsDatastoreBlockListCmd: []byte(`[{"name":"dsa","path":"/mnt/a"},{"name":"dsb","path":"/mnt/b-old"},{"name":"stale-ds","path":"/mnt/stale"}]`),
	}}

	log, warned := runPBSDatastoreRestoreStep(t, PBSRestoreBehaviorClean, staged, runner)
	t.Logf("full log:\n%s", log)

	assertPBSBlockLog(t, log, []string{
		"DEBUG    Start pbs staged apply: dryRun=false stage=/stage",
		"INFO     Restoring PBS datastore configuration...",
		"DEBUG    PBS datastore restore: mode=clean staged=/stage/etc/proxmox-backup/datastore.cfg",
		"DEBUG    PBS datastore restore: backup datastores=4 names=dsa,dsb,dsc,dsd",
		"INFO       Datastores to restore: 4",
		"INFO       Names: dsa, dsb, dsc, dsd",
		`DEBUG    PBS datastore restore: reading current datastores cmd="proxmox-backup-manager datastore list --output-format=json"`,
		"DEBUG    PBS datastore restore: current datastores=3 dsa=/mnt/a dsb=/mnt/b-old stale-ds=/mnt/stale",
		"DEBUG    PBS datastore restore: datastore=stale-ds not in backup action=remove",
		"DEBUG    PBS datastore restore: datastore=stale-ds removed",
		"INFO     Datastore stale-ds is configured on this PBS but not present in the backup",
		"INFO     ✓ PBS datastore stale-ds: removed with its jobs",
		"DEBUG    PBS datastore restore: datastore=dsa backup_path=/mnt/a current_path=/mnt/a action=update",
		"DEBUG    PBS datastore restore: datastore=dsa updated",
		"INFO     ✓ PBS datastore dsa: applied",
		"DEBUG    PBS datastore restore: datastore=dsb backup_path=/mnt/b current_path=/mnt/b-old path differs action=remove,create",
		"DEBUG    PBS datastore restore: datastore=dsb removed",
		"DEBUG    PBS datastore restore: datastore=dsb created path=/mnt/b",
		"INFO     Datastore dsb moved from /mnt/b-old to /mnt/b, the data in /mnt/b-old is not moved",
		"INFO     ✓ PBS datastore dsb: applied",
		"DEBUG    PBS datastore restore: datastore=dsc backup_path=/mnt/c current_path=none action=create",
		"DEBUG    PBS datastore restore: datastore=dsc created",
		"INFO     ✓ PBS datastore dsc: applied",
		"DEBUG    PBS datastore restore: datastore=dsd backup_path=none action=skip",
		"INFO     Datastore dsd has no path in the backup",
		"WARNING  ⚠ PBS datastore dsd: not applied",
		"WARNING  ⚠ PBS datastore configuration: partially applied",
		"DEBUG    End pbs staged apply (error=PBS datastore configuration incomplete: 1 datastore(s) not applied [dsd], duration=<d>)",
		"DEBUG    PBS staged config apply: outcome already reported: PBS datastore configuration incomplete: 1 datastore(s) not applied [dsd]",
	})
	if !warned {
		t.Fatal("a datastore not applied must end the restore with warnings")
	}
	want := []string{
		pbsDatastoreBlockListCmd,
		"proxmox-backup-manager datastore remove stale-ds",
		"proxmox-backup-manager datastore update dsa",
		"proxmox-backup-manager datastore remove dsb",
		"proxmox-backup-manager datastore create dsb /mnt/b",
		"proxmox-backup-manager datastore create dsc /mnt/c",
	}
	if strings.Join(runner.CallsList(), "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls=%v want %v", runner.CallsList(), want)
	}
}

// Merge: dsb is not on this PBS, its create and its update are both refused; dsa
// and dsc are still applied, and nothing follows the block outcome.
func TestPBSDatastoreRestoreBlock_MergeCreateAndUpdateFail(t *testing.T) {
	staged := "datastore: dsa\n\tpath /mnt/a\n\n" +
		"datastore: dsb\n\tpath /mnt/b\n\n" +
		"datastore: dsc\n\tpath /mnt/c\n"
	runner := &FakeCommandRunner{
		Outputs: map[string][]byte{
			pbsDatastoreBlockListCmd: []byte(`[{"name":"dsa","path":"/mnt/a"},{"name":"other","path":"/mnt/other"}]`),
		},
		Errors: map[string]error{
			"proxmox-backup-manager datastore create dsb /mnt/b": errors.New("unable to create datastore 'dsb': path is not a directory"),
			"proxmox-backup-manager datastore update dsb":        errors.New("no such datastore 'dsb'"),
		},
	}

	log, warned := runPBSDatastoreRestoreStep(t, PBSRestoreBehaviorMerge, staged, runner)
	t.Logf("full log:\n%s", log)

	assertPBSBlockLog(t, log, []string{
		"DEBUG    Start pbs staged apply: dryRun=false stage=/stage",
		"INFO     Restoring PBS datastore configuration...",
		"DEBUG    PBS datastore restore: mode=merge staged=/stage/etc/proxmox-backup/datastore.cfg",
		"DEBUG    PBS datastore restore: backup datastores=3 names=dsa,dsb,dsc",
		"INFO       Datastores to restore: 3",
		"INFO       Names: dsa, dsb, dsc",
		`DEBUG    PBS datastore restore: reading current datastores cmd="proxmox-backup-manager datastore list --output-format=json"`,
		"DEBUG    PBS datastore restore: current datastores=2 dsa=/mnt/a other=/mnt/other",
		"DEBUG    PBS datastore restore: datastore=other not in backup action=keep (merge)",
		"DEBUG    PBS datastore restore: datastore=dsa backup_path=/mnt/a current_path=/mnt/a action=update",
		"DEBUG    PBS datastore restore: datastore=dsa updated",
		"INFO     ✓ PBS datastore dsa: applied",
		"DEBUG    PBS datastore restore: datastore=dsb backup_path=/mnt/b current_path=none action=create",
		`DEBUG    PBS datastore restore: datastore=dsb create failed error="unable to create datastore 'dsb': path is not a directory" action=update`,
		`DEBUG    PBS datastore restore: datastore=dsb update failed error="no such datastore 'dsb'"`,
		`INFO     Datastore dsb was rejected by PBS: "unable to create datastore 'dsb': path is not a directory"`,
		"WARNING  ⚠ PBS datastore dsb: not applied",
		"DEBUG    PBS datastore restore: datastore=dsc backup_path=/mnt/c current_path=none action=create",
		"DEBUG    PBS datastore restore: datastore=dsc created",
		"INFO     ✓ PBS datastore dsc: applied",
		"WARNING  ⚠ PBS datastore configuration: partially applied",
		"DEBUG    End pbs staged apply (error=PBS datastore configuration incomplete: 1 datastore(s) not applied [dsb], duration=<d>)",
		"DEBUG    PBS staged config apply: outcome already reported: PBS datastore configuration incomplete: 1 datastore(s) not applied [dsb]",
	})
	if !warned {
		t.Fatal("a datastore not applied must end the restore with warnings")
	}
}

// The step's full log for the cases outside the approved scenario: a datastore
// rejected by PBS (1), nothing to restore (10), the datastores on this PBS not
// readable in Clean (12) and in Merge (13), the staged datastore.cfg not readable
// (14). In 12 and 14 (Clean) the file-based fallback still runs after the block.
func TestPBSDatastoreRestoreBlock_UncoveredCasesFullLog(t *testing.T) {
	cases := []struct {
		name     string
		behavior PBSRestoreBehavior
		staged   string
		runner   *FakeCommandRunner
		visible  []string
		warned   bool
	}{
		{
			name:     "1-merge-create-and-update-refused",
			behavior: PBSRestoreBehaviorMerge,
			staged:   "datastore: ds1\n\tpath /mnt/ds1\n",
			runner: &FakeCommandRunner{
				Outputs: map[string][]byte{pbsDatastoreBlockListCmd: []byte(`[]`)},
				Errors: map[string]error{
					"proxmox-backup-manager datastore create ds1 /mnt/ds1": errors.New(pbsCreateRefused),
					"proxmox-backup-manager datastore update ds1":          errors.New(pbsUpdateRefused),
				},
			},
			visible: []string{
				"INFO     Restoring PBS datastore configuration...",
				"INFO       Datastores to restore: 1",
				"INFO       Names: ds1",
				`INFO     Datastore ds1 was rejected by PBS: "parameter verification failed - 'keep-last': value must have a minimum value of 1 (got 0)"`,
				"WARNING  ⚠ PBS datastore ds1: not applied",
				"WARNING  ⚠ PBS datastore configuration: not applied",
			},
			warned: true,
		},
		{
			name:     "10-clean-nothing-to-restore",
			behavior: PBSRestoreBehaviorClean,
			staged:   "# no datastores\n",
			runner:   &FakeCommandRunner{Outputs: map[string][]byte{pbsDatastoreBlockListCmd: []byte(`[]`)}},
			visible: []string{
				"INFO     Restoring PBS datastore configuration...",
				"INFO       Datastores to restore: 0",
				"SKIP     PBS datastore configuration: nothing to restore",
			},
		},
		{
			// The approved sequence: the file fallback writes datastore.cfg, ds1 is on
			// the root filesystem of the test host.
			name:     "12-clean-current-datastores-unreadable",
			behavior: PBSRestoreBehaviorClean,
			staged:   "datastore: ds1\n\tpath /mnt/ds1\n",
			runner: &FakeCommandRunner{Errors: map[string]error{
				pbsDatastoreBlockListCmd: errors.New("exit status 255: Error: unable to open chunk store"),
			}},
			visible: []string{
				"INFO     Restoring PBS datastore configuration...",
				"INFO       Datastores to restore: 1",
				"INFO       Names: ds1",
				"INFO     The datastores configured on this PBS could not be read",
				"INFO     Datastore ds1 path /mnt/ds1 is on the root filesystem",
				"WARNING  ⚠ PBS datastore ds1: applied without its mount",
				"WARNING  ⚠ PBS datastore configuration: applied from the backup file",
			},
			warned: true,
		},
		{
			name:     "13-merge-current-datastores-unreadable",
			behavior: PBSRestoreBehaviorMerge,
			staged:   "datastore: ds1\n\tpath /mnt/ds1\n",
			runner: &FakeCommandRunner{Errors: map[string]error{
				pbsDatastoreBlockListCmd: errors.New("exit status 255: Error: unable to open chunk store"),
			}},
			visible: []string{
				"INFO     Restoring PBS datastore configuration...",
				"INFO       Datastores to restore: 1",
				"INFO       Names: ds1",
				"INFO     The datastores configured on this PBS could not be read",
				"INFO     ✓ PBS datastore ds1: applied",
				"INFO     ✓ PBS datastore configuration: applied",
			},
		},
		{
			name:     "14-clean-staged-datastore-cfg-unreadable",
			behavior: PBSRestoreBehaviorClean,
			staged:   pbsStagedUnreadable,
			runner:   &FakeCommandRunner{},
			visible: []string{
				"INFO     Restoring PBS datastore configuration...",
				`INFO     The datastore configuration in the backup could not be read: "read staged datastore.cfg: read <fakefs>/stage/etc/proxmox-backup/datastore.cfg: is a directory"`,
				"WARNING  ⚠ PBS datastore configuration: not applied",
			},
			warned: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log, warned := runPBSDatastoreRestoreStep(t, tc.behavior, tc.staged, tc.runner)
			t.Logf("full log:\n%s", strings.Join(pbsBlockLogLines(log), "\n"))
			if tc.visible != nil {
				if got := pbsVisibleLines(log); strings.Join(got, "\n") != strings.Join(tc.visible, "\n") {
					t.Fatalf("visible lines\n--- got ---\n%s\n--- want ---\n%s", strings.Join(got, "\n"), strings.Join(tc.visible, "\n"))
				}
			}
			if warned != tc.warned {
				t.Fatalf("restoreHadWarnings=%v want %v", warned, tc.warned)
			}
		})
	}
}

// pbsLogAtomicTemp matches the temp file name writeFileAtomic renames into place.
var pbsLogAtomicTemp = regexp.MustCompile(`\.proxsave\.tmp\.[0-9]+`)

// The file fallback in Clean mode, after the datastores on this PBS could not be
// read, when it cannot write datastore.cfg (a directory is in its place) and when a
// deferred definition cannot be saved (the restore run directory cannot be made).
func TestPBSDatastoreRestoreBlock_FallbackWriteFailures(t *testing.T) {
	listFails := func() *FakeCommandRunner {
		return &FakeCommandRunner{Errors: map[string]error{
			pbsDatastoreBlockListCmd: errors.New("exit status 255: Error: unable to open chunk store"),
		}}
	}
	cases := []struct {
		name    string
		staged  string
		prepare func(*FakeFS)
		visible []string
	}{
		{
			name:   "datastore-cfg-not-written",
			staged: "datastore: ds1\n\tpath /mnt/ds1\n",
			prepare: func(f *FakeFS) {
				if err := f.MkdirAll("/etc/proxmox-backup/datastore.cfg/x", 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
			},
			visible: []string{
				"INFO     Restoring PBS datastore configuration...",
				"INFO       Datastores to restore: 1",
				"INFO       Names: ds1",
				"INFO     The datastores configured on this PBS could not be read",
				`INFO     The datastore configuration could not be written to this PBS: "write /etc/proxmox-backup/datastore.cfg: rename <fakefs>/etc/proxmox-backup/datastore.cfg<tmp> <fakefs>/etc/proxmox-backup/datastore.cfg: file exists"`,
				"WARNING  ⚠ PBS datastore configuration: not applied",
			},
		},
		{
			name:   "deferred-definition-not-saved",
			staged: "datastore: ds1\n\tpath /mnt/ds1\n\ndatastore: ds2\n\tpath /\n",
			prepare: func(f *FakeFS) {
				if err := f.WriteFile("/runbase", []byte("x"), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
				restoreRunDirPath = "/runbase/run"
			},
			visible: []string{
				"INFO     Restoring PBS datastore configuration...",
				"INFO       Datastores to restore: 2",
				"INFO       Names: ds1, ds2",
				"INFO     The datastores configured on this PBS could not be read",
				"INFO     Datastore ds1 path /mnt/ds1 is on the root filesystem",
				"WARNING  ⚠ PBS datastore ds1: applied without its mount",
				`INFO     Datastore ds2 is not safe to apply now, its definition could not be saved: "mkdir <fakefs>/runbase: not a directory"`,
				"WARNING  ⚠ PBS datastore ds2: not applied",
				"WARNING  ⚠ PBS datastore configuration: applied from the backup file",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log, warned := runPBSDatastoreRestoreStep(t, PBSRestoreBehaviorClean, tc.staged, listFails(), tc.prepare)
			t.Logf("full log:\n%s", strings.Join(pbsBlockLogLines(log), "\n"))
			var got []string
			for _, line := range pbsVisibleLines(log) {
				got = append(got, pbsLogAtomicTemp.ReplaceAllString(line, "<tmp>"))
			}
			if strings.Join(got, "\n") != strings.Join(tc.visible, "\n") {
				t.Fatalf("visible lines\n--- got ---\n%s\n--- want ---\n%s", strings.Join(got, "\n"), strings.Join(tc.visible, "\n"))
			}
			if !warned {
				t.Fatal("the restore must end with warnings")
			}
		})
	}
}

// pbsVisibleLines returns the INFO, SKIP, WARNING and ERROR lines of a log, without
// their timestamps: what the operator sees.
func pbsVisibleLines(log string) []string {
	var lines []string
	for _, line := range pbsBlockLogLines(log) {
		if strings.HasPrefix(line, "INFO") || strings.HasPrefix(line, "SKIP") || strings.HasPrefix(line, "WARNING") || strings.HasPrefix(line, "ERROR") {
			lines = append(lines, line)
		}
	}
	return lines
}

// Refusals in the shape osCommandRunner.RunStdout returns them: the exit status, then
// the stderr of proxmox-backup-manager. The "Error: " lines are the shape measured on
// PBS (diagnostics, 2026-10-02); the datastore names and the usage text are fixtures.
const (
	pbsCreateRefused = "exit status 255: Error: parameter verification failed - 'keep-last': value must have a minimum value of 1 (got 0)\n\nUsage: proxmox-backup-manager datastore create <name> <path> [OPTIONS]"
	pbsUpdateRefused = "exit status 255: Error: permission check failed - missing Datastore.Modify on /datastore/ds1"
)

// Every path of a datastore, with the exact lines the operator sees and the error
// the caller records the restore warning from.
func TestApplyPBSDatastoreCfgViaAPI_BlockPaths(t *testing.T) {
	const (
		head1 = "INFO     Restoring PBS datastore configuration..."
		count = "INFO       Datastores to restore: 1"
		names = "INFO       Names: ds1"
		ds1   = "datastore: ds1\n    path /new\n    comment c1\n"
	)
	cases := []struct {
		name       string
		strict     bool
		staged     string
		rows       string
		errs       map[string]error
		wantLines  []string
		wantCalls  []string
		notApplied []string
		notRemoved []string
	}{
		{
			name:   "all-applied",
			staged: ds1,
			rows:   `[{"name":"ds1","path":"/new"}]`,
			wantLines: []string{head1, count, names,
				"INFO     ✓ PBS datastore ds1: applied",
				"INFO     ✓ PBS datastore configuration: applied",
			},
			wantCalls: []string{pbsDatastoreBlockListCmd, "proxmox-backup-manager datastore update ds1 --comment c1"},
		},
		{
			name:   "nothing-to-do",
			strict: true,
			staged: "# no datastores\n",
			rows:   `[]`,
			wantLines: []string{head1,
				"INFO       Datastores to restore: 0",
				"SKIP     PBS datastore configuration: nothing to restore",
			},
			wantCalls: []string{pbsDatastoreBlockListCmd},
		},
		{
			name:   "not-listed-create-refused-update-applies",
			staged: ds1,
			rows:   `[]`,
			errs:   map[string]error{"proxmox-backup-manager datastore create ds1 /new --comment c1": errors.New("datastore 'ds1' already exists")},
			wantLines: []string{head1, count, names,
				"INFO     ✓ PBS datastore ds1: applied",
				"INFO     ✓ PBS datastore configuration: applied",
			},
			wantCalls: []string{pbsDatastoreBlockListCmd,
				"proxmox-backup-manager datastore create ds1 /new --comment c1",
				"proxmox-backup-manager datastore update ds1 --comment c1",
			},
		},
		{
			name:   "update-fails",
			staged: ds1,
			rows:   `[{"name":"ds1","path":"/new"}]`,
			errs:   map[string]error{"proxmox-backup-manager datastore update ds1 --comment c1": errors.New(pbsUpdateRefused)},
			wantLines: []string{head1, count, names,
				`INFO     Datastore ds1 was rejected by PBS: "permission check failed - missing Datastore.Modify on /datastore/ds1"`,
				"WARNING  ⚠ PBS datastore ds1: not applied",
				"WARNING  ⚠ PBS datastore configuration: not applied",
			},
			wantCalls:  []string{pbsDatastoreBlockListCmd, "proxmox-backup-manager datastore update ds1 --comment c1"},
			notApplied: []string{"ds1"},
		},
		{
			name:   "create-and-update-fail",
			staged: ds1,
			rows:   `[]`,
			errs: map[string]error{
				"proxmox-backup-manager datastore create ds1 /new --comment c1": errors.New(pbsCreateRefused),
				"proxmox-backup-manager datastore update ds1 --comment c1":      errors.New(pbsUpdateRefused),
			},
			wantLines: []string{head1, count, names,
				`INFO     Datastore ds1 was rejected by PBS: "parameter verification failed - 'keep-last': value must have a minimum value of 1 (got 0)"`,
				"WARNING  ⚠ PBS datastore ds1: not applied",
				"WARNING  ⚠ PBS datastore configuration: not applied",
			},
			wantCalls: []string{pbsDatastoreBlockListCmd,
				"proxmox-backup-manager datastore create ds1 /new --comment c1",
				"proxmox-backup-manager datastore update ds1 --comment c1",
			},
			notApplied: []string{"ds1"},
		},
		{
			name:   "clean-path-differs-remove-refused",
			strict: true,
			staged: ds1,
			rows:   `[{"name":"ds1","path":"/old"}]`,
			errs:   map[string]error{"proxmox-backup-manager datastore remove ds1": errors.New("datastore is in use")},
			wantLines: []string{head1, count, names,
				"INFO     Datastore ds1 is at /old on this PBS and at /new in the backup",
				"WARNING  ⚠ PBS datastore ds1: not applied",
				"WARNING  ⚠ PBS datastore configuration: not applied",
			},
			wantCalls:  []string{pbsDatastoreBlockListCmd, "proxmox-backup-manager datastore remove ds1"},
			notApplied: []string{"ds1"},
		},
		{
			name:   "clean-path-change-fails-rollback-applies",
			strict: true,
			staged: ds1,
			rows:   `[{"name":"ds1","path":"/old"}]`,
			errs:   map[string]error{"proxmox-backup-manager datastore create ds1 /new --comment c1": errors.New("create refused")},
			wantLines: []string{head1, count, names,
				"INFO     Datastore ds1 is at /old on this PBS and at /new in the backup",
				"WARNING  ⚠ PBS datastore ds1: not applied",
				"WARNING  ⚠ PBS datastore configuration: not applied",
			},
			wantCalls: []string{pbsDatastoreBlockListCmd,
				"proxmox-backup-manager datastore remove ds1",
				"proxmox-backup-manager datastore create ds1 /new --comment c1",
				"proxmox-backup-manager datastore create ds1 /old --comment c1",
			},
			notApplied: []string{"ds1"},
		},
		{
			name:   "clean-path-change-and-rollback-fail",
			strict: true,
			staged: ds1,
			rows:   `[{"name":"ds1","path":"/old"}]`,
			errs: map[string]error{
				"proxmox-backup-manager datastore create ds1 /new --comment c1": errors.New("create refused"),
				"proxmox-backup-manager datastore create ds1 /old --comment c1": errors.New("rollback refused"),
			},
			wantLines: []string{head1, count, names,
				"INFO     Datastore ds1 is at /old on this PBS and at /new in the backup",
				"WARNING  ⚠ PBS datastore ds1: not configured, the data in /old is kept",
				"WARNING  ⚠ PBS datastore configuration: not applied",
			},
			wantCalls: []string{pbsDatastoreBlockListCmd,
				"proxmox-backup-manager datastore remove ds1",
				"proxmox-backup-manager datastore create ds1 /new --comment c1",
				"proxmox-backup-manager datastore create ds1 /old --comment c1",
			},
			notApplied: []string{"ds1"},
		},
		{
			name:   "merge-path-differs-keeps-current",
			staged: ds1,
			rows:   `[{"name":"ds1","path":"/old"}]`,
			wantLines: []string{head1, count, names,
				"INFO     Datastore ds1 stays at /old, the backup has it at /new",
				"INFO     ✓ PBS datastore ds1: applied",
				"INFO     ✓ PBS datastore configuration: applied",
			},
			wantCalls: []string{pbsDatastoreBlockListCmd, "proxmox-backup-manager datastore update ds1 --comment c1"},
		},
		{
			name:   "merge-path-differs-update-refused",
			staged: ds1,
			rows:   `[{"name":"ds1","path":"/old"}]`,
			errs:   map[string]error{"proxmox-backup-manager datastore update ds1 --comment c1": errors.New(pbsUpdateRefused)},
			wantLines: []string{head1, count, names,
				`INFO     Datastore ds1 was rejected by PBS: "permission check failed - missing Datastore.Modify on /datastore/ds1"`,
				"WARNING  ⚠ PBS datastore ds1: not applied",
				"WARNING  ⚠ PBS datastore configuration: not applied",
			},
			wantCalls:  []string{pbsDatastoreBlockListCmd, "proxmox-backup-manager datastore update ds1 --comment c1"},
			notApplied: []string{"ds1"},
		},
		{
			name:   "clean-stale-remove-refused",
			strict: true,
			staged: ds1,
			rows:   `[{"name":"ds1","path":"/new"},{"name":"stale","path":"/stale"}]`,
			errs:   map[string]error{"proxmox-backup-manager datastore remove stale": errors.New("datastore is in use")},
			wantLines: []string{head1, count, names,
				"INFO     Datastore stale is configured on this PBS but not present in the backup",
				"WARNING  ⚠ PBS datastore stale: not removed",
				"INFO     ✓ PBS datastore ds1: applied",
				"WARNING  ⚠ PBS datastore configuration: partially applied",
			},
			wantCalls: []string{pbsDatastoreBlockListCmd,
				"proxmox-backup-manager datastore remove stale",
				"proxmox-backup-manager datastore update ds1 --comment c1",
			},
			notRemoved: []string{"stale"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
			logger := logging.New(types.LogLevelDebug, false)
			var logBuf bytes.Buffer
			logger.SetOutput(&logBuf)
			writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/datastore.cfg", tc.staged, 0o640)
			runner.outputs = map[string][]byte{pbsDatastoreBlockListCmd: []byte(tc.rows)}
			runner.errs = tc.errs

			err := applyPBSDatastoreCfgViaAPI(context.Background(), logger, stageRoot, tc.strict)

			if got := pbsVisibleLines(logBuf.String()); strings.Join(got, "\n") != strings.Join(tc.wantLines, "\n") {
				t.Fatalf("visible lines\n--- got ---\n%s\n--- want ---\n%s\n--- full log ---\n%s", strings.Join(got, "\n"), strings.Join(tc.wantLines, "\n"), logBuf.String())
			}
			if strings.Join(runner.calls, "\n") != strings.Join(tc.wantCalls, "\n") {
				t.Fatalf("calls=%v want %v", runner.calls, tc.wantCalls)
			}
			if len(tc.notApplied) == 0 && len(tc.notRemoved) == 0 {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			var incomplete *pbsDatastoreRestoreIncompleteError
			if !errors.As(err, &incomplete) {
				t.Fatalf("expected *pbsDatastoreRestoreIncompleteError, got %T %v", err, err)
			}
			if strings.Join(incomplete.notApplied, ",") != strings.Join(tc.notApplied, ",") ||
				strings.Join(incomplete.notRemoved, ",") != strings.Join(tc.notRemoved, ",") {
				t.Fatalf("notApplied=%v notRemoved=%v, want %v %v", incomplete.notApplied, incomplete.notRemoved, tc.notApplied, tc.notRemoved)
			}
			if got, want := errors.Is(err, errPBSCleanRemoveIncomplete), len(tc.notRemoved) > 0; got != want {
				t.Fatalf("errors.Is(errPBSCleanRemoveIncomplete)=%v want %v", got, want)
			}
		})
	}
}

// The block's outcome is the last visible line of the datastore.cfg apply: the
// caller writes neither "PBS API apply: datastore.cfg failed" nor the step summary
// for it, runs no file fallback over it, and still marks the restore with warnings.
// Another item that fails in the same step keeps its summary line.
func TestMaybeApplyPBSConfigsFromStage_DatastoreBlockOutcomeIsTheLastLine(t *testing.T) {
	t.Run("datastore only", func(t *testing.T) {
		runner := &FakeCommandRunner{
			Outputs: map[string][]byte{pbsDatastoreBlockListCmd: []byte(`[{"name":"ds1","path":"/old"}]`)},
			Errors:  map[string]error{"proxmox-backup-manager datastore remove ds1": errors.New("datastore is in use")},
		}
		log, warned := runPBSDatastoreRestoreStep(t, PBSRestoreBehaviorClean, "datastore: ds1\n\tpath /new\n", runner)
		if !warned {
			t.Fatal("restore must end with warnings")
		}
		got := pbsVisibleLines(log)
		if last := got[len(got)-1]; last != "WARNING  ⚠ PBS datastore configuration: not applied" {
			t.Fatalf("the block outcome must be the last visible line, got %q\n%s", last, log)
		}
		for _, unwanted := range []string{"datastore.cfg failed", "failed to apply", "falling back to file-based"} {
			if strings.Contains(log, unwanted) {
				t.Fatalf("unexpected %q in log:\n%s", unwanted, log)
			}
		}
	})

	t.Run("datastore and another item", func(t *testing.T) {
		origS3 := pbsStagedApplyS3CfgViaAPIFn
		t.Cleanup(func() { pbsStagedApplyS3CfgViaAPIFn = origS3 })
		pbsStagedApplyS3CfgViaAPIFn = func(context.Context, *logging.Logger, string, bool) error {
			return errors.New("forced s3 error")
		}
		runner := &FakeCommandRunner{
			Outputs: map[string][]byte{pbsDatastoreBlockListCmd: []byte(`[]`)},
			Errors: map[string]error{
				"proxmox-backup-manager datastore create ds1 /new": errors.New("create refused"),
				"proxmox-backup-manager datastore update ds1":      errors.New("update refused"),
			},
		}
		log, warned := runPBSDatastoreRestoreStep(t, PBSRestoreBehaviorMerge, "datastore: ds1\n\tpath /new\n", runner)
		if !warned {
			t.Fatal("restore must end with warnings")
		}
		if !strings.Contains(log, "WARNING  PBS staged config apply: 1 PBS config item(s) failed to apply: s3.cfg\n") {
			t.Fatalf("the other item keeps its summary line, log:\n%s", log)
		}
		if strings.Contains(log, "datastore.cfg failed") || strings.Contains(log, "s3.cfg, datastore.cfg") {
			t.Fatalf("datastore.cfg must not be repeated after its block, log:\n%s", log)
		}
	})
}
