package orchestrator

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunNetworkHealthChecksIncludesCorosyncQuorumOK(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})

	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_CLIENT", "")

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	if err := fakeFS.WriteFile("/etc/pve/corosync.conf", []byte("nodelist {}\n"), 0o640); err != nil {
		t.Fatalf("write corosync.conf: %v", err)
	}

	restoreCmd = &fakeCommandRunner{
		outputs: map[string][]byte{
			"ip route show default": []byte("default via 192.0.2.254 dev vmbr0\n"),
			"ip -o link show dev vmbr0": []byte(
				"5: vmbr0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP mode DEFAULT group default qlen 1000\n",
			),
			"ip -o addr show dev vmbr0 scope global": []byte(
				"5: vmbr0    inet 192.0.2.1/24 brd 192.0.2.255 scope global vmbr0\\       valid_lft forever preferred_lft forever\n",
			),
			"mountpoint -q /etc/pve":          []byte(""),
			"systemctl is-active pve-cluster": []byte("active\n"),
			"systemctl is-active corosync":    []byte("active\n"),
			"pvecm status": []byte(
				"Quorum information\n" +
					"------------------\n" +
					"Nodes:            3\n" +
					"Quorate:          Yes\n" +
					"\n" +
					"Votequorum information\n" +
					"----------------------\n" +
					"Expected votes:   3\n" +
					"Total votes:      3\n" +
					"\n" +
					"Ring0_addr:       10.0.0.11\n",
			),
		},
	}

	report := runNetworkHealthChecks(context.Background(), networkHealthOptions{
		SystemType:        SystemTypePVE,
		CommandTimeout:    50 * time.Millisecond,
		EnableGatewayPing: false,
		EnableDNSResolve:  false,
	})
	if report.Severity != networkHealthOK {
		t.Fatalf("severity=%v want %v\n%s", report.Severity, networkHealthOK, report.Details())
	}
	details := report.Details()
	if !strings.Contains(details, "corosync service") {
		t.Fatalf("expected corosync service check in report:\n%s", details)
	}
	if !strings.Contains(details, "Cluster quorum") {
		t.Fatalf("expected Cluster quorum check in report:\n%s", details)
	}
	if !strings.Contains(details, "quorate=yes") {
		t.Fatalf("expected quorate=yes in report:\n%s", details)
	}
}

func TestRunNetworkHealthChecksCorosyncQuorumWarnButNotCritical(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})

	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_CLIENT", "")

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	if err := fakeFS.WriteFile("/etc/pve/corosync.conf", []byte("nodelist {}\n"), 0o640); err != nil {
		t.Fatalf("write corosync.conf: %v", err)
	}

	restoreCmd = &fakeCommandRunner{
		outputs: map[string][]byte{
			"ip route show default": []byte("default via 192.0.2.254 dev vmbr0\n"),
			"ip -o link show dev vmbr0": []byte(
				"5: vmbr0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP mode DEFAULT group default qlen 1000\n",
			),
			"ip -o addr show dev vmbr0 scope global": []byte(
				"5: vmbr0    inet 192.0.2.1/24 brd 192.0.2.255 scope global vmbr0\\       valid_lft forever preferred_lft forever\n",
			),
			"mountpoint -q /etc/pve":          []byte(""),
			"systemctl is-active pve-cluster": []byte("active\n"),
			"systemctl is-active corosync":    []byte("inactive\n"),
			"pvecm status": []byte(
				"Quorum information\n" +
					"------------------\n" +
					"Nodes:            2\n" +
					"Quorate:          No\n" +
					"\n" +
					"Votequorum information\n" +
					"----------------------\n" +
					"Expected votes:   2\n" +
					"Total votes:      1\n",
			),
		},
		errs: map[string]error{
			"systemctl is-active corosync": errors.New("exit status 3"),
		},
	}

	report := runNetworkHealthChecks(context.Background(), networkHealthOptions{
		SystemType:        SystemTypePVE,
		CommandTimeout:    50 * time.Millisecond,
		EnableGatewayPing: false,
		EnableDNSResolve:  false,
	})
	if report.Severity != networkHealthWarn {
		t.Fatalf("severity=%v want %v\n%s", report.Severity, networkHealthWarn, report.Details())
	}
	if strings.Contains(report.Details(), networkHealthCritical.String()) {
		t.Fatalf("expected no CRITICAL checks in report:\n%s", report.Details())
	}
}
