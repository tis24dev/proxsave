package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
	Path string `json:"path"` // path relative to the archive root, slash-separated
	Mode uint32 `json:"mode"` // original regular-file permission bits
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

// ApplyOptimizations executes the requested optimizations in sequence.
func ApplyOptimizations(ctx context.Context, logger *logging.Logger, root string, cfg OptimizationConfig) error {
	if !cfg.Enabled() {
		return nil
	}

	logger.Info("Running backup optimizations (dedup=%v prefilter=%v)",
		cfg.EnableDeduplication, cfg.EnablePrefilter)

	if cfg.EnableDeduplication {
		logger.Debug("Starting deduplication stage")
		if err := deduplicateFiles(ctx, logger, root); err != nil {
			// A dedup error means the staging tree may still hold symlinks the restore
			// cannot materialize (manifest unwritten, partial revert): fail rather than
			// archive a tree that would lose fidelity on restore (issue #70). The
			// happy path and a fully-reverted manifest failure both return nil.
			return fmt.Errorf("deduplication: %w", err)
		}
		logger.Debug("Deduplication stage completed")
	}

	if cfg.EnablePrefilter {
		logger.Debug("Starting prefilter stage (max file size %d bytes)", cfg.PrefilterMaxFileSizeBytes)
		if err := prefilterFiles(ctx, logger, root, cfg.PrefilterMaxFileSizeBytes); err != nil {
			logger.Warning("Content prefilter failed: %v", err)
		} else {
			logger.Debug("Prefilter stage completed")
		}
	}

	return nil
}

func deduplicateFiles(ctx context.Context, logger *logging.Logger, root string) error {
	logger.Debug("Scanning files for deduplication")

	hashes := make(map[string]string)
	var duplicates int
	var manifest []DedupManifestEntry
	var replaced []dedupReplacement

	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open dedup root: %w", err)
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
			manifest = append(manifest, DedupManifestEntry{
				Path: filepath.ToSlash(rel),
				Mode: uint32(info.Mode().Perm()),
			})
			replaced = append(replaced, dedupReplacement{
				duplicate: path,
				canonical: existing,
				mode:      info.Mode().Perm(),
			})
			logger.Debug("Deduplicated %s → %s", path, existing)
		} else {
			hashes[hash] = path
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("deduplication walk failed: %w", err)
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
			return fmt.Errorf("write dedup manifest: %w (reverted %d/%d symlinks)", err, reverted, len(replaced))
		}
		logger.Info("Deduplication aborted (manifest unwritable); %d symlink(s) reverted to regular files", reverted)
		return nil
	}

	logger.Info("Deduplication completed: %d duplicates replaced", duplicates)
	return nil
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

func prefilterFiles(ctx context.Context, logger *logging.Logger, root string, maxSize int64) error {
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
		return fmt.Errorf("open prefilter root: %w", err)
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
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".txt", ".log", ".md":
			if changed, err := normalizeTextFile(rootFS, rel); err == nil && changed {
				stats.optimized++
			}
		case ".conf", ".cfg", ".ini":
			if isStructuredConfigPath(path) {
				stats.skippedStructured++
				return nil
			}
			if changed, err := normalizeConfigFile(rootFS, rel); err == nil && changed {
				stats.optimized++
			}
		case ".json":
			if isStructuredConfigPath(path) {
				stats.skippedStructured++
				return nil
			}
			if changed, err := minifyJSON(rootFS, rel); err == nil && changed {
				stats.optimized++
			}
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("prefilter walk failed: %w", err)
	}

	logger.Info("Prefilter completed: optimized=%d scanned=%d skipped_structured=%d skipped_symlink=%d", stats.optimized, stats.scanned, stats.skippedStructured, stats.skippedSymlink)
	return nil
}

// normalizeTextFile reads and rewrites name through root, an *os.Root opened on
// the staging tree. Using os.Root confines all I/O inside that tree at the
// syscall level, so a path that tried to escape (via "..") would be rejected —
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
	return true, root.WriteFile(name, normalized, defaultOptimizedFilePerm)
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
	return true, root.WriteFile(name, minified, defaultOptimizedFilePerm)
}
