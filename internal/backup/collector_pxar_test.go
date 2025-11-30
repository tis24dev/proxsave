package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestDownsampleRoots(t *testing.T) {
	roots := []string{"a", "b", "c", "d"}
	if got := downsampleRoots(roots, 0); !reflect.DeepEqual(got, roots) {
		t.Fatalf("limit=0 should return original slice")
	}
	limited := downsampleRoots(roots, 2)
	if len(limited) != 2 {
		t.Fatalf("expected limited slice len 2, got %d", len(limited))
	}
	seen := map[string]bool{}
	for _, r := range limited {
		if seen[r] {
			t.Fatalf("duplicate in downsampled roots: %s", r)
		}
		seen[r] = true
	}
}

func TestDeterministicShuffleAndSeed(t *testing.T) {
	items := []string{"one", "two", "three"}
	seed := deterministicSeed("a", "b")
	seed2 := deterministicSeed("a", "b", "c")
	if seed == seed2 {
		t.Fatalf("different seed inputs should differ")
	}

	first := append([]string(nil), items...)
	second := append([]string(nil), items...)
	shuffleStringsDeterministic(first, seed)
	shuffleStringsDeterministic(second, seed)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("shuffle with same seed should be deterministic")
	}
}

func TestPxarRootSelector(t *testing.T) {
	sel := newPxarRootSelector(2)
	for _, p := range []string{"a", "b", "c", "d"} {
		sel.consider(p)
	}
	results := sel.results()
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if sel.total != 4 || !sel.capped {
		t.Fatalf("selector total=%d capped=%v want total=4 capped=true", sel.total, sel.capped)
	}
}

func TestHashPathAndUniquePaths(t *testing.T) {
	if hashPath("foo") == hashPath("bar") {
		t.Fatalf("expected different hashes for different inputs")
	}
	paths := []string{"a", "a", "b"}
	unique := uniquePaths(paths)
	if len(unique) != 2 || unique[0] != "a" || unique[1] != "b" {
		t.Fatalf("uniquePaths failed: %#v", unique)
	}
}

func TestSampleFilesRespectsPatternsAndLimit(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, GetDefaultCollectorConfig(), root, types.ProxmoxBS, false)

	mk := func(rel, content string) {
		path := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte(content), 0o640)
	}
	mk("keep1.txt", "data")
	mk("skip.log", "data")
	mk(filepath.Join("nested", "keep2.txt"), "data")

	ctx := context.Background()
	include := []string{"*.txt"}
	exclude := []string{"skip*"}

	results, err := c.sampleFiles(ctx, root, include, exclude, 3, 2)
	if err != nil {
		t.Fatalf("sampleFiles error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (limit), got %d", len(results))
	}
	for _, r := range results {
		if filepath.Ext(r.RelativePath) != ".txt" {
			t.Fatalf("unexpected file in results: %+v", r)
		}
		if r.SizeHuman == "" {
			t.Fatalf("SizeHuman should be set")
		}
	}
}

func TestSampleDirectoriesDepthAndLimit(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, GetDefaultCollectorConfig(), root, types.ProxmoxVE, false)

	makeDir := func(rel string) {
		_ = os.MkdirAll(filepath.Join(root, rel), 0o755)
	}
	makeDir("a/b")
	makeDir("c")
	makeDir("d/e/f")

	key := fmt.Sprintf("%s|fanout=%d|max=%d", root, c.config.PxarScanFanoutLevel, c.config.PxarScanMaxRoots)
	c.rootsCache[key] = []string{root}

	ctx := context.Background()
	dirs, err := c.sampleDirectories(ctx, root, 1, 2)
	if err != nil {
		t.Fatalf("sampleDirectories error: %v", err)
	}
	if len(dirs) != 2 {
		t.Fatalf("expected limit 2, got %d", len(dirs))
	}
	for _, d := range dirs {
		if strings.Count(d, "/") > 0 {
			t.Fatalf("expected depth < 1, got %s", d)
		}
	}
}
