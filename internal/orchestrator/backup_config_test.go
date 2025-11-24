package orchestrator

import (
	"reflect"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/tis24dev/proxmox-backup/internal/backup"
	"github.com/tis24dev/proxmox-backup/internal/config"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

func TestBuildArchiverConfig(t *testing.T) {
	recipient, err := age.NewScryptRecipient("passphrase")
	if err != nil {
		t.Fatalf("NewScryptRecipient: %v", err)
	}
	recipients := []age.Recipient{recipient}
	cfg := BuildArchiverConfig(types.CompressionZstd, 3, 4, "fast", true, true, recipients)

	expected := &backup.ArchiverConfig{
		Compression:        types.CompressionZstd,
		CompressionLevel:   3,
		CompressionThreads: 4,
		CompressionMode:    "fast",
		DryRun:             true,
		EncryptArchive:     true,
		AgeRecipients:      recipients,
	}

	if !reflect.DeepEqual(cfg, expected) {
		t.Fatalf("BuildArchiverConfig = %#v, want %#v", cfg, expected)
	}
}

func TestInitializeBackupStats(t *testing.T) {
	start := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	cfg := &config.Config{
		SecondaryEnabled:    true,
		CloudEnabled:        true,
		SecondaryPath:       "/mnt/secondary",
		CloudRemote:         "remote:/cloud",
		MaxLocalBackups:     5,
		MaxSecondaryBackups: 3,
		MaxCloudBackups:     2,
		TelegramEnabled:     true,
		TelegramBotType:     "centralized",
		BackupPath:          "/var/backups",
	}

	stats := InitializeBackupStats(
		"node1",
		types.ProxmoxVE,
		"7.0",
		"1.2.3",
		start,
		cfg,
		types.CompressionGzip,
		"standard",
		6,
		8,
		"",
		"server-123",
		"aa:bb:cc",
	)

	if stats.Hostname != "node1" || stats.ProxmoxType != types.ProxmoxVE {
		t.Fatalf("unexpected host/type: %+v", stats)
	}
	if stats.Compression != types.CompressionGzip || stats.CompressionLevel != 6 || stats.CompressionThreads != 8 {
		t.Fatalf("compression fields not set: %+v", stats)
	}
	if stats.LocalPath != "/var/backups" {
		t.Fatalf("LocalPath fallback not applied, got %q", stats.LocalPath)
	}
	if stats.SecondaryStatus != "ok" || stats.CloudStatus != "ok" {
		t.Fatalf("storage statuses not set: secondary=%s cloud=%s", stats.SecondaryStatus, stats.CloudStatus)
	}
	if stats.TelegramStatus != "centralized" {
		t.Fatalf("telegram status = %s, want centralized", stats.TelegramStatus)
	}
	if stats.ExitCode != types.ExitSuccess.Int() {
		t.Fatalf("exit code = %d, want success", stats.ExitCode)
	}
}

func TestInitializeBackupStats_NoConfig(t *testing.T) {
	start := time.Now()
	stats := InitializeBackupStats(
		"host",
		types.ProxmoxUnknown,
		"",
		"v1",
		start,
		nil,
		types.CompressionNone,
		"standard",
		0,
		0,
		"/backup",
		"",
		"",
	)

	if stats.LocalPath != "/backup" {
		t.Fatalf("LocalPath = %s, want /backup", stats.LocalPath)
	}
	if stats.SecondaryStatus != "disabled" || stats.CloudStatus != "disabled" {
		t.Fatalf("expected storage disabled, got secondary=%s cloud=%s", stats.SecondaryStatus, stats.CloudStatus)
	}
	if stats.TelegramStatus != "disabled" {
		t.Fatalf("expected telegram disabled, got %s", stats.TelegramStatus)
	}
}
