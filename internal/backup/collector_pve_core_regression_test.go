package backup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These cases catch the old cluster toggle bypass and the snapshot path bypassing
// the ordinary copy exclusions, including custom source and archive destinations.
func TestPVECoreDBCaptureAndExclusions(t *testing.T) {
	for _, tc := range []struct {
		name, exclusion         string
		enabled, clustered, dry bool
	}{
		{name: "cluster_disabled"},
		{name: "read_only_snapshot", enabled: true},
		{name: "cluster_directory_snapshot", enabled: true, clustered: true},
		{name: "basename", exclusion: "config.db*", enabled: true},
		{name: "source_override", exclusion: "source", enabled: true},
		{name: "destination", exclusion: "destination", enabled: true},
		{name: "canonical_archive", exclusion: "var/lib/pve-cluster/config.db*", enabled: true},
		{name: "cluster_directory_sidecars", exclusion: "config.db*", enabled: true, clustered: true},
		{name: "dry_run", exclusion: "config.db*", enabled: true, dry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs := 0
			c, clusterDir, _ := configDBCollector(t, CollectorDeps{
				LookPath: func(string) (string, error) { return "/usr/bin/sqlite3", nil },
				RunCommand: func(_ context.Context, _ string, args ...string) ([]byte, error) {
					runs++
					if !strings.Contains(strings.Join(args, "|"), "-readonly|") {
						return nil, errors.New("source must be read-only")
					}
					dest := strings.TrimSuffix(strings.TrimPrefix(args[len(args)-1], `.backup "`), `"`)
					return nil, os.WriteFile(dest, []byte("CONSISTENT-SNAPSHOT"), 0600)
				},
			})
			c.config.BackupClusterConfig = tc.enabled
			c.dryRun = tc.dry
			src := filepath.Join(clusterDir, "config.db")
			dest := c.targetPathFor(src)
			for _, suffix := range []string{"-wal", "-shm"} {
				if err := os.WriteFile(src+suffix, []byte("sidecar"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			pattern := tc.exclusion
			if pattern == "source" {
				pattern = src
			}
			if pattern == "destination" {
				pattern = dest
			}
			if pattern != "" {
				c.config.ExcludePatterns = []string{pattern}
			}
			if err := c.collectPVEClusterSnapshot(context.Background(), tc.clustered); err != nil {
				t.Fatal(err)
			}
			c.populatePVEManifest()
			entry := c.pveManifest["var/lib/pve-cluster/config.db"]
			if pattern != "" {
				if runs != 0 {
					t.Fatalf("excluded source invoked SQLite %d times", runs)
				}
				if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("excluded database present: %v", err)
				}
				if entry.Status != StatusSkipped {
					t.Fatalf("manifest status = %s, want skipped", entry.Status)
				}
				if tc.clustered {
					for _, suffix := range []string{"-wal", "-shm"} {
						if _, err := os.Lstat(dest + suffix); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("excluded sidecar present: %v", err)
						}
					}
				}
				if c.GetStats().FilesProcessed != 0 {
					t.Fatalf("excluded artifact counted as processed: %+v", c.GetStats())
				}
			} else {
				data, err := os.ReadFile(dest)
				if err != nil || string(data) != "CONSISTENT-SNAPSHOT" {
					t.Fatalf("database was not captured independently: %q, %v", data, err)
				}
				for _, suffix := range []string{"-wal", "-shm", "-journal"} {
					if _, err := os.Stat(dest + suffix); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("snapshot retains live sidecar %s: %v", suffix, err)
					}
				}
				if entry.Status != StatusCollected {
					t.Fatalf("manifest status = %s, want collected", entry.Status)
				}
			}
		})
	}
}

func TestPVECoreDBSnapshotMissingSourceCannotCreateIt(t *testing.T) {
	c, clusterDir, _ := configDBCollector(t, CollectorDeps{})
	src := filepath.Join(clusterDir, "config.db")
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	if err := c.snapshotSQLiteDB(context.Background(), src, c.targetPathFor(src)); err == nil {
		t.Fatal("missing source snapshot succeeded")
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot created a missing live database: %v", err)
	}
}

// Exercise the production safeexec command boundary against a live WAL database.
// A separate connection remains open so the committed row is still in its WAL.
func TestPVECoreDBSnapshotRealWAL(t *testing.T) {
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 not installed")
	}
	c, clusterDir, _ := configDBCollector(t, CollectorDeps{})
	src := filepath.Join(clusterDir, "config.db")
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sqlite, src)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = fmt.Fprintln(stdin, ".quit"); _ = stdin.Close(); _ = cmd.Wait() })
	if _, err := fmt.Fprintln(stdin, "PRAGMA journal_mode=WAL;\nPRAGMA wal_autocheckpoint=0;\nCREATE TABLE recovery(value TEXT);\nINSERT INTO recovery VALUES('committed-in-wal');\n.print READY"); err != nil {
		t.Fatal(err)
	}
	scan := bufio.NewScanner(stdout)
	ready := false
	for scan.Scan() {
		if scan.Text() == "READY" {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatalf("SQLite writer did not initialize: %v", scan.Err())
	}
	if info, err := os.Stat(src + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("no live WAL: %v", err)
	}
	if err := c.collectPVEClusterSnapshot(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	dest := c.targetPathFor(src)
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(dest + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale sidecar beside independent snapshot: %s (%v)", suffix, err)
		}
	}
	out, err := exec.Command(sqlite, "-readonly", dest, "PRAGMA integrity_check; SELECT value FROM recovery;").CombinedOutput()
	if err != nil || string(out) != "ok\ncommitted-in-wal\n" {
		t.Fatalf("incoherent snapshot %q: %v", out, err)
	}
}

// A single node or a pvecm missing-config message is insufficient. Only the full
// consistent runtime fixture may suppress expected Corosync missing warnings.
func TestPVEStandaloneEvidenceAndManifest(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mutate      func(*testing.T, *Collector, string)
		wantMissing int64
	}{
		{name: "healthy_standalone", wantMissing: 0},
		{name: "missing_mount", wantMissing: 2, mutate: func(t *testing.T, c *Collector, root string) {
			mustCoreWrite(t, filepath.Join(root, "proc/self/mountinfo"), "")
		}},
		{name: "malformed_members", wantMissing: 2, mutate: func(t *testing.T, c *Collector, root string) {
			mustCoreWrite(t, filepath.Join(root, "etc/pve/.members"), "invalid")
		}},
		{name: "cluster_membership", wantMissing: 2, mutate: func(t *testing.T, c *Collector, root string) {
			mustCoreWrite(t, filepath.Join(root, "etc/pve/.members"), `{"nodename":"pve1","version":1,"cluster":{"name":"lost-cluster","nodes":2,"quorate":1}}`)
		}},
		{name: "multiple_nodes", wantMissing: 2, mutate: func(t *testing.T, c *Collector, root string) {
			if err := os.MkdirAll(filepath.Join(root, "etc/pve/nodes/pve2"), 0755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "forced_local", wantMissing: 2, mutate: func(t *testing.T, c *Collector, root string) {
			mustCoreWrite(t, filepath.Join(root, "proc/123/cmdline"), "/usr/bin/pmxcfs\x00-l\x00")
		}},
		{name: "permission_error", wantMissing: 2, mutate: func(t *testing.T, c *Collector, root string) {
			c.deps.Stat = func(path string) (os.FileInfo, error) {
				if path == c.effectiveCorosyncConfigPath() {
					return nil, os.ErrPermission
				}
				return os.Stat(path)
			}
		}},
		{name: "failed_corosync", wantMissing: 2, mutate: func(t *testing.T, c *Collector, root string) {
			base := c.deps.RunCommand
			c.deps.RunCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "systemctl" && strings.Contains(strings.Join(args, " "), "corosync") {
					return []byte("ActiveState=failed\nSubState=failed\nMainPID=0\n"), nil
				}
				return base(ctx, name, args...)
			}
		}},
		{name: "service_query_error", wantMissing: 2, mutate: func(t *testing.T, c *Collector, root string) {
			base := c.deps.RunCommand
			c.deps.RunCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "systemctl" {
					return nil, os.ErrPermission
				}
				return base(ctx, name, args...)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, root := standaloneEvidenceCollector(t)
			if tc.mutate != nil {
				tc.mutate(t, c, root)
			}
			clustered, detectErr := c.isClusteredPVE(context.Background())
			if clustered {
				t.Fatal("fixture must not be classified as a configured cluster")
			}
			if tc.wantMissing > 0 && detectErr == nil {
				t.Error("unknown evidence silently became standalone")
			}
			if tc.wantMissing == 0 && detectErr != nil {
				t.Fatalf("healthy standalone rejected: %v", detectErr)
			}
			state := newCollectionState(c)
			if err := newPVEValidationBricks()[0].Run(context.Background(), state); err != nil {
				t.Fatal(err)
			}
			warningsBeforeManifest := c.logger.WarningCount()
			c.populatePVEManifest()
			if got := c.GetStats().FilesNotFound; got != tc.wantMissing {
				t.Fatalf("missing = %d, want %d", got, tc.wantMissing)
			}
			if got := c.logger.WarningCount() - warningsBeforeManifest; got != tc.wantMissing {
				t.Fatalf("manifest warnings = %d, want %d", got, tc.wantMissing)
			}
			for _, key := range []string{"etc/pve/corosync.conf", "etc/corosync/authkey"} {
				if c.pveManifest[key].Status != StatusNotFound {
					t.Fatalf("%s manifest must preserve raw absence: %+v", key, c.pveManifest[key])
				}
			}
		})
	}
}

func standaloneEvidenceCollector(t *testing.T) (*Collector, string) {
	t.Helper()
	root := t.TempDir()
	mustCoreWrite(t, filepath.Join(root, "etc/pve/.members"), `{"nodename":"pve1","version":0}`)
	mustCoreWrite(t, filepath.Join(root, "var/lib/pve-cluster/config.db"), "db")
	mustCoreWrite(t, filepath.Join(root, "proc/self/mountinfo"), "23 1 0:12 / "+filepath.Join(root, "etc/pve")+" rw,relatime - fuse /dev/fuse rw,user_id=0,group_id=0,default_permissions,allow_other\n")
	mustCoreWrite(t, filepath.Join(root, "proc/123/cmdline"), "/usr/bin/pmxcfs\x00")
	if err := os.MkdirAll(filepath.Join(root, "etc/pve/nodes/pve1"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := &CollectorConfig{PVEConfigPath: filepath.Join(root, "etc/pve"), PVEClusterPath: filepath.Join(root, "var/lib/pve-cluster"), BackupClusterConfig: true}
	c := NewCollectorWithDeps(newTestLogger(), cfg, t.TempDir(), "pve", true, CollectorDeps{
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
			joined := strings.Join(args, " ")
			if name == "pvecm" {
				return []byte("Error: Corosync config '/etc/pve/corosync.conf' does not exist - is this node part of a cluster?\n"), errors.New("exit status 2")
			}
			if name == "systemctl" {
				if strings.Contains(joined, "corosync") {
					return []byte("ActiveState=inactive\nSubState=dead\nMainPID=0\n"), nil
				}
				if strings.Contains(joined, "pve-cluster") {
					return []byte("ActiveState=active\nSubState=running\nMainPID=123\n"), nil
				}
			}
			return nil, fmt.Errorf("unexpected command %s %s", name, joined)
		},
	})
	return c, root
}

func mustCoreWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPVEStandaloneRejectsChangingOrInaccessibleEvidence(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *Collector, string)
	}{
		{"prefixed_namespace", func(t *testing.T, c *Collector, root string) {
			c.config.SystemRootPrefix = root
			c.config.PVEConfigPath = "/etc/pve"
			c.config.PVEClusterPath = "/var/lib/pve-cluster"
		}},
		{"authkey_remains", func(t *testing.T, c *Collector, root string) {
			mustCoreWrite(t, filepath.Join(root, "etc/corosync/authkey"), "key")
		}},
		{"missing_members_version", func(t *testing.T, c *Collector, root string) {
			mustCoreWrite(t, filepath.Join(root, "etc/pve/.members"), `{"nodename":"pve1"}`)
		}},
		{"single_runtime_member", func(t *testing.T, c *Collector, root string) {
			mustCoreWrite(t, filepath.Join(root, "etc/pve/.members"), `{"nodename":"pve1","version":0,"nodelist":{"pve1":{"id":1,"online":1,"ip":"192.0.2.1"}}}`)
		}},
		{"members_access_error", func(t *testing.T, c *Collector, root string) {
			base := c.deps.ReadFile
			c.deps.ReadFile = func(path string) ([]byte, error) {
				if filepath.Base(path) == ".members" {
					return nil, os.ErrPermission
				}
				return base(path)
			}
		}},
		{"process_access_error", func(t *testing.T, c *Collector, root string) {
			base := c.deps.ReadFile
			c.deps.ReadFile = func(path string) ([]byte, error) {
				if filepath.Base(path) == "cmdline" {
					return nil, os.ErrPermission
				}
				return base(path)
			}
		}},
		{"mount_access_error", func(t *testing.T, c *Collector, root string) {
			base := c.deps.ReadFile
			c.deps.ReadFile = func(path string) ([]byte, error) {
				if filepath.Base(path) == "mountinfo" {
					return nil, os.ErrPermission
				}
				return base(path)
			}
		}},
		{"pmxcfs_down", func(t *testing.T, c *Collector, root string) {
			base := c.deps.RunCommand
			c.deps.RunCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "systemctl" && strings.Contains(strings.Join(args, " "), "pve-cluster") {
					return []byte("ActiveState=inactive\nSubState=dead\nMainPID=0\n"), nil
				}
				return base(ctx, name, args...)
			}
		}},
		{"corosync_activates", func(t *testing.T, c *Collector, root string) {
			base := c.deps.RunCommand
			calls := 0
			c.deps.RunCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "systemctl" && strings.Contains(strings.Join(args, " "), "corosync") {
					calls++
					if calls%2 == 0 {
						return []byte("ActiveState=active\nSubState=running\nMainPID=456\n"), nil
					}
				}
				return base(ctx, name, args...)
			}
		}},
		{"members_change", func(t *testing.T, c *Collector, root string) {
			base := c.deps.ReadFile
			calls := 0
			c.deps.ReadFile = func(path string) ([]byte, error) {
				if filepath.Base(path) == ".members" {
					calls++
					if calls%2 == 0 {
						return []byte(`{"nodename":"pve1","version":1}`), nil
					}
				}
				return base(path)
			}
		}},
		{"config_appears", func(t *testing.T, c *Collector, root string) {
			base := c.deps.Stat
			calls := 0
			c.deps.Stat = func(path string) (os.FileInfo, error) {
				if path == c.effectiveCorosyncConfigPath() {
					calls++
					if calls%2 == 0 {
						return os.Stat(filepath.Join(root, "var/lib/pve-cluster/config.db"))
					}
				}
				return base(path)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, root := standaloneEvidenceCollector(t)
			tc.mutate(t, c, root)
			clustered, err := c.isClusteredPVE(context.Background())
			if clustered || err == nil {
				t.Fatalf("uncertain topology became configured or standalone: clustered=%v err=%v", clustered, err)
			}
			if strings.HasSuffix(tc.name, "access_error") && !errors.Is(err, os.ErrPermission) {
				t.Fatalf("lost underlying access error: %v", err)
			}
			if tc.name == "prefixed_namespace" && !errors.Is(err, errors.ErrUnsupported) {
				t.Fatalf("lost namespace uncertainty: %v", err)
			}
		})
	}
}

func TestPVECoreMissingDatabaseStillWarnsOnStandalone(t *testing.T) {
	for _, clusterEnabled := range []bool{false, true} {
		t.Run(fmt.Sprint(clusterEnabled), func(t *testing.T) {
			c, root := standaloneEvidenceCollector(t)
			c.config.BackupClusterConfig = clusterEnabled
			if err := os.Remove(filepath.Join(root, "var/lib/pve-cluster/config.db")); err != nil {
				t.Fatal(err)
			}
			if err := newPVEValidationBricks()[0].Run(context.Background(), newCollectionState(c)); err != nil {
				t.Fatal(err)
			}
			c.populatePVEManifest()
			if c.GetStats().FilesNotFound != 1 || c.logger.WarningCount() != 1 {
				t.Fatalf("core database absence was hidden: stats=%+v warnings=%d", c.GetStats(), c.logger.WarningCount())
			}
			if c.pveManifest["var/lib/pve-cluster/config.db"].Status != StatusNotFound {
				t.Fatalf("database manifest: %+v", c.pveManifest["var/lib/pve-cluster/config.db"])
			}
		})
	}
}

func TestPVECoreDBRawFallbackRemainsIndependentAndExcluded(t *testing.T) {
	for _, excluded := range []bool{false, true} {
		t.Run(fmt.Sprint(excluded), func(t *testing.T) {
			c, dir, log := configDBCollector(t, CollectorDeps{LookPath: func(string) (string, error) { return "", os.ErrNotExist }})
			c.config.BackupClusterConfig = false
			if excluded {
				c.config.ExcludePatterns = []string{"config.db*"}
			}
			if err := c.collectPVEClusterSnapshot(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(c.targetPathFor(filepath.Join(dir, "config.db")))
			if excluded {
				if !errors.Is(err, os.ErrNotExist) || c.logger.WarningCount() != 0 {
					t.Fatalf("excluded fallback captured or warned: %q %v %s", data, err, log.String())
				}
			} else if err != nil || string(data) != "RAW-BASE-BYTES" || c.logger.WarningCount() != 1 {
				t.Fatalf("raw fallback missing or reported as consistent: %q %v %s", data, err, log.String())
			}
		})
	}
}

func TestPVEStandaloneRejectsDanglingCorosyncConfiguration(t *testing.T) {
	c, _ := standaloneEvidenceCollector(t)
	if err := os.Symlink("missing-corosync.conf", c.effectiveCorosyncConfigPath()); err != nil {
		t.Fatal(err)
	}
	clustered, err := c.isClusteredPVE(context.Background())
	if !clustered && err == nil {
		t.Fatal("dangling Corosync configuration was treated as healthy standalone")
	}
}

func TestPVECoreFailedSnapshotAndFallbackNeverPublishPartialDatabase(t *testing.T) {
	c, dir, _ := configDBCollector(t, CollectorDeps{
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		RunCommand: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			dest := strings.TrimSuffix(strings.TrimPrefix(args[len(args)-1], `.backup "`), `"`)
			if err := os.WriteFile(dest, []byte("PARTIAL-SQLITE"), 0600); err != nil {
				return nil, err
			}
			return nil, errors.New("snapshot interrupted")
		},
	})
	src := filepath.Join(dir, "config.db")
	oldOpen := osOpen
	osOpen = func(path string) (*os.File, error) {
		if path == src {
			return nil, os.ErrPermission
		}
		return oldOpen(path)
	}
	t.Cleanup(func() { osOpen = oldOpen })
	if err := c.collectPVEClusterSnapshot(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	c.populatePVEManifest()
	if entry := c.pveManifest["var/lib/pve-cluster/config.db"]; entry.Status != StatusFailed {
		t.Fatalf("failed capture reported as %s: %+v", entry.Status, entry)
	}
	if _, err := os.Stat(c.targetPathFor(src)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial database published: %v", err)
	}
}

func TestPVEStandaloneRejectsUnexpectedEmptyMembership(t *testing.T) {
	for _, value := range []string{"{}", "null"} {
		t.Run(value, func(t *testing.T) {
			c, root := standaloneEvidenceCollector(t)
			mustCoreWrite(t, filepath.Join(root, "etc/pve/.members"), `{"nodename":"pve1","version":0,"nodelist":`+value+`}`)
			clustered, err := c.isClusteredPVE(context.Background())
			if !clustered && err == nil {
				t.Fatal("unexpected runtime membership was classified as healthy standalone")
			}
		})
	}
}
