package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// pbsUnexpectedTarget is a config the staged apply restores through the PBS API,
// with the words of its block.
type pbsUnexpectedTarget struct {
	name     string
	category string
	file     string // under etc/proxmox-backup
	staged   string
	hook     *func(context.Context, *logging.Logger, string, bool) error
	opening  string
	count    string
	subject  string // subject of the configuration events
	outcome  string
}

func pbsUnexpectedTargets() []pbsUnexpectedTarget {
	return []pbsUnexpectedTarget{
		{"acme-plugins", "pbs_host", "acme/plugins.cfg", "dns: p1\n\tapi cf\n", &pbsStagedApplyAcmePluginsCfgViaAPIFn,
			"Restoring PBS ACME plugins...", "ACME plugins to restore: 1", "The ACME plugin configuration", "PBS ACME plugin configuration"},
		{"traffic-control", "pbs_host", "traffic-control.cfg", "rule: p1\n\trate-in 10MiB\n", &pbsStagedApplyTrafficControlCfgViaAPIFn,
			"Restoring PBS traffic control rules...", "Traffic control rules to restore: 1", "The traffic control configuration", "PBS traffic control configuration"},
		{"s3", "datastore_pbs", "s3.cfg", "s3-endpoint: p1\n\tendpoint s3.example.com\n", &pbsStagedApplyS3CfgViaAPIFn,
			"Restoring PBS S3 endpoints...", "S3 endpoints to restore: 1", "The S3 endpoint configuration", "PBS S3 endpoint configuration"},
		{"remote", "pbs_remotes", "remote.cfg", "remote: p1\n\thost 10.0.0.1\n", &pbsStagedApplyRemoteCfgViaAPIFn,
			"Restoring PBS remotes...", "Remotes to restore: 1", "The remote configuration", "PBS remote configuration"},
		{"sync", "pbs_jobs", "sync.cfg", "sync: p1\n\tstore ds1\n", &pbsStagedApplySyncCfgViaAPIFn,
			"Restoring PBS sync jobs...", "Sync jobs to restore: 1", "The sync job configuration", "PBS sync job configuration"},
		{"verification", "pbs_jobs", "verification.cfg", "verification: p1\n\tstore ds1\n", &pbsStagedApplyVerificationCfgViaAPIFn,
			"Restoring PBS verify jobs...", "Verify jobs to restore: 1", "The verify job configuration", "PBS verify job configuration"},
		{"prune", "pbs_jobs", "prune.cfg", "prune: p1\n\tstore ds1\n", &pbsStagedApplyPruneCfgViaAPIFn,
			"Restoring PBS prune jobs...", "Prune jobs to restore: 1", "The prune job configuration", "PBS prune job configuration"},
	}
}

// runPBSUnexpectedErrorStep runs the staged apply of tg's category with tg's API
// apply returning apiErr and every other API apply succeeding, through the
// restore step runner. It returns the log and whether the restore ends with
// warnings.
func runPBSUnexpectedErrorStep(t *testing.T, tg pbsUnexpectedTarget, behavior PBSRestoreBehavior, apiErr error, prepare func(*FakeFS)) (string, bool) {
	t.Helper()
	origFS, origCmd := restoreFS, restoreCmd
	origIsReal, origGeteuid := pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn
	origEnsure := pbsStagedApplyEnsurePBSServicesForAPIFn
	origNode, origDS := pbsStagedApplyNodeCfgViaAPIFn, pbsStagedApplyDatastoreCfgViaAPIFn
	saved := map[*func(context.Context, *logging.Logger, string, bool) error]func(context.Context, *logging.Logger, string, bool) error{}
	for _, other := range pbsUnexpectedTargets() {
		saved[other.hook] = *other.hook
	}
	t.Cleanup(func() {
		restoreFS, restoreCmd = origFS, origCmd
		pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn = origIsReal, origGeteuid
		pbsStagedApplyEnsurePBSServicesForAPIFn = origEnsure
		pbsStagedApplyNodeCfgViaAPIFn, pbsStagedApplyDatastoreCfgViaAPIFn = origNode, origDS
		for hook, fn := range saved {
			*hook = fn
		}
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreCmd = &FakeCommandRunner{}
	pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
	pbsStagedApplyGeteuidFn = func() int { return 0 }
	pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error { return nil }
	for _, other := range pbsUnexpectedTargets() {
		*other.hook = func(context.Context, *logging.Logger, string, bool) error { return nil }
	}
	pbsStagedApplyNodeCfgViaAPIFn = func(context.Context, string, []string) error { return nil }
	pbsStagedApplyDatastoreCfgViaAPIFn = func(context.Context, *logging.Logger, string, bool) error { return nil }
	*tg.hook = func(context.Context, *logging.Logger, string, bool) error { return apiErr }

	if err := fakeFS.WriteFile("/stage/etc/proxmox-backup/"+tg.file, []byte(tg.staged), 0o640); err != nil {
		t.Fatal(err)
	}
	if prepare != nil {
		prepare(fakeFS)
	}

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	plan := &RestorePlan{SystemType: SystemTypePBS, PBSRestoreBehavior: behavior, NormalCategories: []Category{{ID: tg.category}}}
	w := &restoreUIWorkflowRun{ctx: context.Background(), logger: logger, plan: plan}
	step := restoreStageApplyStep{name: "PBS staged config apply", run: func() error {
		return maybeApplyPBSConfigsFromStage(w.ctx, w.logger, w.plan, "/stage", false)
	}}
	if err := w.runStageApplyStep(step); err != nil {
		t.Fatalf("runStageApplyStep: %v", err)
	}
	return strings.ReplaceAll(buf.String(), fakeFS.Root, "<fakefs>"), w.restoreHadWarnings
}

// An error a section block does not return today (none of them can) is handled
// like the datastore block's: the block's opening, the event carrying the error,
// then in Clean mode the file written from the backup as the block, in Merge mode
// the configuration not applied. No step summary, and the restore ends with
// warnings.
func TestMaybeApplyPBSConfigsFromStage_UnexpectedSectionErrorIsTheBlock(t *testing.T) {
	apiErr := errors.New("unexpected API error")
	for _, tg := range pbsUnexpectedTargets() {
		event := tg.subject + ` could not be applied through the PBS API: "unexpected API error"`
		cases := []struct {
			name     string
			behavior PBSRestoreBehavior
			prepare  func(*FakeFS)
			want     []string
			written  bool
		}{
			{"clean", PBSRestoreBehaviorClean, nil, []string{
				"INFO     " + tg.opening,
				"INFO       " + tg.count,
				"INFO       Names: p1",
				"INFO     " + event,
				"WARNING  ⚠ " + tg.outcome + ": applied from the backup file",
			}, true},
			{"clean-write-fails", PBSRestoreBehaviorClean, func(f *FakeFS) {
				// A directory where the file is written: the write fails.
				_ = f.MkdirAll("/etc/proxmox-backup/"+tg.file+"/x", 0o755)
			}, []string{
				"INFO     " + tg.opening,
				"INFO       " + tg.count,
				"INFO       Names: p1",
				"INFO     " + event,
				"INFO     " + tg.subject + ` could not be written to this PBS: "<write error>"`,
				"WARNING  ⚠ " + tg.outcome + ": not applied",
			}, false},
			{"merge", PBSRestoreBehaviorMerge, nil, []string{
				"INFO     " + tg.opening,
				"INFO       " + tg.count,
				"INFO       Names: p1",
				"INFO     " + event,
				"WARNING  ⚠ " + tg.outcome + ": not applied",
			}, false},
		}
		for _, tc := range cases {
			t.Run(tg.name+"/"+tc.name, func(t *testing.T) {
				log, warned := runPBSUnexpectedErrorStep(t, tg, tc.behavior, apiErr, tc.prepare)
				t.Logf("full log:\n%s", strings.Join(pbsBlockLogLines(log), "\n"))
				var got []string
				for _, line := range pbsVisibleLines(log) {
					if i := strings.Index(line, ` could not be written to this PBS: "`); i >= 0 {
						line = line[:i] + ` could not be written to this PBS: "<write error>"`
					}
					got = append(got, line)
				}
				if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
					t.Fatalf("visible lines\n--- got ---\n%s\n--- want ---\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
				}
				if !warned {
					t.Fatal("a ⚠ outcome must end the restore with warnings")
				}
				if !strings.Contains(log, "DEBUG    PBS staged config apply: outcome already reported: ") {
					t.Fatal("the caller must take the block outcome as already reported")
				}
				if strings.Contains(log, "failed to apply") {
					t.Fatal("no step summary of failed items")
				}
				data, err := restoreFS.ReadFile("/etc/proxmox-backup/" + tg.file)
				if tc.written {
					if err != nil || string(data) != tg.staged {
						t.Fatalf("target = %q, %v; want the backup file written", data, err)
					}
				} else if err == nil {
					t.Fatalf("target written in %s: %q", tc.name, data)
				}
			})
		}
	}
}

// The same error of the datastore block in Merge mode: its opening, the event
// carrying the error, and the configuration not applied, as the section configs.
func TestPBSDatastoreRestoreBlock_UnexpectedAPIErrorInMergeIsNotApplied(t *testing.T) {
	log, warned := runPBSDatastoreRestoreStep(t, PBSRestoreBehaviorMerge, "datastore: ds1\n\tpath /mnt/ds1\n", &FakeCommandRunner{}, func(*FakeFS) {
		pbsStagedApplyDatastoreCfgViaAPIFn = func(context.Context, *logging.Logger, string, bool) error {
			return errors.New("unexpected datastore API error")
		}
	})
	t.Logf("full log:\n%s", strings.Join(pbsBlockLogLines(log), "\n"))
	want := []string{
		"INFO     Restoring PBS datastore configuration...",
		"INFO       Datastores to restore: 1",
		"INFO       Names: ds1",
		`INFO     The datastore configuration could not be applied through the PBS API: "unexpected datastore API error"`,
		"WARNING  ⚠ PBS datastore configuration: not applied",
	}
	if got := pbsVisibleLines(log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("visible lines\n--- got ---\n%s\n--- want ---\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !warned {
		t.Fatal("a ⚠ outcome must end the restore with warnings")
	}
	if !strings.Contains(log, "DEBUG    PBS staged config apply: outcome already reported: ") {
		t.Fatal("the caller must take the block outcome as already reported")
	}
	if _, err := restoreFS.Stat("/etc/proxmox-backup/datastore.cfg"); err == nil {
		t.Fatal("Merge mode must not write datastore.cfg")
	}
}

// A cancellation that comes with the datastore block's unexpected error stops the
// step at once: no block, no file written from the backup.
func TestMaybeApplyPBSConfigsFromStage_DatastoreUnexpectedErrorAfterCancellationStops(t *testing.T) {
	origFS := restoreFS
	origIsReal, origGeteuid := pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn
	origEnsure, origDS := pbsStagedApplyEnsurePBSServicesForAPIFn, pbsStagedApplyDatastoreCfgViaAPIFn
	origS3 := pbsStagedApplyS3CfgViaAPIFn
	t.Cleanup(func() {
		restoreFS = origFS
		pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn = origIsReal, origGeteuid
		pbsStagedApplyEnsurePBSServicesForAPIFn, pbsStagedApplyDatastoreCfgViaAPIFn = origEnsure, origDS
		pbsStagedApplyS3CfgViaAPIFn = origS3
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
	pbsStagedApplyGeteuidFn = func() int { return 0 }
	pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error { return nil }
	pbsStagedApplyS3CfgViaAPIFn = func(context.Context, *logging.Logger, string, bool) error { return nil }
	if err := fakeFS.WriteFile("/stage/etc/proxmox-backup/datastore.cfg", []byte("datastore: ds1\n\tpath /mnt/ds1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pbsStagedApplyDatastoreCfgViaAPIFn = func(context.Context, *logging.Logger, string, bool) error {
		cancel()
		return errors.New("signal: killed")
	}

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	plan := &RestorePlan{SystemType: SystemTypePBS, PBSRestoreBehavior: PBSRestoreBehaviorClean, NormalCategories: []Category{{ID: "datastore_pbs"}}}
	w := &restoreUIWorkflowRun{ctx: ctx, logger: logger, plan: plan}
	step := restoreStageApplyStep{name: "PBS staged config apply", run: func() error {
		return maybeApplyPBSConfigsFromStage(w.ctx, w.logger, w.plan, "/stage", false)
	}}
	if err := w.runStageApplyStep(step); !restoreAbortOrInput(err) {
		t.Fatalf("runStageApplyStep = %v; want an abort", err)
	}
	if got := pbsVisibleLines(buf.String()); len(got) != 0 {
		t.Fatalf("no visible line expected on cancellation:\n%s", strings.Join(got, "\n"))
	}
	if _, err := fakeFS.Stat("/etc/proxmox-backup/datastore.cfg"); err == nil {
		t.Fatal("datastore.cfg must not be written after a cancellation")
	}
}
