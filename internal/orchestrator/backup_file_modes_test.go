package orchestrator

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestRunGoBackupBackupFileModes runs a whole backup to a real primary and secondary
// and pins the mode (and, where a non-root test can, the owner) of every file the run
// leaves there: 0600 by default, 0640 BACKUP_USER:BACKUP_GROUP when
// SET_BACKUP_PERMISSIONS asks for it, already for the run that wrote them.
func TestRunGoBackupBackupFileModes(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping end-to-end backup test in short mode")
	}
	u, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	g, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Skipf("cannot resolve current group: %v", err)
	}

	for _, tc := range []struct {
		name      string
		bundle    bool
		shared    bool
		primary   []string
		secondary []string
	}{
		{"default bundle on", true, false, []string{".bundle.tar"}, []string{".bundle.tar"}},
		{"default bundle off", false, false, []string{"", ".manifest.json", ".metadata", ".sha256"}, []string{"", ".metadata", ".sha256"}},
		{"shared bundle on", true, true, []string{".bundle.tar"}, []string{".bundle.tar"}},
		{"shared bundle off", false, true, []string{"", ".manifest.json", ".metadata", ".sha256"}, []string{"", ".metadata", ".sha256"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := logging.New(types.LogLevelError, false)
			backupDir := t.TempDir()
			secondaryDir := t.TempDir()
			configPath := filepath.Join(t.TempDir(), "backup.env")
			if err := os.WriteFile(configPath, []byte("BACKUP_CONFIG_FILE=true\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{
				ConfigPath:             configPath,
				BackupConfigFile:       true,
				SystemRootPrefix:       t.TempDir(), // isolate collection from the real host
				BackupPath:             backupDir,
				SecondaryEnabled:       true,
				SecondaryPath:          secondaryDir,
				BundleAssociatedFiles:  tc.bundle,
				RetentionPolicy:        "simple",
				LocalRetentionDays:     5,
				SecondaryRetentionDays: 5,
			}
			if tc.shared {
				cfg.SetBackupPermissions = true
				cfg.BackupUser = u.Username
				cfg.BackupGroup = g.Name
			}

			orch := New(logger, false)
			orch.SetBackupConfig(backupDir, t.TempDir(), types.CompressionNone, 0, 0, "standard", nil)
			orch.SetConfig(cfg)
			local, err := storage.NewLocalStorage(cfg, logger, "mode-host")
			if err != nil {
				t.Fatal(err)
			}
			second, err := storage.NewSecondaryStorage(cfg, logger, "mode-host")
			if err != nil {
				t.Fatal(err)
			}
			probe := &createdModesProbe{dir: backupDir}
			orch.RegisterStorageTarget(probe)
			orch.RegisterStorageTarget(NewStorageAdapter(local, logger, cfg))
			orch.RegisterStorageTarget(NewStorageAdapter(second, logger, cfg))

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			stats, err := orch.RunGoBackup(ctx, &environment.EnvironmentInfo{Type: types.ProxmoxUnknown, Version: "unknown"}, "mode-host")
			if err != nil {
				t.Fatalf("RunGoBackup: %v", err)
			}
			base := filepath.Base(stats.ArchivePath)
			if tc.bundle {
				base = base[:len(base)-len(".bundle.tar")]
			}

			// Every file is created root-only, whatever SET_BACKUP_PERMISSIONS says; only
			// the storage step may open it to the group.
			if len(probe.modes) != len(tc.primary) {
				t.Fatalf("files before storage: %v, want %d", probe.modes, len(tc.primary))
			}
			for name, perm := range probe.modes {
				if perm != backup.ArtifactFilePerm {
					t.Errorf("%s created with mode %o, want %o", name, perm, backup.ArtifactFilePerm)
				}
			}

			wantMode, wantUID, wantGID, checkOwner := backup.ArtifactFilePerm, 0, 0, os.Geteuid() == 0
			if tc.shared {
				wantMode, wantUID, wantGID, checkOwner = backup.SharedArtifactFilePerm, os.Getuid(), os.Getgid(), true
			}
			for _, dest := range []struct {
				dir      string
				suffixes []string
			}{{backupDir, tc.primary}, {secondaryDir, tc.secondary}} {
				var want []string
				for _, s := range dest.suffixes {
					want = append(want, base+s)
				}
				sort.Strings(want)
				entries, err := os.ReadDir(dest.dir)
				if err != nil {
					t.Fatal(err)
				}
				var got []string
				for _, e := range entries {
					if e.Type().IsRegular() && e.Name() != "backup.env" {
						got = append(got, e.Name())
					}
				}
				if len(got) != len(want) {
					t.Fatalf("%s: files %v, want %v", dest.dir, got, want)
				}
				for i, name := range got {
					if name != want[i] {
						t.Fatalf("%s: files %v, want %v", dest.dir, got, want)
					}
					info, err := os.Stat(filepath.Join(dest.dir, name))
					if err != nil {
						t.Fatal(err)
					}
					if perm := info.Mode().Perm(); perm != wantMode {
						t.Errorf("%s: mode %o, want %o", name, perm, wantMode)
					}
					if checkOwner {
						st := info.Sys().(*syscall.Stat_t)
						if int(st.Uid) != wantUID || int(st.Gid) != wantGID {
							t.Errorf("%s: owner %d:%d, want %d:%d", name, st.Uid, st.Gid, wantUID, wantGID)
						}
					}
				}
			}
		})
	}
}

// createdModesProbe is a storage target registered ahead of the real ones: it records
// the mode of every file the run left on the primary before any backend touched it.
type createdModesProbe struct {
	dir   string
	modes map[string]os.FileMode
}

func (p *createdModesProbe) Sync(ctx context.Context, stats *BackupStats) error {
	p.modes = map[string]os.FileMode{}
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		p.modes[e.Name()] = info.Mode().Perm()
	}
	return nil
}
