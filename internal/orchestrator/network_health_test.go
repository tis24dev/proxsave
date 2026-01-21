package orchestrator

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

type fakeCommandRunner struct {
	outputs map[string][]byte
	errs    map[string]error
	calls   []string
}

func (f *fakeCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, key)
	if err, ok := f.errs[key]; ok {
		return f.outputs[key], err
	}
	if out, ok := f.outputs[key]; ok {
		return out, nil
	}
	return []byte{}, nil
}

func TestRunNetworkHealthChecksOKWithSSH(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	t.Setenv("SSH_CONNECTION", "192.0.2.10 12345 192.0.2.1 22")

	fake := &fakeCommandRunner{
		outputs: map[string][]byte{
			"ip route get 192.0.2.10": []byte("192.0.2.10 via 192.0.2.254 dev vmbr0 src 192.0.2.1 uid 0\n    cache\n"),
			"ip route show default":   []byte("default via 192.0.2.254 dev vmbr0\n"),
			"ip -o link show dev vmbr0": []byte(
				"5: vmbr0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP mode DEFAULT group default qlen 1000\n",
			),
			"ip -o addr show dev vmbr0 scope global": []byte(
				"5: vmbr0    inet 192.0.2.1/24 brd 192.0.2.255 scope global vmbr0\\       valid_lft forever preferred_lft forever\n",
			),
		},
	}
	restoreCmd = fake

	logger := logging.New(types.LogLevelDebug, false)
	report := runNetworkHealthChecks(context.Background(), networkHealthOptions{
		CommandTimeout:     50 * time.Millisecond,
		EnableGatewayPing:  false,
		ForceSSHRouteCheck: false,
	})
	logNetworkHealthReport(logger, report)
	if report.Severity != networkHealthOK {
		t.Fatalf("severity=%v want %v\n%s", report.Severity, networkHealthOK, report.Details())
	}
	if !strings.Contains(report.Details(), "SSH route") {
		t.Fatalf("expected SSH route in details: %s", report.Details())
	}
}

func TestRunNetworkHealthChecksCriticalWhenSSHRouteMissing(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	t.Setenv("SSH_CONNECTION", "203.0.113.9 12345 203.0.113.1 22")

	fake := &fakeCommandRunner{
		outputs: map[string][]byte{
			"ip route show default": []byte("default via 203.0.113.254 dev vmbr0\n"),
			"ip -o link show dev vmbr0": []byte(
				"5: vmbr0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP mode DEFAULT group default qlen 1000\n",
			),
			"ip -o addr show dev vmbr0 scope global": []byte(
				"5: vmbr0    inet 203.0.113.1/24 brd 203.0.113.255 scope global vmbr0\\       valid_lft forever preferred_lft forever\n",
			),
		},
		errs: map[string]error{
			"ip route get 203.0.113.9": errors.New("RTNETLINK answers: Network is unreachable"),
		},
	}
	restoreCmd = fake

	logger := logging.New(types.LogLevelDebug, false)
	report := runNetworkHealthChecks(context.Background(), networkHealthOptions{
		CommandTimeout:     50 * time.Millisecond,
		EnableGatewayPing:  false,
		ForceSSHRouteCheck: false,
	})
	logNetworkHealthReport(logger, report)
	if report.Severity != networkHealthCritical {
		t.Fatalf("severity=%v want %v\n%s", report.Severity, networkHealthCritical, report.Details())
	}
}

func TestRunNetworkHealthChecksWarnWhenNoDefaultRoute(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_CLIENT", "")

	fake := &fakeCommandRunner{
		outputs: map[string][]byte{
			"ip route show default": []byte(""),
		},
	}
	restoreCmd = fake

	logger := logging.New(types.LogLevelDebug, false)
	report := runNetworkHealthChecks(context.Background(), networkHealthOptions{
		CommandTimeout:     50 * time.Millisecond,
		EnableGatewayPing:  false,
		ForceSSHRouteCheck: false,
	})
	logNetworkHealthReport(logger, report)
	if report.Severity != networkHealthWarn {
		t.Fatalf("severity=%v want %v\n%s", report.Severity, networkHealthWarn, report.Details())
	}
}

func TestRunNetworkHealthChecksIncludesDNSAndLocalPort(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	origDNS := dnsLookupHostFunc
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
		dnsLookupHostFunc = origDNS
	})

	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_CLIENT", "")

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	if err := fakeFS.WriteFile("/etc/resolv.conf", []byte("nameserver 1.1.1.1\n"), 0o644); err != nil {
		t.Fatalf("write resolv.conf: %v", err)
	}

	restoreCmd = &fakeCommandRunner{
		outputs: map[string][]byte{
			"ip route show default": []byte(""),
		},
	}

	dnsLookupHostFunc = func(ctx context.Context, host string) ([]string, error) {
		return []string{"203.0.113.1"}, nil
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	report := runNetworkHealthChecks(context.Background(), networkHealthOptions{
		CommandTimeout:   200 * time.Millisecond,
		EnableDNSResolve: true,
		DNSResolveHost:   "proxmox.com",
		LocalPortChecks: []tcpPortCheck{
			{Name: "Test port", Address: "127.0.0.1", Port: port},
		},
	})

	details := report.Details()
	if !strings.Contains(details, "DNS config") {
		t.Fatalf("expected DNS config check in report:\n%s", details)
	}
	if !strings.Contains(details, "DNS resolve") {
		t.Fatalf("expected DNS resolve check in report:\n%s", details)
	}
	if !strings.Contains(details, "Test port") {
		t.Fatalf("expected local port check in report:\n%s", details)
	}
}
