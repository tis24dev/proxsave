package orchestrator

import (
	"os"
	"strings"
	"testing"
)

// The IP the rollback restores is read from the before snapshot the UI flow writes in
// the diagnostics directory (the rollback work dir is /tmp/proxsave and holds none),
// and a VLAN interface is found under the name@parent form iproute2 prints.
func TestNotCommittedErrorReadsTheOriginalIPFromTheDiagnosticsSnapshot(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	snapshot := "/restore/network_apply_1/before.txt"
	content := strings.Join([]string{
		"$ ip -br addr",
		"lo               UNKNOWN        127.0.0.1/8 ::1/128",
		"vmbr0.10@vmbr0   UP             192.168.10.5/24",
		"$ ip route",
	}, "\n")
	if err := fakeFS.WriteFile(snapshot, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	handle := &networkRollbackHandle{workDir: "/tmp/proxsave", beforeSnapshot: snapshot}
	err := buildNetworkApplyNotCommittedError(t.Context(), newTestLogger(), "vmbr0.10", handle)
	if err == nil || err.OriginalIP != "192.168.10.5/24" {
		t.Fatalf("OriginalIP = %q, want 192.168.10.5/24 (err %+v)", err.OriginalIP, err)
	}
}
