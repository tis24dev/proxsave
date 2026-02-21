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
	if err := applyPBSNotificationsViaAPI(context.Background(), logger, stageRoot, false); err != nil {
		t.Fatalf("applyPBSNotificationsViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager notification endpoint smtp create Gmail-relay user@example.com --from-address pbs@example.com --server smtp.gmail.com --port 587 --username user --password secret123",
		"proxmox-backup-manager notification matcher create default-matcher --target Gmail-relay",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}
