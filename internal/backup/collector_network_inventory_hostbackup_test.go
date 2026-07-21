package backup

import (
	"bytes"
	"context"
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
