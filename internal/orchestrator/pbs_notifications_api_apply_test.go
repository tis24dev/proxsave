package orchestrator

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestApplyPBSNotificationsViaAPI_CreatesEndpointAndMatcher(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"

	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/notifications.cfg", []byte(
		"smtp: Gmail-relay\n"+
			"    recipients user@example.com\n"+
			"    from-address pbs@example.com\n"+
			"    server smtp.gmail.com\n"+
			"    port 587\n"+
			"    username user\n"+
			"\n"+
			"matcher: default-matcher\n"+
			"    target Gmail-relay\n",
	), 0o640); err != nil {
		t.Fatalf("write staged notifications.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/notifications-priv.cfg", []byte(
		"smtp: Gmail-relay\n"+
			"    password secret123\n",
	), 0o600); err != nil {
		t.Fatalf("write staged notifications-priv.cfg: %v", err)
	}

	runner := &fakeCommandRunner{}
	restoreCmd = runner

	logger := logging.New(types.LogLevelDebug, false)
	rep, err := applyPBSNotificationsViaAPI(context.Background(), logger, stageRoot, false)
	if err != nil {
		t.Fatalf("applyPBSNotificationsViaAPI error: %v", err)
	}
	if !rep.mutated() {
		t.Fatal("staged notifications.cfg was present, so the apply must report that it ran")
	}

	want := []string{
		"proxmox-backup-manager notification endpoint smtp create Gmail-relay user@example.com --from-address pbs@example.com --server smtp.gmail.com --port 587 --username user --password secret123",
		"proxmox-backup-manager notification matcher create default-matcher --target Gmail-relay",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestBuildPBSNotificationDesiredState_NilLoggerSkipsMalformedSections(t *testing.T) {
	cfgSections := []proxmoxNotificationSection{
		{Type: "unknown", Name: "ignored"},
		{Type: "smtp", Name: "missing-recipient"},
		{Type: "gotify", Name: "missing-server"},
		{
			Type: "gotify",
			Name: "missing-token",
			Entries: []proxmoxNotificationEntry{
				{Key: "server", Value: "https://gotify.example"},
			},
		},
	}

	desired := buildPBSNotificationDesiredState(cfgSections, nil, nil, &pbsNotificationApplyReport{})
	if len(desired.endpoints) != 0 {
		t.Fatalf("expected malformed endpoints to be skipped, got=%v", desired.endpoints)
	}
	if len(desired.matchers) != 0 {
		t.Fatalf("expected no matchers, got=%v", desired.matchers)
	}
}

// A backup that carries no notifications.cfg is not a failure, but it is not an apply
// either. Before this was distinguished, the caller logged "PBS notifications applied via
// API" for a run that had touched nothing.
func TestApplyPBSNotificationsViaAPI_NothingStagedIsNotAnApply(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	runner := &fakeCommandRunner{}
	restoreCmd = runner

	logger := logging.New(types.LogLevelDebug, false)

	for _, strict := range []bool{false, true} {
		rep, err := applyPBSNotificationsViaAPI(context.Background(), logger, "/empty-stage", strict)
		if err != nil {
			t.Fatalf("strict=%v: an absent notifications.cfg is not an error: %v", strict, err)
		}
		if rep.mutated() {
			t.Fatalf("strict=%v: nothing was staged, so nothing can have been applied", strict)
		}
	}

	if len(runner.calls) != 0 {
		t.Fatalf("no proxmox-backup-manager command should run with an empty stage, got: %v", runner.calls)
	}
}

// stagePBSNotificationFixture writes a minimal but realistic staged notification config:
// one smtp endpoint whose password lives in the priv file, and one matcher pointing at it.
func stagePBSNotificationFixture(t *testing.T, fakeFS *FakeFS, stageRoot string) {
	t.Helper()

	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/notifications.cfg", []byte(
		"smtp: Gmail-relay\n"+
			"    recipients user@example.com\n"+
			"    from-address pbs@example.com\n"+
			"    server smtp.gmail.com\n"+
			"    port 587\n"+
			"    username user\n"+
			"\n"+
			"matcher: default-matcher\n"+
			"    target Gmail-relay\n",
	), 0o640); err != nil {
		t.Fatalf("write staged notifications.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/notifications-priv.cfg", []byte(
		"smtp: Gmail-relay\n"+
			"    password secret123\n",
	), 0o600); err != nil {
		t.Fatalf("write staged notifications-priv.cfg: %v", err)
	}
}

// Clean mode is the destructive one: it removes live objects the backup does not contain.
// It had no coverage at all, so nothing proved the strict branch ran, let alone that a
// completed run reports applied=true.
func TestApplyPBSNotificationsViaAPI_StrictRemovesExtrasAndReportsApplied(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	stagePBSNotificationFixture(t, fakeFS, stageRoot)

	// A live matcher the backup does not contain: strict mode must remove it.
	runner := &fakeCommandRunner{
		outputs: map[string][]byte{
			"proxmox-backup-manager notification matcher list": []byte(`[{"name":"default-matcher"},{"name":"stale-matcher"}]`),
		},
	}
	restoreCmd = runner

	logger := logging.New(types.LogLevelDebug, false)

	rep, err := applyPBSNotificationsViaAPI(context.Background(), logger, stageRoot, true)
	if err != nil {
		t.Fatalf("applyPBSNotificationsViaAPI error: %v", err)
	}
	if !rep.mutated() {
		t.Fatal("a completed strict apply must report applied=true")
	}

	joined := strings.Join(runner.calls, "\n")
	if !strings.Contains(joined, "notification matcher remove stale-matcher") {
		t.Fatalf("strict mode must remove the matcher missing from the backup, calls:\n%s", joined)
	}
	if strings.Contains(joined, "notification matcher remove default-matcher") {
		t.Fatalf("a matcher present in the backup must be kept, calls:\n%s", joined)
	}
}

// applied is only useful if it is trustworthy when things go wrong. Without this, a later
// refactor could return true alongside an error and no test would notice.
func TestApplyPBSNotificationsViaAPI_FailureReportsNotApplied(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	stagePBSNotificationFixture(t, fakeFS, stageRoot)

	// upsertPBSNotificationMatcher falls back to update when create fails, so both have to
	// fail for the matcher sync to report an error.
	boom := errors.New("proxmox-backup-manager exploded")
	runner := &fakeCommandRunner{
		errs: map[string]error{
			"proxmox-backup-manager notification matcher create default-matcher --target Gmail-relay": boom,
			"proxmox-backup-manager notification matcher update default-matcher --target Gmail-relay": boom,
		},
	}
	restoreCmd = runner

	logger := logging.New(types.LogLevelDebug, false)

	rep, err := applyPBSNotificationsViaAPI(context.Background(), logger, stageRoot, false)
	if err == nil {
		t.Fatal("a failing matcher sync must surface an error")
	}
	// The report is an account, not a verdict: the smtp endpoint WAS written before the
	// matcher failed, and hiding that would leave the operator believing the live config is
	// untouched when it is half-updated.
	if rep.endpointsUpserted != 1 {
		t.Fatalf("the endpoint written before the failure must be counted, got %d", rep.endpointsUpserted)
	}
	if rep.matchersUpserted != 0 {
		t.Fatalf("the matcher failed, so it must not be counted, got %d", rep.matchersUpserted)
	}
	if !rep.mutated() {
		t.Fatal("a partially applied run must report that something was written")
	}
}

// The two error propagations out of applyPBSNotificationsViaAPI. Neither needs root, a real
// filesystem or a live PBS: FakeCommandRunner turns the underlying command into a failure.
func TestApplyPBSNotificationsViaAPI_PropagatesSyncFailures(t *testing.T) {
	boom := errors.New("proxmox-backup-manager exploded")

	tests := []struct {
		name   string
		strict bool
		errs   map[string]error
	}{
		{
			// strict-only path: removeExtraPBSNotificationMatchers cannot list what is live.
			name:   "matcher list failure aborts a Clean apply",
			strict: true,
			errs: map[string]error{
				"proxmox-backup-manager notification matcher list": boom,
			},
		},
		{
			// syncPBSNotificationEndpoints: create fails, and so does the update fallback.
			name:   "endpoint sync failure aborts the apply",
			strict: false,
			errs: map[string]error{
				"proxmox-backup-manager notification endpoint smtp create Gmail-relay user@example.com --from-address pbs@example.com --server smtp.gmail.com --port 587 --username user --password secret123": boom,
				"proxmox-backup-manager notification endpoint smtp update Gmail-relay user@example.com --from-address pbs@example.com --server smtp.gmail.com --port 587 --username user --password secret123": boom,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origCmd := restoreCmd
			origFS := restoreFS
			t.Cleanup(func() {
				restoreCmd = origCmd
				restoreFS = origFS
			})

			fakeFS := NewFakeFS()
			t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
			restoreFS = fakeFS

			stageRoot := "/stage"
			stagePBSNotificationFixture(t, fakeFS, stageRoot)

			restoreCmd = &fakeCommandRunner{errs: tt.errs}
			logger := logging.New(types.LogLevelDebug, false)

			rep, err := applyPBSNotificationsViaAPI(context.Background(), logger, stageRoot, tt.strict)
			if err == nil {
				t.Fatal("the failure must be propagated, not swallowed")
			}
			if rep.mutated() {
				t.Fatal("an aborted apply must not report applied=true")
			}
		})
	}
}
