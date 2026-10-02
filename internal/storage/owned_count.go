package storage

import (
	"context"
	"strings"

	"github.com/tis24dev/proxsave/internal/types"
)

// CountOwnedBackups answers, for a listing taken OUTSIDE a retention pass, the
// question RetentionSummary.Owned answers after one: how many of these archives
// this host is answerable for at the backend's location (discussion #292). It
// exists for a run that fails before its copies: retention never runs there, so
// the startup listing is the only one there is, and printing its unscoped length
// against this host's limit is the "40/7" the scoped count was introduced to end.
//
// It runs the attribution ApplyRetention runs, in the same order: the manifest
// owner lookup on the backends whose List leaves it out (secondary and cloud, one
// "rclone cat" per archive on the cloud, under the same management budget), then
// scopeRetentionToHost, then the two populations Owned adds back, archives nobody
// can name and archives carrying this host's short name under another spelling. It
// does NONE of applyRetentionHostScope's reporting. That function writes WARNING
// lines, every WARNING is counted by ParseLogCounts and pins the run at exit 1
// through applyIssueExitCode, and a count taken at startup must add no line of
// that kind. Nothing here logs above Debug.
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
	var id retentionIdentity
	resolve := func(context.Context, []*types.BackupMetadata) {}
	switch b := backend.(type) {
	case *LocalStorage:
		if b == nil {
			return 0, false
		}
		id = retentionIdentity{hostname: b.hostname, aliases: b.hostAliases, serverID: b.serverID}
	case *SecondaryStorage:
		if b == nil {
			return 0, false
		}
		id = retentionIdentity{hostname: b.hostname, aliases: b.hostAliases, serverID: b.serverID}
		resolve = b.resolveRetentionOwners
	case *CloudStorage:
		if b == nil {
			return 0, false
		}
		id = retentionIdentity{hostname: b.hostname, aliases: b.hostAliases, serverID: b.serverID}
		resolve = b.resolveRetentionOwners
	default:
		return 0, false
	}
	if strings.TrimSpace(id.hostname) == "" {
		return 0, false
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
	return ownedBackupCount(listing, id), true
}

// ownedBackupCount is the arithmetic ApplyRetention publishes as Owned before its
// deletions: the archives scoped to this host plus the two out-of-scope populations
// applyRetentionHostScope returns as unmanaged. It is kept apart from that function
// because that one also reports, and TestOwnedBackupCountMatchesRetentionScope pins
// the two to the same answer.
func ownedBackupCount(backups []*types.BackupMetadata, id retentionIdentity) int {
	owned, foreign := scopeRetentionToHost(backups, id)
	return len(owned) + retentionUnattributable(foreign) + retentionSpellingMismatches(foreign, id)
}
