package orchestrator

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestDetectLegacyPVEPrefix(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		want  string
	}{
		{"appliance archive", []string{"./etc/hostname", "./host/", "./host/etc/pve/", "./host/etc/pve/storage.cfg"}, "host"},
		{"deeper prefix", []string{"./mnt/host/etc/pve/storage.cfg"}, "mnt/host"},
		{"canonical archive", []string{"./etc/pve/storage.cfg", "./etc/hostname"}, ""},
		{"canonical wins over a nested copy", []string{"./root/old/etc/pve/storage.cfg", "./etc/pve/storage.cfg"}, ""},
		{"no PVE at all", []string{"./etc/hostname", "./etc/proxmox-backup/datastore.cfg"}, ""},
		{"two candidates", []string{"./a/etc/pve/x", "./b/etc/pve/y"}, ""},
		{"only PVE roots and their parents under the prefix", []string{"./host", "./host/etc", "./host/var", "./host/var/lib", "./host/etc/pve/storage.cfg", "./host/etc/vzdump.conf", "./host/etc/ceph/ceph.conf", "./host/var/lib/pve-cluster/config.db"}, "host"},
		{"a system tree under the prefix is a copy", []string{"./home/u/x/etc/pve/storage.cfg", "./home/u/x/etc/hostname"}, ""},
		{"an archive marker under the prefix is a copy", []string{"./home/u/x/etc/pve/storage.cfg", "./home/u/x/var/lib/proxsave-info/backup_metadata.txt"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectLegacyPVEPrefix(tc.paths); got != tc.want {
				t.Fatalf("detectLegacyPVEPrefix = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRemapLegacyPVEEntry(t *testing.T) {
	cases := []struct {
		name, want  string
		underPrefix bool
	}{
		{"./host/etc/pve/storage.cfg", "./etc/pve/storage.cfg", false},
		{"./host/etc/pve/", "./etc/pve/", false},
		{"host/var/lib/pve-cluster/config.db", "var/lib/pve-cluster/config.db", false},
		{"./host/etc/corosync/authkey", "./etc/corosync/authkey", false},
		{"./host/etc/vzdump.conf", "./etc/vzdump.conf", false},
		{"./host/etc/ceph/ceph.conf", "./etc/ceph/ceph.conf", false},
		{"./host/", "./host/", true},
		{"./host/etc/", "./host/etc/", true},
		{"./host/var/lib/", "./host/var/lib/", true},
		{"./etc/hostname", "./etc/hostname", false},
		{"./hostile/etc/pve/x", "./hostile/etc/pve/x", false},
	}
	for _, tc := range cases {
		got, under := remapLegacyPVEEntry(tc.name, "host")
		if got != tc.want || under != tc.underPrefix {
			t.Errorf("remapLegacyPVEEntry(%q) = %q, %v; want %q, %v", tc.name, got, under, tc.want, tc.underPrefix)
		}
	}
}

type legacyTarEntry struct {
	name    string
	dir     bool
	content string
}

func writeLegacyPrefixArchive(t *testing.T, path string, entries []legacyTarEntry) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o640, ModTime: time.Now(), Typeflag: tar.TypeReg, Size: int64(len(e.content))}
		if e.dir {
			hdr.Typeflag, hdr.Mode, hdr.Size = tar.TypeDir, 0o755, 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if !e.dir {
			if _, err := tw.Write([]byte(e.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

var legacyAppliancePVEEntries = []legacyTarEntry{
	{name: "./etc/", dir: true},
	{name: "./etc/hostname", content: "pve1\n"},
	{name: "./var/lib/proxsave-info/backup_metadata.txt", content: "BACKUP_TYPE=pve\nHOSTNAME=pve1\n"},
	{name: "./host/", dir: true},
	{name: "./host/etc/", dir: true},
	{name: "./host/etc/pve/", dir: true},
	{name: "./host/etc/pve/storage.cfg", content: "dir: local\n"},
	{name: "./host/etc/vzdump.conf", content: "#\n"},
	{name: "./host/etc/corosync/", dir: true},
	{name: "./host/etc/corosync/authkey", content: "k"},
	{name: "./host/var/", dir: true},
	{name: "./host/var/lib/", dir: true},
	{name: "./host/var/lib/pve-cluster/", dir: true},
	{name: "./host/var/lib/pve-cluster/config.db", content: "db"},
}

// An older SYSTEM_ROOT_PREFIX backup holds its PVE files under ./host/: the analysis finds
// the PVE categories there and says so once, and the extraction writes them at their host
// paths without creating a /host tree.
func TestLegacyPrefixArchiveRestoresPVEAtHostPaths(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })
	restoreFS = osFS{}

	archive := filepath.Join(t.TempDir(), "legacy.tar")
	writeLegacyPrefixArchive(t, archive, legacyAppliancePVEEntries)
	t.Cleanup(func() { rememberLegacyPVEPrefix(archive, "") })

	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	categories, _, err := AnalyzeRestoreArchive(archive, logger)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"pve_cluster", "storage_pve", "corosync"} {
		if !hasCategoryID(categories, id) {
			t.Errorf("analysis misses category %s", id)
		}
	}
	remapLine := "PVE configuration - stored under host/ in this backup (taken with SYSTEM_ROOT_PREFIX), restored to its host paths"
	if got := strings.Count(buf.String(), remapLine); got != 1 {
		t.Fatalf("want the remap line once, got %d:\n%s", got, buf.String())
	}

	dest := t.TempDir()
	if err := extractArchiveNative(context.Background(), restoreArchiveOptions{
		archivePath: archive, destRoot: dest, logger: logger, mode: RestoreModeFull,
	}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"etc/pve/storage.cfg", "etc/vzdump.conf", "etc/corosync/authkey", "var/lib/pve-cluster/config.db", "etc/hostname"} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Errorf("full restore misses %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "host")); !os.IsNotExist(err) {
		t.Errorf("full restore created a host/ tree: %v", err)
	}

	selDest := t.TempDir()
	if err := extractArchiveNative(context.Background(), restoreArchiveOptions{
		archivePath: archive, destRoot: selDest, logger: logger, mode: RestoreModeCustom,
		categories: []Category{*GetCategoryByID("pve_cluster", categories)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(selDest, "var/lib/pve-cluster/config.db")); err != nil {
		t.Errorf("selective restore misses config.db: %v", err)
	}
}

// A canonical archive is untouched: no remap, no line.
func TestCanonicalArchiveIsNotRemapped(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })
	restoreFS = osFS{}

	archive := filepath.Join(t.TempDir(), "canonical.tar")
	writeLegacyPrefixArchive(t, archive, []legacyTarEntry{
		{name: "./etc/pve/storage.cfg", content: "dir: local\n"},
		{name: "./host/etc/other.conf", content: "x"},
	})
	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	if _, _, err := AnalyzeRestoreArchive(archive, logger); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "restored to its host paths") || legacyPVEPrefixFor(archive) != "" {
		t.Fatalf("canonical archive was remapped:\n%s", buf.String())
	}
}

// Only the PVE recipe wrote PVE files under the prefix, and nothing else, so the remap
// needs the archive's own metadata to say it covered PVE and a prefix that holds PVE files
// only. An extracted PVE archive under /home, which every backup collects whole, has one
// X/etc/pve too, next to X/etc/hostname and X/var/lib/proxsave-info: it is a copy and
// stays where it is, whatever the metadata says. So does any single X/etc/pve of an
// archive without readable metadata.
func TestLegacyPrefixNeedsPVEMetadata(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })
	restoreFS = osFS{}

	legacyShaped := []legacyTarEntry{
		{name: "./etc/hostname", content: "pve1\n"},
		{name: "./host/etc/pve/storage.cfg", content: "dir: local\n"},
		{name: "./host/etc/corosync/authkey", content: "k"},
		{name: "./host/var/lib/pve-cluster/config.db", content: "db"},
	}
	homeCopy := []legacyTarEntry{
		{name: "./etc/proxmox-backup/datastore.cfg", content: "datastore: store1\n"},
		{name: "./home/admin/pve1-extract/etc/hostname", content: "pve1\n"},
		{name: "./home/admin/pve1-extract/etc/pve/storage.cfg", content: "dir: local\n"},
		{name: "./home/admin/pve1-extract/etc/corosync/authkey", content: "k"},
		{name: "./home/admin/pve1-extract/var/lib/pve-cluster/config.db", content: "db"},
		{name: "./home/admin/pve1-extract/var/lib/proxsave-info/backup_metadata.txt", content: "BACKUP_TYPE=pve\n"},
	}
	for _, tc := range []struct {
		name     string
		entries  []legacyTarEntry
		metadata string
		want     string
	}{
		{"pbs backup with a PVE copy under /home", homeCopy, "BACKUP_TYPE=pbs\n", ""},
		{"pve backup with a PVE copy under /home", homeCopy, "BACKUP_TYPE=pve\n", ""},
		{"legacy layout without metadata", legacyShaped, "", ""},
		{"legacy layout of a pbs backup", legacyShaped, "BACKUP_TYPE=pbs\n", ""},
		{"legacy layout of a pve backup", legacyShaped, "BACKUP_TYPE=pve\n", "host"},
		{"legacy layout of a dual backup by its targets", legacyShaped, "BACKUP_TARGETS=pve,pbs\n", "host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := append([]legacyTarEntry{}, tc.entries...)
			if tc.metadata != "" {
				entries = append(entries, legacyTarEntry{name: "./var/lib/proxsave-info/backup_metadata.txt", content: tc.metadata})
			}
			archive := filepath.Join(t.TempDir(), "backup.tar")
			writeLegacyPrefixArchive(t, archive, entries)
			t.Cleanup(func() { rememberLegacyPVEPrefix(archive, "") })

			logger := logging.New(types.LogLevelInfo, false)
			var buf bytes.Buffer
			logger.SetOutput(&buf)
			categories, _, err := AnalyzeRestoreArchive(archive, logger)
			if err != nil {
				t.Fatal(err)
			}
			if got := legacyPVEPrefixFor(archive); got != tc.want {
				t.Fatalf("legacy prefix = %q, want %q\n%s", got, tc.want, buf.String())
			}
			remapped := strings.Contains(buf.String(), "restored to its host paths")
			if remapped != (tc.want != "") {
				t.Fatalf("remap line logged = %v, want %v:\n%s", remapped, tc.want != "", buf.String())
			}
			if tc.want == "" && (hasCategoryID(categories, "pve_cluster") || hasCategoryID(categories, "corosync")) {
				t.Fatalf("the prefixed copy was analysed as PVE configuration: %v", categories)
			}
		})
	}
}

// A deduplicated PVE file of an older SYSTEM_ROOT_PREFIX backup is extracted at its host
// path; it is rebuilt from its canonical, which the archive still stores under the prefix.
func TestLegacyPrefixDedupDuplicateIsRebuiltAtHostPath(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })
	restoreFS = osFS{}

	root := t.TempDir()
	archive := writeTarArchiveForTest(t, root, map[string]string{"host/etc/pve/qemu-server/100.conf": "memory: 1\n"})
	rememberLegacyPVEPrefix(archive, "host")
	t.Cleanup(func() { rememberLegacyPVEPrefix(archive, "") })

	destRoot := t.TempDir()
	dir := filepath.Join(destRoot, "etc/pve/qemu-server")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("100.conf", filepath.Join(dir, "101.conf")); err != nil {
		t.Fatal(err)
	}
	writeDedupManifestForTest(t, destRoot, []backup.DedupManifestEntry{{Path: "host/etc/pve/qemu-server/101.conf", Mode: 0o640}})

	extracted := map[string]bool{"etc/pve/qemu-server/101.conf": true}
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logging.New(types.LogLevelError, false), true, extracted); err != nil {
		t.Fatalf("materialize dedup symlinks: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "101.conf"))
	if err != nil || string(data) != "memory: 1\n" {
		t.Fatalf("101.conf = %q, %v; want the canonical content", data, err)
	}
	if info, err := os.Lstat(filepath.Join(dir, "101.conf")); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("101.conf is still a link: %v", err)
	}
}
