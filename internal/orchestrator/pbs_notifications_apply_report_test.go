package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func stageCfg(t *testing.T, fakeFS *FakeFS, stageRoot, body string) {
	t.Helper()
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/notifications.cfg", []byte(body), 0o640); err != nil {
		t.Fatalf("write staged notifications.cfg: %v", err)
	}
}

func withFakeApplyEnv(t *testing.T, runner *fakeCommandRunner) *FakeFS {
	t.Helper()
	origCmd, origFS := restoreCmd, restoreFS
	fakeFS := NewFakeFS()
	t.Cleanup(func() {
		restoreCmd, restoreFS = origCmd, origFS
		_ = os.RemoveAll(fakeFS.Root)
	})
	restoreFS = fakeFS
	restoreCmd = runner
	return fakeFS
}

// The confirmed bug: a staged notifications.cfg that exists but says nothing used to return
// applied=true after issuing zero commands, and the caller printed "applied via API".
func TestApplyPBSNotificationsViaAPI_EmptyStagedCfgIsNotAnApply(t *testing.T) {
	for _, body := range []string{"", "   \n\t\n"} {
		t.Run("body="+strings.TrimSpace(body)+"|", func(t *testing.T) {
			runner := &fakeCommandRunner{}
			fakeFS := withFakeApplyEnv(t, runner)
			stageCfg(t, fakeFS, "/stage", body)

			rep, err := applyPBSNotificationsViaAPI(context.Background(), logging.New(types.LogLevelDebug, false), "/stage", false)
			if err != nil {
				t.Fatalf("an empty staged file is not an error: %v", err)
			}
			if !rep.staged {
				t.Fatal("the file existed, so staged must be true")
			}
			if !rep.stagedEmpty {
				t.Fatal("the file was empty, and that fact is the whole point")
			}
			if rep.planned != 0 || rep.mutated() {
				t.Fatalf("nothing was named and nothing can have been applied, got %+v", rep)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("no command may run, got: %v", runner.calls)
			}
		})
	}
}

// Same defect, reached through content that parses to nothing usable.
func TestApplyPBSNotificationsViaAPI_UnusableSectionsAreNotAnApply(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"comment only", "# only a comment\n"},
		{"no section header", "this is not a config at all\n"},
		{"unknown section type", "ntfy: n1\n    server https://example\n"},
		{"endpoint missing a required field", "smtp: relay\n    server mail.example.com\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeCommandRunner{}
			fakeFS := withFakeApplyEnv(t, runner)
			stageCfg(t, fakeFS, "/stage", tt.body)

			rep, err := applyPBSNotificationsViaAPI(context.Background(), logging.New(types.LogLevelDebug, false), "/stage", false)
			if err != nil {
				t.Fatalf("unusable content is not an error here: %v", err)
			}
			if rep.planned != 0 || rep.mutated() {
				t.Fatalf("nothing usable was named, got %+v", rep)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("no command may run, got: %v", runner.calls)
			}
		})
	}
}

// Clean 1:1 must not delete a live endpoint of a kind whose staged section we failed to
// rebuild: "live but not desired" then means "we could not translate it", not "the backup
// lacked it". The fixture is this repo's own PBS shape — mailto-user is not a positional
// pbsEndpointSinglePositional accepts, so the section is dropped in translation.
func TestApplyPBSNotificationsViaAPI_CleanSkipsRemovalForKindsWithDroppedSections(t *testing.T) {
	runner := &fakeCommandRunner{
		outputs: map[string][]byte{
			"proxmox-backup-manager notification endpoint smtp list": []byte(`[{"name":"example"},{"name":"other"}]`),
		},
	}
	fakeFS := withFakeApplyEnv(t, runner)
	stageCfg(t, fakeFS, "/stage", "smtp: example\n    mailto-user root@pam\n    server mail.example.com\n")

	rep, err := applyPBSNotificationsViaAPI(context.Background(), logging.New(types.LogLevelDebug, false), "/stage", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range runner.calls {
		if strings.Contains(c, "endpoint smtp remove") {
			t.Fatalf("a kind we failed to translate must not have its live objects deleted, got: %v", runner.calls)
		}
	}
	if len(rep.removalsSkipped) != 1 || rep.removalsSkipped[0] != "smtp" {
		t.Fatalf("the skipped removal pass must be recorded, got %+v", rep.removalsSkipped)
	}
}

// Clean mode with an empty staged file still deletes. That is real work and must be
// counted, named, and warned about — previously it was logged nowhere at any level.
func TestApplyPBSNotificationsViaAPI_CleanEmptyStageWipeIsWarned(t *testing.T) {
	runner := &fakeCommandRunner{
		outputs: map[string][]byte{
			"proxmox-backup-manager notification matcher list":       []byte(`[{"name":"m1"},{"name":"m2"}]`),
			"proxmox-backup-manager notification endpoint smtp list": []byte(`[{"name":"relay"}]`),
		},
	}
	fakeFS := withFakeApplyEnv(t, runner)
	stageCfg(t, fakeFS, "/stage", "")

	rep, err := applyPBSNotificationsViaAPI(context.Background(), logging.New(types.LogLevelDebug, false), "/stage", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rep.removed) != 3 {
		t.Fatalf("every deletion must be counted and named, got %v", rep.removed)
	}
	if !rep.mutated() {
		t.Fatal("deleting three live objects is a mutation")
	}

	var out bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SwapOutput(&out)
	logPBSNotificationApplyReport(logger, PBSRestoreBehaviorClean, rep)

	logged := out.String()
	if !strings.Contains(logged, "removed 3 live object(s)") {
		t.Fatalf("the deletions must be named, got:\n%s", logged)
	}
	if !strings.Contains(logged, "on that evidence alone") {
		t.Fatalf("a wipe authorised by a file naming nothing must be warned, got:\n%s", logged)
	}
}

// A create that falls back to update is one logical change, not two. This is why the
// counters sit at the call sites rather than at the command choke point.
func TestApplyPBSNotificationsViaAPI_UpsertCountsOnceWhenCreateFallsBackToUpdate(t *testing.T) {
	runner := &fakeCommandRunner{
		errs: map[string]error{
			"proxmox-backup-manager notification endpoint smtp create relay root@pam --server mail.example.com": errors.New("already exists"),
		},
	}
	fakeFS := withFakeApplyEnv(t, runner)
	stageCfg(t, fakeFS, "/stage", "smtp: relay\n    mailto root@pam\n    server mail.example.com\n")

	rep, err := applyPBSNotificationsViaAPI(context.Background(), logging.New(types.LogLevelDebug, false), "/stage", false)
	if err != nil {
		t.Fatalf("the update fallback should succeed: %v", err)
	}
	if rep.endpointsUpserted != 1 {
		t.Fatalf("create-then-update is one change, got %d", rep.endpointsUpserted)
	}
}

// The sentence the operator reads is the artifact this whole change is about, so it is
// asserted directly, at the caller, for every outcome.
func TestMaybeApplyNotificationsFromStage_PBSSentences(t *testing.T) {
	tests := []struct {
		name        string
		body        string // "" means: stage no file at all
		stageFile   bool
		wantContain string
		wantAbsent  string
	}{
		{name: "no file staged", stageFile: false, wantContain: "contains no notifications.cfg", wantAbsent: "applied via API"},
		{name: "staged but empty", stageFile: true, body: "", wantContain: "the staged notifications.cfg is empty", wantAbsent: "applied via API"},
		{name: "no section recognised", stageFile: true, body: "# only a comment\n", wantContain: "no section header was recognised", wantAbsent: "applied via API"},
		{name: "all sections skipped", stageFile: true, body: "ntfy: n1\n    server https://example\n", wantContain: "staged section(s) were skipped", wantAbsent: "applied via API"},
		{name: "real apply", stageFile: true, body: "matcher: default-matcher\n    target mail-to-root\n", wantContain: "applied via API", wantAbsent: "contains no notifications.cfg"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origCmd, origFS := restoreCmd, restoreFS
			origEuid, origAPIEuid, origVerify := notificationsApplyGeteuid, pbsAPIApplyGeteuid, serviceVerifyTimeout
			t.Cleanup(func() {
				restoreCmd, restoreFS = origCmd, origFS
				notificationsApplyGeteuid, pbsAPIApplyGeteuid = origEuid, origAPIEuid
				serviceVerifyTimeout = origVerify
			})

			restoreFS = osFS{}
			notificationsApplyGeteuid = func() int { return 0 }
			pbsAPIApplyGeteuid = func() int { return 0 }
			serviceVerifyTimeout = 50 * time.Millisecond

			stageRoot := t.TempDir()
			if tt.stageFile {
				dir := filepath.Join(stageRoot, "etc", "proxmox-backup")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "notifications.cfg"), []byte(tt.body), 0o640); err != nil {
					t.Fatalf("write: %v", err)
				}
			}

			restoreCmd = &FakeCommandRunner{Outputs: map[string][]byte{
				"which systemctl":                          {},
				"proxmox-backup-manager version":           []byte("proxmox-backup-manager 4.0.0"),
				"systemctl start proxmox-backup":           {},
				"systemctl start proxmox-backup-proxy":     {},
				"systemctl is-active proxmox-backup":       []byte("active"),
				"systemctl is-active proxmox-backup-proxy": []byte("active"),
			}}

			var out bytes.Buffer
			logger := logging.New(types.LogLevelDebug, false)
			logger.SwapOutput(&out)

			plan := &RestorePlan{
				SystemType:         SystemTypePBS,
				StagedCategories:   []Category{{ID: "pbs_notifications"}},
				PBSRestoreBehavior: PBSRestoreBehaviorMerge,
			}

			if err := maybeApplyNotificationsFromStage(context.Background(), logger, plan, stageRoot, false); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			logged := out.String()
			if !strings.Contains(logged, tt.wantContain) {
				t.Fatalf("want %q in the log, got:\n%s", tt.wantContain, logged)
			}
			if strings.Contains(logged, tt.wantAbsent) {
				t.Fatalf("must not say %q, got:\n%s", tt.wantAbsent, logged)
			}
		})
	}
}
