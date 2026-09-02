//go:build livepve

package orchestrator

// Live validation harness for the staged-apply series (fable-check bugs 1-5).
// Build:  CGO_ENABLED=0 go test -c -tags livepve ./internal/orchestrator -o livepve.test
// Run AS ROOT ON A PVE TEST NODE:  ./livepve.test -test.run TestLivePVE -test.v
//
// Every test is idempotent on purpose: identical bytes for the files, identical
// values for the API calls, and the one thing it CREATES (CT 990, config only)
// it deletes again. Still: test node only, file-level backups first.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func livePVEGuard(t *testing.T) *logging.Logger {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("live PVE checks need root")
	}
	if _, err := os.Stat("/etc/pve/local"); err != nil {
		t.Skip("not a PVE node (no /etc/pve/local)")
	}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(os.Stdout)
	return logger
}

func liveStage(t *testing.T, rel string, data []byte) string {
	t.Helper()
	root := t.TempDir()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, data, 0o640); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLivePVEDatacenterCfg(t *testing.T) {
	logger := livePVEGuard(t)
	before, err := os.ReadFile("/etc/pve/datacenter.cfg")
	if err != nil {
		t.Fatal(err)
	}
	stage := liveStage(t, "etc/pve/datacenter.cfg", before)
	if err := applyPVEDatacenterCfgFromStage(context.Background(), logger, stage); err != nil {
		t.Fatalf("datacenter apply: %v", err)
	}
	after, err := os.ReadFile("/etc/pve/datacenter.cfg")
	if err != nil || string(after) != string(before) {
		t.Fatalf("datacenter.cfg changed or unreadable after identical apply: err=%v\nbefore=%q\nafter=%q", err, before, after)
	}
}

func TestLivePVEVzdumpCron(t *testing.T) {
	logger := livePVEGuard(t)
	before, err := os.ReadFile("/etc/pve/vzdump.cron")
	if err != nil {
		t.Fatal(err)
	}
	stage := liveStage(t, "etc/pve/vzdump.cron", before)
	if err := applyPVEVzdumpCronFromStage(logger, stage); err != nil {
		t.Fatalf("vzdump.cron apply: %v", err)
	}
	after, err := os.ReadFile("/etc/pve/vzdump.cron")
	if err != nil || string(after) != string(before) {
		t.Fatalf("vzdump.cron changed after identical apply: err=%v", err)
	}
}

func TestLivePVEStorageCreateThenSet(t *testing.T) {
	logger := livePVEGuard(t)
	// The current `local` block, identical values: create must fail (already
	// defined), the set fallback must apply cleanly.
	cfg := "dir: local\n\tpath /var/lib/vz\n\tcontent iso,vztmpl,backup,import\n"
	stage := liveStage(t, "etc/pve/storage.cfg", []byte(cfg))
	applied, failed, err := applyStorageCfg(context.Background(), filepath.Join(stage, "etc/pve/storage.cfg"), logger)
	if err != nil {
		t.Fatalf("applyStorageCfg: %v", err)
	}
	if applied != 1 || failed != 0 {
		t.Fatalf("applied=%d failed=%d, want 1/0 (create-then-set on existing 'local')", applied, failed)
	}
}

func TestLivePVEExistingGuestWithMeta(t *testing.T) {
	logger := livePVEGuard(t)
	const conf = "/etc/pve/qemu-server/101.conf"
	before, err := os.ReadFile(conf)
	if err != nil {
		t.Skipf("no VM 101 on this node: %v", err)
	}
	if !strings.Contains(string(before), "meta:") {
		t.Skip("VM 101 has no meta line; the bug-2 shape is not on this node")
	}
	node := localNodeName()
	stage := liveStage(t, "etc/pve/nodes/"+node+"/qemu-server/101.conf", before)
	applied, failed := applyVMConfigs(context.Background(), []vmEntry{{
		VMID: "101", Kind: "qemu", Name: "openwrt25",
		Path: filepath.Join(stage, "etc/pve/nodes", node, "qemu-server", "101.conf"),
	}}, logger)
	if applied != 1 || failed != 0 {
		t.Fatalf("applied=%d failed=%d, want 1/0 for the meta-carrying stopped guest", applied, failed)
	}
	after, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "meta: creation-qemu") {
		t.Fatalf("the guest lost its meta line:\n%s", after)
	}
}

func TestLivePVEMissingLxcRegistration(t *testing.T) {
	logger := livePVEGuard(t)
	const vmid = "990"
	confPath := "/etc/pve/lxc/" + vmid + ".conf"
	if _, err := os.Stat(confPath); err == nil {
		t.Fatalf("CT %s already exists on this node; refusing to touch it", vmid)
	}
	t.Cleanup(func() {
		if err := exec.Command("pvesh", "delete", "/nodes/"+localNodeName()+"/lxc/"+vmid).Run(); err != nil {
			_ = os.Remove(confPath)
		}
	})

	conf := "hostname: proxsave-livecheck\nmemory: 128\ncores: 1\nostype: unmanaged\n"
	node := localNodeName()
	stage := liveStage(t, "etc/pve/nodes/"+node+"/lxc/"+vmid+".conf", []byte(conf))
	applied, failed := applyVMConfigs(context.Background(), []vmEntry{{
		VMID: vmid, Kind: "lxc", Name: "proxsave-livecheck",
		Path: filepath.Join(stage, "etc/pve/nodes", node, "lxc", vmid+".conf"),
	}}, logger)
	if applied != 1 || failed != 0 {
		t.Fatalf("applied=%d failed=%d, want 1/0 for the missing-guest registration", applied, failed)
	}
	out, err := exec.Command("pvesh", "get", "/nodes/"+node+"/lxc/"+vmid+"/config", "--output-format=json").Output()
	if err != nil {
		t.Fatalf("the registered CT does not answer on the API: %v", err)
	}
	if !strings.Contains(string(out), "proxsave-livecheck") {
		t.Fatalf("API answers but without our hostname: %s", out)
	}
}
