package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

type fakeStorageBackend struct {
	name     string
	location storage.BackupLocation
	enabled  bool
	critical bool

	fsInfo *storage.FilesystemInfo
	fsErr  error

	stats    *storage.StorageStats
	statsErr error

	list    []*types.BackupMetadata
	listErr error
}

func (f *fakeStorageBackend) Name() string { return f.name }

func (f *fakeStorageBackend) Location() storage.BackupLocation { return f.location }

func (f *fakeStorageBackend) IsEnabled() bool { return f.enabled }

func (f *fakeStorageBackend) IsCritical() bool { return f.critical }

func (f *fakeStorageBackend) DetectFilesystem(ctx context.Context) (*storage.FilesystemInfo, error) {
	return f.fsInfo, f.fsErr
}

func (f *fakeStorageBackend) Store(ctx context.Context, backupFile string, metadata *types.BackupMetadata) error {
	return nil
}

func (f *fakeStorageBackend) List(ctx context.Context) ([]*types.BackupMetadata, error) {
	return f.list, f.listErr
}

func (f *fakeStorageBackend) Delete(ctx context.Context, backupFile string) error { return nil }

func (f *fakeStorageBackend) ApplyRetention(ctx context.Context, cfg storage.RetentionConfig) (int, error) {
	return 0, nil
}

func (f *fakeStorageBackend) VerifyUpload(ctx context.Context, localFile, remoteFile string) (bool, error) {
	return true, nil
}

func (f *fakeStorageBackend) GetStats(ctx context.Context) (*storage.StorageStats, error) {
	return f.stats, f.statsErr
}

func TestExtractRemoteName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{" gdrive ", "gdrive"},
		{"gdrive:path/to/dir", "gdrive"},
		{"remote:", "remote"},
	}

	for _, tt := range tests {
		if got := extractRemoteName(tt.in); got != tt.want {
			t.Fatalf("extractRemoteName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestExtractTokenAndSeparators(t *testing.T) {
	line := `PATH=/usr/bin:/bin; /usr/local/bin/proxsave --dry-run && echo "done"`

	start := bytes.Index([]byte(line), []byte("proxsave"))
	if start < 0 {
		t.Fatalf("failed to locate proxsave in %q", line)
	}
	end := start + len("proxsave")

	token := extractToken(line, start, end)
	if token != "/usr/local/bin/proxsave" {
		t.Fatalf("extractToken() = %q, want %q", token, "/usr/local/bin/proxsave")
	}

	for _, b := range []byte{';', '&', '|', '>', '<', '(', ')'} {
		if !isCommandSeparator(b) {
			t.Fatalf("expected %q to be a command separator", b)
		}
	}
	if isCommandSeparator('a') {
		t.Fatalf("did not expect 'a' to be a command separator")
	}
}

func TestFetchBackupList(t *testing.T) {
	ctx := context.Background()
	backend := &fakeStorageBackend{
		enabled: true,
		list: []*types.BackupMetadata{
			{BackupFile: "one.tar.zst"},
			{BackupFile: "two.tar.zst"},
		},
	}

	got := fetchBackupList(ctx, backend)
	if len(got) != 2 {
		t.Fatalf("fetchBackupList() len=%d, want 2", len(got))
	}
	if got[0].BackupFile != "one.tar.zst" || got[1].BackupFile != "two.tar.zst" {
		t.Fatalf("fetchBackupList() unexpected results: %+v", got)
	}

	backend.listErr = errors.New("boom")
	if got := fetchBackupList(ctx, backend); got != nil {
		t.Fatalf("fetchBackupList() expected nil on error, got %+v", got)
	}
}

func TestDetectFilesystemInfo(t *testing.T) {
	logger, buf := newBufferedTestLogger()
	ctx := context.Background()

	t.Run("disabled backend returns nil", func(t *testing.T) {
		backend := &fakeStorageBackend{enabled: false}
		info, err := detectFilesystemInfo(ctx, backend, "/data", logger)
		if err != nil || info != nil {
			t.Fatalf("detectFilesystemInfo() = (%v,%v), want (nil,nil)", info, err)
		}
	})

	t.Run("non-critical error returns nil", func(t *testing.T) {
		buf.Reset()
		backend := &fakeStorageBackend{
			name:     "secondary",
			location: storage.LocationSecondary,
			enabled:  true,
			critical: false,
			fsErr:    errors.New("detect failed"),
		}
		info, err := detectFilesystemInfo(ctx, backend, "/data", logger)
		if err != nil || info != nil {
			t.Fatalf("detectFilesystemInfo() = (%v,%v), want (nil,nil)", info, err)
		}
		if out := buf.String(); out == "" || !bytes.Contains([]byte(out), []byte("filesystem detection failed")) {
			t.Fatalf("expected debug log about filesystem detection failure, got: %s", out)
		}
	})

	t.Run("critical error is returned", func(t *testing.T) {
		backend := &fakeStorageBackend{
			name:     "primary",
			location: storage.LocationPrimary,
			enabled:  true,
			critical: true,
			fsErr:    errors.New("detect failed"),
		}
		if _, err := detectFilesystemInfo(ctx, backend, "/data", logger); err == nil {
			t.Fatalf("expected error for critical backend")
		}
	})

	t.Run("no-ownership logs differ by location", func(t *testing.T) {
		tests := []struct {
			name     string
			location storage.BackupLocation
			want     string
		}{
			{"cloud uses debug", storage.LocationCloud, "does not support ownership changes (cloud remote)"},
			{"local uses info", storage.LocationPrimary, "does not support ownership changes; chown/chmod will be skipped"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				buf.Reset()
				backend := &fakeStorageBackend{
					name:     "backend",
					location: tt.location,
					enabled:  true,
					fsInfo: &storage.FilesystemInfo{
						Type:              storage.FilesystemFUSE,
						SupportsOwnership: false,
					},
				}
				info, err := detectFilesystemInfo(ctx, backend, "/data", logger)
				if err != nil || info == nil {
					t.Fatalf("detectFilesystemInfo() = (%v,%v), want (info,nil)", info, err)
				}
				if out := buf.String(); !bytes.Contains([]byte(out), []byte(tt.want)) {
					t.Fatalf("expected log %q, got: %s", tt.want, out)
				}
			})
		}
	})
}

func TestStorageFormattingHelpers(t *testing.T) {
	if got := formatStorageLabel("/data", nil); got != "/data [unknown]" {
		t.Fatalf("formatStorageLabel(nil) = %q", got)
	}

	if got := formatStorageLabel("/data", &storage.FilesystemInfo{Type: storage.FilesystemExt4}); got != "/data [ext4]" {
		t.Fatalf("formatStorageLabel(ext4) = %q", got)
	}

	if got := formatDetailedFilesystemLabel("", nil); got != "disabled" {
		t.Fatalf("formatDetailedFilesystemLabel(empty) = %q", got)
	}

	if got := formatDetailedFilesystemLabel("/data", nil); got != "/data -> Filesystem: unknown (detection unavailable)" {
		t.Fatalf("formatDetailedFilesystemLabel(nil) = %q", got)
	}

	info := &storage.FilesystemInfo{
		Type:              storage.FilesystemNFS,
		SupportsOwnership: false,
		IsNetworkFS:       true,
		MountPoint:        "/mnt",
	}
	got := formatDetailedFilesystemLabel("/data", info)
	want := "/data -> Filesystem: nfs (no ownership) [network] [mount: /mnt]"
	if got != want {
		t.Fatalf("formatDetailedFilesystemLabel(nfs) = %q, want %q", got, want)
	}
}

func TestFetchStorageStats(t *testing.T) {
	logger, buf := newBufferedTestLogger()
	ctx := context.Background()

	backend := &fakeStorageBackend{enabled: true, stats: &storage.StorageStats{TotalBackups: 3}}
	if got := fetchStorageStats(ctx, backend, logger, "label"); got == nil || got.TotalBackups != 3 {
		t.Fatalf("fetchStorageStats() = %+v, want TotalBackups=3", got)
	}

	buf.Reset()
	backend.statsErr = errors.New("boom")
	if got := fetchStorageStats(ctx, backend, logger, "label"); got != nil {
		t.Fatalf("fetchStorageStats() expected nil on error, got %+v", got)
	}
	if out := buf.String(); !bytes.Contains([]byte(out), []byte("unable to gather stats")) {
		t.Fatalf("expected debug log about stats error, got: %s", out)
	}
}

func TestFormatStorageInitSummary(t *testing.T) {
	cfgSimple := &config.Config{
		LocalRetentionDays: 7,
		RetentionPolicy:    "simple",
	}
	simple := formatStorageInitSummary("Local", cfgSimple, storage.LocationPrimary, &storage.StorageStats{TotalBackups: 2}, nil)
	if !bytes.Contains([]byte(simple), []byte("Policy: simple")) {
		t.Fatalf("expected simple policy label, got: %s", simple)
	}

	cfgWarn := &config.Config{
		LocalRetentionDays: 7,
		RetentionPolicy:    "simple",
	}
	warnSimple := formatStorageInitSummary("Local", cfgWarn, storage.LocationPrimary, nil, nil)
	if !bytes.Contains([]byte(warnSimple), []byte("⚠ Local initialized with warnings")) {
		t.Fatalf("expected warning summary, got: %s", warnSimple)
	}

	cfgGFS := &config.Config{
		RetentionPolicy:  "gfs",
		RetentionDaily:   1,
		RetentionWeekly:  0,
		RetentionMonthly: 0,
		RetentionYearly:  -1,
	}
	now := time.Now()
	backups := []*types.BackupMetadata{
		{Timestamp: now.Add(-1 * time.Hour)},
		{Timestamp: now.Add(-2 * time.Hour)},
	}
	stats := &storage.StorageStats{TotalBackups: 2}

	summary := formatStorageInitSummary("Local", cfgGFS, storage.LocationPrimary, stats, backups)
	if !bytes.Contains([]byte(summary), []byte("Kept (est.):")) {
		t.Fatalf("expected GFS summary to include retention estimates, got: %s", summary)
	}
}

func TestCleanupAfterRun(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&bytes.Buffer{})

	zero := fmt.Sprintf("/tmp/backup_status_update_%d.lock", time.Now().UnixNano())
	nonZero := fmt.Sprintf("/tmp/backup_status_update_%d_nonzero.lock", time.Now().UnixNano())

	t.Cleanup(func() {
		_ = os.Remove(zero)
		_ = os.Remove(nonZero)
	})

	if err := os.WriteFile(zero, nil, 0o600); err != nil {
		t.Fatalf("WriteFile(zero): %v", err)
	}
	if err := os.WriteFile(nonZero, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(nonZero): %v", err)
	}

	cleanupAfterRun(logger)

	if _, err := os.Stat(zero); !os.IsNotExist(err) {
		t.Fatalf("expected zero-size lock file to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(nonZero); err != nil {
		t.Fatalf("expected non-zero lock file to remain, stat err=%v", err)
	}
}
