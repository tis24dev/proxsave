package orchestrator

import (
	"context"
	"errors"
	"io"
	"strings"
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

	detectFilesystemFn func(context.Context) (*storage.FilesystemInfo, error)
	storeFn            func(context.Context, string, *types.BackupMetadata) error
	listFn             func(context.Context) ([]*types.BackupMetadata, error)
	applyRetentionFn   func(context.Context, storage.RetentionConfig) (int, error)
	getStatsFn         func(context.Context) (*storage.StorageStats, error)

	calls []string
}

func (f *fakeStorageBackend) record(call string) {
	f.calls = append(f.calls, call)
}

func (f *fakeStorageBackend) Name() string { return f.name }

func (f *fakeStorageBackend) Location() storage.BackupLocation {
	f.record("Location")
	return f.location
}

func (f *fakeStorageBackend) IsEnabled() bool {
	f.record("IsEnabled")
	return f.enabled
}

func (f *fakeStorageBackend) IsCritical() bool {
	f.record("IsCritical")
	return f.critical
}

func (f *fakeStorageBackend) DetectFilesystem(ctx context.Context) (*storage.FilesystemInfo, error) {
	f.record("DetectFilesystem")
	if f.detectFilesystemFn != nil {
		return f.detectFilesystemFn(ctx)
	}
	return &storage.FilesystemInfo{Type: storage.FilesystemUnknown}, nil
}

func (f *fakeStorageBackend) Store(ctx context.Context, backupFile string, metadata *types.BackupMetadata) error {
	f.record("Store")
	if f.storeFn != nil {
		return f.storeFn(ctx, backupFile, metadata)
	}
	return nil
}

func (f *fakeStorageBackend) List(ctx context.Context) ([]*types.BackupMetadata, error) {
	f.record("List")
	if f.listFn != nil {
		return f.listFn(ctx)
	}
	return nil, nil
}

func (f *fakeStorageBackend) Delete(ctx context.Context, backupFile string) error {
	f.record("Delete")
	return nil
}

func (f *fakeStorageBackend) ApplyRetention(ctx context.Context, cfg storage.RetentionConfig) (int, error) {
	f.record("ApplyRetention")
	if f.applyRetentionFn != nil {
		return f.applyRetentionFn(ctx, cfg)
	}
	return 0, nil
}

func (f *fakeStorageBackend) VerifyUpload(ctx context.Context, localFile, remoteFile string) (bool, error) {
	f.record("VerifyUpload")
	return true, nil
}

func (f *fakeStorageBackend) GetStats(ctx context.Context) (*storage.StorageStats, error) {
	f.record("GetStats")
	if f.getStatsFn != nil {
		return f.getStatsFn(ctx)
	}
	return &storage.StorageStats{}, nil
}

func newStorageAdapterTestLogger() *logging.Logger {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	return logger
}

func sampleAdapterStats() *BackupStats {
	return &BackupStats{
		ArchivePath:  "/tmp/archive.tar",
		ArchiveSize:  123,
		StartTime:    time.Unix(1700000000, 0),
		Checksum:     "deadbeef",
		Compression:  types.CompressionZstd,
		ProxmoxType:  types.ProxmoxVE,
		Version:      "1.0.0",
		ClusterMode:  "standalone",
		FilesMissing: 0,
	}
}

func TestStorageAdapter_SetFilesystemInfo(t *testing.T) {
	logger := newStorageAdapterTestLogger()

	backend := &fakeStorageBackend{
		name:     "backend",
		location: storage.LocationPrimary,
		enabled:  true,
	}
	adapter := NewStorageAdapter(backend, logger, &config.Config{})

	initial := &storage.FilesystemInfo{Type: storage.FilesystemExt4}
	adapter.SetFilesystemInfo(initial)
	if adapter.fsInfo != initial {
		t.Fatalf("fsInfo was not set to initial pointer")
	}

	adapter.SetFilesystemInfo(nil)
	if adapter.fsInfo != initial {
		t.Fatalf("fsInfo changed after SetFilesystemInfo(nil)")
	}
}

func TestStorageAdapterSync_DisabledBackendSetsStatus(t *testing.T) {
	logger := newStorageAdapterTestLogger()

	tests := []struct {
		name     string
		location storage.BackupLocation
		check    func(*BackupStats) string
	}{
		{"primary", storage.LocationPrimary, func(stats *BackupStats) string { return stats.LocalStatus }},
		{"secondary", storage.LocationSecondary, func(stats *BackupStats) string { return stats.SecondaryStatus }},
		{"cloud", storage.LocationCloud, func(stats *BackupStats) string { return stats.CloudStatus }},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeStorageBackend{
				name:     "backend",
				location: tt.location,
				enabled:  false,
			}

			adapter := NewStorageAdapter(backend, logger, &config.Config{})
			stats := &BackupStats{}
			if err := adapter.Sync(context.Background(), stats); err != nil {
				t.Fatalf("Sync returned error: %v", err)
			}

			if got := tt.check(stats); got != "disabled" {
				t.Fatalf("status = %q; want disabled", got)
			}

			for _, call := range backend.calls {
				if call == "DetectFilesystem" || call == "Store" || call == "ApplyRetention" || call == "GetStats" {
					t.Fatalf("unexpected call %q for disabled backend (calls=%v)", call, backend.calls)
				}
			}
		})
	}
}

func TestStorageAdapterSync_NonCriticalDetectFilesystemErrorSkipsBackend(t *testing.T) {
	logger := newStorageAdapterTestLogger()

	backend := &fakeStorageBackend{
		name:     "secondary",
		location: storage.LocationSecondary,
		enabled:  true,
		critical: false,
		detectFilesystemFn: func(context.Context) (*storage.FilesystemInfo, error) {
			return nil, errors.New("boom")
		},
		storeFn: func(context.Context, string, *types.BackupMetadata) error {
			t.Fatalf("Store should not be called when filesystem detection fails")
			return nil
		},
	}

	adapter := NewStorageAdapter(backend, logger, nil)
	stats := sampleAdapterStats()

	if err := adapter.Sync(context.Background(), stats); err != nil {
		t.Fatalf("Sync returned error: %v; want nil for non-critical backend", err)
	}
	if got := stats.SecondaryStatus; got != "error" {
		t.Fatalf("SecondaryStatus = %q; want error", got)
	}
}

func TestStorageAdapterSync_CriticalDetectFilesystemErrorReturnsError(t *testing.T) {
	logger := newStorageAdapterTestLogger()

	backend := &fakeStorageBackend{
		name:     "primary",
		location: storage.LocationPrimary,
		enabled:  true,
		critical: true,
		detectFilesystemFn: func(context.Context) (*storage.FilesystemInfo, error) {
			return nil, errors.New("boom")
		},
	}

	adapter := NewStorageAdapter(backend, logger, nil)
	stats := sampleAdapterStats()

	err := adapter.Sync(context.Background(), stats)
	if err == nil {
		t.Fatalf("expected error for critical backend")
	}
	if got := stats.LocalStatus; got != "error" {
		t.Fatalf("LocalStatus = %q; want error", got)
	}
	if !strings.Contains(err.Error(), "CRITICAL") {
		t.Fatalf("error = %q; want CRITICAL marker", err.Error())
	}
}

func TestStorageAdapterSync_NonCriticalStoreErrorFinalizesErrorAndContinues(t *testing.T) {
	logger := newStorageAdapterTestLogger()
	cfg := &config.Config{
		SecondaryRetentionDays: 2,
	}

	backend := &fakeStorageBackend{
		name:     "secondary",
		location: storage.LocationSecondary,
		enabled:  true,
		critical: false,
		detectFilesystemFn: func(context.Context) (*storage.FilesystemInfo, error) {
			return &storage.FilesystemInfo{Type: storage.FilesystemExt4}, nil
		},
		storeFn: func(ctx context.Context, backupFile string, metadata *types.BackupMetadata) error {
			if backupFile != "/tmp/archive.tar" {
				t.Fatalf("Store backupFile = %q; want %q", backupFile, "/tmp/archive.tar")
			}
			if metadata == nil || metadata.BackupFile != backupFile || metadata.Checksum != "deadbeef" {
				t.Fatalf("unexpected metadata: %#v", metadata)
			}
			return errors.New("store fail")
		},
		applyRetentionFn: func(ctx context.Context, cfg storage.RetentionConfig) (int, error) {
			if cfg.Policy != "simple" || cfg.MaxBackups != 2 {
				t.Fatalf("retention config = %#v; want simple keep=2", cfg)
			}
			return 0, nil
		},
		listFn: func(context.Context) ([]*types.BackupMetadata, error) {
			return []*types.BackupMetadata{{BackupFile: "one"}}, nil
		},
		getStatsFn: func(context.Context) (*storage.StorageStats, error) {
			return &storage.StorageStats{
				TotalBackups:   3,
				AvailableSpace: 10,
				TotalSpace:     20,
			}, nil
		},
	}

	adapter := NewStorageAdapter(backend, logger, cfg)
	adapter.SetInitialStats(&storage.StorageStats{TotalBackups: 1})

	stats := sampleAdapterStats()
	stats.SecondaryEnabled = false

	if err := adapter.Sync(context.Background(), stats); err != nil {
		t.Fatalf("Sync returned error: %v; want nil for non-critical store error", err)
	}
	if got := stats.SecondaryStatus; got != "error" {
		t.Fatalf("SecondaryStatus = %q; want error", got)
	}
	if !stats.SecondaryEnabled {
		t.Fatalf("expected SecondaryEnabled to be set true")
	}
	if stats.SecondaryBackups != 3 {
		t.Fatalf("SecondaryBackups = %d; want 3", stats.SecondaryBackups)
	}
	if stats.SecondaryRetentionPolicy != "simple" {
		t.Fatalf("SecondaryRetentionPolicy = %q; want simple", stats.SecondaryRetentionPolicy)
	}
}

func TestStorageAdapterSync_NonCriticalRetentionErrorFinalizesWarning(t *testing.T) {
	logger := newStorageAdapterTestLogger()
	cfg := &config.Config{
		LocalRetentionDays: 1,
	}

	backend := &fakeStorageBackend{
		name:     "primary",
		location: storage.LocationPrimary,
		enabled:  true,
		critical: false,
		detectFilesystemFn: func(context.Context) (*storage.FilesystemInfo, error) {
			return &storage.FilesystemInfo{Type: storage.FilesystemExt4}, nil
		},
		storeFn: func(context.Context, string, *types.BackupMetadata) error { return nil },
		applyRetentionFn: func(context.Context, storage.RetentionConfig) (int, error) {
			return 0, errors.New("retention fail")
		},
		getStatsFn: func(context.Context) (*storage.StorageStats, error) {
			return &storage.StorageStats{
				TotalBackups:   1,
				AvailableSpace: 5,
				TotalSpace:     10,
			}, nil
		},
	}

	adapter := NewStorageAdapter(backend, logger, cfg)
	stats := sampleAdapterStats()
	stats.LocalStatus = ""

	if err := adapter.Sync(context.Background(), stats); err != nil {
		t.Fatalf("Sync returned error: %v; want nil for non-critical retention error", err)
	}
	if got := stats.LocalStatus; got != "warning" {
		t.Fatalf("LocalStatus = %q; want warning", got)
	}
	if stats.LocalRetentionPolicy != "simple" {
		t.Fatalf("LocalRetentionPolicy = %q; want simple", stats.LocalRetentionPolicy)
	}
}
