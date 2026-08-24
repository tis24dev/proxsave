package orchestrator

import (
	"context"
	"os"
	"reflect"
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
	applied, err := applyPBSNotificationsViaAPI(context.Background(), logger, stageRoot, false)
	if err != nil {
		t.Fatalf("applyPBSNotificationsViaAPI error: %v", err)
	}
	if !applied {
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

	desired := buildPBSNotificationDesiredState(cfgSections, nil, nil)
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
		applied, err := applyPBSNotificationsViaAPI(context.Background(), logger, "/empty-stage", strict)
		if err != nil {
			t.Fatalf("strict=%v: an absent notifications.cfg is not an error: %v", strict, err)
		}
		if applied {
			t.Fatalf("strict=%v: nothing was staged, so nothing can have been applied", strict)
		}
	}

	if len(runner.calls) != 0 {
		t.Fatalf("no proxmox-backup-manager command should run with an empty stage, got: %v", runner.calls)
	}
}
