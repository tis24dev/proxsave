package orchestrator

import (
	"os"
	"testing"
)

func TestDetectNICNamingOverrideRules_FindsUdevAndSystemdLinkRules(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if err := fakeFS.AddDir("/etc/udev/rules.d"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	udevRule := `# Example persistent net naming
SUBSYSTEM=="net", ACTION=="add", ATTR{address}=="00:11:22:33:44:55", NAME="eth0"
`
	if err := fakeFS.AddFile("/etc/udev/rules.d/70-persistent-net.rules", []byte(udevRule)); err != nil {
		t.Fatalf("write udev rule: %v", err)
	}

	if err := fakeFS.AddDir("/etc/systemd/network"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	linkRule := `[Match]
MACAddress=66:77:88:99:aa:bb

[Link]
Name=lan0
`
	if err := fakeFS.AddFile("/etc/systemd/network/10-test.link", []byte(linkRule)); err != nil {
		t.Fatalf("write link rule: %v", err)
	}

	report, err := detectNICNamingOverrideRules(nil)
	if err != nil {
		t.Fatalf("detectNICNamingOverrideRules error: %v", err)
	}
	if report.Empty() {
		t.Fatalf("expected overrides, got none")
	}

	udevFound := false
	linkFound := false
	for _, rule := range report.Rules {
		switch rule.Kind {
		case nicNamingOverrideUdev:
			if rule.Name == "eth0" && rule.MAC == "00:11:22:33:44:55" {
				udevFound = true
			}
		case nicNamingOverrideSystemdLink:
			if rule.Name == "lan0" && rule.MAC == "66:77:88:99:aa:bb" {
				linkFound = true
			}
		}
	}
	if !udevFound {
		t.Fatalf("expected udev naming override to be detected; rules=%#v", report.Rules)
	}
	if !linkFound {
		t.Fatalf("expected systemd-link naming override to be detected; rules=%#v", report.Rules)
	}
}
