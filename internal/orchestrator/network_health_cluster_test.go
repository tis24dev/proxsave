package orchestrator

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// A standalone host must not need Corosync quorum to pass recovery checks. A
// configured cluster with one member still needs its quorum checked, including
// when it is the sole survivor of a larger cluster and has lost quorum.
func TestRecoveryHealthDistinguishesStandaloneAndOneMemberCluster(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured bool
		expected   int
		quorate    bool
		want       networkHealthSeverity
	}{
		{name: "standalone", want: networkHealthOK},
		{name: "one_member_cluster", configured: true, expected: 1, quorate: true, want: networkHealthOK},
		{name: "one_survivor_without_quorum", configured: true, expected: 3, want: networkHealthWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originalFS, originalCmd := restoreFS, restoreCmd
			t.Cleanup(func() { restoreFS, restoreCmd = originalFS, originalCmd })
			fs := NewFakeFS()
			t.Cleanup(func() { _ = os.RemoveAll(fs.Root) })
			restoreFS = fs
			runner := &fakeCommandRunner{
				outputs: map[string][]byte{
					"mountpoint -q /etc/pve":          {},
					"systemctl is-active pve-cluster": []byte("active\n"),
					"systemctl is-active corosync":    []byte("inactive\n"),
					"pvecm status":                    []byte("Corosync configuration does not exist\n"),
				},
				errs: map[string]error{
					"pvecm status":                 errors.New("exit status 2"),
					"systemctl is-active corosync": errors.New("exit status 3"),
				},
			}
			if tc.configured {
				if err := fs.WriteFile("/etc/pve/corosync.conf", []byte("nodelist {}\n"), 0o640); err != nil {
					t.Fatal(err)
				}
				runner.outputs["pvecm status"] = []byte(pvecmStatusOutput(1, tc.expected, tc.quorate))
				runner.outputs["systemctl is-active corosync"] = []byte("active\n")
				delete(runner.errs, "pvecm status")
				delete(runner.errs, "systemctl is-active corosync")
			}
			restoreCmd = runner
			report := &networkHealthReport{}
			runCorosyncClusterHealthChecks(context.Background(), 50*time.Millisecond, nil, report)
			if report.Severity != tc.want {
				t.Fatalf("health=%v; want %v\n%s", report.Severity, tc.want, report.Details())
			}
			var checkedMount, checkedQuorum bool
			for _, check := range report.Checks {
				switch check.Name {
				case "PMXCFS":
					checkedMount = true
				case "Cluster quorum":
					checkedQuorum = true
				}
			}
			if !checkedMount || checkedQuorum != tc.configured {
				t.Errorf("mount checked=%t quorum checked=%t; want true, %t\n%s", checkedMount, checkedQuorum, tc.configured, report.Details())
			}
		})
	}
}

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
