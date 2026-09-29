package backup

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func writeArchivePathFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func archiveFileList(t *testing.T, tempDir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(tempDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(tempDir, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

func runPVEFileBricks(t *testing.T, c *Collector) {
	t.Helper()
	c.deps.LookPath = func(string) (string, error) { return "", fmt.Errorf("not installed") }
	runSelectedBricksForTest(t, context.Background(), c, newPVERecipe(),
		func(s *collectionState) { s.pve.clustered = true },
		brickPVEConfigSnapshot, brickPVEClusterSnapshot, brickPVEFirewallSnapshot, brickPVEVZDumpSnapshot,
		brickPVEVMQEMUConfigs, brickPVEVMLXCConfigs, brickPVECephConfigSnapshot, brickPVEManifestFinalize,
	)
}

var canonicalPVEArchiveFiles = []string{
	"etc/ceph/ceph.conf",
	"etc/corosync/authkey",
	"etc/pve/corosync.conf",
	"etc/pve/firewall/cluster.fw",
	"etc/pve/lxc/101.conf",
	"etc/pve/qemu-server/100.conf",
	"etc/pve/storage.cfg",
	"etc/vzdump.conf",
	"var/lib/pve-cluster/config.db",
}

// Under SYSTEM_ROOT_PREFIX the PVE files are read from the host tree and written at
// their host paths: /etc/pve used to land in the archive as ./<prefix>/etc/pve, where
// no restore category matched it.
func TestPVEArchivePathsUnderSystemRootPrefix(t *testing.T) {
	root := t.TempDir()
	writeArchivePathFixture(t, root, map[string]string{
		"etc/pve/storage.cfg":           "dir: local\n",
		"etc/pve/corosync.conf":         "totem {}\n",
		"etc/pve/qemu-server/100.conf":  "memory: 1\n",
		"etc/pve/lxc/101.conf":          "memory: 1\n",
		"etc/pve/firewall/cluster.fw":   "[OPTIONS]\n",
		"etc/vzdump.conf":               "#\n",
		"etc/corosync/authkey":          "k",
		"etc/ceph/ceph.conf":            "fsid = 1\n",
		"var/lib/pve-cluster/config.db": "db",
	})

	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = root
	cfg.HostBackupMode = true
	cfg.BackupCephConfig = true
	tempDir := t.TempDir()
	c := NewCollector(logging.New(types.LogLevelError, false), cfg, tempDir, types.ProxmoxVE, false)
	runPVEFileBricks(t, c)

	got := archiveFileList(t, tempDir)
	for _, want := range canonicalPVEArchiveFiles {
		if !containsString(got, want) {
			t.Errorf("archive misses %s", want)
		}
	}
	prefixRel := strings.TrimPrefix(filepath.ToSlash(root), "/")
	for _, file := range got {
		if strings.HasPrefix(file, prefixRel+"/") {
			t.Errorf("archive holds %s under the prefix", file)
		}
	}
	for _, key := range []string{"etc/pve/qemu-server", "etc/pve/corosync.conf", "var/lib/pve-cluster/config.db", "etc/corosync/authkey", "etc/vzdump.conf"} {
		if _, ok := c.pveManifest[key]; !ok {
			t.Errorf("PVE manifest misses key %s", key)
		}
	}
}

// Path overrides pick where a file is read, never where it lands: the archive keeps the
// canonical layout the restore categories read.
func TestPVEArchivePathsWithPathOverrides(t *testing.T) {
	base := t.TempDir()
	writeArchivePathFixture(t, base, map[string]string{
		"pve/storage.cfg":          "dir: local\n",
		"pve/qemu-server/100.conf": "memory: 1\n",
		"pve/lxc/101.conf":         "memory: 1\n",
		"pve/firewall/cluster.fw":  "[OPTIONS]\n",
		"cluster/config.db":        "db",
		"alt/corosync.conf":        "totem {}\n",
		"alt/vzdump.conf":          "#\n",
		"alt/ceph/ceph.conf":       "fsid = 1\n",
	})

	cfg := GetDefaultCollectorConfig()
	cfg.PVEConfigPath = filepath.Join(base, "pve")
	cfg.PVEClusterPath = filepath.Join(base, "cluster")
	cfg.CorosyncConfigPath = filepath.Join(base, "alt/corosync.conf")
	cfg.VzdumpConfigPath = filepath.Join(base, "alt/vzdump.conf")
	cfg.CephConfigPath = filepath.Join(base, "alt/ceph")
	cfg.BackupCephConfig = true
	tempDir := t.TempDir()
	c := NewCollector(logging.New(types.LogLevelError, false), cfg, tempDir, types.ProxmoxVE, false)
	runPVEFileBricks(t, c)

	got := archiveFileList(t, tempDir)
	for _, want := range []string{
		"etc/ceph/ceph.conf",
		"etc/pve/corosync.conf",
		"etc/pve/firewall/cluster.fw",
		"etc/pve/lxc/101.conf",
		"etc/pve/qemu-server/100.conf",
		"etc/pve/storage.cfg",
		"etc/vzdump.conf",
		"var/lib/pve-cluster/config.db",
	} {
		if !containsString(got, want) {
			t.Errorf("archive misses %s", want)
		}
	}
	baseRel := strings.TrimPrefix(filepath.ToSlash(base), "/")
	for _, file := range got {
		if strings.HasPrefix(file, baseRel+"/") {
			t.Errorf("archive holds %s at its override path", file)
		}
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
