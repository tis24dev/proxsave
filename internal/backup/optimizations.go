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
	"sort"
	"strings"

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
		if err := splitFile(path, destBase, chunkSize); err != nil {
			logger.Warning("Failed to chunk %s: %v", path, err)
			return nil
		}

		if err := os.Remove(path); err != nil {
			logger.Warning("Failed to remove original file %s after chunking: %v", path, err)
		} else if err := os.WriteFile(path+".chunked", []byte{}, defaultChunkFilePerm); err != nil {
			logger.Warning("Failed to write chunk marker for %s: %v", path, err)
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

func splitFile(path, destBase string, chunkSize int64) error {
	if err := os.MkdirAll(filepath.Dir(destBase), defaultChunkDirPerm); err != nil {
		return err
	}

	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()

	buf := make([]byte, chunkBufferSize)
	index := 0
	for {
		index++
		chunkPath := fmt.Sprintf("%s.%03d.chunk", destBase, index)
		done, err := writeChunk(in, chunkPath, buf, chunkSize)
		if err != nil {
			return err
		}
		if done {
			break
		}
	}
	return nil
}

func writeChunk(src *os.File, chunkPath string, buf []byte, limit int64) (bool, error) {
	out, err := os.OpenFile(chunkPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, defaultChunkFilePerm)
	if err != nil {
		return false, err
	}
	defer out.Close()

	var written int64
	for written < limit {
		remaining := limit - written
		if remaining < int64(len(buf)) {
			buf = buf[:remaining]
		}
		n, err := src.Read(buf)
		if n > 0 {
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				return false, wErr
			}
			written += int64(n)
		}
		if err != nil {
			if err == io.EOF {
				return true, nil
			}
			return false, err
		}
		if written >= limit {
			return false, nil
		}
	}
	return false, nil
}

func prefilterFiles(ctx context.Context, logger *logging.Logger, root string, maxSize int64) error {
	if maxSize <= 0 {
		maxSize = defaultPrefilterMaxSizeBytes
	}
	logger.Debug("Prefiltering files under %s (max size %d bytes)", root, maxSize)

	var processed int
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

		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() == 0 || info.Size() > maxSize {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".txt", ".log", ".md":
			if err := normalizeTextFile(path); err == nil {
				processed++
			}
		case ".conf", ".cfg", ".ini":
			if err := normalizeConfigFile(path); err == nil {
				processed++
			}
		case ".json":
			if err := minifyJSON(path); err == nil {
				processed++
			}
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("prefilter walk failed: %w", err)
	}

	logger.Info("Prefilter completed: %d files optimized", processed)
	return nil
}

func normalizeTextFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	normalized := bytes.ReplaceAll(data, []byte("\r"), nil)
	if bytes.Equal(data, normalized) {
		return nil
	}
	return os.WriteFile(path, normalized, defaultChunkFilePerm)
}

func normalizeConfigFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	filtered := lines[:0]
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		filtered = append(filtered, line)
	}
	sort.Strings(filtered)
	return os.WriteFile(path, []byte(strings.Join(filtered, "\n")), defaultChunkFilePerm)
}

func minifyJSON(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var tmp any
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	minified, err := json.Marshal(tmp)
	if err != nil {
		return err
	}
	return os.WriteFile(path, minified, defaultChunkFilePerm)
}
