package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

const (
	defaultChunkSizeBytes        = 10 * 1024 * 1024
	defaultChunkThresholdBytes   = 50 * 1024 * 1024
	defaultPrefilterMaxSizeBytes = 8 * 1024 * 1024
	chunkBufferSize              = 1 << 20 // 1 MiB
	defaultChunkDirPerm          = 0o755
	defaultChunkFilePerm         = 0o640
)

type chunkedFileMetadata struct {
	Version         int    `json:"version"`
	SizeBytes       int64  `json:"size_bytes"`
	ChunkSizeBytes  int64  `json:"chunk_size_bytes"`
	ChunkCount      int    `json:"chunk_count"`
	SHA256          string `json:"sha256,omitempty"`
	Mode            uint32 `json:"mode"`
	UID             int    `json:"uid"`
	GID             int    `json:"gid"`
	ModTimeUnixNano int64  `json:"mod_time_unix_nano"`
}

// OptimizationConfig controls optional preprocessing steps executed before archiving.
type OptimizationConfig struct {
	EnableChunking            bool
	EnableDeduplication       bool
	EnablePrefilter           bool
	ChunkSizeBytes            int64
	ChunkThresholdBytes       int64
	PrefilterMaxFileSizeBytes int64
}

// Enabled returns true if at least one optimization is active.
func (c OptimizationConfig) Enabled() bool {
	return c.EnableChunking || c.EnableDeduplication || c.EnablePrefilter
}

// ApplyOptimizations executes the requested optimizations in sequence.
func ApplyOptimizations(ctx context.Context, logger *logging.Logger, root string, cfg OptimizationConfig) error {
	if !cfg.Enabled() {
		return nil
	}

	logger.Info("Running backup optimizations (chunking=%v dedup=%v prefilter=%v)",
		cfg.EnableChunking, cfg.EnableDeduplication, cfg.EnablePrefilter)

	if cfg.EnableDeduplication {
		logger.Debug("Starting deduplication stage")
		if err := deduplicateFiles(ctx, logger, root); err != nil {
			logger.Warning("File deduplication failed: %v", err)
		} else {
			logger.Debug("Deduplication stage completed")
		}
	}

	if cfg.EnablePrefilter {
		logger.Debug("Starting prefilter stage (max file size %d bytes)", cfg.PrefilterMaxFileSizeBytes)
		if err := prefilterFiles(ctx, logger, root, cfg.PrefilterMaxFileSizeBytes); err != nil {
			logger.Warning("Content prefilter failed: %v", err)
		} else {
			logger.Debug("Prefilter stage completed")
		}
	}

	if cfg.EnableChunking {
		logger.Debug("Starting chunking stage (chunk size %d bytes threshold %d bytes)", cfg.ChunkSizeBytes, cfg.ChunkThresholdBytes)
		if err := chunkLargeFiles(ctx, logger, root, cfg.ChunkSizeBytes, cfg.ChunkThresholdBytes); err != nil {
			logger.Warning("Chunking failed: %v", err)
		} else {
			logger.Debug("Chunking stage completed")
		}
	}

	return nil
}

func deduplicateFiles(ctx context.Context, logger *logging.Logger, root string) error {
	logger.Debug("Scanning files for deduplication")

	hashes := make(map[string]string)
	var duplicates int

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
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
		if relErr == nil && shouldSkipDedupPath(rel) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() == 0 {
			return nil
		}

		hash, err := hashFile(path)
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
			logger.Debug("Deduplicated %s → %s", path, existing)
		} else {
			hashes[hash] = path
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("deduplication walk failed: %w", err)
	}

	logger.Info("Deduplication completed: %d duplicates replaced", duplicates)
	return nil
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

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

func replaceWithSymlink(target, duplicate string) error {
	if err := os.Remove(duplicate); err != nil {
		return err
	}
	rel, err := filepath.Rel(filepath.Dir(duplicate), target)
	if err != nil {
		rel = target
	}
	return os.Symlink(rel, duplicate)
}

func chunkLargeFiles(ctx context.Context, logger *logging.Logger, root string, chunkSize, threshold int64) error {
	if chunkSize <= 0 {
		chunkSize = defaultChunkSizeBytes
	}
	if threshold <= 0 {
		threshold = defaultChunkThresholdBytes
	}
	logger.Debug("Scanning %s for files >= %d bytes to chunk (chunk size %d)", root, threshold, chunkSize)

	chunkDir := filepath.Join(root, "chunked_files")
	if err := os.MkdirAll(chunkDir, defaultChunkDirPerm); err != nil {
		return fmt.Errorf("create chunk dir: %w", err)
	}

	var processed int
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if path == chunkDir {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if strings.HasPrefix(path, chunkDir) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		if !info.Mode().IsRegular() || info.Size() < threshold {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		destBase := filepath.Join(chunkDir, rel)

		meta := chunkedFileMetadata{
			Version:         1,
			ChunkSizeBytes:  chunkSize,
			Mode:            uint32(info.Mode()),
			UID:             -1,
			GID:             -1,
			ModTimeUnixNano: info.ModTime().UnixNano(),
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat != nil {
			meta.UID = int(stat.Uid)
			meta.GID = int(stat.Gid)
		}

		result, err := splitFile(path, destBase, chunkSize)
		if err != nil {
			logger.Warning("Failed to chunk %s: %v", path, err)
			return nil
		}
		meta.SizeBytes = result.SizeBytes
		meta.ChunkCount = result.ChunkCount
		meta.SHA256 = result.SHA256

		cleanupChunks := func() {
			chunks, err := discoverChunks(destBase)
			if err != nil {
				return
			}
			for _, c := range chunks {
				_ = os.Remove(c.Path)
			}
		}

		markerPath := path + ".chunked"
		payload, err := json.Marshal(meta)
		if err != nil {
			logger.Warning("Failed to encode chunk metadata for %s: %v", path, err)
			cleanupChunks()
			return nil
		}
		if err := os.WriteFile(markerPath, append(payload, '\n'), defaultChunkFilePerm); err != nil {
			logger.Warning("Failed to write chunk marker for %s: %v", path, err)
			_ = os.Remove(markerPath)
			cleanupChunks()
			return nil
		}
		// Best-effort: preserve the original file's mtime on the marker too.
		mt := time.Unix(0, meta.ModTimeUnixNano)
		_ = os.Chtimes(markerPath, mt, mt)

		if err := os.Remove(path); err != nil {
			logger.Warning("Failed to remove original file %s after chunking: %v", path, err)
			_ = os.Remove(markerPath)
			cleanupChunks()
			return nil
		}
		processed++
		logger.Debug("Chunked %s into %s", path, destBase)
		return nil
	})

	if err != nil {
		return fmt.Errorf("chunking walk failed: %w", err)
	}

	logger.Info("Chunking completed: %d large files processed", processed)
	return nil
}

type splitFileResult struct {
	ChunkCount int
	SizeBytes  int64
	SHA256     string
}

func splitFile(path, destBase string, chunkSize int64) (splitFileResult, error) {
	if err := os.MkdirAll(filepath.Dir(destBase), defaultChunkDirPerm); err != nil {
		return splitFileResult{}, err
	}

	in, err := os.Open(path)
	if err != nil {
		return splitFileResult{}, err
	}
	defer in.Close()

	buf := make([]byte, chunkBufferSize)
	hasher := sha256.New()
	var createdChunks []string
	cleanup := func() {
		for _, p := range createdChunks {
			_ = os.Remove(p)
		}
	}
	chunkCount := 0
	var total int64
	for {
		chunkPath := fmt.Sprintf("%s.%03d.chunk", destBase, chunkCount+1)
		done, n, err := writeChunk(in, chunkPath, buf, chunkSize, hasher)
		if err != nil {
			cleanup()
			return splitFileResult{}, err
		}
		if n > 0 {
			createdChunks = append(createdChunks, chunkPath)
			total += n
			chunkCount++
		}
		if done {
			break
		}
	}
	if chunkCount == 0 {
		return splitFileResult{}, fmt.Errorf("chunking produced no output for %s", path)
	}
	return splitFileResult{
		ChunkCount: chunkCount,
		SizeBytes:  total,
		SHA256:     fmt.Sprintf("%x", hasher.Sum(nil)),
	}, nil
}

func writeChunk(src *os.File, chunkPath string, buf []byte, limit int64, hasher hash.Hash) (bool, int64, error) {
	out, err := os.OpenFile(chunkPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, defaultChunkFilePerm)
	if err != nil {
		return false, 0, err
	}
	defer out.Close()

	var written int64
	for written < limit {
		remaining := limit - written
		toRead := buf
		if remaining < int64(len(toRead)) {
			toRead = toRead[:remaining]
		}
		n, err := src.Read(toRead)
		if n > 0 {
			if _, wErr := out.Write(toRead[:n]); wErr != nil {
				return false, written, wErr
			}
			if hasher != nil {
				if _, hErr := hasher.Write(toRead[:n]); hErr != nil {
					return false, written, hErr
				}
			}
			written += int64(n)
		}
		if err != nil {
			if err == io.EOF {
				if written == 0 {
					_ = out.Close()
					_ = os.Remove(chunkPath)
				}
				return true, written, nil
			}
			return false, written, err
		}
		if written >= limit {
			var probe [1]byte
			n, pErr := src.Read(probe[:])
			if n > 0 {
				if _, sErr := src.Seek(-int64(n), io.SeekCurrent); sErr != nil {
					return false, written, fmt.Errorf("seek after probe: %w", sErr)
				}
				return false, written, nil
			}
			if pErr == io.EOF {
				return true, written, nil
			}
			if pErr != nil {
				return false, written, pErr
			}
			return false, written, fmt.Errorf("unexpected empty read while probing for EOF")
		}
	}
	return false, written, nil
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

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
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

		stats.scanned++
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".txt", ".log", ".md":
			if changed, err := normalizeTextFile(path); err == nil && changed {
				stats.optimized++
			}
		case ".conf", ".cfg", ".ini":
			if isStructuredConfigPath(path) {
				stats.skippedStructured++
				return nil
			}
			if changed, err := normalizeConfigFile(path); err == nil && changed {
				stats.optimized++
			}
		case ".json":
			if changed, err := minifyJSON(path); err == nil && changed {
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

func normalizeTextFile(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	normalized := bytes.ReplaceAll(data, []byte("\r"), nil)
	if bytes.Equal(data, normalized) {
		return false, nil
	}
	return true, os.WriteFile(path, normalized, defaultChunkFilePerm)
}

func normalizeConfigFile(path string) (bool, error) {
	// Config files can be whitespace/ordering-sensitive (e.g. section headers).
	// Only perform safe, semantic-preserving normalization:
	//   1. Strip UTF-8 BOM
	//   2. Normalize CRLF → LF, stray CR → LF
	//   3. Strip trailing whitespace from each line
	//   4. Consolidate trailing newlines to exactly one
	// Line order, leading indentation, comments, and blank lines between
	// sections are preserved.
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if len(data) == 0 {
		return false, nil
	}

	normalized := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	normalized = bytes.ReplaceAll(normalized, []byte("\r\n"), []byte("\n"))
	normalized = bytes.ReplaceAll(normalized, []byte("\r"), []byte("\n"))

	lines := bytes.Split(normalized, []byte("\n"))
	for i, line := range lines {
		lines[i] = bytes.TrimRight(line, " \t")
	}
	normalized = bytes.Join(lines, []byte("\n"))

	normalized = append(bytes.TrimRight(normalized, "\n"), '\n')

	if bytes.Equal(data, normalized) {
		return false, nil
	}
	return true, os.WriteFile(path, normalized, defaultChunkFilePerm)
}

func minifyJSON(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		return false, err
	}
	compacted := buf.Bytes()
	if bytes.Equal(data, compacted) {
		return false, nil
	}
	return true, os.WriteFile(path, compacted, defaultChunkFilePerm)
}

// ReassembleChunkedFiles locates .chunked marker files under root,
// concatenates the matching .NNN.chunk fragments from the chunked_files
// directory, writes the reassembled file, and cleans up markers and chunks.
func ReassembleChunkedFiles(logger *logging.Logger, root string) error {
	chunkDir := filepath.Join(root, "chunked_files")
	if _, err := os.Stat(chunkDir); os.IsNotExist(err) {
		return nil
	}

	var markers []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path == chunkDir {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".chunked") {
			markers = append(markers, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk for chunk markers: %w", err)
	}

	if len(markers) == 0 {
		return nil
	}

	var reassembled int
	var incomplete bool

	for _, marker := range markers {
		originalPath := strings.TrimSuffix(marker, ".chunked")
		rel, err := filepath.Rel(root, originalPath)
		if err != nil {
			logger.Warning("Failed to compute rel path for %s: %v", originalPath, err)
			incomplete = true
			continue
		}
		chunkBase := filepath.Join(chunkDir, rel)

		chunks, err := discoverChunks(chunkBase)
		if err != nil || len(chunks) == 0 {
			logger.Warning("No chunks found for %s (base=%s): %v", rel, chunkBase, err)
			incomplete = true
			continue
		}

		meta, err := readChunkedFileMetadata(marker)
		if err != nil {
			logger.Warning("Chunk marker metadata unreadable for %s: %v", rel, err)
			incomplete = true
			continue
		}

		ambiguous, err := validateChunkSet(meta, chunks)
		if err != nil {
			logger.Warning("Chunk set incomplete for %s: %v", rel, err)
			incomplete = true
			continue
		}
		if meta == nil && ambiguous {
			logger.Warning("Legacy chunk marker without metadata for %s; reassembly cannot fully verify completeness", rel)
		}

		if err := concatenateChunks(originalPath, chunks, meta); err != nil {
			logger.Warning("Failed to reassemble %s: %v", rel, err)
			incomplete = true
			continue
		}
		if meta != nil {
			applyChunkedMetadata(logger, originalPath, meta)
		}

		_ = os.Remove(marker)
		for _, c := range chunks {
			_ = os.Remove(c.Path)
		}
		logger.Debug("Reassembled %s from %d chunks", rel, len(chunks))
		reassembled++
	}

	// Remove chunked_files dir tree if now empty.
	if reassembled > 0 && !incomplete {
		removeEmptyDirs(chunkDir)
		_ = os.Remove(chunkDir)
	}

	return nil
}

type chunkInfo struct {
	Index int
	Path  string
}

// discoverChunks returns the numerically-sorted list of chunks for a base.
// Chunks are named <base>.<index>.chunk where <index> is a positive integer.
func discoverChunks(base string) ([]chunkInfo, error) {
	dir := filepath.Dir(base)
	prefix := filepath.Base(base) + "."

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var chunks []chunkInfo
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".chunk") {
			continue
		}
		idxStr := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".chunk")
		idx, err := strconv.Atoi(idxStr)
		if err != nil || idx <= 0 {
			continue
		}
		chunks = append(chunks, chunkInfo{Index: idx, Path: filepath.Join(dir, name)})
	}

	sort.Slice(chunks, func(i, j int) bool { return chunks[i].Index < chunks[j].Index })
	return chunks, nil
}

func concatenateChunks(dest string, chunks []chunkInfo, meta *chunkedFileMetadata) error {
	if err := os.MkdirAll(filepath.Dir(dest), defaultChunkDirPerm); err != nil {
		return err
	}

	tmpDir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(tmpDir, "."+filepath.Base(dest)+".reassemble-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := os.Chmod(tmpPath, defaultChunkFilePerm); err != nil {
		tmp.Close()
		return err
	}

	buf := make([]byte, chunkBufferSize)

	var hasher hash.Hash
	if meta != nil && meta.SHA256 != "" {
		hasher = sha256.New()
	}

	var written int64
	for _, chunk := range chunks {
		in, err := os.Open(chunk.Path)
		if err != nil {
			tmp.Close()
			return err
		}
		var dst io.Writer = tmp
		if hasher != nil {
			dst = io.MultiWriter(tmp, hasher)
		}
		n, err := io.CopyBuffer(dst, in, buf)
		if cErr := in.Close(); cErr != nil && err == nil {
			err = cErr
		}
		if err != nil {
			tmp.Close()
			return err
		}
		written += n
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	if meta != nil {
		if meta.SizeBytes > 0 && written != meta.SizeBytes {
			return fmt.Errorf("size mismatch after reassembly: got %d bytes, expected %d", written, meta.SizeBytes)
		}
		if hasher != nil {
			got := fmt.Sprintf("%x", hasher.Sum(nil))
			if got != meta.SHA256 {
				return fmt.Errorf("sha256 mismatch after reassembly")
			}
		}
	}

	if err := os.Rename(tmpPath, dest); err != nil {
		return err
	}
	tmpPath = ""
	return nil
}

func removeEmptyDirs(root string) {
	var dirs []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i])
	}
}

func readChunkedFileMetadata(markerPath string) (*chunkedFileMetadata, error) {
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}

	var meta chunkedFileMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	if meta.Version != 1 {
		return nil, fmt.Errorf("unsupported chunk metadata version %d", meta.Version)
	}
	if meta.ChunkCount <= 0 || meta.ChunkSizeBytes <= 0 || meta.SizeBytes <= 0 {
		return nil, fmt.Errorf("invalid chunk metadata (count=%d chunkSize=%d size=%d)", meta.ChunkCount, meta.ChunkSizeBytes, meta.SizeBytes)
	}
	return &meta, nil
}

func validateChunkSet(meta *chunkedFileMetadata, chunks []chunkInfo) (bool, error) {
	if len(chunks) == 0 {
		return false, fmt.Errorf("no chunk files present")
	}

	for i, c := range chunks {
		want := i + 1
		if c.Index != want {
			return false, fmt.Errorf("missing or out-of-order chunk: expected index %d, got %d", want, c.Index)
		}
	}

	if meta == nil {
		// Legacy (empty marker): best-effort structural validation.
		var chunkSize int64
		sizes := make([]int64, len(chunks))
		for i, c := range chunks {
			info, err := os.Stat(c.Path)
			if err != nil {
				return false, fmt.Errorf("stat chunk %s: %w", c.Path, err)
			}
			if !info.Mode().IsRegular() {
				return false, fmt.Errorf("chunk is not a regular file: %s", c.Path)
			}
			sizes[i] = info.Size()
			if sizes[i] > chunkSize {
				chunkSize = sizes[i]
			}
		}
		if chunkSize <= 0 {
			return false, fmt.Errorf("invalid chunk size inferred")
		}
		for i := 0; i < len(sizes)-1; i++ {
			if sizes[i] != chunkSize {
				return false, fmt.Errorf("chunk size mismatch for index %d: got %d, expected %d", i+1, sizes[i], chunkSize)
			}
		}
		last := sizes[len(sizes)-1]
		if last <= 0 || last > chunkSize {
			return false, fmt.Errorf("last chunk size invalid: %d (chunkSize=%d)", last, chunkSize)
		}
		return last == chunkSize, nil
	}

	if meta.ChunkCount != len(chunks) {
		return false, fmt.Errorf("chunk count mismatch: expected %d, found %d", meta.ChunkCount, len(chunks))
	}

	for i, c := range chunks {
		info, err := os.Stat(c.Path)
		if err != nil {
			return false, fmt.Errorf("stat chunk %s: %w", c.Path, err)
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("chunk is not a regular file: %s", c.Path)
		}
		expected := meta.ChunkSizeBytes
		if i == meta.ChunkCount-1 {
			expected = meta.SizeBytes - meta.ChunkSizeBytes*int64(meta.ChunkCount-1)
		}
		if expected <= 0 {
			return false, fmt.Errorf("invalid expected chunk size for index %d", i+1)
		}
		if info.Size() != expected {
			return false, fmt.Errorf("chunk size mismatch for index %d: got %d, expected %d", i+1, info.Size(), expected)
		}
	}

	return false, nil
}

func applyChunkedMetadata(logger *logging.Logger, destPath string, meta *chunkedFileMetadata) {
	if meta == nil {
		return
	}

	if meta.UID >= 0 || meta.GID >= 0 {
		uid := meta.UID
		gid := meta.GID
		if uid < 0 {
			uid = -1
		}
		if gid < 0 {
			gid = -1
		}
		if err := os.Chown(destPath, uid, gid); err != nil {
			logger.Debug("Failed to chown reassembled file %s: %v", destPath, err)
		}
	}

	if err := os.Chmod(destPath, os.FileMode(meta.Mode)); err != nil {
		logger.Debug("Failed to chmod reassembled file %s: %v", destPath, err)
	}

	mt := time.Unix(0, meta.ModTimeUnixNano)
	if err := os.Chtimes(destPath, mt, mt); err != nil {
		logger.Debug("Failed to set timestamps on reassembled file %s: %v", destPath, err)
	}
}
