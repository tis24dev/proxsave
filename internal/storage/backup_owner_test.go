package storage

import (
	"context"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestBackupOwnerHost(t *testing.T) {
	tests := []struct {
		name string
		meta types.BackupMetadata
		want string
	}{
		{
			name: "the manifest is authoritative",
			meta: types.BackupMetadata{BackupFile: "server2-backup-20250102-100000.tar.zst", Hostname: "server1"},
			want: "server1",
		},
		{
			name: "no manifest falls back to the filename token",
			meta: types.BackupMetadata{BackupFile: "server1-backup-20250102-100000.tar.zst"},
			want: "server1",
		},
		{
			name: "the fallback reads the basename, not the path",
			meta: types.BackupMetadata{BackupFile: "/mnt/nas/server1-backup-20250102-100000.tar.zst"},
			want: "server1",
		},
		{
			// "proxmox" is the product name, not a host: attributing it would stop
			// every other machine from rotating its own legacy archives.
			name: "a legacy name carries no host token",
			meta: types.BackupMetadata{BackupFile: "proxmox-backup-20250102-100000.tar.gz"},
			want: "",
		},
		{
			name: "an unparsable name yields nothing",
			meta: types.BackupMetadata{BackupFile: "something-else.tar.gz"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backupOwnerHost(&tt.meta); got != tt.want {
				t.Fatalf("backupOwnerHost = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBackupBelongsToHost(t *testing.T) {
	tests := []struct {
		name     string
		meta     *types.BackupMetadata
		hostname string
		want     bool
	}{
		{name: "own backup", meta: &types.BackupMetadata{Hostname: "server1"}, hostname: "server1", want: true},
		{name: "case insensitive", meta: &types.BackupMetadata{Hostname: "SERVER1"}, hostname: "server1", want: true},
		{name: "another host", meta: &types.BackupMetadata{Hostname: "server2"}, hostname: "server1", want: false},
		// Fail-closed: an entry nobody can attribute is left alone rather than
		// deleted on a guess, and a machine that cannot name itself claims nothing.
		{name: "unattributable", meta: &types.BackupMetadata{BackupFile: "mystery.tar.gz"}, hostname: "server1", want: false},
		{name: "unknown local hostname", meta: &types.BackupMetadata{Hostname: "server1"}, hostname: "", want: false},
		{name: "nil entry", meta: nil, hostname: "server1", want: false},
		// A legacy archive has no host token at all, so it stays prunable by
		// whoever lists it - otherwise it would never be rotated again anywhere.
		{name: "legacy name stays ours", meta: &types.BackupMetadata{BackupFile: "proxmox-backup-20250102-100000.tar.gz"}, hostname: "server1", want: true},
		{name: "legacy name with a foreign manifest is not ours", meta: &types.BackupMetadata{BackupFile: "proxmox-backup-20250102-100000.tar.gz", Hostname: "server2"}, hostname: "server1", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backupBelongsToHost(tt.meta, tt.hostname); got != tt.want {
				t.Fatalf("backupBelongsToHost = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestScopeRetentionToHostKeepsSingleHostLocations is the no-regression pin: on the
// ordinary layout - one host writing into its own directory - scoping must be a
// no-op, or retention would silently stop pruning and the location would grow
// without bound.
func TestScopeRetentionToHostKeepsSingleHostLocations(t *testing.T) {
	backups := []*types.BackupMetadata{
		{BackupFile: "pve1-backup-20250103-100000.tar.zst", Hostname: "pve1"},
		{BackupFile: "pve1-backup-20250102-100000.tar.zst", Hostname: "pve1"},
		{BackupFile: "pve1-backup-20250101-100000.tar.zst"}, // manifest unreadable
	}

	owned, foreign := scopeRetentionToHost(backups, "pve1")

	if len(owned) != len(backups) {
		t.Fatalf("kept %d of %d; a single-host location must not shrink: foreign=%+v", len(owned), len(backups), foreign)
	}
}

// TestApplyRetentionDoesNotDeleteOtherHostsBackups is the end-to-end regression pin
// for the reported data-loss bug. host "server1" is listed at a remote root that
// also holds server2's and server3's archives; with MaxBackups=1 those two are the
// "oldest" and were deleted, irreversibly and with only a Debug line naming them.
func TestApplyRetentionDoesNotDeleteOtherHostsBackups(t *testing.T) {
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "gdrive"}
	cs := newCloudStorageForTest(cfg)
	cs.hostname = "server1"

	listing := "" +
		"      100 2025-01-03 10:00:00.000000000 server1-backup-20250103-100000.tar.zst\n" +
		"       10 2025-01-03 10:00:00.000000000 server1-backup-20250103-100000.tar.zst.sha256\n" +
		"      100 2025-01-02 10:00:00.000000000 server1-backup-20250102-100000.tar.zst\n" +
		"       10 2025-01-02 10:00:00.000000000 server1-backup-20250102-100000.tar.zst.sha256\n" +
		"      100 2025-01-02 10:00:00.000000000 server2-backup-20250102-100000.tar.zst\n" +
		"       10 2025-01-02 10:00:00.000000000 server2-backup-20250102-100000.tar.zst.sha256\n" +
		"      100 2025-01-01 10:00:00.000000000 server3-backup-20250101-100000.tar.zst\n" +
		"       10 2025-01-01 10:00:00.000000000 server3-backup-20250101-100000.tar.zst.sha256\n"

	var calls []commandCall
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, commandCall{name: name, args: append([]string(nil), args...)})
		for _, a := range args {
			if a == "lsl" {
				return []byte(listing), nil
			}
		}
		// Every `cat` returns nothing usable, so attribution degrades to the
		// filename token - the path an operator with unreadable manifests is on.
		return nil, nil
	}

	if _, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}

	deletedForeign := false
	deletedOwn := false
	for _, call := range calls {
		joined := strings.Join(call.args, " ")
		if !strings.Contains(joined, "delete") {
			continue
		}
		if strings.Contains(joined, "server2") || strings.Contains(joined, "server3") {
			deletedForeign = true
		}
		if strings.Contains(joined, "server1") {
			deletedOwn = true
		}
	}
	if deletedForeign {
		t.Errorf("retention deleted another host's backup: %+v", calls)
	}
	// The scoping must not have disabled retention altogether: this host is over
	// its own limit of 1 and its older archive still has to go.
	if !deletedOwn {
		t.Errorf("retention deleted nothing of this host's own: %+v", calls)
	}
}
