// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
)

// Bounds on the dedup reads, kept as vars so a malicious or corrupt archive can
// never OOM the (root) restore and tests can lower them. Dedup canonicals are
// config files, so these are DoS backstops, not real-world limits.
var (
	maxDedupManifestBytes  int64 = 16 << 20 // 16 MiB
	maxDedupCanonicalBytes int64 = 64 << 20 // 64 MiB
)

type restoreArchiveOptions struct {
	archivePath string
	destRoot    string
	logger      *logging.Logger
	categories  []Category
	mode        RestoreMode
	logFile     *os.File
	logFilePath string
	skipFn      func(entryName string) bool
	// failOnPartialExtraction makes extractArchiveNative return an error when one
	// or more entries fail to extract. Best-effort callers leave it false (a
	// partial extraction is reported as a warning); the staged restore path sets
	// it true so an incomplete stage is never applied to the live system (BH-002).
	failOnPartialExtraction bool
}

type restoreExtractionStats struct {
	filesExtracted int
	filesSkipped   int
	filesFailed    int
}

type restoreExtractionLog struct {
	logger       *logging.Logger
	logFile      *os.File
	logFilePath  string
	restoredTemp *os.File
	skippedTemp  *os.File
}

// extractArchiveNative extracts TAR archives natively in Go, preserving all timestamps.
func extractArchiveNative(ctx context.Context, opts restoreArchiveOptions) (err error) {
	file, err := restoreFS.Open(opts.archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer closeIntoErr(&err, file, "close archive")

	reader, err := createDecompressionReader(ctx, file, opts.archivePath)
	if err != nil {
		return fmt.Errorf("create decompression reader: %w", err)
	}
	defer closeDecompressionReader(reader, &err, "close decompression reader")

	extractionLog := newRestoreExtractionLog(opts)
	defer extractionLog.close()
	extractionLog.writeHeader(opts)

	stats, extractedSet, err := processRestoreArchiveEntries(ctx, tar.NewReader(reader), opts, extractionLog)
	if err != nil {
		return err
	}

	extractionLog.writeSummary(stats)
	logRestoreExtractionSummary(opts, stats)

	// Turn deduplicated symlinks back into regular files by rebuilding them from the
	// archive, so selective restore never leaves a dangling link and full restore
	// preserves the original file type (issue #70). Safe on every extraction: it
	// never deletes and is a no-op when no dedup manifest is present.
	if err := materializeDedupSymlinks(ctx, opts.archivePath, opts.destRoot, opts.logger, opts.failOnPartialExtraction, extractedSet); err != nil {
		// On the staged path (failOnPartialExtraction) an incompletely reconstructed
		// dedup tree must not be applied to the live system; elsewhere it is a
		// recoverable warning and the (kept) manifest lets a re-run finish.
		if opts.failOnPartialExtraction {
			return err
		}
		opts.logger.Warning("Dedup materialization incomplete: %v", err)
	}

	// When the caller cannot safely act on a partial result (the staged restore
	// path, which would otherwise apply an incomplete tree of PVE/PBS/network/
	// secret config to the live system), surface per-entry extraction failures as
	// an error instead of silently succeeding (BH-002).
	if opts.failOnPartialExtraction && stats.filesFailed > 0 {
		return fmt.Errorf("incomplete extraction: %d entr(ies) failed to extract (%d extracted); refusing to apply a partial result", stats.filesFailed, stats.filesExtracted)
	}
	return nil
}

func newRestoreExtractionLog(opts restoreArchiveOptions) *restoreExtractionLog {
	extractionLog := &restoreExtractionLog{
		logger:      opts.logger,
		logFile:     opts.logFile,
		logFilePath: opts.logFilePath,
	}
	if opts.logFile == nil {
		return extractionLog
	}

	if tmp, err := restoreFS.CreateTemp("", "restored_entries_*.log"); err == nil {
		extractionLog.restoredTemp = tmp
	} else {
		opts.logger.Warning("Could not create temporary file for restored entries: %v", err)
	}
	if tmp, err := restoreFS.CreateTemp("", "skipped_entries_*.log"); err == nil {
		extractionLog.skippedTemp = tmp
	} else {
		opts.logger.Warning("Could not create temporary file for skipped entries: %v", err)
	}
	return extractionLog
}

func (log *restoreExtractionLog) close() {
	closeAndRemoveRestoreTemp(log.restoredTemp)
	closeAndRemoveRestoreTemp(log.skippedTemp)
}

func closeAndRemoveRestoreTemp(file *os.File) {
	if file == nil {
		return
	}
	_ = file.Close()
	_ = restoreFS.Remove(file.Name())
}

func (log *restoreExtractionLog) writeHeader(opts restoreArchiveOptions) {
	if log.logFile == nil {
		return
	}
	_, _ = fmt.Fprintf(log.logFile, "=== PROXMOX RESTORE LOG ===\n")
	_, _ = fmt.Fprintf(log.logFile, "Date: %s\n", nowRestore().Format("2006-01-02 15:04:05"))
	_, _ = fmt.Fprintf(log.logFile, "Mode: %s\n", getModeName(opts.mode))
	if len(opts.categories) > 0 {
		_, _ = fmt.Fprintf(log.logFile, "Selected categories: %d categories\n", len(opts.categories))
		for _, cat := range opts.categories {
			_, _ = fmt.Fprintf(log.logFile, "  - %s (%s)\n", cat.Name, cat.ID)
		}
	} else {
		_, _ = fmt.Fprintf(log.logFile, "Selected categories: ALL (full restore)\n")
	}
	_, _ = fmt.Fprintf(log.logFile, "Archive: %s\n", filepath.Base(opts.archivePath))
	_, _ = fmt.Fprintf(log.logFile, "\n")
}

func processRestoreArchiveEntries(ctx context.Context, tarReader *tar.Reader, opts restoreArchiveOptions, extractionLog *restoreExtractionLog) (restoreExtractionStats, map[string]bool, error) {
	var stats restoreExtractionStats
	selectiveMode := len(opts.categories) > 0
	// Record what we actually extracted this run so dedup materialization is gated to
	// it (F-05-01): only a duplicate symlink created by THIS run is ever rebuilt, never
	// a pre-existing live symlink an out-of-scope or malicious manifest entry points at.
	// Built for full restore too (not just selective) so restore-to-/ is protected.
	extractedSet := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return stats, extractedSet, err
		}

		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return stats, extractedSet, fmt.Errorf("read tar header: %w", err)
		}

		if skipRestoreArchiveEntry(header, opts, selectiveMode, extractionLog, &stats) {
			continue
		}
		// A hardlink aliases an existing on-disk file; in a selective restore its
		// target must belong to a selected category. A cross-category hardlink
		// (e.g. an in-category name aliasing /etc/shadow) is never legitimate, so
		// refuse it. Symlinks are intentionally NOT constrained this way: their
		// targets legitimately point outside the category.
		if selectiveMode && header.Typeflag == tar.TypeLink && !restoreEntryMatchesCategories(header.Linkname, opts.categories) {
			opts.logger.Warning("Refusing hardlink %s: target %s is outside the selected categories", header.Name, header.Linkname)
			stats.filesFailed++
			extractionLog.recordSkipped(header.Name, "hardlink target outside selected categories")
			continue
		}
		if err := extractTarEntry(tarReader, header, opts.destRoot, opts.logger); err != nil {
			opts.logger.Warning("Failed to extract %s: %v", header.Name, err)
			stats.filesFailed++
			continue
		}

		stats.filesExtracted++
		if extractedSet != nil {
			extractedSet[dedupCleanArchivePath(header.Name)] = true
		}
		extractionLog.recordRestored(header.Name)
		if stats.filesExtracted%100 == 0 {
			opts.logger.Debug("Extracted %d files...", stats.filesExtracted)
		}
	}
	return stats, extractedSet, nil
}

func isDedupManifestEntry(name string) bool {
	clean := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(name)), "/")
	return clean == backup.DedupManifestRelPath
}

func skipRestoreArchiveEntry(header *tar.Header, opts restoreArchiveOptions, selectiveMode bool, extractionLog *restoreExtractionLog, stats *restoreExtractionStats) bool {
	// The dedup manifest is always extracted, regardless of selected categories, so
	// the post-extraction pass can materialize deduplicated symlinks (issue #70).
	if isDedupManifestEntry(header.Name) {
		return false
	}
	if opts.skipFn != nil && opts.skipFn(header.Name) {
		stats.filesSkipped++
		extractionLog.recordSkipped(header.Name, "skipped by restore policy")
		return true
	}
	if !selectiveMode || restoreEntryMatchesCategories(header.Name, opts.categories) {
		return false
	}
	stats.filesSkipped++
	extractionLog.recordSkipped(header.Name, "does not match any selected category")
	return true
}

func restoreEntryMatchesCategories(entryName string, categories []Category) bool {
	for _, cat := range categories {
		if PathMatchesCategory(entryName, cat) {
			return true
		}
	}
	return false
}

func (log *restoreExtractionLog) recordSkipped(name, reason string) {
	if log.skippedTemp != nil {
		_, _ = fmt.Fprintf(log.skippedTemp, "SKIPPED: %s (%s)\n", name, reason)
	}
}

func (log *restoreExtractionLog) recordRestored(name string) {
	if log.restoredTemp != nil {
		_, _ = fmt.Fprintf(log.restoredTemp, "RESTORED: %s\n", name)
	}
}

func (log *restoreExtractionLog) writeSummary(stats restoreExtractionStats) {
	if log.logFile == nil {
		return
	}
	_, _ = fmt.Fprintf(log.logFile, "=== FILES RESTORED ===\n")
	log.copyTempEntries(log.restoredTemp, "restored")
	_, _ = fmt.Fprintf(log.logFile, "\n")

	_, _ = fmt.Fprintf(log.logFile, "=== FILES SKIPPED ===\n")
	log.copyTempEntries(log.skippedTemp, "skipped")
	_, _ = fmt.Fprintf(log.logFile, "\n")

	_, _ = fmt.Fprintf(log.logFile, "=== SUMMARY ===\n")
	_, _ = fmt.Fprintf(log.logFile, "Total files extracted: %d\n", stats.filesExtracted)
	_, _ = fmt.Fprintf(log.logFile, "Total files skipped: %d\n", stats.filesSkipped)
	_, _ = fmt.Fprintf(log.logFile, "Total files failed: %d\n", stats.filesFailed)
	_, _ = fmt.Fprintf(log.logFile, "Total files in archive: %d\n", stats.filesExtracted+stats.filesSkipped+stats.filesFailed)
}

func (log *restoreExtractionLog) copyTempEntries(tempFile *os.File, label string) {
	if tempFile == nil {
		return
	}
	if _, err := tempFile.Seek(0, 0); err == nil {
		if _, err := io.Copy(log.logFile, tempFile); err != nil {
			log.logger.Warning("Could not write %s entries to log: %v", label, err)
		}
	}
}

func logRestoreExtractionSummary(opts restoreArchiveOptions, stats restoreExtractionStats) {
	if stats.filesFailed == 0 {
		if len(opts.categories) > 0 {
			opts.logger.Info("Successfully restored all %d configuration files/directories", stats.filesExtracted)
		} else {
			opts.logger.Info("Successfully restored all %d files/directories", stats.filesExtracted)
		}
	} else {
		if opts.logFilePath != "" {
			opts.logger.Warning("Restored %d files/directories; %d item(s) failed (see detailed log)", stats.filesExtracted, stats.filesFailed)
		} else {
			opts.logger.Warning("Restored %d files/directories; %d item(s) failed", stats.filesExtracted, stats.filesFailed)
		}
	}

	if stats.filesSkipped > 0 {
		if opts.logFilePath != "" {
			opts.logger.Info("%d additional archive entries (logs, diagnostics, system defaults) were left unchanged on this system; see detailed log for details", stats.filesSkipped)
		} else {
			opts.logger.Info("%d additional archive entries (logs, diagnostics, system defaults) were left unchanged on this system", stats.filesSkipped)
		}
	}

	if opts.logFilePath != "" {
		opts.logger.Info("Detailed restore log: %s", opts.logFilePath)
	}
}

type materializeTarget struct {
	path string // absolute duplicate path under destRoot (currently a symlink)
	mode os.FileMode
	uid  *uint32 // source owner uid from the manifest; nil (old manifest) -> skip chown
	gid  *uint32 // source owner gid from the manifest; nil (old manifest) -> skip chown
}

// materializeDedupSymlinks reads the dedup manifest written at backup time and
// replaces each recorded symlink with a regular file rebuilt from the BACKUP ARCHIVE
// content. Reading the canonical bytes from the archive (never from the possibly
// stale on-disk/live target, and never deleting the symlink) is what makes a
// selective/staged restore safe: a selected duplicate is reconstructed even when its
// dedup canonical's category was not selected or its on-disk copy failed to extract,
// and it never picks up stale live content (issue #70). It is a no-op when no
// manifest is present (deduplication was off or found no duplicates).
func materializeDedupSymlinks(ctx context.Context, archivePath, destRoot string, logger *logging.Logger, strict bool, extractedSet map[string]bool) error {
	manifestTarget, _, err := sanitizeRestoreEntryTargetWithFS(restoreFS, destRoot, backup.DedupManifestRelPath)
	if err != nil {
		return nil
	}
	manifestFile, err := restoreFS.Open(manifestTarget)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no dedup manifest: nothing to materialize
		}
		// The manifest exists but cannot be opened (EACCES/EIO): on the strict/staged
		// path refuse to apply the tree, consistent with the read/parse/oversize guards.
		if strict {
			return fmt.Errorf("dedup manifest cannot be opened; refusing to apply a partial staged restore: %w", err)
		}
		return nil // best-effort: tolerate an unreadable manifest
	}
	data, rerr := io.ReadAll(io.LimitReader(manifestFile, maxDedupManifestBytes+1))
	_ = manifestFile.Close()
	if rerr != nil {
		if strict {
			return fmt.Errorf("dedup manifest unreadable; refusing to apply a partial staged restore: %w", rerr)
		}
		return nil // unreadable manifest (matches the prior ReadFile-error behavior)
	}
	if int64(len(data)) > maxDedupManifestBytes {
		logger.Warning("Dedup manifest exceeds %d bytes; refusing to load it", maxDedupManifestBytes)
		if strict {
			return fmt.Errorf("dedup manifest exceeds %d bytes; refusing to apply a partial staged restore", maxDedupManifestBytes)
		}
		removeDedupManifest(manifestTarget)
		return nil
	}
	var entries []backup.DedupManifestEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		logger.Warning("Dedup manifest unreadable; skipping symlink materialization: %v", err)
		if strict {
			// A corrupt manifest means the extracted duplicates cannot be materialized;
			// on the strict/staged path refuse to apply the tree (keep the manifest so a
			// re-run can recover), consistent with the oversize and missing-canonical guards.
			return fmt.Errorf("dedup manifest corrupt; refusing to apply a partial staged restore: %w", err)
		}
		// Best-effort: nothing can be materialized, but do not leave the garbage
		// (force-extracted under var/lib/proxsave-info) lingering on the restored system.
		removeDedupManifest(manifestTarget)
		return nil
	}

	// Map each canonical archive path to the extracted duplicate symlinks that need
	// its content. Only duplicates actually present on disk are considered.
	needByCanonical := map[string][]materializeTarget{}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Path) == "" {
			continue
		}
		if extractedSet != nil && !extractedSet[dedupCleanArchivePath(entry.Path)] {
			// Only rebuild duplicates actually extracted this run (selective AND full),
			// never a pre-existing live symlink or an out-of-scope manifest entry (F-05-01).
			continue
		}
		target, _, err := sanitizeRestoreEntryTargetWithFS(restoreFS, destRoot, entry.Path)
		if err != nil {
			continue
		}
		info, err := restoreFS.Lstat(target)
		if err != nil {
			continue // duplicate not extracted: its own category was not selected
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue // already a regular file (the target was extracted too)
		}
		linkTarget, err := restoreFS.Readlink(target)
		if err != nil {
			continue
		}
		// Canonical archive-relative path, derived purely lexically as the inverse of
		// the relative link replaceWithSymlink wrote at backup time.
		canonicalRel := dedupCleanArchivePath(path.Join(path.Dir(filepath.ToSlash(entry.Path)), filepath.ToSlash(linkTarget)))
		needByCanonical[canonicalRel] = append(needByCanonical[canonicalRel], materializeTarget{
			path: target,
			mode: os.FileMode(entry.Mode).Perm(),
			uid:  entry.Uid,
			gid:  entry.Gid,
		})
	}

	if len(needByCanonical) > 0 {
		materialized, missing, completed := materializeFromArchive(ctx, archivePath, needByCanonical, logger)
		if !completed {
			// The archive scan was cut short (context canceled / open or read error):
			// keep the manifest so a re-run can finish, rather than dropping it and
			// stranding un-materialized symlinks with no way to recover. Surface it so a
			// staged restore that cannot tolerate a partial result fails closed instead
			// of applying an incompletely reconstructed tree (BH-002).
			logger.Warning("Dedup: materialization did not complete (%d rebuilt so far); keeping the manifest for a retry", materialized)
			return fmt.Errorf("dedup materialization incomplete: %d file(s) rebuilt before the archive scan stopped; manifest kept for retry", materialized)
		}
		if materialized > 0 || missing > 0 {
			logger.Info("Dedup: materialized %d deduplicated file(s) from the archive; %d left as link(s) due to missing canonical content", materialized, missing)
		}
		if strict && missing > 0 {
			// A staged/strict restore cannot apply a tree with dangling deduplicated
			// links. Keep the manifest+state and fail closed, like the !completed branch.
			logger.Warning("Dedup: %d deduplicated file(s) have no canonical in the archive; refusing to apply a partial staged restore", missing)
			return fmt.Errorf("dedup materialization incomplete: %d deduplicated file(s) have no canonical in the archive; refusing to apply a partial staged restore", missing)
		}
	}

	// Drop the manifest (and the now-empty proxsave-info dir we may have force-created)
	// so it does not linger on the restored system.
	removeDedupManifest(manifestTarget)
	return nil
}

// removeDedupManifest deletes the materialized-then-consumed dedup manifest and, if
// force-extraction created an otherwise-empty var/lib/proxsave-info directory on the
// destination, removes that too (Remove on a non-empty dir fails and is a no-op).
func removeDedupManifest(manifestTarget string) {
	_ = restoreFS.Remove(manifestTarget)
	_ = restoreFS.Remove(filepath.Dir(manifestTarget))
}

// dedupCleanArchivePath normalizes a name to the archive-relative slash form used
// for manifest/target matching (no leading "./" or "/").
func dedupCleanArchivePath(name string) string {
	return strings.TrimPrefix(path.Clean(filepath.ToSlash(name)), "/")
}

// materializeFromArchive streams the (already decrypted) archive once and rebuilds
// each pending duplicate from its canonical's bytes, reading one canonical at a time
// (bounded memory). A duplicate whose canonical is absent from the archive is left
// as a symlink (never deleted). It returns how many were materialized, how many were
// left as links (canonical genuinely missing from the archive), and whether the scan
// ran to completion. completed is false when the archive could not be opened/read or
// the scan was canceled mid-way; the caller then keeps the manifest for a retry
// instead of dropping it and stranding un-materialized symlinks.
func materializeFromArchive(ctx context.Context, archivePath string, needByCanonical map[string][]materializeTarget, logger *logging.Logger) (materialized, missing int, completed bool) {
	file, err := restoreFS.Open(archivePath)
	if err != nil {
		logger.Warning("Dedup: could not open the archive to rebuild deduplicated files; left as links: %v", err)
		return 0, 0, false
	}
	defer func() { _ = file.Close() }()
	reader, err := createDecompressionReader(ctx, file, archivePath)
	if err != nil {
		logger.Warning("Dedup: could not read the archive to rebuild deduplicated files; left as links: %v", err)
		return 0, 0, false
	}
	defer func() { _ = reader.Close() }()

	found := map[string]bool{}
	writeOK := true
	tr := tar.NewReader(reader)
	for len(found) < len(needByCanonical) {
		if err := ctx.Err(); err != nil {
			logger.Warning("Dedup: archive scan canceled while rebuilding deduplicated files: %v", err)
			return materialized, 0, false
		}
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			logger.Warning("Dedup: error reading the archive while rebuilding deduplicated files: %v", err)
			return materialized, 0, false
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name := dedupCleanArchivePath(header.Name)
		dups, ok := needByCanonical[name]
		if !ok {
			continue
		}
		content, err := io.ReadAll(io.LimitReader(tr, maxDedupCanonicalBytes+1))
		if err != nil {
			logger.Warning("Dedup: failed to read canonical %q from the archive: %v", name, err)
			continue // leave its duplicates as links (counted as missing below)
		}
		if int64(len(content)) > maxDedupCanonicalBytes {
			logger.Warning("Dedup: canonical %q exceeds %d bytes; leaving its duplicate(s) as link(s)", name, maxDedupCanonicalBytes)
			continue // not marked found -> counted missing -> strict fails closed
		}
		found[name] = true
		for _, d := range dups {
			if werr := writeMaterializedFile(d.path, content, d.mode, d.uid, d.gid, logger); werr != nil {
				logger.Warning("Dedup: failed to materialize %s from archive: %v", name, werr)
				writeOK = false // a transient write failure: keep the manifest for a retry
				continue
			}
			materialized++
		}
	}

	for name, dups := range needByCanonical {
		if !found[name] {
			logger.Warning("Dedup: canonical %q is missing from the archive; %d file(s) left as symlink(s)", name, len(dups))
			missing += len(dups)
		}
	}
	// completed=false on a write failure so the caller keeps the manifest and a re-run
	// can finish the still-symlinked duplicate(s); a genuinely missing canonical
	// (corrupt backup, not retryable) does not block manifest cleanup.
	return materialized, missing, writeOK
}

// writeMaterializedFile atomically replaces a path (typically a dedup symlink) with
// a regular file holding content, via a sibling temp + rename so a crash never
// leaves the path missing.
func writeMaterializedFile(target string, content []byte, mode os.FileMode, uid, gid *uint32, logger *logging.Logger) error {
	if mode == 0 {
		mode = 0o600
	}
	tmp, err := restoreFS.CreateTemp(filepath.Dir(target), restoreTempPattern)
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = restoreFS.Remove(tmpPath)
		return fmt.Errorf("write temp: %w", err)
	}
	// Restore the source owner recorded in the manifest (F07-04), BEFORE chmod since
	// chown can strip setuid/setgid bits. Best-effort to match the normal
	// extracted-file path (restore_archive_entries.go): an unprivileged run or a
	// chown-unsupported FS must not fail the restore. A nil owner (pre-F07-04
	// manifest) is left untouched, never forced to 0:0.
	if uid != nil && gid != nil {
		if err := atomicFileChown(tmp, int(*uid), int(*gid)); err != nil && logger != nil {
			logger.Debug("Failed to chown materialized file %s: %v", target, err)
		}
	}
	if err := atomicFileChmod(tmp, mode.Perm()); err != nil {
		_ = tmp.Close()
		_ = restoreFS.Remove(tmpPath)
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = restoreFS.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := restoreFS.Rename(tmpPath, target); err != nil {
		_ = restoreFS.Remove(tmpPath)
		return fmt.Errorf("replace symlink with file: %w", err)
	}
	return nil
}
