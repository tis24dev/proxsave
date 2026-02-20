package orchestrator

import (
	"os"
	"testing"
)

func TestSyncDirExact_PrunesExtraneousFiles(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	restoreFS = fakeFS

	stage := "/stage/etc/pve/firewall"
	dest := "/etc/pve/firewall"

	if err := fakeFS.AddFile(stage+"/cluster.fw", []byte("new\n")); err != nil {
		t.Fatalf("add staged cluster.fw: %v", err)
	}
	if err := fakeFS.AddFile(stage+"/vm/100.fw", []byte("vm\n")); err != nil {
		t.Fatalf("add staged vm/100.fw: %v", err)
	}

	if err := fakeFS.AddFile(dest+"/cluster.fw", []byte("old\n")); err != nil {
		t.Fatalf("add dest cluster.fw: %v", err)
	}
	if err := fakeFS.AddFile(dest+"/old.fw", []byte("remove\n")); err != nil {
		t.Fatalf("add dest old.fw: %v", err)
	}

	if _, err := syncDirExact(stage, dest); err != nil {
		t.Fatalf("syncDirExact error: %v", err)
	}

	data, err := fakeFS.ReadFile(dest + "/cluster.fw")
	if err != nil {
		t.Fatalf("read dest cluster.fw: %v", err)
	}
	if string(data) != "new\n" {
		t.Fatalf("unexpected cluster.fw content: %q", string(data))
	}

	if _, err := fakeFS.Stat(dest + "/old.fw"); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected old.fw to be removed; stat err=%v", err)
	}
}

func TestApplyPVEFirewallFromStage_AppliesFirewallAndHostFW(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	restoreFS = fakeFS

	node, _ := os.Hostname()
	node = shortHost(node)
	if node == "" {
		node = "localhost"
	}

	stageRoot := "/stage"
	if err := fakeFS.AddFile(stageRoot+"/etc/pve/firewall/cluster.fw", []byte("cluster\n")); err != nil {
		t.Fatalf("add staged firewall: %v", err)
	}
	if err := fakeFS.AddFile(stageRoot+"/etc/pve/nodes/"+node+"/host.fw", []byte("host\n")); err != nil {
		t.Fatalf("add staged host.fw: %v", err)
	}

	if err := fakeFS.AddFile("/etc/pve/firewall/old.fw", []byte("remove\n")); err != nil {
		t.Fatalf("add existing old.fw: %v", err)
	}

	applied, err := applyPVEFirewallFromStage(newTestLogger(), stageRoot)
	if err != nil {
		t.Fatalf("applyPVEFirewallFromStage error: %v", err)
	}
	if len(applied) == 0 {
		t.Fatalf("expected applied paths, got none")
	}

	if _, err := fakeFS.Stat("/etc/pve/firewall/old.fw"); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected old.fw to be removed; stat err=%v", err)
	}

	data, err := fakeFS.ReadFile("/etc/pve/firewall/cluster.fw")
	if err != nil {
		t.Fatalf("read cluster.fw: %v", err)
	}
	if string(data) != "cluster\n" {
		t.Fatalf("unexpected cluster.fw content: %q", string(data))
	}

	data, err = fakeFS.ReadFile("/etc/pve/nodes/" + node + "/host.fw")
	if err != nil {
		t.Fatalf("read host.fw: %v", err)
	}
	if string(data) != "host\n" {
		t.Fatalf("unexpected host.fw content: %q", string(data))
	}
}

func TestApplyPVEFirewallFromStage_MapsSingleHostFWCandidate(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	restoreFS = fakeFS

	node, _ := os.Hostname()
	node = shortHost(node)
	if node == "" {
		node = "localhost"
	}

	stageRoot := "/stage"
	if err := fakeFS.AddFile(stageRoot+"/etc/pve/nodes/othernode/host.fw", []byte("host\n")); err != nil {
		t.Fatalf("add staged host.fw: %v", err)
	}

	_, err := applyPVEFirewallFromStage(newTestLogger(), stageRoot)
	if err != nil {
		t.Fatalf("applyPVEFirewallFromStage error: %v", err)
	}

	data, err := fakeFS.ReadFile("/etc/pve/nodes/" + node + "/host.fw")
	if err != nil {
		t.Fatalf("read mapped host.fw: %v", err)
	}
	if string(data) != "host\n" {
		t.Fatalf("unexpected mapped host.fw content: %q", string(data))
	}
}
