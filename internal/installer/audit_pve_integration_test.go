package installer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Feed logs from the complete PVE collector through the real audit parser. Only
// external host commands and the subprocess transport of those logs are faked.
func TestPVEAuditDoesNotOfferDisablingCoreDatabase(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		missingDB, unknown   bool
		disableACL, firewall bool
		wantKey              string
		wantSuggestions      int
	}{
		{name: "standalone"},
		{name: "standalone_missing_database", missingDB: true},
		{name: "unknown_missing_corosync", unknown: true, wantSuggestions: 1, wantKey: "BACKUP_CLUSTER_CONFIG"},
		{name: "acl_disabled", disableACL: true},
		{name: "optional_firewall_missing", firewall: true, wantSuggestions: 1, wantKey: "BACKUP_PVE_FIREWALL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(rel, data string) {
				t.Helper()
				path := filepath.Join(root, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			members := `{"nodename":"pve1","version":0}`
			if tc.unknown {
				members = "invalid"
			}
			write("etc/pve/.members", members)
			write("etc/pve/user.cfg", "")
			write("etc/pve/nodes/pve1/node.cfg", "")
			if !tc.missingDB {
				write("var/lib/pve-cluster/config.db", "db")
			}
			write("proc/self/mountinfo", "23 1 0:12 / "+filepath.Join(root, "etc/pve")+" rw,relatime - fuse /dev/fuse rw,user_id=0,group_id=0\n")
			write("proc/123/cmdline", "/usr/bin/pmxcfs\x00")
			var output bytes.Buffer
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(&output)
			cfg := &backup.CollectorConfig{PVEConfigPath: filepath.Join(root, "etc/pve"), PVEClusterPath: filepath.Join(root, "var/lib/pve-cluster"), BackupClusterConfig: true, BackupPVEACL: !tc.disableACL, BackupPVEFirewall: tc.firewall}
			c := backup.NewCollectorWithDeps(logger, cfg, t.TempDir(), types.ProxmoxVE, true, backup.CollectorDeps{
				LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
				ReadFile: func(path string) ([]byte, error) {
					if strings.HasPrefix(path, "/proc/") {
						path = filepath.Join(root, path)
					}
					return os.ReadFile(path)
				},
				Stat: func(path string) (os.FileInfo, error) {
					if strings.HasPrefix(path, "/etc/corosync/") {
						path = filepath.Join(root, path)
					}
					return os.Stat(path)
				},
				RunCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
					if name == "pvecm" {
						return []byte("Error: Corosync config '/etc/pve/corosync.conf' does not exist - is this node part of a cluster?\n"), errors.New("exit status 2")
					}
					if name == "systemctl" && len(args) > 0 && args[0] == "show" {
						if strings.Contains(strings.Join(args, " "), "corosync") {
							return []byte("MainPID=0\nActiveState=inactive\nSubState=dead\n"), nil
						}
						if strings.Contains(strings.Join(args, " "), "pve-cluster") {
							return []byte("MainPID=123\nActiveState=active\nSubState=running\n"), nil
						}
					}
					return []byte("[]"), nil
				},
			})
			if err := c.CollectPVEConfigs(context.Background()); err != nil {
				t.Fatal(err)
			}
			if tc.disableACL && (!strings.Contains(output.String(), "config.db*") || !strings.Contains(output.String(), "BACKUP_EXCLUDE_PATTERNS")) {
				t.Fatalf("missing effective credential exclusion guidance: %s", output.String())
			}
			write("backup.env", "BACKUP_CLUSTER_CONFIG=true\nBACKUP_PVE_FIREWALL=true\n")
			oldRunner := postInstallAuditRunner
			t.Cleanup(func() { postInstallAuditRunner = oldRunner })
			postInstallAuditRunner = func(context.Context, string, string) (string, int, error) { return output.String(), 1, nil }
			suggestions, err := CollectPostInstallDisableSuggestions(context.Background(), "/test/proxsave", filepath.Join(root, "backup.env"))
			if err != nil {
				t.Fatal(err)
			}
			if len(suggestions) != tc.wantSuggestions {
				t.Fatalf("core database disable suggestion or missing cluster warning: %+v\n%s", suggestions, output.String())
			}
			if tc.wantSuggestions > 0 && suggestions[0].Key != tc.wantKey {
				t.Fatalf("unexpected optional-feature suggestion: %+v", suggestions)
			}
		})
	}
}
