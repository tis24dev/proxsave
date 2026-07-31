package storage

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/types"
)

// resolveRetentionOwners fills in each candidate's Hostname from its remote manifest
// so cloud retention can attribute a backup the same way the local and secondary
// locations do.
//
// List() deliberately does NOT do this: it is also the cheap path behind the run
// counter and the stats screen, and attributing costs one `rclone cat` per archive.
// Retention is the only caller that must know WHO owns a backup before deleting it,
// so the cost is paid here and only here.
//
// Every failure leaves Hostname empty, which backupBelongsToHost reads as "not
// ours" - a backup whose manifest cannot be read is left alone rather than deleted
// on a guess.
func (c *CloudStorage) resolveRetentionOwners(ctx context.Context, backups []*types.BackupMetadata) {
	for _, b := range backups {
		if b == nil || strings.TrimSpace(b.Hostname) != "" {
			continue
		}
		b.Hostname = c.remoteManifestHostname(ctx, b.BackupFile)
	}
}

// remoteManifestSuffixes are the sidecar names an archive's manifest can carry,
// newest first. ".metadata" is what the uploader writes next to the archive;
// ".manifest.json" is the completion sidecar the verify step also accepts.
var remoteManifestSuffixes = []string{".metadata", ".manifest.json"}

// remoteManifestHostname reads the archive's manifest off the remote and returns its
// "hostname" field, or "" when it cannot be read. Bundles carry the manifest inside
// the tar, which would mean downloading the whole archive, so they are attributed
// only if a sidecar happens to sit beside them.
func (c *CloudStorage) remoteManifestHostname(ctx context.Context, filename string) string {
	rel := strings.TrimSpace(filename)
	if rel == "" {
		return ""
	}
	for _, suffix := range remoteManifestSuffixes {
		out, err := c.exec(ctx, "rclone", append(c.buildRcloneArgs("cat")[1:], c.remotePathFor(rel+suffix))...)
		if err != nil {
			continue
		}
		var manifest backup.Manifest
		if err := json.Unmarshal(out, &manifest); err != nil {
			c.logger.Debug("Cloud storage: manifest %s%s is not readable JSON: %v", rel, suffix, err)
			continue
		}
		if host := strings.TrimSpace(manifest.Hostname); host != "" {
			return host
		}
	}
	c.logger.Debug("Cloud storage: no readable manifest for %s; retention will not consider it", rel)
	return ""
}
