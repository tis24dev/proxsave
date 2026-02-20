package backup

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/safefs"
)

func (c *Collector) sampleDirectoriesBounded(ctx context.Context, root string, maxDepth, limit int, ioTimeout time.Duration) ([]string, error) {
	results := make([]string, 0, limit)
	if limit <= 0 || maxDepth <= 0 {
		return results, nil
	}

	root = filepath.Clean(root)
	stack := []string{root}

	for len(stack) > 0 && len(results) < limit {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		dirPath := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		entries, err := safefs.ReadDir(ctx, dirPath, ioTimeout)
		if err != nil {
			return results, err
		}

		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return results, err
			}
			if !entry.IsDir() {
				continue
			}
			child := filepath.Join(dirPath, entry.Name())
			if c.shouldExclude(child) {
				continue
			}

			rel, relErr := filepath.Rel(root, child)
			if relErr != nil || rel == "." || strings.HasPrefix(rel, "..") {
				continue
			}
			rel = filepath.ToSlash(rel)
			depth := strings.Count(rel, "/")
			if depth >= maxDepth {
				continue
			}

			results = append(results, rel)
			if len(results) >= limit {
				break
			}
			if depth < maxDepth-1 {
				stack = append(stack, child)
			}
		}
	}

	return results, nil
}

func (c *Collector) sampleFilesBounded(ctx context.Context, root string, includePatterns, excludePatterns []string, maxDepth, limit int, ioTimeout time.Duration) ([]FileSummary, error) {
	results := make([]FileSummary, 0, limit)
	if limit <= 0 {
		return results, nil
	}

	root = filepath.Clean(root)
	stack := []string{root}

	for len(stack) > 0 && len(results) < limit {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		dirPath := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		entries, err := safefs.ReadDir(ctx, dirPath, ioTimeout)
		if err != nil {
			return results, err
		}

		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return results, err
			}

			name := entry.Name()
			full := filepath.Join(dirPath, name)
			if c.shouldExclude(full) {
				continue
			}

			if entry.IsDir() {
				rel, relErr := filepath.Rel(root, full)
				if relErr != nil || rel == "." || strings.HasPrefix(rel, "..") {
					continue
				}
				rel = filepath.ToSlash(rel)
				depth := strings.Count(rel, "/")
				if depth >= maxDepth {
					continue
				}
				stack = append(stack, full)
				continue
			}

			rel, relErr := filepath.Rel(root, full)
			if relErr != nil || rel == "." || strings.HasPrefix(rel, "..") {
				continue
			}

			if len(excludePatterns) > 0 && matchAnyPattern(excludePatterns, name, rel) {
				continue
			}
			if len(includePatterns) > 0 && !matchAnyPattern(includePatterns, name, rel) {
				continue
			}

			info, err := safefs.Stat(ctx, full, ioTimeout)
			if err != nil {
				if errors.Is(err, safefs.ErrTimeout) {
					return results, err
				}
				continue
			}

			results = append(results, FileSummary{
				RelativePath: filepath.ToSlash(rel),
				SizeBytes:    info.Size(),
				SizeHuman:    FormatBytes(info.Size()),
				ModTime:      info.ModTime(),
			})
			if len(results) >= limit {
				break
			}
		}
	}

	return results, nil
}
