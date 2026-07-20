package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tis24dev/proxsave/internal/logging"
)

const (
	defaultPrefilterMaxSizeBytes = 8 * 1024 * 1024
	defaultOptimizedFilePerm     = 0o640
)

// DedupManifestRelPath is where deduplicateFiles records the symlinks it created,
// relative to the staging/archive root. The restore reads it (always extracted)
// to materialize those symlinks back into regular files, so selective restore
// never produces dangling links and full restore preserves file-type fidelity
// (issue #70).
const DedupManifestRelPath = "var/lib/proxsave-info/dedup_manifest.json"

// DedupManifestEntry records one file that deduplication replaced with a symlink.
type DedupManifestEntry struct {
	Path string  `json:"path"`          // path relative to the archive root, slash-separated
	Mode uint32  `json:"mode"`          // original regular-file permission bits
	Uid  *uint32 `json:"uid,omitempty"` // original owner uid; nil in pre-F07-04 manifests -> restore skips chown
	Gid  *uint32 `json:"gid,omitempty"` // original owner gid; nil in pre-F07-04 manifests -> restore skips chown
}

// OptimizationConfig controls optional preprocessing steps executed before archiving.
type OptimizationConfig struct {
	EnableDeduplication       bool
	EnablePrefilter           bool
	PrefilterMaxFileSizeBytes int64
}

// Enabled returns true if at least one optimization is active.
func (c OptimizationConfig) Enabled() bool {
	return c.EnableDeduplication || c.EnablePrefilter
}

// OptimizationResult reports what the optimization stages removed from the staged
// tree. Callers use BytesReclaimed to correct the reported uncompressed-payload size
// (issue #73): dedup and prefilter shrink the tree AFTER the collection stats were
// snapshotted, so the pre-optimization byte total would otherwise inflate the
// compression ratio shown in reports/notifications/metrics.
type OptimizationResult struct {
	BytesReclaimed     int64 // bytes removed from the staged tree by dedup + prefilter
	DuplicatesReplaced int
}

// ApplyOptimizations executes the requested optimizations in sequence and reports
// how many bytes they reclaimed.
func ApplyOptimizations(ctx context.Context, logger *logging.Logger, root string, cfg OptimizationConfig) (OptimizationResult, error) {
	var res OptimizationResult
	if !cfg.Enabled() {
		return res, nil
	}

	logger.Info("Running backup optimizations (dedup=%v prefilter=%v)",
		cfg.EnableDeduplication, cfg.EnablePrefilter)

	if cfg.EnableDeduplication {
		logger.Debug("Starting deduplication stage")
		dups, reclaimed, err := deduplicateFiles(ctx, logger, root)
		if err != nil {
			// A dedup error means the staging tree may still hold symlinks the restore
			// cannot materialize (manifest unwritten, partial revert): fail rather than
			// archive a tree that would lose fidelity on restore (issue #70). The
			// happy path and a fully-reverted manifest failure both return nil.
			return OptimizationResult{}, fmt.Errorf("deduplication: %w", err)
		}
		res.DuplicatesReplaced = dups
		res.BytesReclaimed += reclaimed
		logger.Debug("Deduplication stage completed")
	}

	if cfg.EnablePrefilter {
		logger.Debug("Starting prefilter stage (max file size %d bytes)", cfg.PrefilterMaxFileSizeBytes)
		reclaimed, err := prefilterFiles(ctx, logger, root, cfg.PrefilterMaxFileSizeBytes)
		if err != nil {
			logger.Warning("Content prefilter failed: %v", err)
		} else {
			res.BytesReclaimed += reclaimed
			logger.Debug("Prefilter stage completed")
		}
	}

	return res, nil
}

func deduplicateFiles(ctx context.Context, logger *logging.Logger, root string) (int, int64, error) {
	logger.Debug("Scanning files for deduplication")

	hashes := make(map[string]string)
	var duplicates int
	var bytesReclaimed int64
	var manifest []DedupManifestEntry
	var replaced []dedupReplacement

	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return 0, 0, fmt.Errorf("open dedup root: %w", err)
	}
	defer func() { _ = rootFS.Close() }()

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return fmt.Errorf("compute path relative to %s: %w", root, relErr)
		}
		if shouldSkipDedupPath(rel) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() == 0 {
			return nil
		}

		hash, err := hashFile(rootFS, rel)
		if err != nil {
			logger.Warning("Failed to hash %s: %v", path, err)
			return nil
		}

		if existing, ok := hashes[hash]; ok {
			if err := replaceWithSymlink(existing, path); err != nil {
				logger.Warning("Failed to replace duplicate %s: %v", path, err)
				return nil
			}
			duplicates++
			bytesReclaimed += info.Size()
			entry := DedupManifestEntry{
				Path: filepath.ToSlash(rel),
				Mode: uint32(info.Mode().Perm()),
			}
			// Record the source owner so the restore can chown the rebuilt file back
			// to it (F07-04). Left nil on non-unix FileInfo -> the restore skips chown.
			if st, ok := info.Sys().(*syscall.Stat_t); ok && st != nil {
				uid := st.Uid
				gid := st.Gid
				entry.Uid = &uid
				entry.Gid = &gid
			}
			manifest = append(manifest, entry)
			replaced = append(replaced, dedupReplacement{
				duplicate: path,
				canonical: existing,
				mode:      info.Mode().Perm(),
			})
			logger.Debug("Deduplicated %s -> %s", path, existing)
		} else {
			hashes[hash] = path
		}
		return nil
	})

	if err != nil {
		return 0, 0, fmt.Errorf("deduplication walk failed: %w", err)
	}

	if err := writeDedupManifest(root, manifest); err != nil {
		// Without the manifest the restore cannot materialize these symlinks, so an
		// unrecorded symlink would ship and break fidelity (issue #70). Revert every
		// symlink back to a regular file so the archive degrades to "no dedup this
		// run" rather than carrying unrecoverable links.
		logger.Warning("Failed to write dedup manifest; reverting %d deduplicated symlink(s) to regular files: %v", len(replaced), err)
		reverted := 0
		for _, r := range replaced {
			if rerr := revertDedupSymlink(r); rerr != nil {
				logger.Warning("Failed to revert deduplicated symlink %s: %v", r.duplicate, rerr)
				continue
			}
			reverted++
		}
		if reverted != len(replaced) {
			return 0, 0, fmt.Errorf("write dedup manifest: %w (reverted %d/%d symlinks)", err, reverted, len(replaced))
		}
		// All symlinks reverted to regular files: nothing was actually reclaimed.
		logger.Info("Deduplication aborted (manifest unwritable); %d symlink(s) reverted to regular files", reverted)
		return 0, 0, nil
	}

	logger.Info("Deduplication completed: %d duplicates replaced", duplicates)
	return duplicates, bytesReclaimed, nil
}

// writeDedupManifest records the deduplicated symlinks so the restore can
// materialize them back into regular files (issue #70). It is a no-op when no
// files were deduplicated.
func writeDedupManifest(root string, entries []DedupManifestEntry) error {
	if len(entries) == 0 {
		return nil
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	dest := filepath.Join(root, filepath.FromSlash(DedupManifestRelPath))
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o600)
}

func shouldSkipDedupPath(rel string) bool {
	rel = filepath.ToSlash(rel)
	switch rel {
	case "etc/resolv.conf",
		"etc/hostname",
		"etc/hosts",
		"etc/fstab":
		return true
	default:
		return false
	}
}

func hashFile(root *os.Root, name string) (sum string, err error) {
	f, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer closeIntoErr(&err, f, "close file for hash")

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

func replaceWithSymlink(target, duplicate string) error {
	rel, err := filepath.Rel(filepath.Dir(duplicate), target)
	if err != nil {
		rel = target
	}
	// Create the symlink at a UNIQUE temporary name in the same directory, then
	// atomically rename it over the duplicate. A unique name (not the fixed
	// duplicate+".dedup.tmp") avoids destroying a real staged file that happens to
	// carry that suffix, and the rename keeps the replacement fail-closed: on any
	// error the original duplicate is left untouched (issues #70/#71).
	tmpFile, err := os.CreateTemp(filepath.Dir(duplicate), ".proxsave-dedup-*")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	_ = tmpFile.Close()
	_ = os.Remove(tmp) // os.Symlink needs a non-existent path
	if err := os.Symlink(rel, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, duplicate); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// dedupReplacement remembers a symlink dedup created so it can be reverted to a
// regular file if the manifest cannot be written (so an unrecorded symlink, which
// the restore could not materialize, is never shipped).
type dedupReplacement struct {
	duplicate string // absolute staged path now holding the symlink
	canonical string // absolute staged path of the kept original
	mode      os.FileMode
}

// revertDedupSymlink turns one dedup symlink back into a regular copy of its
// canonical. Used when the manifest write fails so the archive carries plain files.
// It writes to a sibling temp then renames over the symlink, so a failed write never
// leaves the duplicate missing (no remove-then-write window).
func revertDedupSymlink(r dedupReplacement) error {
	content, err := os.ReadFile(r.canonical)
	if err != nil {
		return err
	}
	mode := r.mode.Perm()
	if mode == 0 {
		mode = 0o600
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.duplicate), ".proxsave-dedup-revert-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, r.duplicate); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func prefilterFiles(ctx context.Context, logger *logging.Logger, root string, maxSize int64) (int64, error) {
	if maxSize <= 0 {
		maxSize = defaultPrefilterMaxSizeBytes
	}
	logger.Debug("Prefiltering files under %s (max size %d bytes)", root, maxSize)

	type prefilterStats struct {
		scanned           int
		optimized         int
		skippedStructured int
		skippedSymlink    int
	}
	var stats prefilterStats
	var reclaimed int64

	isStructuredConfigPath := func(path string) bool {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return false
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		rel = strings.TrimPrefix(rel, "./")
		switch {
		case strings.HasPrefix(rel, "etc/proxmox-backup/"):
			return true
		case strings.HasPrefix(rel, "etc/pve/"):
			return true
		case strings.HasPrefix(rel, "etc/ssh/"):
			return true
		case strings.HasPrefix(rel, "etc/pam.d/"):
			return true
		case strings.HasPrefix(rel, "etc/systemd/system/"):
			return true
		default:
			return false
		}
	}

	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return 0, fmt.Errorf("open prefilter root: %w", err)
	}
	defer func() { _ = rootFS.Close() }()

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}

		info, err := os.Lstat(path)
		if err != nil || info == nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			stats.skippedSymlink++
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() == 0 || info.Size() > maxSize {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return fmt.Errorf("compute path relative to %s: %w", root, relErr)
		}

		stats.scanned++
		before := info.Size()
		changed := false
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".txt", ".log", ".md":
			if c, err := normalizeTextFile(rootFS, rel); err != nil {
				logger.Warning("Prefilter: failed to optimize %s (kept original): %v", rel, err)
			} else if c {
				changed = true
			}
		case ".conf", ".cfg", ".ini":
			if isStructuredConfigPath(path) {
				stats.skippedStructured++
				return nil
			}
			if c, err := normalizeConfigFile(rootFS, rel); err != nil {
				logger.Warning("Prefilter: failed to optimize %s (kept original): %v", rel, err)
			} else if c {
				changed = true
			}
		case ".json":
			if isStructuredConfigPath(path) {
				stats.skippedStructured++
				return nil
			}
			if c, err := minifyJSON(rootFS, rel); err != nil {
				logger.Warning("Prefilter: failed to optimize %s (kept original): %v", rel, err)
			} else if c {
				changed = true
			}
		}
		if changed {
			stats.optimized++
			// Account for bytes removed (issue #73 ratio correction); re-stat the
			// rewritten file (best-effort).
			if newInfo, serr := os.Lstat(path); serr == nil && newInfo.Size() < before {
				reclaimed += before - newInfo.Size()
			}
		}
		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("prefilter walk failed: %w", err)
	}

	logger.Info("Prefilter completed: optimized=%d scanned=%d skipped_structured=%d skipped_symlink=%d reclaimed_bytes=%d", stats.optimized, stats.scanned, stats.skippedStructured, stats.skippedSymlink, reclaimed)
	return reclaimed, nil
}

// prefilterRootRename is the rename step of atomicRootRewrite, isolated as a var so
// tests can force a post-write failure and assert the staged original is preserved.
var prefilterRootRename = func(root *os.Root, oldname, newname string) error {
	return root.Rename(oldname, newname)
}

// prefilterFileChown is the chown step of atomicRootRewrite, isolated as a var so
// tests can assert the temp is chowned back to the source uid/gid without root.
var prefilterFileChown = (*os.File).Chown

// atomicRootRewrite writes data to name atomically within root: it O_EXCL-creates a
// uniquely named temp sibling, writes+closes it, then renames it over name. On any
// error the temp is removed and name is left untouched, so a failed rewrite never
// truncates the staged original. All I/O is confined to root (os.Root), so no path
// escapes the staging tree (same traversal defense as normalizeTextFile).
func atomicRootRewrite(root *os.Root, name string, data []byte) error {
	// Preserve the existing file's mode AND ownership. The rewrite swaps the inode
	// via rename, so unlike the old in-place root.WriteFile (which left an existing
	// file's mode and uid/gid untouched) a fresh temp would otherwise carry the backup
	// process's ownership and its own creation mode, dropping the permission bits and
	// uid/gid the collector preserved (backup fidelity).
	perm := os.FileMode(0o600)
	uid, gid := -1, -1
	if fi, err := root.Lstat(name); err == nil {
		perm = fi.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(st.Uid), int(st.Gid)
		}
	}
	tmp := name + "." + randomHexSuffix() + ".tmp"
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, perm.Perm())
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = root.Remove(tmp)
		return err
	}
	// Chown before Chmod: chown can clear setuid/setgid, so set the exact mode last.
	if uid >= 0 {
		if err := prefilterFileChown(f, uid, gid); err != nil {
			_ = f.Close()
			_ = root.Remove(tmp)
			return err
		}
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		_ = root.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	if err := prefilterRootRename(root, tmp, name); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

// randomHexSuffix returns a 16-hex-char token for a unique temp name, so the O_EXCL
// create never clobbers a real staged file that happens to carry a fixed suffix (the
// dedup path avoids fixed suffixes for the same reason).
func randomHexSuffix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "tmp"
	}
	return hex.EncodeToString(b[:])
}

// normalizeTextFile reads and rewrites name through root, an *os.Root opened on
// the staging tree. Using os.Root confines all I/O inside that tree at the
// syscall level, so a path that tried to escape (via "..") would be rejected;
// no taint/path-traversal is possible (this is why there is no #nosec here).
func normalizeTextFile(root *os.Root, name string) (bool, error) {
	data, err := root.ReadFile(name)
	if err != nil {
		return false, err
	}
	normalized := bytes.ReplaceAll(data, []byte("\r"), nil)
	if bytes.Equal(data, normalized) {
		return false, nil
	}
	return true, atomicRootRewrite(root, name, normalized)
}

func normalizeConfigFile(root *os.Root, name string) (bool, error) {
	// Config files can be whitespace/ordering-sensitive (e.g. section headers).
	// Only perform safe, semantic-preserving normalization here.
	return normalizeTextFile(root, name)
}

func minifyJSON(root *os.Root, name string) (bool, error) {
	data, err := root.ReadFile(name)
	if err != nil {
		return false, err
	}
	// json.Compact strips only insignificant whitespace at the token level. Unlike
	// an Unmarshal-into-any + Marshal round-trip it preserves number text/precision
	// (no >2^53 rounding), key order and duplicate keys, so the payload stays
	// byte-faithful aside from whitespace (issue #72).
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		return false, err
	}
	minified := buf.Bytes()
	if bytes.Equal(bytes.TrimSpace(data), minified) {
		return false, nil
	}
	return true, atomicRootRewrite(root, name, minified)
}
