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
func runPBSSectionRestoreStep(t *testing.T, behavior PBSRestoreBehavior, categories []string, files map[string]string, runner *FakeCommandRunner, prepare ...func()) (string, bool, *FakeFS) {
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
	for _, p := range prepare {
		p()
	}

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
	for _, unwanted := range []string{"PBS API apply", "failed to apply", "falling back to file-based"} {
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
		"DEBUG    PBS remote restore: remote=r1 password decoded from base64",
		`DEBUG    PBS remote restore: remote=r1 create failed error="exit status 255: Error: parameter verification failed - 'name': remote 'r1' already exists." action=update`,
		"DEBUG    PBS remote restore: remote=r1 updated",
		"INFO     ✓ PBS remote r1: applied",
		"DEBUG    PBS remote restore: remote=r2 password decoded from base64",
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
				"DEBUG    PBS remote restore: remote=r1 password decoded from base64",
				`DEBUG    PBS remote restore: remote=r1 create failed error="exit status 255: Error: parameter verification failed - 'name': remote 'r1' already exists." action=update`,
				"DEBUG    PBS remote restore: remote=r1 updated",
				"INFO     ✓ PBS remote r1: applied",
				"DEBUG    PBS remote restore: remote=r2 password decoded from base64",
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
				"DEBUG    PBS remote restore: remote=r1 password decoded from base64",
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
				"DEBUG    PBS remote restore: remote=r1 password decoded from base64",
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
				"DEBUG    PBS remote restore: remote=r1 password decoded from base64",
				`DEBUG    PBS remote restore: remote=r1 create failed error="exit status 255: Error: parameter verification failed - 'name': remote 'r1' already exists." action=update`,
				"DEBUG    PBS remote restore: remote=r1 updated",
				"INFO     ✓ PBS remote r1: applied",
				"DEBUG    PBS remote restore: remote=r2 password decoded from base64",
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
			if len(incomplete.notRemoved) != 0 {
				t.Fatalf("no stale object was left: not removed = %v", incomplete.notRemoved)
			}
		})
	}
}

// A refused remove makes the block's error the incomplete one naming the stale
// object, also when every object of the backup was applied; a block with nothing
// refused returns nil.
func TestPBSSectionRestoreBlock_IncompleteErrorIdentity(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/prune.cfg", "prune: p1\n\tstore ds1\n", 0o640)
	runner.outputs = map[string][]byte{"proxmox-backup-manager prune-job list --output-format=json": []byte(`[{"id":"p1"},{"id":"old"}]`)}
	runner.errs = map[string]error{"proxmox-backup-manager prune-job remove old": errors.New("exit status 255: Error: refused")}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&bytes.Buffer{})

	err := applyPBSPruneCfgViaAPI(context.Background(), logger, stageRoot, true)
	var incomplete *pbsSectionRestoreIncompleteError
	if !errors.As(err, &incomplete) || strings.Join(incomplete.notRemoved, ",") != "old" {
		t.Fatalf("err=%T %v: want a pbsSectionRestoreIncompleteError with old not removed", err, err)
	}
	if want := "PBS prune job configuration incomplete: 1 stale prune job(s) not removed [old]"; err.Error() != want {
		t.Fatalf("error %q, want %q", err.Error(), want)
	}
	if len(incomplete.notApplied) != 0 || strings.Join(incomplete.notRemoved, ",") != "old" {
		t.Fatalf("notApplied=%v notRemoved=%v", incomplete.notApplied, incomplete.notRemoved)
	}
}

// A staged file absent from the backup is DEBUG only: no block, no command.
func TestPBSSectionRestoreBlock_AbsentStagedFile(t *testing.T) {
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
}

// pbsRemoteStagedReadErr is the error of reading the staged remote.cfg when a
// directory stands where the file should be, as the block logs it.
const pbsRemoteStagedReadErr = "read staged etc/proxmox-backup/remote.cfg: read <fakefs>/stage/etc/proxmox-backup/remote.cfg: is a directory"

// A staged file that cannot be read ends the block as not applied, in Clean and
// Merge alike: no command, no file-based fallback (it would read the same file),
// and the caller adds nothing; the restore ends with warnings.
func TestPBSSectionRestoreBlock_StagedUnreadable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		behavior PBSRestoreBehavior
		mode     string
	}{
		{"clean", PBSRestoreBehaviorClean, "clean"},
		{"merge", PBSRestoreBehaviorMerge, "merge"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &FakeCommandRunner{}
			log, warned, fakeFS := runPBSSectionRestoreStep(t, tc.behavior, []string{"pbs_remotes"}, map[string]string{"etc/proxmox-backup/remote.cfg": pbsStagedUnreadable}, runner)
			assertPBSBlockLog(t, log, []string{
				"DEBUG    Start pbs staged apply: dryRun=false stage=/stage",
				"INFO     Restoring PBS remotes...",
				"DEBUG    PBS remote restore: mode=" + tc.mode + " staged=/stage/etc/proxmox-backup/remote.cfg",
				`DEBUG    PBS remote restore: staged=/stage/etc/proxmox-backup/remote.cfg read failed error="` + pbsRemoteStagedReadErr + `"`,
				`INFO     The remote configuration in the backup could not be read: "` + pbsRemoteStagedReadErr + `"`,
				"WARNING  ⚠ PBS remote configuration: not applied",
				"DEBUG    End pbs staged apply (error=" + pbsRemoteStagedReadErr + ", duration=<d>)",
				"DEBUG    PBS staged config apply: outcome already reported: " + pbsRemoteStagedReadErr,
			})
			if !warned {
				t.Fatal("restore must end with warnings")
			}
			if calls := runner.CallsList(); len(calls) != 0 {
				t.Fatalf("no command may run, calls: %v", calls)
			}
			assertNoSectionCallerLines(t, log)
			if _, err := fakeFS.Stat("/etc/proxmox-backup/remote.cfg"); err == nil {
				t.Fatal("no file-based fallback may run")
			}
		})
	}
}

// pbsRemoteListUnparsable is what proxmox-backup-manager 4.2.8 answered on VM 201
// (2026-10-17) to `remote list` with a remote.cfg it could not parse, in the shape
// osCommandRunner.RunStdout returns it.
const pbsRemoteListUnparsable = `exit status 255: Error: parsing "/etc/proxmox-backup/remote.cfg" failed: line 1 - syntax error (expected header)`

// pbsFaultFS is the FakeFS with the read of readFail and the rename onto
// renameFail failing, to break the current-file read or the commit of an atomic
// write on their own.
type pbsFaultFS struct {
	*FakeFS
	readFail   string
	renameFail string
}

func (f *pbsFaultFS) ReadFile(path string) ([]byte, error) {
	if path == f.readFail {
		return nil, errors.New("injected read failure")
	}
	return f.FakeFS.ReadFile(path)
}

func (f *pbsFaultFS) Rename(oldpath, newpath string) error {
	if newpath == f.renameFail {
		return errors.New("injected rename failure")
	}
	return f.FakeFS.Rename(oldpath, newpath)
}

// Clean mode, the remotes configured on this PBS cannot be read: the block writes
// the staged remote.cfg as it is (or removes the target for a staged file without
// remotes), names every remote of the current file that the backup does not have,
// and ends with "applied from the backup file"; when the write fails nothing is
// named and the configuration is not applied. Either way the caller adds nothing
// and the restore ends with warnings.
func TestPBSSectionRestoreBlock_CurrentUnreadable(t *testing.T) {
	const (
		target    = "/etc/proxmox-backup/remote.cfg"
		twoStaged = "remote: r1\n\thost 10.0.0.1\n\nremote: r2\n\thost 10.0.0.2\n"
		current   = "remote: r1\n\thost 10.0.0.1\n\nremote: old\n\thost 10.0.0.9\n"
		listErr   = "proxmox-backup-manager remote list --output-format=json failed: " + pbsRemoteListUnparsable
		listDebug = "DEBUG    PBS remote restore: remote list failed error=\"exit status 255: Error: parsing \\\"/etc/proxmox-backup/remote.cfg\\\" failed: line 1 - syntax error (expected header)\""
		reading   = `DEBUG    PBS remote restore: reading current remotes cmd="proxmox-backup-manager remote list --output-format=json"`
		start     = "DEBUG    Start pbs staged apply: dryRun=false stage=/stage"
		fallback  = "DEBUG    PBS remote restore: fallback=file target=/etc/proxmox-backup/remote.cfg source=/stage/etc/proxmox-backup/remote.cfg"
		event     = "INFO     The remotes configured on this PBS could not be read"
		applied   = "WARNING  ⚠ PBS remote configuration: applied from the backup file"
		syncNone  = "DEBUG    PBS remote restore: fallback current sync jobs=0 remotes_used="
	)
	openTwo := []string{
		"INFO     Restoring PBS remotes...",
		"DEBUG    PBS remote restore: mode=clean staged=/stage/etc/proxmox-backup/remote.cfg",
		"DEBUG    PBS remote restore: backup remotes=2 names=r1,r2",
		"INFO       Remotes to restore: 2",
		"INFO       Names: r1, r2",
		event, fallback,
	}
	openNone := []string{
		"INFO     Restoring PBS remotes...",
		"DEBUG    PBS remote restore: mode=clean staged=/stage/etc/proxmox-backup/remote.cfg",
		"DEBUG    PBS remote restore: backup remotes=0 names=",
		"INFO       Remotes to restore: 0",
		event, fallback,
	}
	helperOK := []string{
		"DEBUG    Start pbs staged apply remote.cfg: stage=/stage",
		"DEBUG    pbs staged apply file: Applied etc/proxmox-backup/remote.cfg -> /etc/proxmox-backup/remote.cfg",
		"DEBUG    End pbs staged apply remote.cfg (ok, duration=<d>)",
	}
	dropOld := []string{
		"DEBUG    PBS remote restore: remote=old not in backup action=drop from file",
		"INFO     Remote old is configured on this PBS but not present in the backup",
		"INFO     ✓ PBS remote old: removed",
	}
	dropR1 := []string{
		"DEBUG    PBS remote restore: remote=r1 not in backup action=drop from file",
		"INFO     Remote r1 is configured on this PBS but not present in the backup",
		"INFO     ✓ PBS remote r1: removed",
	}
	tail := func(stepErr string) []string {
		return []string{
			"DEBUG    End pbs staged apply (error=" + stepErr + ", duration=<d>)",
			"DEBUG    PBS staged config apply: outcome already reported: " + stepErr,
		}
	}
	join := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	listFails := func() *FakeCommandRunner {
		return &FakeCommandRunner{Errors: map[string]error{pbsRemoteListCmd: errors.New(pbsRemoteListUnparsable)}}
	}
	const renameErr = "write /etc/proxmox-backup/remote.cfg: injected rename failure"

	cases := []struct {
		name    string
		staged  string
		current *string
		runner  *FakeCommandRunner
		fault   *pbsFaultFS
		want    []string
		// wantTarget is the content of the target after the step; nil: absent.
		wantTarget *string
	}{
		{
			name: "non-empty-drops-one", staged: twoStaged, current: pbsStrPtr(current), runner: listFails(),
			want: join([]string{start, reading, listDebug}, openTwo, []string{
				"DEBUG    PBS remote restore: fallback current file remotes=2 names=old,r1",
				syncNone,
			}, helperOK, []string{"DEBUG    PBS remote restore: fallback written"}, dropOld, []string{applied}, tail(listErr)),
			wantTarget: pbsStrPtr(strings.TrimSpace(twoStaged) + "\n"),
		},
		{
			name: "list-unparseable-no-current-file", staged: twoStaged, runner: &FakeCommandRunner{Outputs: map[string][]byte{pbsRemoteListCmd: []byte("not json")}},
			want: join([]string{start, reading,
				`DEBUG    PBS remote restore: remote list returned unparseable output error="invalid character 'o' in literal null (expecting 'u')"`,
			}, openTwo, []string{
				"DEBUG    PBS remote restore: fallback current file remotes=0 names=",
				syncNone,
			}, helperOK, []string{"DEBUG    PBS remote restore: fallback written", applied},
				tail("parse remote list: invalid character 'o' in literal null (expecting 'u')")),
			wantTarget: pbsStrPtr(strings.TrimSpace(twoStaged) + "\n"),
		},
		{
			name: "empty", staged: "", current: pbsStrPtr(current), runner: listFails(),
			want: join([]string{start, reading, listDebug}, openNone, []string{
				"DEBUG    PBS remote restore: fallback current file remotes=2 names=old,r1",
				syncNone,
				"DEBUG    PBS remote restore: fallback staged file has no remotes comments_only=false, target removed",
			}, dropOld, dropR1, []string{applied}, tail(listErr)),
		},
		{
			name: "comments-only", staged: "# no remotes\n", current: pbsStrPtr(current), runner: listFails(),
			want: join([]string{start, reading, listDebug}, openNone, []string{
				"DEBUG    PBS remote restore: fallback current file remotes=2 names=old,r1",
				syncNone,
				"DEBUG    PBS remote restore: fallback staged file has no remotes comments_only=true, target removed",
			}, dropOld, dropR1, []string{applied}, tail(listErr)),
		},
		{
			name: "current-file-unreadable-names-nothing", staged: twoStaged, current: pbsStrPtr(current), runner: listFails(),
			fault: &pbsFaultFS{readFail: target},
			want: join([]string{start, reading, listDebug}, openTwo, []string{
				`DEBUG    PBS remote restore: fallback current file read failed error="injected read failure"`,
				syncNone,
			}, helperOK, []string{"DEBUG    PBS remote restore: fallback written", applied}, tail(listErr)),
			wantTarget: nil, // the injected read failure hides the target; checked below
		},
		{
			// "remote :r1" is a header for the section parser and not for the file
			// write, which would skip the file: the block says it was not written.
			name: "header-the-write-rejects", staged: "remote :r1\n\thost 10.0.0.1\n", current: pbsStrPtr(current), runner: listFails(),
			want: join([]string{start, reading, listDebug}, []string{
				"INFO     Restoring PBS remotes...",
				"DEBUG    PBS remote restore: mode=clean staged=/stage/etc/proxmox-backup/remote.cfg",
				"DEBUG    PBS remote restore: backup remotes=1 names=r1",
				"INFO       Remotes to restore: 1",
				"INFO       Names: r1",
				event, fallback,
				"DEBUG    PBS remote restore: fallback current file remotes=2 names=old,r1",
				syncNone,
				`DEBUG    PBS remote restore: fallback write failed error="etc/proxmox-backup/remote.cfg does not look like a valid PBS config file (missing section header)"`,
				`INFO     The remote configuration could not be written to this PBS: "etc/proxmox-backup/remote.cfg does not look like a valid PBS config file (missing section header)"`,
				"WARNING  ⚠ PBS remote configuration: not applied",
			}, tail(listErr+"; file fallback: etc/proxmox-backup/remote.cfg does not look like a valid PBS config file (missing section header)")),
			wantTarget: pbsStrPtr(current),
		},
		{
			name: "write-fails-names-nothing", staged: twoStaged, current: pbsStrPtr(current), runner: listFails(),
			fault: &pbsFaultFS{renameFail: target},
			want: join([]string{start, reading, listDebug}, openTwo, []string{
				"DEBUG    PBS remote restore: fallback current file remotes=2 names=old,r1",
				syncNone,
				"DEBUG    Start pbs staged apply remote.cfg: stage=/stage",
				"DEBUG    End pbs staged apply remote.cfg (error=" + renameErr + ", duration=<d>)",
				`DEBUG    PBS remote restore: fallback write failed error="` + renameErr + `"`,
				`INFO     The remote configuration could not be written to this PBS: "` + renameErr + `"`,
				"WARNING  ⚠ PBS remote configuration: not applied",
			}, tail(listErr+"; file fallback: "+renameErr)),
			wantTarget: pbsStrPtr(current),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var prepare []func()
			if tc.current != nil {
				content := *tc.current
				prepare = append(prepare, func() {
					if err := restoreFS.WriteFile(target, []byte(content), 0o640); err != nil {
						t.Fatal(err)
					}
				})
			}
			if tc.fault != nil {
				fault := tc.fault
				prepare = append(prepare, func() {
					fault.FakeFS = restoreFS.(*FakeFS)
					restoreFS = fault
				})
			}
			log, warned, fakeFS := runPBSSectionRestoreStep(t, PBSRestoreBehaviorClean, []string{"pbs_remotes"}, map[string]string{"etc/proxmox-backup/remote.cfg": tc.staged}, tc.runner, prepare...)
			assertPBSBlockLog(t, log, tc.want)
			if !warned {
				t.Fatal("restore must end with warnings")
			}
			if calls := tc.runner.CallsList(); strings.Join(calls, "\n") != pbsRemoteListCmd {
				t.Fatalf("only the list may run, calls: %v", calls)
			}
			assertNoSectionCallerLines(t, log)
			for _, unwanted := range []string{"WARNING  PBS staged apply:", "is empty; removing"} {
				if strings.Contains(log, unwanted) {
					t.Fatalf("the file helper's own line %q must not appear in the block:\n%s", unwanted, log)
				}
			}
			got, err := fakeFS.ReadFile(target)
			switch {
			case tc.fault != nil && tc.fault.readFail != "":
				if err != nil || string(got) != strings.TrimSpace(tc.staged)+"\n" {
					t.Fatalf("target %q (err %v), want the staged file", got, err)
				}
			case tc.wantTarget == nil:
				if err == nil {
					t.Fatalf("the target must be removed, got %q", got)
				}
			default:
				if err != nil || string(got) != *tc.wantTarget {
					t.Fatalf("target %q (err %v), want %q", got, err, *tc.wantTarget)
				}
			}
		})
	}
}

func pbsStrPtr(s string) *string { return &s }

// With other items in the same step: an item whose API apply returned an error
// the blocks do not return today gets its own block, and the section blocks are
// not repeated; two blocks whose outcomes are logged both reach the DEBUG line of
// the step.
func TestMaybeApplyPBSConfigsFromStage_SectionBlockOutcomeIsNotRepeated(t *testing.T) {
	r1 := pbsRemoteSection("r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP)
	refused := map[string]error{
		pbsRemoteCmd("create", "r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP): errors.New(pbsRemoteCreateBadFingerprint),
		pbsRemoteCmd("update", "r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP): errors.New(pbsRemoteUpdateBadFingerprint),
	}

	t.Run("with an unexpected error", func(t *testing.T) {
		// The s3.cfg apply fails with a plain error: in Merge mode its block says
		// the configuration was not applied.
		runner := &FakeCommandRunner{Errors: refused}
		log, warned, _ := runPBSSectionRestoreStep(t, PBSRestoreBehaviorMerge, []string{"pbs_remotes", "datastore_pbs"}, map[string]string{
			"etc/proxmox-backup/remote.cfg": r1,
			"etc/proxmox-backup/s3.cfg":     "s3-endpoint: e1\n\tendpoint s3.example.com\n",
		}, runner, func() {
			pbsStagedApplyS3CfgViaAPIFn = func(context.Context, *logging.Logger, string, bool) error { return errors.New("forced s3 error") }
		})
		if !warned {
			t.Fatal("restore must end with warnings")
		}
		got := strings.Join(pbsVisibleLines(log), "\n")
		for _, want := range []string{
			`INFO     The S3 endpoint configuration could not be applied through the PBS API: "forced s3 error"` + "\n" + "WARNING  ⚠ PBS S3 endpoint configuration: not applied",
			"WARNING  ⚠ PBS remote configuration: not applied",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("missing %q, visible lines:\n%s", want, got)
			}
		}
		if strings.Contains(log, "failed to apply") || strings.Contains(log, "remote.cfg failed") {
			t.Fatalf("no step summary and no repeated block, log:\n%s", log)
		}
		want := "DEBUG    PBS staged config apply: outcome already reported: forced s3 error; PBS remote configuration incomplete: 1 remote(s) not applied [r1]"
		if lines := pbsBlockLogLines(log); lines[len(lines)-1] != want {
			t.Fatalf("last line %q, want %q", lines[len(lines)-1], want)
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

// When PBS refuses the create only because the object already exists, the update
// is the attempt that says why the object was not applied: its refusal is the one
// the event carries. Every section config, through the shared block.
func TestPBSSectionRestoreBlock_AlreadyExistsRejectionCarriesTheUpdateRefusal(t *testing.T) {
	const event = `"parameter verification failed - 'fingerprint': value does not match the regex pattern"`

	t.Run("remote through the step", func(t *testing.T) {
		r1 := pbsRemoteSection("r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP)
		runner := &FakeCommandRunner{Errors: map[string]error{
			pbsRemoteCmd("create", "r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP): errors.New(pbsRemoteCreateExists),
			pbsRemoteCmd("update", "r1", "10.0.0.1", "secret-r1", pbsRemoteGoodFP): errors.New(pbsRemoteUpdateBadFingerprint),
		}}
		log, warned, _ := runPBSSectionRestoreStep(t, PBSRestoreBehaviorMerge, []string{"pbs_remotes"}, map[string]string{"etc/proxmox-backup/remote.cfg": r1}, runner)
		want := []string{
			"INFO     Restoring PBS remotes...",
			"INFO       Remotes to restore: 1",
			"INFO       Names: r1",
			"INFO     Remote r1 was rejected by PBS: " + event,
			"WARNING  ⚠ PBS remote r1: not applied",
			"WARNING  ⚠ PBS remote configuration: not applied",
		}
		if got := pbsVisibleLines(log); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("visible lines\n--- got ---\n%s\n--- want ---\n%s\n--- full log ---\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"), log)
		}
		if !warned {
			t.Fatal("restore must end with warnings")
		}
	})

	for _, k := range []struct {
		name, file, command, eventNoun string
		apply                          func(context.Context, *logging.Logger, string, bool) error
	}{
		{"remote", "remote.cfg", "remote", "Remote", applyPBSRemoteCfgViaAPI},
		{"s3", "s3.cfg", "s3 endpoint", "S3 endpoint", applyPBSS3CfgViaAPI},
		{"sync", "sync.cfg", "sync-job", "Sync job", applyPBSSyncCfgViaAPI},
		{"verification", "verification.cfg", "verify-job", "Verify job", applyPBSVerificationCfgViaAPI},
		{"prune", "prune.cfg", "prune-job", "Prune job", applyPBSPruneCfgViaAPI},
		{"traffic-control", "traffic-control.cfg", "traffic-control", "Traffic control rule", applyPBSTrafficControlCfgViaAPI},
	} {
		t.Run(k.name, func(t *testing.T) {
			stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
			writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/"+k.file, "x: a1\n\tcomment c\n", 0o640)
			cmd := "proxmox-backup-manager " + k.command
			runner.errs = map[string]error{
				cmd + " create a1 --comment c": errors.New(pbsRemoteCreateExists),
				cmd + " update a1 --comment c": errors.New(pbsRemoteUpdateBadFingerprint),
			}
			var logBuf bytes.Buffer
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(&logBuf)
			_ = k.apply(context.Background(), logger, stageRoot, false)
			want := "INFO     " + k.eventNoun + " a1 was rejected by PBS: " + event
			found := false
			for _, line := range pbsVisibleLines(logBuf.String()) {
				if strings.Contains(line, " was rejected by PBS: ") {
					if line != want {
						t.Fatalf("event %q, want %q", line, want)
					}
					found = true
				}
			}
			if !found {
				t.Fatalf("no rejection event, log:\n%s", logBuf.String())
			}
		})
	}
}

// One table over the six section configs for the two cases that stop before any
// object: the staged file cannot be read (no command, not applied), and in Clean
// mode this PBS's objects cannot be read (the staged file is written as it is, and
// the object of the current file that the backup does not have is named as
// removed). Each config uses its own words, and writes only its own file: the
// three job configs are staged together and each writes one of them.
func TestPBSSectionRestoreBlock_StoppedBlockPerKind(t *testing.T) {
	files := []string{"remote.cfg", "s3.cfg", "sync.cfg", "verification.cfg", "prune.cfg", "traffic-control.cfg"}
	for _, k := range []struct {
		name, file, command                      string
		opening, count, current, config, outcome string
		event, subject                           string
		apply                                    func(context.Context, *logging.Logger, string, bool) error
	}{
		{"remote", "remote.cfg", "remote", "Restoring PBS remotes...", "  Remotes to restore: 1", "The remotes configured on this PBS could not be read", "The remote configuration", "PBS remote configuration", "Remote", "PBS remote", applyPBSRemoteCfgViaAPI},
		{"s3", "s3.cfg", "s3 endpoint", "Restoring PBS S3 endpoints...", "  S3 endpoints to restore: 1", "The S3 endpoints configured on this PBS could not be read", "The S3 endpoint configuration", "PBS S3 endpoint configuration", "S3 endpoint", "PBS S3 endpoint", applyPBSS3CfgViaAPI},
		{"sync", "sync.cfg", "sync-job", "Restoring PBS sync jobs...", "  Sync jobs to restore: 1", "The sync jobs configured on this PBS could not be read", "The sync job configuration", "PBS sync job configuration", "Sync job", "PBS sync job", applyPBSSyncCfgViaAPI},
		{"verification", "verification.cfg", "verify-job", "Restoring PBS verify jobs...", "  Verify jobs to restore: 1", "The verify jobs configured on this PBS could not be read", "The verify job configuration", "PBS verify job configuration", "Verify job", "PBS verify job", applyPBSVerificationCfgViaAPI},
		{"prune", "prune.cfg", "prune-job", "Restoring PBS prune jobs...", "  Prune jobs to restore: 1", "The prune jobs configured on this PBS could not be read", "The prune job configuration", "PBS prune job configuration", "Prune job", "PBS prune job", applyPBSPruneCfgViaAPI},
		{"traffic-control", "traffic-control.cfg", "traffic-control", "Restoring PBS traffic control rules...", "  Traffic control rules to restore: 1", "The traffic control rules configured on this PBS could not be read", "The traffic control configuration", "PBS traffic control configuration", "Traffic control rule", "PBS traffic control rule", applyPBSTrafficControlCfgViaAPI},
	} {
		t.Run(k.name+"/staged-unreadable", func(t *testing.T) {
			stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
			if err := fs.MkdirAll(stageRoot+"/etc/proxmox-backup/"+k.file, 0o750); err != nil {
				t.Fatal(err)
			}
			var logBuf bytes.Buffer
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(&logBuf)

			err := k.apply(context.Background(), logger, stageRoot, true)

			readErr := "read staged etc/proxmox-backup/" + k.file + ": read <fakefs>/stage/etc/proxmox-backup/" + k.file + ": is a directory"
			want := []string{
				"INFO     " + k.opening,
				"INFO     " + k.config + ` in the backup could not be read: "` + readErr + `"`,
				"WARNING  ⚠ " + k.outcome + ": not applied",
			}
			if got := pbsVisibleLines(logBuf.String()); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("visible lines\n--- got ---\n%s\n--- want ---\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			var stopped *pbsSectionRestoreStoppedError
			if !errors.As(err, &stopped) || !pbsSectionOutcomeLogged(err) {
				t.Fatalf("err=%T %v, want a pbsSectionRestoreStoppedError", err, err)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("no command may run, calls: %v", runner.calls)
			}
		})

		t.Run(k.name+"/current-unreadable", func(t *testing.T) {
			stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
			for _, f := range files {
				writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/"+f, "x: a1\n\tcomment from-"+f+"\n", 0o640)
			}
			// This PBS has a1 and old in its own file; the backup has a1 only.
			if err := fs.WriteFile("/etc/proxmox-backup/"+k.file, []byte("x: a1\n\tcomment current\n\nx: old\n\tcomment current\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			cmd := "proxmox-backup-manager " + k.command
			runner.errs = map[string]error{cmd + " list --output-format=json": errors.New(pbsRemoteListUnparsable)}
			var logBuf bytes.Buffer
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(&logBuf)

			err := k.apply(context.Background(), logger, stageRoot, true)

			want := []string{
				"INFO     " + k.opening,
				"INFO     " + k.count,
				"INFO       Names: a1",
				"INFO     " + k.current,
				"INFO     " + k.event + " old is configured on this PBS but not present in the backup",
				"INFO     ✓ " + k.subject + " old: removed",
				"WARNING  ⚠ " + k.outcome + ": applied from the backup file",
			}
			if got := pbsVisibleLines(logBuf.String()); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("visible lines\n--- got ---\n%s\n--- want ---\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if !strings.Contains(logBuf.String(), "fallback=file target=/etc/proxmox-backup/"+k.file+" source=/stage/etc/proxmox-backup/"+k.file+"\n") {
				t.Fatalf("missing the fallback DEBUG line, log:\n%s", logBuf.String())
			}
			var stopped *pbsSectionRestoreStoppedError
			if !errors.As(err, &stopped) || !pbsSectionOutcomeLogged(err) {
				t.Fatalf("err=%T %v, want a pbsSectionRestoreStoppedError", err, err)
			}
			if strings.Join(runner.calls, "\n") != cmd+" list --output-format=json" {
				t.Fatalf("only the list may run, calls: %v", runner.calls)
			}
			for _, f := range files {
				got, readErr := fs.ReadFile("/etc/proxmox-backup/" + f)
				if f == k.file {
					if readErr != nil || string(got) != "x: a1\n\tcomment from-"+f+"\n" {
						t.Fatalf("%s written %q (err %v), want the staged file", f, got, readErr)
					}
					continue
				}
				if readErr == nil {
					t.Fatalf("%s must not be written by the %s block, got %q", f, k.name, got)
				}
			}
		})
	}
}

// The remote.cfg file fallback behaves like PBS, which refuses to remove a remote
// a sync job uses: such a remote of this PBS that the backup does not have keeps
// its current section in the written file and is named as not removed. A remote
// no job uses is removed as before; a sync.cfg that cannot be read keeps none.
func TestPBSSectionRestoreBlock_FileFallbackKeepsARemoteASyncJobUses(t *testing.T) {
	const (
		target  = "/etc/proxmox-backup/remote.cfg"
		syncCfg = "/etc/proxmox-backup/sync.cfg"
		current = "remote: r1\n\thost 10.0.0.1\n\nremote: rtmp\n\thost 10.0.0.7\n\tauth-id sync@pbs\n\nremote: old\n\thost 10.0.0.9\n"
		jobs    = "sync: sj-rtmp\n\tremote rtmp\n\tremote-store src\n\tstore ds1\n\nsync: sj-push\n\tremote rtmp\n\tremote-store dst\n\tstore ds1\n\tsync-direction push\n\nsync: sj-local\n\tremote-store ds2\n\tstore ds1\n"
		staged  = "remote: r1\n\thost 10.0.0.1\n\nremote: r2\n\thost 10.0.0.2\n"
		rtmp    = "remote: rtmp\n\thost 10.0.0.7\n\tauth-id sync@pbs"
		listErr = "proxmox-backup-manager remote list --output-format=json failed: " + pbsRemoteListUnparsable
	)
	head := []string{
		"DEBUG    Start pbs staged apply: dryRun=false stage=/stage",
		`DEBUG    PBS remote restore: reading current remotes cmd="proxmox-backup-manager remote list --output-format=json"`,
		"DEBUG    PBS remote restore: remote list failed error=\"exit status 255: Error: parsing \\\"/etc/proxmox-backup/remote.cfg\\\" failed: line 1 - syntax error (expected header)\"",
		"INFO     Restoring PBS remotes...",
		"DEBUG    PBS remote restore: mode=clean staged=/stage/etc/proxmox-backup/remote.cfg",
	}
	two := []string{
		"DEBUG    PBS remote restore: backup remotes=2 names=r1,r2",
		"INFO       Remotes to restore: 2",
		"INFO       Names: r1, r2",
	}
	none := []string{
		"DEBUG    PBS remote restore: backup remotes=0 names=",
		"INFO       Remotes to restore: 0",
	}
	fallback := []string{
		"INFO     The remotes configured on this PBS could not be read",
		"DEBUG    PBS remote restore: fallback=file target=/etc/proxmox-backup/remote.cfg source=/stage/etc/proxmox-backup/remote.cfg",
		"DEBUG    PBS remote restore: fallback current file remotes=3 names=old,r1,rtmp",
	}
	dropOld := []string{
		"DEBUG    PBS remote restore: remote=old not in backup action=drop from file",
		"INFO     Remote old is configured on this PBS but not present in the backup",
		"INFO     ✓ PBS remote old: removed",
	}
	keepRtmp := []string{
		"DEBUG    PBS remote restore: remote=rtmp not in backup used by sync jobs=sj-push,sj-rtmp action=keep",
		"INFO     Remote rtmp is configured on this PBS but not present in the backup",
		"WARNING  ⚠ PBS remote rtmp: not removed",
	}
	tail := []string{
		"WARNING  ⚠ PBS remote configuration: applied from the backup file",
		"DEBUG    End pbs staged apply (error=" + listErr + ", duration=<d>)",
		"DEBUG    PBS staged config apply: outcome already reported: " + listErr,
	}
	join := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}

	cases := []struct {
		name       string
		staged     string
		jobsFile   string // pbsStagedUnreadable: a directory stands there
		want       []string
		wantTarget string
	}{
		{
			name: "non-empty-stage", staged: staged, jobsFile: jobs,
			want: join(head, two, fallback, []string{
				"DEBUG    PBS remote restore: fallback current sync jobs=3 remotes_used=rtmp",
				"DEBUG    PBS remote restore: fallback written kept=rtmp",
			}, dropOld, keepRtmp, tail),
			wantTarget: strings.TrimSpace(staged) + "\n\n" + rtmp + "\n",
		},
		{
			name: "empty-stage", staged: "", jobsFile: jobs,
			want: join(head, none, fallback, []string{
				"DEBUG    PBS remote restore: fallback current sync jobs=3 remotes_used=rtmp",
				"DEBUG    PBS remote restore: fallback staged file has no remotes comments_only=false, target written kept=rtmp",
			}, dropOld, []string{
				"DEBUG    PBS remote restore: remote=r1 not in backup action=drop from file",
				"INFO     Remote r1 is configured on this PBS but not present in the backup",
				"INFO     ✓ PBS remote r1: removed",
			}, keepRtmp, tail),
			wantTarget: rtmp + "\n",
		},
		{
			name: "sync-cfg-unreadable", staged: staged, jobsFile: pbsStagedUnreadable,
			want: join(head, two, fallback, []string{
				`DEBUG    PBS remote restore: fallback current sync jobs file=/etc/proxmox-backup/sync.cfg read failed error="read <fakefs>/etc/proxmox-backup/sync.cfg: is a directory"`,
				"DEBUG    Start pbs staged apply remote.cfg: stage=/stage",
				"DEBUG    pbs staged apply file: Applied etc/proxmox-backup/remote.cfg -> /etc/proxmox-backup/remote.cfg",
				"DEBUG    End pbs staged apply remote.cfg (ok, duration=<d>)",
				"DEBUG    PBS remote restore: fallback written",
			}, dropOld, []string{
				"DEBUG    PBS remote restore: remote=rtmp not in backup action=drop from file",
				"INFO     Remote rtmp is configured on this PBS but not present in the backup",
				"INFO     ✓ PBS remote rtmp: removed",
			}, tail),
			wantTarget: strings.TrimSpace(staged) + "\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &FakeCommandRunner{Errors: map[string]error{pbsRemoteListCmd: errors.New(pbsRemoteListUnparsable)}}
			jobsFile := tc.jobsFile
			prepare := func() {
				if err := restoreFS.WriteFile(target, []byte(current), 0o640); err != nil {
					t.Fatal(err)
				}
				if jobsFile == pbsStagedUnreadable {
					if err := restoreFS.MkdirAll(syncCfg, 0o750); err != nil {
						t.Fatal(err)
					}
					return
				}
				if err := restoreFS.WriteFile(syncCfg, []byte(jobsFile), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			log, warned, fakeFS := runPBSSectionRestoreStep(t, PBSRestoreBehaviorClean, []string{"pbs_remotes"}, map[string]string{"etc/proxmox-backup/remote.cfg": tc.staged}, runner, prepare)
			assertPBSBlockLog(t, log, tc.want)
			if !warned {
				t.Fatal("restore must end with warnings")
			}
			if got, err := fakeFS.ReadFile(target); err != nil || string(got) != tc.wantTarget {
				t.Fatalf("remote.cfg %q (err %v), want %q", got, err, tc.wantTarget)
			}
			if jobsFile != pbsStagedUnreadable {
				if got, err := fakeFS.ReadFile(syncCfg); err != nil || string(got) != jobsFile {
					t.Fatalf("sync.cfg must stay as it is, got %q (err %v)", got, err)
				}
			}
		})
	}
}

// pbsAPIUnavailable makes the staged apply find the PBS API unavailable.
func pbsAPIUnavailable() {
	pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error {
		return errors.New("proxmox-backup-proxy did not start: exit status 1")
	}
}

const pbsAPIUnavailableWarning = "WARNING  PBS API apply unavailable; falling back to file-based staged apply where possible: proxmox-backup-proxy did not start: exit status 1"

// Clean mode without the PBS API: remote.cfg is written from the backup file as
// the same block, with the event of the datastores, and the remote of this PBS
// the backup does not have is named as removed. The caller adds nothing more and
// the restore ends with warnings.
func TestPBSSectionRestoreBlock_APIUnavailableRemote(t *testing.T) {
	const target = "/etc/proxmox-backup/remote.cfg"
	staged := "remote: r1\n\thost 10.0.0.1\n\nremote: r2\n\thost 10.0.0.2\n"
	runner := &FakeCommandRunner{}
	log, warned, fakeFS := runPBSSectionRestoreStep(t, PBSRestoreBehaviorClean, []string{"pbs_remotes"}, map[string]string{"etc/proxmox-backup/remote.cfg": staged}, runner, pbsAPIUnavailable, func() {
		if err := restoreFS.WriteFile(target, []byte("remote: r1\n\thost 10.0.0.1\n\nremote: old\n\thost 10.0.0.9\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	})
	assertPBSBlockLog(t, log, []string{
		"DEBUG    Start pbs staged apply: dryRun=false stage=/stage",
		pbsAPIUnavailableWarning,
		"INFO     Restoring PBS remotes...",
		"DEBUG    PBS remote restore: mode=clean staged=/stage/etc/proxmox-backup/remote.cfg",
		"DEBUG    PBS remote restore: backup remotes=2 names=r1,r2",
		"INFO       Remotes to restore: 2",
		"INFO       Names: r1, r2",
		"INFO     The PBS API is not available",
		"DEBUG    PBS remote restore: fallback=file target=/etc/proxmox-backup/remote.cfg source=/stage/etc/proxmox-backup/remote.cfg",
		"DEBUG    PBS remote restore: fallback current file remotes=2 names=old,r1",
		"DEBUG    PBS remote restore: fallback current sync jobs=0 remotes_used=",
		"DEBUG    Start pbs staged apply remote.cfg: stage=/stage",
		"DEBUG    pbs staged apply file: Applied etc/proxmox-backup/remote.cfg -> /etc/proxmox-backup/remote.cfg",
		"DEBUG    End pbs staged apply remote.cfg (ok, duration=<d>)",
		"DEBUG    PBS remote restore: fallback written",
		"DEBUG    PBS remote restore: remote=old not in backup action=drop from file",
		"INFO     Remote old is configured on this PBS but not present in the backup",
		"INFO     ✓ PBS remote old: removed",
		"WARNING  ⚠ PBS remote configuration: applied from the backup file",
		"DEBUG    End pbs staged apply (error=remote.cfg: PBS API unavailable, duration=<d>)",
		"DEBUG    PBS staged config apply: outcome already reported: remote.cfg: PBS API unavailable",
	})
	if !warned {
		t.Fatal("restore must end with warnings")
	}
	if len(runner.CallsList()) != 0 {
		t.Fatalf("no command may run without the API, calls: %v", runner.CallsList())
	}
	if got, err := fakeFS.ReadFile(target); err != nil || string(got) != strings.TrimSpace(staged)+"\n" {
		t.Fatalf("remote.cfg %q (err %v), want the staged file", got, err)
	}
}

// One table over the six section configs without the PBS API: each opens its
// block with its own words, says the API is not available, writes only its own
// file and names the object of this PBS the backup does not have.
func TestPBSSectionRestoreBlock_APIUnavailablePerKind(t *testing.T) {
	for _, k := range []struct {
		name, category, file, prefix, key, keys string
		opening, count, event, subject, outcome string
		helper                                  []string
	}{
		{"remote", "pbs_remotes", "remote.cfg", "PBS remote restore: ", "remote", "remotes",
			"Restoring PBS remotes...", "  Remotes to restore: 2", "Remote", "PBS remote", "PBS remote configuration",
			[]string{"DEBUG    PBS remote restore: fallback current sync jobs=0 remotes_used=", "DEBUG    Start pbs staged apply remote.cfg: stage=/stage", "DEBUG    pbs staged apply file: Applied etc/proxmox-backup/remote.cfg -> /etc/proxmox-backup/remote.cfg", "DEBUG    End pbs staged apply remote.cfg (ok, duration=<d>)"}},
		{"s3", "datastore_pbs", "s3.cfg", "PBS S3 endpoint restore: ", "s3-endpoint", "s3-endpoints",
			"Restoring PBS S3 endpoints...", "  S3 endpoints to restore: 2", "S3 endpoint", "PBS S3 endpoint", "PBS S3 endpoint configuration",
			[]string{"DEBUG    Start pbs staged apply s3.cfg: stage=/stage", "DEBUG    pbs staged apply file: Applied etc/proxmox-backup/s3.cfg -> /etc/proxmox-backup/s3.cfg", "DEBUG    End pbs staged apply s3.cfg (ok, duration=<d>)"}},
		{"sync", "pbs_jobs", "sync.cfg", "PBS sync job restore: ", "sync-job", "sync-jobs",
			"Restoring PBS sync jobs...", "  Sync jobs to restore: 2", "Sync job", "PBS sync job", "PBS sync job configuration",
			[]string{"DEBUG    pbs staged apply file: Applied etc/proxmox-backup/sync.cfg -> /etc/proxmox-backup/sync.cfg"}},
		{"verification", "pbs_jobs", "verification.cfg", "PBS verify job restore: ", "verify-job", "verify-jobs",
			"Restoring PBS verify jobs...", "  Verify jobs to restore: 2", "Verify job", "PBS verify job", "PBS verify job configuration",
			[]string{"DEBUG    pbs staged apply file: Applied etc/proxmox-backup/verification.cfg -> /etc/proxmox-backup/verification.cfg"}},
		{"prune", "pbs_jobs", "prune.cfg", "PBS prune job restore: ", "prune-job", "prune-jobs",
			"Restoring PBS prune jobs...", "  Prune jobs to restore: 2", "Prune job", "PBS prune job", "PBS prune job configuration",
			[]string{"DEBUG    pbs staged apply file: Applied etc/proxmox-backup/prune.cfg -> /etc/proxmox-backup/prune.cfg"}},
		{"traffic-control", "pbs_host", "traffic-control.cfg", "PBS traffic control rule restore: ", "traffic-control-rule", "traffic-control-rules",
			"Restoring PBS traffic control rules...", "  Traffic control rules to restore: 2", "Traffic control rule", "PBS traffic control rule", "PBS traffic control configuration",
			[]string{"DEBUG    pbs staged apply file: Applied etc/proxmox-backup/traffic-control.cfg -> /etc/proxmox-backup/traffic-control.cfg"}},
	} {
		t.Run(k.name, func(t *testing.T) {
			target := "/etc/proxmox-backup/" + k.file
			staged := "x: a1\n\tcomment staged\n\nx: a2\n\tcomment staged\n"
			runner := &FakeCommandRunner{}
			log, warned, fakeFS := runPBSSectionRestoreStep(t, PBSRestoreBehaviorClean, []string{k.category}, map[string]string{"etc/proxmox-backup/" + k.file: staged}, runner, pbsAPIUnavailable, func() {
				if err := restoreFS.WriteFile(target, []byte("x: a1\n\tcomment current\n\nx: old\n\tcomment current\n"), 0o640); err != nil {
					t.Fatal(err)
				}
			})
			want := []string{
				"INFO     " + k.opening,
				"DEBUG    " + k.prefix + "mode=clean staged=/stage/etc/proxmox-backup/" + k.file,
				"DEBUG    " + k.prefix + "backup " + k.keys + "=2 names=a1,a2",
				"INFO     " + k.count,
				"INFO       Names: a1, a2",
				"INFO     The PBS API is not available",
				"DEBUG    " + k.prefix + "fallback=file target=" + target + " source=/stage/etc/proxmox-backup/" + k.file,
				"DEBUG    " + k.prefix + "fallback current file " + k.keys + "=2 names=a1,old",
			}
			want = append(want, k.helper...)
			want = append(want,
				"DEBUG    "+k.prefix+"fallback written",
				"DEBUG    "+k.prefix+k.key+"=old not in backup action=drop from file",
				"INFO     "+k.event+" old is configured on this PBS but not present in the backup",
				"INFO     ✓ "+k.subject+" old: removed",
				"WARNING  ⚠ "+k.outcome+": applied from the backup file",
			)
			lines := pbsBlockLogLines(log)
			start := -1
			for i, line := range lines {
				if line == want[0] {
					start = i
				}
			}
			if start < 0 || start+len(want) > len(lines) || strings.Join(lines[start:start+len(want)], "\n") != strings.Join(want, "\n") {
				t.Fatalf("block lines\n--- want ---\n%s\n--- full log ---\n%s", strings.Join(want, "\n"), strings.Join(lines, "\n"))
			}
			if !warned {
				t.Fatal("restore must end with warnings")
			}
			if len(runner.CallsList()) != 0 {
				t.Fatalf("no command may run without the API, calls: %v", runner.CallsList())
			}
			for _, unwanted := range []string{"PBS staged apply: etc/proxmox-backup/" + k.file, "PBS staged apply: " + k.file, "failed to apply", "falling back to file-based " + k.file} {
				if strings.Contains(log, unwanted) {
					t.Fatalf("unexpected %q in log:\n%s", unwanted, log)
				}
			}
			if got, err := fakeFS.ReadFile(target); err != nil || string(got) != strings.TrimSpace(staged)+"\n" {
				t.Fatalf("%s %q (err %v), want the staged file", k.file, got, err)
			}
		})
	}
}

// Without the PBS API the three job configs each write their own file, and only
// it, as three blocks.
func TestPBSSectionRestoreBlock_APIUnavailableJobsWriteOneFileEach(t *testing.T) {
	files := []string{"sync.cfg", "verification.cfg", "prune.cfg"}
	staged := map[string]string{}
	for _, f := range files {
		staged["etc/proxmox-backup/"+f] = "x: from-" + f + "\n\tcomment staged\n"
	}
	runner := &FakeCommandRunner{}
	log, warned, fakeFS := runPBSSectionRestoreStep(t, PBSRestoreBehaviorClean, []string{"pbs_jobs"}, staged, runner, pbsAPIUnavailable)
	var outcomes []string
	for _, line := range pbsVisibleLines(log) {
		if strings.Contains(line, "configuration: ") {
			outcomes = append(outcomes, line)
		}
	}
	want := []string{
		"WARNING  ⚠ PBS sync job configuration: applied from the backup file",
		"WARNING  ⚠ PBS verify job configuration: applied from the backup file",
		"WARNING  ⚠ PBS prune job configuration: applied from the backup file",
	}
	if strings.Join(outcomes, "\n") != strings.Join(want, "\n") {
		t.Fatalf("outcomes\n%s\nwant\n%s\nlog:\n%s", strings.Join(outcomes, "\n"), strings.Join(want, "\n"), log)
	}
	if !warned {
		t.Fatal("restore must end with warnings")
	}
	if strings.Contains(log, "failed to apply") || strings.Contains(log, "job configs") {
		t.Fatalf("the caller must add nothing, log:\n%s", log)
	}
	for _, f := range files {
		if got, err := fakeFS.ReadFile("/etc/proxmox-backup/" + f); err != nil || string(got) != "x: from-"+f+"\n\tcomment staged\n" {
			t.Fatalf("%s %q (err %v), want its own staged file", f, got, err)
		}
	}
}
