package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// These tests exercise the collector's diagnostic boundary: flattening errors,
// dropping detail indentation, changing severity, or silencing unknown topology
// must not change the approved facts or hide the missing cluster artifacts.
func TestPVEUnknownClusterDiagnosticBlocks(t *testing.T) {
	for _, tc := range []struct {
		name, reason, detail string
		mutate               func(*testing.T, *Collector, string)
	}{
		{"filesystem_error", "cannot inspect CONFIG", "Error: permission denied", func(t *testing.T, c *Collector, root string) {
			c.deps.Stat = func(path string) (os.FileInfo, error) {
				return nil, &os.PathError{Op: "stat", Path: path, Err: syscall.EACCES}
			}
		}},
		{"authkey", "authkey found without Corosync configuration", "", func(t *testing.T, c *Collector, root string) {
			base := c.deps.Stat
			c.deps.Stat = func(path string) (os.FileInfo, error) {
				if path == "/etc/corosync/authkey" {
					return os.Stat(filepath.Join(root, "var/lib/pve-cluster/config.db"))
				}
				return base(path)
			}
		}},
		{"unrecognized_pvecm", "pvecm status returned unrecognized output", "", func(t *testing.T, c *Collector, root string) {
			c.deps.RunCommand = func(context.Context, string, ...string) ([]byte, error) { return []byte("unknown output"), nil }
		}},
		{"systemctl_unavailable", "systemctl unavailable", "Error: permission denied", func(t *testing.T, c *Collector, root string) {
			c.deps.LookPath = func(name string) (string, error) {
				if name == "systemctl" {
					return "", &os.PathError{Op: "stat", Path: "/usr/bin/systemctl", Err: syscall.EACCES}
				}
				return "/usr/bin/" + name, nil
			}
		}},
		{"service_query", "cannot query corosync.service", "Error: permission denied", func(t *testing.T, c *Collector, root string) {
			base := c.deps.RunCommand
			c.deps.RunCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "systemctl" {
					return nil, &os.PathError{Op: "exec", Path: "/usr/bin/systemctl", Err: syscall.EACCES}
				}
				return base(ctx, name, args...)
			}
		}},
		{"service_state", "unexpected state for corosync.service", "State: failed/failed; PID: 42", func(t *testing.T, c *Collector, root string) {
			base := c.deps.RunCommand
			c.deps.RunCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "systemctl" {
					return []byte("ActiveState=failed\nSubState=failed\nMainPID=42\n"), nil
				}
				return base(ctx, name, args...)
			}
		}},
		{"mount_read", "cannot read pmxcfs mount information", "Error: permission denied", denyPVEEvidence("/proc/self/mountinfo")},
		{"missing_mount", "PVE configuration path is not mounted as pmxcfs", "", func(t *testing.T, c *Collector, root string) {
			mustCoreWrite(t, filepath.Join(root, "proc/self/mountinfo"), "")
		}},
		{"membership_read", "cannot read pmxcfs membership", "Error: permission denied", denyPVEEvidence(".members")},
		{"membership_invalid", "invalid pmxcfs membership data", "", func(t *testing.T, c *Collector, root string) {
			mustCoreWrite(t, filepath.Join(root, "etc/pve/.members"), "bad")
		}},
		{"membership_cluster", "pmxcfs membership contains cluster evidence", "", func(t *testing.T, c *Collector, root string) {
			mustCoreWrite(t, filepath.Join(root, "etc/pve/.members"), `{"nodename":"pve1","version":0,"nodelist":{}}`)
		}},
		{"membership_changed", "pmxcfs membership changed during detection", "", func(t *testing.T, c *Collector, root string) {
			base, reads := c.deps.ReadFile, 0
			c.deps.ReadFile = func(path string) ([]byte, error) {
				if filepath.Base(path) == ".members" {
					reads++
					if reads > 1 {
						return []byte(`{"nodename":"pve1","version":1}`), nil
					}
				}
				return base(path)
			}
		}},
		{"nodes_conflict", "PVE node directories contain conflicting cluster evidence", "", func(t *testing.T, c *Collector, root string) {
			mustCoreWrite(t, filepath.Join(root, "etc/pve/nodes/other/node.cfg"), "")
		}},
		{"process_read", "cannot inspect pmxcfs process", "Error: permission denied", denyPVEEvidence("/proc/123/cmdline")},
		{"process_mode", "pmxcfs process is not running in normal mode", "", func(t *testing.T, c *Collector, root string) {
			mustCoreWrite(t, filepath.Join(root, "proc/123/cmdline"), "/usr/bin/pmxcfs\x00-l\x00")
		}},
		{"configuration_changed", "Corosync configuration changed", "", func(t *testing.T, c *Collector, root string) {
			base, reads := c.deps.Stat, 0
			c.deps.Stat = func(path string) (os.FileInfo, error) {
				if path == c.effectiveCorosyncConfigPath() {
					reads++
					if reads > 1 {
						return os.Stat(filepath.Join(root, "var/lib/pve-cluster/config.db"))
					}
				}
				return base(path)
			}
		}},
		{"service_changed", "PVE service state changed during detection", "", func(t *testing.T, c *Collector, root string) {
			base, reads := c.deps.RunCommand, 0
			c.deps.RunCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "systemctl" && len(args) > 1 && args[1] == "pve-cluster.service" {
					reads++
					if reads > 1 {
						return []byte("ActiveState=active\nSubState=running\nMainPID=124\n"), nil
					}
				}
				return base(ctx, name, args...)
			}
		}},
	} {
		for _, enabled := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/cluster=%v", tc.name, enabled), func(t *testing.T) {
				c, root := standaloneEvidenceCollector(t)
				c.config.BackupClusterConfig, c.config.BackupPVEACL = enabled, true
				mustCoreWrite(t, filepath.Join(root, "etc/pve/user.cfg"), "")
				var output bytes.Buffer
				c.logger.SetLevel(types.LogLevelDebug)
				c.logger.SetOutput(&output)
				tc.mutate(t, c, root)
				if err := newPVEValidationBricks()[0].Run(context.Background(), newCollectionState(c)); err != nil {
					t.Fatal(err)
				}
				level, detailLevel := "WARNING", "INFO"
				if !enabled {
					level, detailLevel = "DEBUG", "DEBUG"
				}
				reason := strings.ReplaceAll(tc.reason, "CONFIG", c.effectiveCorosyncConfigPath())
				want := []string{level + "|PVE cluster detection: " + reason}
				if tc.detail != "" {
					want = append(want, detailLevel+"|    "+tc.detail)
				}
				want = append(want, level+"|PVE cluster detection: state unknown")
				assertPVEDiagnosticEvents(t, output.String(), "PVE cluster detection:", want)
				if c.standalonePVE {
					t.Fatal("unknown topology became standalone")
				}
				if err := c.collectPVEClusterSnapshot(context.Background(), false); err != nil {
					t.Fatal(err)
				}
				c.populatePVEManifest()
				if got := c.pveManifest["var/lib/pve-cluster/config.db"].Status; got != StatusCollected {
					t.Fatalf("unknown topology blocked core database: %s", got)
				}
				if enabled {
					if got := c.GetStats().FilesNotFound; got != 2 {
						t.Fatalf("unknown topology hid missing Corosync artifacts: %d", got)
					}
					if !strings.Contains(output.String(), "Corosync configuration: not configured.") {
						t.Fatal("missing Corosync warning suppressed")
					}
				} else if c.logger.WarningCount() != 0 {
					t.Fatalf("disabled cluster emitted warning: %s", output.String())
				}
			})
		}
	}
}

func denyPVEEvidence(suffix string) func(*testing.T, *Collector, string) {
	return func(t *testing.T, c *Collector, root string) {
		base := c.deps.ReadFile
		c.deps.ReadFile = func(path string) ([]byte, error) {
			if strings.HasSuffix(path, suffix) {
				return nil, &os.PathError{Op: "open", Path: path, Err: syscall.EACCES}
			}
			return base(path)
		}
	}
}

func assertPVEDiagnosticEvents(t *testing.T, output, prefix string, want []string) {
	t.Helper()
	var got []string
	for _, line := range strings.Split(output, "\n") {
		_, line, ok := strings.Cut(line, "] ")
		if !ok || len(line) < 9 {
			continue
		}
		level, message := strings.TrimSpace(line[:8]), line[9:]
		if strings.HasPrefix(message, prefix) || strings.HasPrefix(strings.TrimSpace(message), "Error:") || strings.HasPrefix(strings.TrimSpace(message), "State:") || strings.HasPrefix(strings.TrimSpace(message), "Path:") {
			got = append(got, level+"|"+message)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic events:\n got %q\nwant %q\n%s", got, want, output)
	}
}

func TestPVEConfigDBFallbackDiagnosticBlocks(t *testing.T) {
	for _, failure := range []string{"non_regular", "stale_sidecar", "sqlite_error"} {
		t.Run(failure, func(t *testing.T) {
			c, sourceDir, output := configDBCollector(t, CollectorDeps{
				LookPath: func(string) (string, error) { return "/usr/bin/sqlite3", nil },
				RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					if failure == "sqlite_error" {
						return []byte("native sqlite diagnostic"), errors.New("exit status 1")
					}
					path := strings.TrimPrefix(args[len(args)-1], ".backup ")
					return nil, os.WriteFile(strings.Trim(path, `"`), []byte("snapshot"), 0600)
				},
			})
			c.config.BackupClusterConfig = false
			src := filepath.Join(sourceDir, "config.db")
			var want []string
			if failure == "non_regular" {
				if err := os.Remove(src); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(src, 0700); err != nil {
					t.Fatal(err)
				}
				want = append(want, "WARNING|PVE configuration database: not a regular file "+src)
			} else if failure == "stale_sidecar" {
				sidecar := c.targetPathFor(src) + "-wal"
				mustCoreWrite(t, filepath.Join(sidecar, "keep"), "obstruct cleanup")
				want = append(want, "WARNING|PVE configuration database: cannot remove config.db-wal", "DEBUG|    Path: "+sidecar, "DEBUG|    Error: directory not empty")
			}
			want = append(want, "INFO|PVE configuration database: falling back to a raw copy, which can tear mid-write and misses WAL data not yet checkpointed", "WARNING|PVE configuration database: sqlite snapshot failed")
			if err := c.collectPVEClusterSnapshot(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			assertPVEDiagnosticEvents(t, output.String(), "PVE configuration database:", want)
			if failure == "stale_sidecar" {
				if data, err := os.ReadFile(src); err != nil || string(data) != "RAW-BASE-BYTES" {
					t.Fatalf("live source changed: %q, %v", data, err)
				}
			}
		})
	}
}

func TestPVEConfigDBMissingDiagnosticIsCoreWarning(t *testing.T) {
	c, root := standaloneEvidenceCollector(t)
	c.config.BackupClusterConfig = false
	if err := os.Remove(filepath.Join(root, "var/lib/pve-cluster/config.db")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	c.logger.SetLevel(types.LogLevelDebug)
	c.logger.SetOutput(&output)
	c.populatePVEManifest()
	assertPVEDiagnosticEvents(t, output.String(), "PVE configuration database:", []string{"WARNING|PVE configuration database: file not found; database not backed up."})
	if c.GetStats().FilesNotFound != 1 {
		t.Fatal("missing core database was not counted")
	}
	if strings.Contains(output.String(), "BACKUP_CLUSTER_CONFIG=false to disable") {
		t.Fatal("core database recommended disabling cluster capture")
	}
}
