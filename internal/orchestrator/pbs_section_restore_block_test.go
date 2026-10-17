package orchestrator

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Refusals in the shape osCommandRunner.RunStdout returns them: the exit status, then
// the stderr of proxmox-backup-manager. The "Error: " texts were printed by
// proxmox-backup-manager 4.2.8 on VM 201 (2026-10-17); the object names are fixtures.
const (
	pbsRemoteCreateBadFingerprint = "exit status 255: Error: parameter verification failed - 'fingerprint': value does not match the regex pattern\n\nUsage: proxmox-backup-manager remote create <name> --auth-id <string> --host <string> --password <string> [OPTIONS]"
	pbsRemoteUpdateBadFingerprint = "exit status 255: Error: parameter verification failed - 'fingerprint': value does not match the regex pattern\n\nUsage: proxmox-backup-manager remote update <name> [OPTIONS]"
	pbsRemoteCreateExists         = "exit status 255: Error: parameter verification failed - 'name': remote 'r1' already exists."
	pbsRemoteUpdateMissing        = "exit status 255: Error: no such remote 'r1'"
	pbsRemoteRemoveInUse          = "exit status 255: Error: parameter verification failed - 'name': remote 'old' is used by sync job 'job1' (datastore 'ds1')"
	pbsRemoteListCmd              = "proxmox-backup-manager remote list --output-format=json"
)

// runPBSSectionRestoreStep runs the "PBS staged config apply" step through the
// staged-apply step runner with the real section-config API applies, and returns
// the full log (all levels), whether the restore was marked with warnings, and the
// fake filesystem (to check what a fallback wrote). files maps a path under
// /stage to its content.
func runPBSSectionRestoreStep(t *testing.T, behavior PBSRestoreBehavior, categories []string, files map[string]string, runner *FakeCommandRunner) (string, bool, *FakeFS) {
	t.Helper()
	origFS, origCmd := restoreFS, restoreCmd
	origIsReal, origGeteuid := pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn
	origEnsure := pbsStagedApplyEnsurePBSServicesForAPIFn
	origHooks := []func(context.Context, *logging.Logger, string, bool) error{
		pbsStagedApplyTrafficControlCfgViaAPIFn, pbsStagedApplyS3CfgViaAPIFn, pbsStagedApplyRemoteCfgViaAPIFn,
		pbsStagedApplySyncCfgViaAPIFn, pbsStagedApplyVerificationCfgViaAPIFn, pbsStagedApplyPruneCfgViaAPIFn,
	}
	t.Cleanup(func() {
		restoreFS, restoreCmd = origFS, origCmd
		pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn = origIsReal, origGeteuid
		pbsStagedApplyEnsurePBSServicesForAPIFn = origEnsure
		pbsStagedApplyTrafficControlCfgViaAPIFn, pbsStagedApplyS3CfgViaAPIFn, pbsStagedApplyRemoteCfgViaAPIFn = origHooks[0], origHooks[1], origHooks[2]
		pbsStagedApplySyncCfgViaAPIFn, pbsStagedApplyVerificationCfgViaAPIFn, pbsStagedApplyPruneCfgViaAPIFn = origHooks[3], origHooks[4], origHooks[5]
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreCmd = runner
	pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
	pbsStagedApplyGeteuidFn = func() int { return 0 }
	pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error { return nil }
	pbsStagedApplyTrafficControlCfgViaAPIFn = applyPBSTrafficControlCfgViaAPI
	pbsStagedApplyS3CfgViaAPIFn = applyPBSS3CfgViaAPI
	pbsStagedApplyRemoteCfgViaAPIFn = applyPBSRemoteCfgViaAPI
	pbsStagedApplySyncCfgViaAPIFn = applyPBSSyncCfgViaAPI
	pbsStagedApplyVerificationCfgViaAPIFn = applyPBSVerificationCfgViaAPI
	pbsStagedApplyPruneCfgViaAPIFn = applyPBSPruneCfgViaAPI

	for path, content := range files {
		if content == pbsStagedUnreadable {
			// A directory where the file should be: reading it fails.
			if err := fakeFS.MkdirAll("/stage/"+path, 0o750); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := fakeFS.WriteFile("/stage/"+path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	var logBuf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&logBuf)
	cats := make([]Category, 0, len(categories))
	for _, id := range categories {
		cats = append(cats, Category{ID: id})
	}
	plan := &RestorePlan{SystemType: SystemTypePBS, PBSRestoreBehavior: behavior, NormalCategories: cats}
	w := &restoreUIWorkflowRun{ctx: context.Background(), logger: logger, plan: plan}
	step := restoreStageApplyStep{name: "PBS staged config apply", run: func() error {
		return maybeApplyPBSConfigsFromStage(w.ctx, w.logger, w.plan, "/stage", false)
	}}
	if err := w.runStageApplyStep(step); err != nil {
		t.Fatalf("runStageApplyStep: %v", err)
	}
	return logBuf.String(), w.restoreHadWarnings, fakeFS
}

func pbsRemoteSection(name, host, password, fingerprint string) string {
	return "remote: " + name + "\n" +
		"\thost " + host + "\n" +
		"\tauth-id sync@pbs\n" +
		"\tpassword " + base64.StdEncoding.EncodeToString([]byte(password)) + "\n" +
		"\tfingerprint " + fingerprint + "\n"
}

func pbsRemoteCmd(verb, name, host, password, fingerprint string) string {
	return "proxmox-backup-manager remote " + verb + " " + name + " --host " + host + " --auth-id sync@pbs --password " + password + " --fingerprint " + fingerprint
}

const (
	pbsRemoteGoodFP = "aa:bb:cc"
	pbsRemoteBadFP  = "zz"
)

func assertNoSectionCallerLines(t *testing.T, log string) {
	t.Helper()
	for _, unwanted := range []string{"PBS API apply", "failed to apply", "falling back to file-based", "fallback"} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("unexpected %q in log:\n%s", unwanted, log)
		}
	}
}

// The approved remote scenario, Clean: old is on this PBS and not in the backup and
// its remove is refused, r1 exists (create refused, update applies), r2 is refused
// by PBS both ways. The block is the whole log of the step: no warning of the
// caller, no file fallback, and the restore ends with warnings.
func TestPBSSectionRestoreBlock_RemoteCleanScenario(t *testing.T) {
	staged := pbsRemoteSection("r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP) + "\n" + pbsRemoteSection("r2", "10.0.0.2", "secret-r2", pbsRemoteBadFP)
	runner := &FakeCommandRunner{
		Outputs: map[string][]byte{pbsRemoteListCmd: []byte(`[{"auth-id":"root@pam","host":"127.0.0.1","name":"old"},{"auth-id":"sync@pbs","host":"10.0.0.1","name":"r1"}]`)},
		Errors: map[string]error{
			"proxmox-backup-manager remote remove old":                             errors.New(pbsRemoteRemoveInUse),
			pbsRemoteCmd("create", "r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP): errors.New(pbsRemoteCreateExists),
			pbsRemoteCmd("create", "r2", "10.0.0.2", "secret-r2", pbsRemoteBadFP):  errors.New(pbsRemoteCreateBadFingerprint),
			pbsRemoteCmd("update", "r2", "10.0.0.2", "secret-r2", pbsRemoteBadFP):  errors.New(pbsRemoteUpdateBadFingerprint),
		},
	}

	log, warned, fakeFS := runPBSSectionRestoreStep(t, PBSRestoreBehaviorClean, []string{"pbs_remotes"}, map[string]string{"etc/proxmox-backup/remote.cfg": staged}, runner)
	t.Logf("full log:\n%s", log)

	assertPBSBlockLog(t, log, []string{
		"DEBUG    Start pbs staged apply: dryRun=false stage=/stage",
		`DEBUG    PBS remote restore: reading current remotes cmd="proxmox-backup-manager remote list --output-format=json"`,
		"INFO     Restoring PBS remotes...",
		"DEBUG    PBS remote restore: mode=clean staged=/stage/etc/proxmox-backup/remote.cfg",
		"DEBUG    PBS remote restore: backup remotes=2 names=r1,r2",
		"INFO       Remotes to restore: 2",
		"INFO       Names: r1, r2",
		"DEBUG    PBS remote restore: current remotes=2 names=old,r1",
		"DEBUG    PBS remote restore: remote=old not in backup action=remove",
		`DEBUG    PBS remote restore: remote=old remove failed error="exit status 255: Error: parameter verification failed - 'name': remote 'old' is used by sync job 'job1' (datastore 'ds1')"`,
		"INFO     Remote old is configured on this PBS but not present in the backup",
		"WARNING  ⚠ PBS remote old: not removed",
		`DEBUG    PBS remote restore: remote=r1 create failed error="exit status 255: Error: parameter verification failed - 'name': remote 'r1' already exists." action=update`,
		"DEBUG    PBS remote restore: remote=r1 updated",
		"INFO     ✓ PBS remote r1: applied",
		`DEBUG    PBS remote restore: remote=r2 create failed error="exit status 255: Error: parameter verification failed - 'fingerprint': value does not match the regex pattern\n\nUsage: proxmox-backup-manager remote create <name> --auth-id <string> --host <string> --password <string> [OPTIONS]" action=update`,
		`DEBUG    PBS remote restore: remote=r2 update failed error="exit status 255: Error: parameter verification failed - 'fingerprint': value does not match the regex pattern\n\nUsage: proxmox-backup-manager remote update <name> [OPTIONS]"`,
		`INFO     Remote r2 was rejected by PBS: "parameter verification failed - 'fingerprint': value does not match the regex pattern"`,
		"WARNING  ⚠ PBS remote r2: not applied",
		"WARNING  ⚠ PBS remote configuration: partially applied",
		"DEBUG    End pbs staged apply (error=PBS remote configuration incomplete: 1 remote(s) not applied [r2]; 1 stale remote(s) not removed [old], duration=<d>)",
		"DEBUG    PBS staged config apply: outcome already reported: PBS remote configuration incomplete: 1 remote(s) not applied [r2]; 1 stale remote(s) not removed [old]",
	})
	if !warned {
		t.Fatal("a remote not applied or not removed must end the restore with warnings")
	}
	wantCalls := []string{
		pbsRemoteListCmd,
		"proxmox-backup-manager remote remove old",
		pbsRemoteCmd("create", "r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP),
		pbsRemoteCmd("update", "r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP),
		pbsRemoteCmd("create", "r2", "10.0.0.2", "secret-r2", pbsRemoteBadFP),
		pbsRemoteCmd("update", "r2", "10.0.0.2", "secret-r2", pbsRemoteBadFP),
	}
	if strings.Join(runner.CallsList(), "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("calls\n%s\nwant\n%s", strings.Join(runner.CallsList(), "\n"), strings.Join(wantCalls, "\n"))
	}
	for _, secret := range []string{"secret-r1", "secret-r2", base64.StdEncoding.EncodeToString([]byte("secret-r1")), base64.StdEncoding.EncodeToString([]byte("secret-r2"))} {
		if strings.Contains(log, secret) {
			t.Fatalf("secret %q leaked into the log:\n%s", secret, log)
		}
	}
	if _, err := fakeFS.Stat("/etc/proxmox-backup/remote.cfg"); err == nil {
		t.Fatal("the file-based fallback must not run over the outcomes of the block")
	}
}

// The outcomes of the block outside the approved scenario, through the step runner:
// every line (all levels), whether the restore ends with warnings, and the
// commands run.
func TestPBSSectionRestoreBlock_RemoteOutcomes(t *testing.T) {
	r1 := pbsRemoteSection("r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP)
	r1Create := pbsRemoteCmd("create", "r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP)
	r1Update := pbsRemoteCmd("update", "r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP)
	r2 := pbsRemoteSection("r2", "10.0.0.2", "secret-r2", pbsRemoteGoodFP)
	r2Create := pbsRemoteCmd("create", "r2", "10.0.0.2", "secret-r2", pbsRemoteGoodFP)
	r2Update := pbsRemoteCmd("update", "r2", "10.0.0.2", "secret-r2", pbsRemoteGoodFP)
	const start = "DEBUG    Start pbs staged apply: dryRun=false stage=/stage"
	const reading = `DEBUG    PBS remote restore: reading current remotes cmd="proxmox-backup-manager remote list --output-format=json"`

	cases := []struct {
		name      string
		behavior  PBSRestoreBehavior
		staged    string
		runner    *FakeCommandRunner
		want      []string
		wantCalls []string
		warned    bool
	}{
		{
			name:     "all-applied",
			behavior: PBSRestoreBehaviorClean,
			staged:   r1 + "\n" + r2,
			runner: &FakeCommandRunner{
				Outputs: map[string][]byte{pbsRemoteListCmd: []byte(`[{"name":"r1"}]`)},
				Errors:  map[string]error{r1Create: errors.New(pbsRemoteCreateExists)},
			},
			want: []string{start, reading,
				"INFO     Restoring PBS remotes...",
				"DEBUG    PBS remote restore: mode=clean staged=/stage/etc/proxmox-backup/remote.cfg",
				"DEBUG    PBS remote restore: backup remotes=2 names=r1,r2",
				"INFO       Remotes to restore: 2",
				"INFO       Names: r1, r2",
				"DEBUG    PBS remote restore: current remotes=1 names=r1",
				`DEBUG    PBS remote restore: remote=r1 create failed error="exit status 255: Error: parameter verification failed - 'name': remote 'r1' already exists." action=update`,
				"DEBUG    PBS remote restore: remote=r1 updated",
				"INFO     ✓ PBS remote r1: applied",
				"DEBUG    PBS remote restore: remote=r2 created",
				"INFO     ✓ PBS remote r2: applied",
				"INFO     ✓ PBS remote configuration: applied",
				"DEBUG    End pbs staged apply (ok, duration=<d>)",
			},
			wantCalls: []string{pbsRemoteListCmd, r1Create, r1Update, r2Create},
		},
		{
			name:     "all-rejected",
			behavior: PBSRestoreBehaviorClean,
			staged:   r1,
			runner: &FakeCommandRunner{
				Outputs: map[string][]byte{pbsRemoteListCmd: []byte(`[]`)},
				Errors: map[string]error{
					r1Create: errors.New(pbsRemoteCreateBadFingerprint),
					r1Update: errors.New(pbsRemoteUpdateMissing),
				},
			},
			want: []string{start, reading,
				"INFO     Restoring PBS remotes...",
				"DEBUG    PBS remote restore: mode=clean staged=/stage/etc/proxmox-backup/remote.cfg",
				"DEBUG    PBS remote restore: backup remotes=1 names=r1",
				"INFO       Remotes to restore: 1",
				"INFO       Names: r1",
				"DEBUG    PBS remote restore: current remotes=0 names=",
				`DEBUG    PBS remote restore: remote=r1 create failed error="exit status 255: Error: parameter verification failed - 'fingerprint': value does not match the regex pattern\n\nUsage: proxmox-backup-manager remote create <name> --auth-id <string> --host <string> --password <string> [OPTIONS]" action=update`,
				`DEBUG    PBS remote restore: remote=r1 update failed error="exit status 255: Error: no such remote 'r1'"`,
				`INFO     Remote r1 was rejected by PBS: "parameter verification failed - 'fingerprint': value does not match the regex pattern"`,
				"WARNING  ⚠ PBS remote r1: not applied",
				"WARNING  ⚠ PBS remote configuration: not applied",
				"DEBUG    End pbs staged apply (error=PBS remote configuration incomplete: 1 remote(s) not applied [r1], duration=<d>)",
				"DEBUG    PBS staged config apply: outcome already reported: PBS remote configuration incomplete: 1 remote(s) not applied [r1]",
			},
			wantCalls: []string{pbsRemoteListCmd, r1Create, r1Update},
			warned:    true,
		},
		{
			name:     "stale-removed",
			behavior: PBSRestoreBehaviorClean,
			staged:   r1,
			runner: &FakeCommandRunner{
				Outputs: map[string][]byte{pbsRemoteListCmd: []byte(`[{"name":"r1"},{"name":"old"}]`)},
				Errors:  map[string]error{r1Create: errors.New(pbsRemoteCreateExists)},
			},
			want: []string{start, reading,
				"INFO     Restoring PBS remotes...",
				"DEBUG    PBS remote restore: mode=clean staged=/stage/etc/proxmox-backup/remote.cfg",
				"DEBUG    PBS remote restore: backup remotes=1 names=r1",
				"INFO       Remotes to restore: 1",
				"INFO       Names: r1",
				"DEBUG    PBS remote restore: current remotes=2 names=old,r1",
				"DEBUG    PBS remote restore: remote=old not in backup action=remove",
				"DEBUG    PBS remote restore: remote=old removed",
				"INFO     Remote old is configured on this PBS but not present in the backup",
				"INFO     ✓ PBS remote old: removed",
				`DEBUG    PBS remote restore: remote=r1 create failed error="exit status 255: Error: parameter verification failed - 'name': remote 'r1' already exists." action=update`,
				"DEBUG    PBS remote restore: remote=r1 updated",
				"INFO     ✓ PBS remote r1: applied",
				"INFO     ✓ PBS remote configuration: applied",
				"DEBUG    End pbs staged apply (ok, duration=<d>)",
			},
			wantCalls: []string{pbsRemoteListCmd, "proxmox-backup-manager remote remove old", r1Create, r1Update},
		},
		{
			// Merge keeps the remotes of this PBS that the backup does not have: it
			// does not read them and removes none.
			name:     "merge",
			behavior: PBSRestoreBehaviorMerge,
			staged:   r1 + "\n" + r2,
			runner: &FakeCommandRunner{
				Outputs: map[string][]byte{pbsRemoteListCmd: []byte(`[{"name":"r1"},{"name":"old"}]`)},
				Errors: map[string]error{
					r1Create: errors.New(pbsRemoteCreateExists),
					r2Create: errors.New(pbsRemoteCreateBadFingerprint),
					r2Update: errors.New(pbsRemoteUpdateBadFingerprint),
				},
			},
			want: []string{start,
				"INFO     Restoring PBS remotes...",
				"DEBUG    PBS remote restore: mode=merge staged=/stage/etc/proxmox-backup/remote.cfg",
				"DEBUG    PBS remote restore: backup remotes=2 names=r1,r2",
				"INFO       Remotes to restore: 2",
				"INFO       Names: r1, r2",
				`DEBUG    PBS remote restore: remote=r1 create failed error="exit status 255: Error: parameter verification failed - 'name': remote 'r1' already exists." action=update`,
				"DEBUG    PBS remote restore: remote=r1 updated",
				"INFO     ✓ PBS remote r1: applied",
				`DEBUG    PBS remote restore: remote=r2 create failed error="exit status 255: Error: parameter verification failed - 'fingerprint': value does not match the regex pattern\n\nUsage: proxmox-backup-manager remote create <name> --auth-id <string> --host <string> --password <string> [OPTIONS]" action=update`,
				`DEBUG    PBS remote restore: remote=r2 update failed error="exit status 255: Error: parameter verification failed - 'fingerprint': value does not match the regex pattern\n\nUsage: proxmox-backup-manager remote update <name> [OPTIONS]"`,
				`INFO     Remote r2 was rejected by PBS: "parameter verification failed - 'fingerprint': value does not match the regex pattern"`,
				"WARNING  ⚠ PBS remote r2: not applied",
				"WARNING  ⚠ PBS remote configuration: partially applied",
				"DEBUG    End pbs staged apply (error=PBS remote configuration incomplete: 1 remote(s) not applied [r2], duration=<d>)",
				"DEBUG    PBS staged config apply: outcome already reported: PBS remote configuration incomplete: 1 remote(s) not applied [r2]",
			},
			wantCalls: []string{r1Create, r1Update, r2Create, r2Update},
			warned:    true,
		},
		{
			name:     "nothing-to-restore-merge",
			behavior: PBSRestoreBehaviorMerge,
			staged:   "# no remotes\n",
			runner:   &FakeCommandRunner{},
			want: []string{start,
				"INFO     Restoring PBS remotes...",
				"DEBUG    PBS remote restore: mode=merge staged=/stage/etc/proxmox-backup/remote.cfg",
				"DEBUG    PBS remote restore: backup remotes=0 names=",
				"INFO       Remotes to restore: 0",
				"SKIP     PBS remote configuration: nothing to restore",
				"DEBUG    End pbs staged apply (ok, duration=<d>)",
			},
		},
		{
			name:     "nothing-to-restore-clean",
			behavior: PBSRestoreBehaviorClean,
			staged:   "",
			runner:   &FakeCommandRunner{Outputs: map[string][]byte{pbsRemoteListCmd: []byte(`[]`)}},
			want: []string{start, reading,
				"INFO     Restoring PBS remotes...",
				"DEBUG    PBS remote restore: mode=clean staged=/stage/etc/proxmox-backup/remote.cfg",
				"DEBUG    PBS remote restore: backup remotes=0 names=",
				"INFO       Remotes to restore: 0",
				"DEBUG    PBS remote restore: current remotes=0 names=",
				"SKIP     PBS remote configuration: nothing to restore",
				"DEBUG    End pbs staged apply (ok, duration=<d>)",
			},
			wantCalls: []string{pbsRemoteListCmd},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log, warned, fakeFS := runPBSSectionRestoreStep(t, tc.behavior, []string{"pbs_remotes"}, map[string]string{"etc/proxmox-backup/remote.cfg": tc.staged}, tc.runner)
			assertPBSBlockLog(t, log, tc.want)
			if warned != tc.warned {
				t.Fatalf("warned=%v want %v", warned, tc.warned)
			}
			if strings.Join(tc.runner.CallsList(), "\n") != strings.Join(tc.wantCalls, "\n") {
				t.Fatalf("calls\n%s\nwant\n%s", strings.Join(tc.runner.CallsList(), "\n"), strings.Join(tc.wantCalls, "\n"))
			}
			assertNoSectionCallerLines(t, log)
			if _, err := fakeFS.Stat("/etc/proxmox-backup/remote.cfg"); err == nil {
				t.Fatal("no file-based fallback may run")
			}
		})
	}
}

// One table over the six section configs: the same scenario (Clean, a stale object
// removed, a1 applied, a2 refused) logs each config with its own words, and its
// result is the block's incomplete error.
func TestPBSSectionRestoreBlock_LabelsPerKind(t *testing.T) {
	kinds := []struct {
		name    string
		file    string
		header  string
		command string
		listRow string
		apply   func(context.Context, *logging.Logger, string, bool) error
		// prefix and key are the DEBUG words, the rest the visible ones.
		prefix, key, keys                                       string
		opening, count, subject, event, outcome, notAppliedNoun string
	}{
		{
			name: "remote", file: "remote.cfg", header: "remote", command: "remote", listRow: `{"name":%q}`, apply: applyPBSRemoteCfgViaAPI,
			prefix: "PBS remote restore: ", key: "remote", keys: "remotes",
			opening: "Restoring PBS remotes...", count: "  Remotes to restore: 2", subject: "PBS remote", event: "Remote", outcome: "PBS remote configuration", notAppliedNoun: "remote",
		},
		{
			name: "s3", file: "s3.cfg", header: "s3-endpoint", command: "s3 endpoint", listRow: `{"id":%q}`, apply: applyPBSS3CfgViaAPI,
			prefix: "PBS S3 endpoint restore: ", key: "s3-endpoint", keys: "s3-endpoints",
			opening: "Restoring PBS S3 endpoints...", count: "  S3 endpoints to restore: 2", subject: "PBS S3 endpoint", event: "S3 endpoint", outcome: "PBS S3 endpoint configuration", notAppliedNoun: "S3 endpoint",
		},
		{
			name: "sync", file: "sync.cfg", header: "sync", command: "sync-job", listRow: `{"id":%q}`, apply: applyPBSSyncCfgViaAPI,
			prefix: "PBS sync job restore: ", key: "sync-job", keys: "sync-jobs",
			opening: "Restoring PBS sync jobs...", count: "  Sync jobs to restore: 2", subject: "PBS sync job", event: "Sync job", outcome: "PBS sync job configuration", notAppliedNoun: "sync job",
		},
		{
			name: "verification", file: "verification.cfg", header: "verification", command: "verify-job", listRow: `{"id":%q}`, apply: applyPBSVerificationCfgViaAPI,
			prefix: "PBS verify job restore: ", key: "verify-job", keys: "verify-jobs",
			opening: "Restoring PBS verify jobs...", count: "  Verify jobs to restore: 2", subject: "PBS verify job", event: "Verify job", outcome: "PBS verify job configuration", notAppliedNoun: "verify job",
		},
		{
			name: "prune", file: "prune.cfg", header: "prune", command: "prune-job", listRow: `{"id":%q}`, apply: applyPBSPruneCfgViaAPI,
			prefix: "PBS prune job restore: ", key: "prune-job", keys: "prune-jobs",
			opening: "Restoring PBS prune jobs...", count: "  Prune jobs to restore: 2", subject: "PBS prune job", event: "Prune job", outcome: "PBS prune job configuration", notAppliedNoun: "prune job",
		},
		{
			name: "traffic-control", file: "traffic-control.cfg", header: "rule", command: "traffic-control", listRow: `{"name":%q}`, apply: applyPBSTrafficControlCfgViaAPI,
			prefix: "PBS traffic control rule restore: ", key: "traffic-control-rule", keys: "traffic-control-rules",
			opening: "Restoring PBS traffic control rules...", count: "  Traffic control rules to restore: 2", subject: "PBS traffic control rule", event: "Traffic control rule", outcome: "PBS traffic control configuration", notAppliedNoun: "traffic control rule",
		},
	}
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
			writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/"+k.file, k.header+": a1\n\tcomment c\n\n"+k.header+": a2\n\tcomment c\n", 0o640)
			cmd := "proxmox-backup-manager " + k.command
			runner.outputs = map[string][]byte{cmd + " list --output-format=json": []byte("[" + strings.ReplaceAll(k.listRow, "%q", `"a1"`) + "," + strings.ReplaceAll(k.listRow, "%q", `"old"`) + "]")}
			runner.errs = map[string]error{
				cmd + " create a1 --comment c": errors.New(pbsRemoteCreateExists),
				cmd + " create a2 --comment c": errors.New(pbsRemoteCreateBadFingerprint),
				cmd + " update a2 --comment c": errors.New(pbsRemoteUpdateMissing),
			}
			var logBuf bytes.Buffer
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(&logBuf)

			err := k.apply(context.Background(), logger, stageRoot, true)

			createExists := `error="exit status 255: Error: parameter verification failed - 'name': remote 'r1' already exists."`
			createBad := `error="exit status 255: Error: parameter verification failed - 'fingerprint': value does not match the regex pattern\n\nUsage: proxmox-backup-manager remote create <name> --auth-id <string> --host <string> --password <string> [OPTIONS]"`
			updateBad := `error="exit status 255: Error: no such remote 'r1'"`
			assertPBSBlockLog(t, logBuf.String(), []string{
				`DEBUG    ` + k.prefix + `reading current ` + k.keys + ` cmd="` + cmd + ` list --output-format=json"`,
				"INFO     " + k.opening,
				"DEBUG    " + k.prefix + "mode=clean staged=/stage/etc/proxmox-backup/" + k.file,
				"DEBUG    " + k.prefix + "backup " + k.keys + "=2 names=a1,a2",
				"INFO     " + k.count,
				"INFO       Names: a1, a2",
				"DEBUG    " + k.prefix + "current " + k.keys + "=2 names=a1,old",
				"DEBUG    " + k.prefix + k.key + "=old not in backup action=remove",
				"DEBUG    " + k.prefix + k.key + "=old removed",
				"INFO     " + k.event + " old is configured on this PBS but not present in the backup",
				"INFO     ✓ " + k.subject + " old: removed",
				"DEBUG    " + k.prefix + k.key + "=a1 create failed " + createExists + " action=update",
				"DEBUG    " + k.prefix + k.key + "=a1 updated",
				"INFO     ✓ " + k.subject + " a1: applied",
				"DEBUG    " + k.prefix + k.key + "=a2 create failed " + createBad + " action=update",
				"DEBUG    " + k.prefix + k.key + "=a2 update failed " + updateBad,
				"INFO     " + k.event + ` a2 was rejected by PBS: "parameter verification failed - 'fingerprint': value does not match the regex pattern"`,
				"WARNING  ⚠ " + k.subject + " a2: not applied",
				"WARNING  ⚠ " + k.outcome + ": partially applied",
			})
			wantCalls := []string{
				cmd + " list --output-format=json",
				cmd + " remove old",
				cmd + " create a1 --comment c",
				cmd + " update a1 --comment c",
				cmd + " create a2 --comment c",
				cmd + " update a2 --comment c",
			}
			if strings.Join(runner.calls, "\n") != strings.Join(wantCalls, "\n") {
				t.Fatalf("calls\n%s\nwant\n%s", strings.Join(runner.calls, "\n"), strings.Join(wantCalls, "\n"))
			}
			var incomplete *pbsSectionRestoreIncompleteError
			if !errors.As(err, &incomplete) {
				t.Fatalf("expected *pbsSectionRestoreIncompleteError, got %T %v", err, err)
			}
			if want := k.outcome + " incomplete: 1 " + k.notAppliedNoun + "(s) not applied [a2]"; err.Error() != want {
				t.Fatalf("error %q, want %q", err.Error(), want)
			}
			if errors.Is(err, errPBSCleanRemoveIncomplete) {
				t.Fatal("no stale object was left: the error must not match errPBSCleanRemoveIncomplete")
			}
		})
	}
}

// A refused remove makes the block's error match errPBSCleanRemoveIncomplete, also
// when every object of the backup was applied; a block with nothing refused
// returns nil.
func TestPBSSectionRestoreBlock_IncompleteErrorIdentity(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/prune.cfg", "prune: p1\n\tstore ds1\n", 0o640)
	runner.outputs = map[string][]byte{"proxmox-backup-manager prune-job list --output-format=json": []byte(`[{"id":"p1"},{"id":"old"}]`)}
	runner.errs = map[string]error{"proxmox-backup-manager prune-job remove old": errors.New("exit status 255: Error: refused")}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&bytes.Buffer{})

	err := applyPBSPruneCfgViaAPI(context.Background(), logger, stageRoot, true)
	var incomplete *pbsSectionRestoreIncompleteError
	if !errors.As(err, &incomplete) || !errors.Is(err, errPBSCleanRemoveIncomplete) {
		t.Fatalf("err=%T %v: want a pbsSectionRestoreIncompleteError matching errPBSCleanRemoveIncomplete", err, err)
	}
	if want := "PBS prune job configuration incomplete: 1 stale prune job(s) not removed [old]"; err.Error() != want {
		t.Fatalf("error %q, want %q", err.Error(), want)
	}
	if len(incomplete.notApplied) != 0 || strings.Join(incomplete.notRemoved, ",") != "old" {
		t.Fatalf("notApplied=%v notRemoved=%v", incomplete.notApplied, incomplete.notRemoved)
	}
}

// The cases the block does not cover keep the lines they had before it: a staged
// file absent (DEBUG only), a staged file that cannot be read, and in Clean mode a
// list of this PBS's remotes that cannot be read. The last two open no block: the
// caller warns and runs the file-based fallback, as before.
func TestPBSSectionRestoreBlock_UncoveredCasesKeepTheirLines(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		runner := &FakeCommandRunner{}
		log, warned, _ := runPBSSectionRestoreStep(t, PBSRestoreBehaviorClean, []string{"pbs_remotes"}, nil, runner)
		assertPBSBlockLog(t, log, []string{
			"DEBUG    Start pbs staged apply: dryRun=false stage=/stage",
			"DEBUG    PBS remote restore: staged=/stage/etc/proxmox-backup/remote.cfg not in the backup action=skip",
			"DEBUG    End pbs staged apply (ok, duration=<d>)",
		})
		if warned || len(runner.CallsList()) != 0 {
			t.Fatalf("warned=%v calls=%v", warned, runner.CallsList())
		}
	})
	t.Run("list-unreadable-clean", func(t *testing.T) {
		runner := &FakeCommandRunner{Errors: map[string]error{pbsRemoteListCmd: errors.New("exit status 255: Error: unable to read remote.cfg")}}
		log, warned, fakeFS := runPBSSectionRestoreStep(t, PBSRestoreBehaviorClean, []string{"pbs_remotes"}, map[string]string{"etc/proxmox-backup/remote.cfg": "remote: r1\n\thost 10.0.0.1\n"}, runner)
		if got := pbsVisibleLines(log); strings.Join(got, "\n") != strings.Join([]string{
			"WARNING  PBS API apply: remote.cfg failed: proxmox-backup-manager remote list --output-format=json failed: exit status 255: Error: unable to read remote.cfg",
			"WARNING  PBS staged apply: falling back to file-based remote.cfg",
		}, "\n") {
			t.Fatalf("visible lines:\n%s\nfull log:\n%s", strings.Join(got, "\n"), log)
		}
		if !strings.Contains(log, `DEBUG    PBS remote restore: remote list failed error="exit status 255: Error: unable to read remote.cfg"`) {
			t.Fatalf("missing the DEBUG evidence of the failed list:\n%s", log)
		}
		if warned {
			t.Fatal("the fallback applied the file: no warning outcome")
		}
		if _, err := fakeFS.Stat("/etc/proxmox-backup/remote.cfg"); err != nil {
			t.Fatalf("the file-based fallback must still run: %v", err)
		}
	})
	t.Run("staged-unreadable", func(t *testing.T) {
		runner := &FakeCommandRunner{}
		log, warned, _ := runPBSSectionRestoreStep(t, PBSRestoreBehaviorMerge, []string{"pbs_remotes"}, map[string]string{"etc/proxmox-backup/remote.cfg": pbsStagedUnreadable}, runner)
		got := pbsVisibleLines(log)
		if len(got) != 2 || !strings.HasPrefix(got[0], "WARNING  PBS API apply: remote.cfg failed: read staged etc/proxmox-backup/remote.cfg: ") ||
			got[1] != "WARNING  PBS staged config apply: 1 PBS config item(s) failed to apply: remote.cfg" {
			t.Fatalf("visible lines:\n%s", strings.Join(got, "\n"))
		}
		if !warned {
			t.Fatal("restore must end with warnings")
		}
	})
}

// With other items in the same step: an item that fails keeps its summary line
// and the section blocks are not repeated in it; two blocks whose outcomes are
// logged both reach the DEBUG line of the step.
func TestMaybeApplyPBSConfigsFromStage_SectionBlockOutcomeIsNotRepeated(t *testing.T) {
	r1 := pbsRemoteSection("r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP)
	refused := map[string]error{
		pbsRemoteCmd("create", "r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP): errors.New(pbsRemoteCreateBadFingerprint),
		pbsRemoteCmd("update", "r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP): errors.New(pbsRemoteUpdateBadFingerprint),
	}

	t.Run("with a failed item", func(t *testing.T) {
		// s3.cfg cannot be read: an error the block does not cover, reported by the
		// caller as before, and in Merge mode a failed item.
		runner := &FakeCommandRunner{Errors: refused}
		log, warned, _ := runPBSSectionRestoreStep(t, PBSRestoreBehaviorMerge, []string{"pbs_remotes", "datastore_pbs"}, map[string]string{
			"etc/proxmox-backup/remote.cfg": r1,
			"etc/proxmox-backup/s3.cfg":     pbsStagedUnreadable,
		}, runner)
		if !warned {
			t.Fatal("restore must end with warnings")
		}
		got := pbsVisibleLines(log)
		if last := got[len(got)-1]; last != "WARNING  PBS staged config apply: 1 PBS config item(s) failed to apply: s3.cfg" {
			t.Fatalf("the other item keeps its summary line, got %q, log:\n%s", last, log)
		}
		if strings.Contains(log, "remote.cfg failed") || strings.Contains(log, "s3.cfg, remote.cfg") {
			t.Fatalf("remote.cfg must not be repeated after its block, log:\n%s", log)
		}
		if !strings.Contains(log, "WARNING  ⚠ PBS remote configuration: not applied\n") {
			t.Fatalf("missing the remote block outcome, log:\n%s", log)
		}
	})

	t.Run("two blocks", func(t *testing.T) {
		errs := map[string]error{
			"proxmox-backup-manager sync-job create s1 --store ds1": errors.New("exit status 255: Error: refused"),
			"proxmox-backup-manager sync-job update s1 --store ds1": errors.New("exit status 255: Error: refused"),
		}
		for k, v := range refused {
			errs[k] = v
		}
		runner := &FakeCommandRunner{Errors: errs}
		log, warned, _ := runPBSSectionRestoreStep(t, PBSRestoreBehaviorMerge, []string{"pbs_remotes", "pbs_jobs"}, map[string]string{
			"etc/proxmox-backup/remote.cfg": r1,
			"etc/proxmox-backup/sync.cfg":   "sync: s1\n\tstore ds1\n",
		}, runner)
		if !warned {
			t.Fatal("restore must end with warnings")
		}
		want := "DEBUG    PBS staged config apply: outcome already reported: PBS remote configuration incomplete: 1 remote(s) not applied [r1]; PBS sync job configuration incomplete: 1 sync job(s) not applied [s1]"
		lines := pbsBlockLogLines(log)
		if last := lines[len(lines)-1]; last != want {
			t.Fatalf("last line %q, want %q\n%s", last, want, log)
		}
		assertNoSectionCallerLines(t, log)
	})
}
