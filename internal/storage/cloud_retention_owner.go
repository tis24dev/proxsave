package storage

import (
	"context"
	"strings"
	"sync"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/types"
)

// resolveRetentionOwners fills in each candidate's Hostname and ServerID from its
// remote manifest so cloud retention can attribute a backup the same way the local
// and secondary locations do.
//
// List() deliberately does NOT do this: it is also the cheap path behind the run
// counter and the stats screen, and attributing costs one `rclone cat` per archive.
// Retention is the only caller that must know WHO owns a backup before deleting it,
// so the cost is paid here and only here.
//
// Every failure leaves Hostname empty. backupOwnerHost then falls back to the host
// token the filename carries ("<host>-backup-<timestamp>"), so an archive whose
// manifest cannot be read keeps rotating instead of accumulating. An archive whose
// name carries no parseable token is left alone rather than deleted on a guess, with
// no exception: a pre-Go "proxmox-backup-*" name attributes through its KEY=VALUE
// sidecar when one is readable, and is claimed by nobody when none is.
func (c *CloudStorage) resolveRetentionOwners(ctx context.Context, backups []*types.BackupMetadata) {
	pending := make([]*types.BackupMetadata, 0, len(backups))
	for _, b := range backups {
		// A pre-filled Hostname is no longer a reason to skip: with two fields to
		// resolve, skipping on one of them would leave the identity empty and
		// silently disable the mechanism on this backend. See the secondary twin.
		if b == nil {
			continue
		}
		pending = append(pending, b)
	}
	if len(pending) == 0 {
		return
	}

	// The run ctx is cancel-only; every archive costs one `rclone cat` and this
	// function waits on ALL of them, so an unfloored ctx let a single wedged cat
	// hang the unattended run forever. One shared management budget bounds the whole
	// attribution phase: archives left unresolved when it expires keep an empty
	// Hostname and degrade to the filename token, the documented fallback.
	ctx, cancel := c.boundManagementCtx(ctx)
	defer cancel()

	// One round trip per archive, so a large remote is worth parallelising. Bounded by
	// the same CLOUD_PARALLEL_MAX_JOBS the upload path uses, rather than a second knob:
	// both are round trips to the same remote and the operator already tuned that
	// number for it. Each goroutine writes only its own entry, so no lock is needed.
	//
	// NOTE: this deliberately does NOT skip archives whose filename token names another
	// host. The manifest is the authoritative owner and may say the backup is ours even
	// when the filename does not - skipping those would silently stop rotating them.
	workers := c.parallelJobs
	if workers < 1 {
		workers = 1
	}
	if workers > len(pending) {
		workers = len(pending)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for _, b := range pending {
		b := b
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			hostname, serverID := c.remoteManifestOwner(ctx, b.BackupFile)
			if strings.TrimSpace(b.Hostname) == "" {
				b.Hostname = hostname
			}
			if strings.TrimSpace(b.ServerID) == "" {
				b.ServerID = serverID
			}
		}()
	}
	wg.Wait()
}

// remoteManifestSuffixes are the sidecar names an archive's manifest can carry,
// newest first. ".metadata" is what the uploader writes next to the archive;
// ".manifest.json" is the completion sidecar the verify step also accepts.
var remoteManifestSuffixes = []string{".metadata", ".manifest.json"}

// remoteManifestOwner reads the archive's manifest off the remote and returns both
// facts it records about its writer: the host it names and the server identity it
// carries. Both are empty when nothing can be read. Bundles carry the manifest inside
// the tar, which would mean downloading the whole archive, so they are attributed
// only if a sidecar happens to sit beside them.
//
// The two values always come from THE SAME payload, and the suffix loop still stops
// at the first payload that names a host. It never merges a hostname read from
// .metadata with an identity read from .manifest.json: that would fabricate a writer
// no single run ever was, and retention would delete on it. Falling through only when
// a payload names no host preserves exactly the degradation this loop had before.
func (c *CloudStorage) remoteManifestOwner(ctx context.Context, filename string) (hostname, serverID string) {
	rel := strings.TrimSpace(filename)
	if rel == "" {
		return "", ""
	}
	for _, suffix := range remoteManifestSuffixes {
		// Take the binary from buildRcloneArgs like every other call site, rather than
		// hardcoding "rclone": args[0] is whatever the backend resolved, and dropping
		// it would make attribution run a different executable than the rest of the
		// cloud path.
		args := append(c.buildRcloneArgs("cat"), c.remotePathFor(rel+suffix))
		out, err := c.exec(ctx, args[0], args[1:]...)
		if err != nil {
			c.logger.Debug("Cloud storage: cannot read manifest %s%s: %v", rel, suffix, err)
			continue
		}
		// Both manifest forms are accepted here, through the same key precedence the
		// local and secondary paths reach via backup.LoadManifest. Reading only the
		// JSON form is how a shared remote root ended up with one host deleting
		// another host's pre-Go archive together with the KEY=VALUE sidecar that
		// named its owner.
		if host, id := backup.OwnerFromManifestBytes(out); host != "" {
			return host, id
		}
		c.logger.Debug("Cloud storage: manifest %s%s names no host", rel, suffix)
	}
	c.logger.Debug("Cloud storage: no readable manifest for %s; retention will not consider it", rel)
	return "", ""
}
