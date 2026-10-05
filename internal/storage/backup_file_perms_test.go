package storage

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

// currentOwnerNames returns this process's user and primary group by name, the only
// pair a non-root test can hand files to. It skips when the host cannot name them.
func currentOwnerNames(t *testing.T) (string, string) {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	g, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Skipf("cannot resolve current group: %v", err)
	}
	return u.Username, g.Name
}

// writeBackupSet writes the files one run leaves in dir before Store, all at 0644 so
// the test proves Store sets the mode instead of inheriting it. bundle=true leaves
// only the bundle, as the run does after bundling.
func writeBackupSet(t *testing.T, dir string, bundle bool) (string, []string) {
	t.Helper()
	base := filepath.Join(dir, "pve1-backup-20261002-101010.tar.xz")
	files := []string{base, base + ".sha256", base + ".metadata", base + ".manifest.json"}
	storeFile := base
	if bundle {
		files = []string{base + ".bundle.tar"}
		storeFile = files[0]
	}
	for _, f := range files {
		if err := os.WriteFile(f, []byte("data"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
		if err := os.Chmod(f, 0o644); err != nil {
			t.Fatalf("chmod %s: %v", f, err)
		}
	}
	return storeFile, files
}

func assertOwnerAndMode(t *testing.T, path string, mode os.FileMode, uid, gid int, checkOwner bool) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != mode {
		t.Errorf("%s: mode %o, want %o", filepath.Base(path), got, mode)
	}
	if !checkOwner {
		return
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s: no Stat_t", path)
	}
	if int(st.Uid) != uid || int(st.Gid) != gid {
		t.Errorf("%s: owner %d:%d, want %d:%d", filepath.Base(path), st.Uid, st.Gid, uid, gid)
	}
}

func TestIsBackupSetFile(t *testing.T) {
	cases := map[string]bool{
		"pve1-backup-20261002-101010.tar.xz":                    true,
		"pve1-backup-20261002-101010.tar.xz.sha256":             true,
		"pve1-backup-20261002-101010.tar.xz.metadata":           true,
		"pve1-backup-20261002-101010.tar.xz.metadata.sha256":    true,
		"pve1-backup-20261002-101010.tar.xz.manifest.json":      true,
		"pve1-backup-20261002-101010.tar.xz.bundle.tar":         true,
		"pve1-backup-20261002-101010.tar":                       true,
		"proxmox-backup-pve1-20200101.tar.gz":                   true,
		"pve1-backup-20261002-101010.tar.xz.partial":            false,
		"pve1-backup-20261002-101010.tar.xz.bundle.tar.tmp-123": false,
		".tmp-pve1-backup-20261002-101010.tar.xz-456":           false,
		"backup-pve1-20261002-101010.log":                       false,
		"backup-stats-20261002-101010.json":                     false,
		"notes.txt":                                             false,
	}
	for name, want := range cases {
		if got := IsBackupSetFile(name); got != want {
			t.Errorf("IsBackupSetFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestBackupFileOwnershipNeedsBothNamesResolved(t *testing.T) {
	userName, groupName := currentOwnerNames(t)
	logger := newTestLogger()
	cases := []struct {
		name string
		cfg  *config.Config
		want os.FileMode
	}{
		{"nil config", nil, backup.ArtifactFilePerm},
		{"flag off", &config.Config{BackupUser: userName, BackupGroup: groupName}, backup.ArtifactFilePerm},
		{"no user", &config.Config{SetBackupPermissions: true, BackupGroup: groupName}, backup.ArtifactFilePerm},
		{"no group", &config.Config{SetBackupPermissions: true, BackupUser: userName}, backup.ArtifactFilePerm},
		{"unknown user", &config.Config{SetBackupPermissions: true, BackupUser: "proxsave-no-such-user-f3", BackupGroup: groupName}, backup.ArtifactFilePerm},
		{"both set", &config.Config{SetBackupPermissions: true, BackupUser: userName, BackupGroup: groupName}, backup.SharedArtifactFilePerm},
	}
	for _, tc := range cases {
		uid, gid, mode := backupFileOwnership(tc.cfg, logger)
		if mode != tc.want {
			t.Errorf("%s: mode %o, want %o", tc.name, mode, tc.want)
		}
		wantUID, wantGID := 0, 0
		if tc.want == backup.SharedArtifactFilePerm {
			wantUID, wantGID = os.Getuid(), os.Getgid()
		}
		if uid != wantUID || gid != wantGID {
			t.Errorf("%s: owner %d:%d, want %d:%d", tc.name, uid, gid, wantUID, wantGID)
		}
	}
}

func TestLocalStorageStoreBackupFileModes(t *testing.T) {
	userName, groupName := currentOwnerNames(t)
	for _, tc := range []struct {
		name   string
		bundle bool
		shared bool
	}{
		{"default raw files", false, false},
		{"default bundle", true, false},
		{"shared raw files", false, true},
		{"shared bundle", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			storeFile, files := writeBackupSet(t, dir, tc.bundle)
			cfg := &config.Config{BackupPath: dir, BundleAssociatedFiles: tc.bundle}
			if tc.shared {
				cfg.SetBackupPermissions = true
				cfg.BackupUser = userName
				cfg.BackupGroup = groupName
			}
			local, err := NewLocalStorage(cfg, newTestLogger(), "")
			if err != nil {
				t.Fatalf("NewLocalStorage: %v", err)
			}
			if err := local.Store(context.Background(), storeFile, &types.BackupMetadata{}); err != nil {
				t.Fatalf("Store: %v", err)
			}
			for _, f := range files {
				if tc.shared {
					assertOwnerAndMode(t, f, backup.SharedArtifactFilePerm, os.Getuid(), os.Getgid(), true)
				} else {
					// root:root is only reachable as root; the mode is what a non-root run can pin.
					assertOwnerAndMode(t, f, backup.ArtifactFilePerm, 0, 0, os.Geteuid() == 0)
				}
			}
		})
	}
}

func TestSecondaryStorageStoreBackupFileModes(t *testing.T) {
	userName, groupName := currentOwnerNames(t)
	for _, tc := range []struct {
		name   string
		bundle bool
		shared bool
	}{
		{"default raw files", false, false},
		{"default bundle", true, false},
		{"shared raw files", false, true},
		{"shared bundle", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srcDir := t.TempDir()
			destDir := t.TempDir()
			storeFile, files := writeBackupSet(t, srcDir, tc.bundle)
			cfg := &config.Config{
				SecondaryEnabled:      true,
				SecondaryPath:         destDir,
				BundleAssociatedFiles: tc.bundle,
			}
			if tc.shared {
				cfg.SetBackupPermissions = true
				cfg.BackupUser = userName
				cfg.BackupGroup = groupName
			}
			secondary, err := NewSecondaryStorage(cfg, newTestLogger(), "")
			if err != nil {
				t.Fatalf("NewSecondaryStorage: %v", err)
			}
			// The adapter detects the filesystem before Store; ownership support is what
			// gates the owner and mode step on the secondary.
			secondary.fsInfo = &FilesystemInfo{Path: destDir, Type: FilesystemExt4, SupportsOwnership: true}
			if err := secondary.Store(context.Background(), storeFile, &types.BackupMetadata{}); err != nil {
				t.Fatalf("Store: %v", err)
			}
			want := []string{}
			for _, f := range files {
				if filepath.Ext(f) == ".json" {
					continue // the secondary never receives the manifest; .metadata is its copy
				}
				want = append(want, filepath.Join(destDir, filepath.Base(f)))
			}
			for _, f := range want {
				if tc.shared {
					assertOwnerAndMode(t, f, backup.SharedArtifactFilePerm, os.Getuid(), os.Getgid(), true)
				} else {
					assertOwnerAndMode(t, f, backup.ArtifactFilePerm, 0, 0, os.Geteuid() == 0)
				}
			}
		})
	}
}
