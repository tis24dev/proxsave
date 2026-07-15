package orchestrator

import (
	"strings"
	"testing"
)

// F06-02: the rollback prune phase must scope over all four managed network
// dirs, not just /etc/network, or extraneous files added to netplan /
// systemd-networkd / NetworkManager survive the rollback.
func TestRollbackScriptPrunesAllNetworkDirs(t *testing.T) {
	script := buildRollbackScript("/marker", "/backup.tar.gz", "/log", true)
	for _, dir := range []string{
		"/etc/network",
		"/etc/netplan",
		"/etc/systemd/network",
		"/etc/NetworkManager/system-connections",
	} {
		if !strings.Contains(script, dir) {
			t.Fatalf("prune scope must include %s", dir)
		}
	}
	// Guard must accept a manifest that has netplan but no etc/network.
	if strings.Contains(script, `grep -q '^etc/network/' "$MANIFEST"`) {
		t.Fatal("archive guard still hard-requires etc/network; must accept any managed network dir")
	}
	if !strings.Contains(script, "etc/netplan") {
		t.Fatal("archive guard must reference the managed network dirs (netplan)")
	}
}
