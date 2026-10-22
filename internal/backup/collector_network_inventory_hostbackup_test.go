package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestNetworkInventoryHostSysfsMarkerGating pins the round-2 fix: an unreadable
// /sys/class/net under a prefix emits the honest "host sysfs may not be carried"
// Info ONLY under HOST_BACKUP_MODE; a plain prefix keeps the neutral debug line
// (issue #255), so a chroot/snapshot/CI run is not told about a nonexistent bind
// mount.
func TestNetworkInventoryHostSysfsMarkerGating(t *testing.T) {
	cases := []struct {
		name       string
		hostBackup bool
		wantMarker bool
	}{
		{"host-backup mode marks host sysfs", true, true},
		{"plain prefix stays neutral", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A prefix whose /sys/class/net does not exist forces the ReadDir error path.
			root := t.TempDir()
			buf := &bytes.Buffer{}
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(buf)

			c := &Collector{
				logger: logger,
				config: &CollectorConfig{SystemRootPrefix: root, HostBackupMode: tc.hostBackup},
			}
			if err := c.collectNetworkInventory(context.Background(), t.TempDir(), t.TempDir()); err != nil {
				t.Fatalf("collectNetworkInventory: %v", err)
			}

			out := buf.String()
			hasMarker := strings.Contains(out, "host sysfs may not be carried")
			if hasMarker != tc.wantMarker {
				t.Fatalf("host-sysfs marker present=%v want %v; output:\n%s", hasMarker, tc.wantMarker, out)
			}
			if !tc.hostBackup && strings.Contains(out, "host sysfs") {
				t.Fatalf("plain prefix must not mention host sysfs; output:\n%s", out)
			}
		})
	}
}

// network_inventory.json lists the interfaces in name order.
func TestCollectNetworkInventoryListsInterfacesByName(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the network inventory is collected on linux only")
	}
	root := t.TempDir()
	for _, name := range []string{"vmbr0", "eno1", "lo"} {
		if err := os.MkdirAll(filepath.Join(root, "sys", "class", "net", name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	c := newTestCollector(t)
	c.config.SystemRootPrefix = root
	commandsDir := filepath.Join(c.tempDir, "commands")

	if err := c.collectNetworkInventory(context.Background(), commandsDir, ""); err != nil {
		t.Fatalf("collectNetworkInventory: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(commandsDir, "network_inventory.json"))
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	var inv networkInventory
	if err := json.Unmarshal(data, &inv); err != nil {
		t.Fatalf("parse inventory: %v", err)
	}
	var got []string
	for _, iface := range inv.Interfaces {
		got = append(got, iface.Name)
	}
	if want := []string{"eno1", "lo", "vmbr0"}; !slices.Equal(got, want) {
		t.Fatalf("interfaces = %v, want %v", got, want)
	}
}
