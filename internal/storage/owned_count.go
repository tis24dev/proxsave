package storage

import (
	"context"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/types"
)

// CountOwnedBackups answers, for a listing taken OUTSIDE a retention pass, the
// question RetentionSummary.Owned answers after one: how many of these archives
// this host owns at the backend's location (discussion #292). It
// exists for a run that fails before its copies: retention never runs there, so
// the startup listing is the only one there is, and printing its unscoped length
// against this host's limit is the "40/7" the scoped count was introduced to end.
//
// It runs the attribution ApplyRetention runs, in the same order: the manifest
// owner lookup on the backends whose List leaves it out (secondary and cloud, one
// "rclone cat" per archive on the cloud, under the same management budget), then
// scopeRetentionToHost. It does NONE of applyRetentionHostScope's reporting. That function prints facts
// under "Applying retention policy...", and a count taken at startup, outside any
// retention block, must add no line. Nothing here logs above Debug.
//
// The number is taken before any pass deletes, so it equals Owned only for a pass
// that would delete nothing. That is exactly the case it is for: on the path that
// reads it no pass runs at all.
//
// ok is false where ApplyRetention would leave ScopeValid unset (a host that cannot
// name itself) and for a backend this package does not implement, so a caller
// falls back to the unscoped total, the rule StorageAdapter.applyStorageStats
// already applies when retention did not run.
//
// The listing is not modified: the owner lookup writes Hostname and ServerID into
// the entries it is given, so it runs on copies.
func CountOwnedBackups(ctx context.Context, backend Storage, backups []*types.BackupMetadata) (owned int, ok bool) {
	listing, id, ok := attributedListing(ctx, backend, backups)
	if !ok {
		return 0, false
	}
	return ownedBackupCount(listing, id), true
}

// OwnedBackups returns the archives of a listing taken outside a retention pass that
// this host owns, by the rule retention prunes by: owned by name or adopted by server
// identity. Other hosts' archives, archives nothing names and archives under another
// spelling of this host's name are left out. It is what the storage-init GFS tiers are
// classified from, so they count what Total backups counts.
//
// ok is false where CountOwnedBackups' is. The returned entries are copies.
func OwnedBackups(ctx context.Context, backend Storage, backups []*types.BackupMetadata) ([]*types.BackupMetadata, bool) {
	listing, id, ok := attributedListing(ctx, backend, backups)
	if !ok {
		return nil, false
	}
	owned, _ := scopeRetentionToHost(listing, id)
	return owned, true
}

// attributedListing copies a listing and runs on the copies the owner lookup
// ApplyRetention runs (manifest reads on the backends whose List leaves them out).
// ok is false for a host that cannot name itself and for a backend this package does
// not implement.
func attributedListing(ctx context.Context, backend Storage, backups []*types.BackupMetadata) ([]*types.BackupMetadata, retentionIdentity, bool) {
	var id retentionIdentity
	resolve := func(context.Context, []*types.BackupMetadata) {}
	switch b := backend.(type) {
	case *LocalStorage:
		if b == nil {
			return nil, id, false
		}
		id = retentionIdentity{hostname: b.hostname, aliases: b.hostAliases, serverID: b.serverID}
	case *SecondaryStorage:
		if b == nil {
			return nil, id, false
		}
		id = retentionIdentity{hostname: b.hostname, aliases: b.hostAliases, serverID: b.serverID}
		resolve = b.resolveRetentionOwners
	case *CloudStorage:
		if b == nil {
			return nil, id, false
		}
		id = retentionIdentity{hostname: b.hostname, aliases: b.hostAliases, serverID: b.serverID}
		resolve = b.resolveRetentionOwners
	default:
		return nil, id, false
	}
	if strings.TrimSpace(id.hostname) == "" {
		return nil, id, false
	}

	listing := make([]*types.BackupMetadata, 0, len(backups))
	for _, entry := range backups {
		if entry == nil {
			continue
		}
		copied := *entry
		listing = append(listing, &copied)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	resolve(ctx, listing)
	return listing, id, true
}

// ownedStorageStats builds a destination's statistics from its listing, counting ONLY
// the archives this host owns, by the rule retention prunes by: owned by name or
// adopted by server identity. Other hosts' archives and archives nothing names are
// left out of the count, the size and the oldest/newest dates; ListedBackups keeps
// the whole listing. The listing must already carry its owners (the caller resolves
// them on the backends whose List does not). A host that cannot name itself owns
// nothing here, as in retention.
func ownedStorageStats(backups []*types.BackupMetadata, id retentionIdentity) *StorageStats {
	owned, _ := scopeRetentionToHost(backups, id)
	stats := &StorageStats{TotalBackups: len(owned), ListedBackups: len(backups)}
	var oldest, newest *time.Time
	for _, backup := range owned {
		stats.TotalSize += backup.Size
		if oldest == nil || backup.Timestamp.Before(*oldest) {
			t := backup.Timestamp
			oldest = &t
		}
		if newest == nil || backup.Timestamp.After(*newest) {
			t := backup.Timestamp
			newest = &t
		}
	}
	stats.OldestBackup = oldest
	stats.NewestBackup = newest
	return stats
}

// ownedBackupCount is the number ApplyRetention publishes as Owned before its
// deletions: the archives scoped to this host. It is kept apart from that function
// because that one also reports, and TestOwnedBackupCountMatchesRetentionScope pins
// the two to the same answer.
func ownedBackupCount(backups []*types.BackupMetadata, id retentionIdentity) int {
	owned, _ := scopeRetentionToHost(backups, id)
	return len(owned)
}
