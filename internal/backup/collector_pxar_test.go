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

type closedDoneContext struct {
	context.Context
	done chan struct{}
	err  error
}

func newClosedDoneContext(err error) *closedDoneContext {
	ch := make(chan struct{})
	close(ch)
	return &closedDoneContext{
		Context: context.Background(),
		done:    ch,
		err:     err,
	}
}

func (c *closedDoneContext) Done() <-chan struct{} { return c.done }
func (c *closedDoneContext) Err() error            { return c.err }

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

func TestPxarRootSelectorLimitZeroReturnsAllUnique(t *testing.T) {
	sel := newPxarRootSelector(0)
	for _, p := range []string{"a", "a", "b"} {
		sel.consider(p)
	}
	results := sel.results()
	if len(results) != 2 {
		t.Fatalf("expected unique results, got %v", results)
	}
}

func TestPxarRootSelectorSkipsReplacementForHighWeightCandidate(t *testing.T) {
	p1, p2 := "a", "b"
	if hashPath(p1) > hashPath(p2) {
		p1, p2 = p2, p1
	}

	sel := newPxarRootSelector(1)
	sel.consider(p1)
	sel.consider(p2) // higher weight => should be ignored

	results := sel.results()
	if len(results) != 1 || results[0] != p1 {
		t.Fatalf("expected selector to keep low-weight %q, got %v", p1, results)
	}
	if !sel.capped {
		t.Fatalf("expected selector capped=true")
	}
}

func TestRecomputeMaxHandlesEmptyItems(t *testing.T) {
	sel := newPxarRootSelector(1)
	sel.items = nil
	sel.recomputeMax()
	if sel.maxIdx != -1 || sel.maxWeight != 0 {
		t.Fatalf("unexpected recompute state: maxIdx=%d maxWeight=%d", sel.maxIdx, sel.maxWeight)
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

func TestUniquePathsEmptyInput(t *testing.T) {
	if got := uniquePaths(nil); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
	if got := uniquePaths([]string{}); len(got) != 0 {
		t.Fatalf("expected empty slice, got %#v", got)
	}
}

func TestDownsampleRootsStepOneReturnsPrefix(t *testing.T) {
	roots := []string{"a", "b", "c"}
	got := downsampleRoots(roots, 2)
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("expected prefix, got %#v", got)
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

func TestSampleFilesLimitZeroReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, GetDefaultCollectorConfig(), root, types.ProxmoxBS, false)

	results, err := c.sampleFiles(context.Background(), root, nil, nil, 3, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty result, got %d", len(results))
	}
}

func TestSampleFilesReadDirErrorPropagates(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxBS, false)

	_, err := c.sampleFiles(context.Background(), filepath.Join(t.TempDir(), "missing"), nil, nil, 3, 1)
	if err == nil {
		t.Fatalf("expected error for missing root")
	}
}

func TestSampleFilesLimitTriggersDuringTopLevelScan(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, GetDefaultCollectorConfig(), root, types.ProxmoxBS, false)

	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("file-%d.txt", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	results, err := c.sampleFiles(context.Background(), root, nil, nil, 3, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result due to limit, got %d", len(results))
	}
}

func TestSampleFilesReturnsWhenNoWorkerRoots(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, GetDefaultCollectorConfig(), root, types.ProxmoxBS, false)

	if err := os.WriteFile(filepath.Join(root, "top.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write top.txt: %v", err)
	}

	results, err := c.sampleFiles(context.Background(), root, nil, nil, 3, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestSampleFilesUsesDefaultWorkerLimitWhenZero(t *testing.T) {
	root := t.TempDir()
	cfg := GetDefaultCollectorConfig()
	cfg.PxarIntraConcurrency = 0

	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, cfg, t.TempDir(), types.ProxmoxBS, false)

	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "n.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write n.txt: %v", err)
	}

	key := fmt.Sprintf("%s|fanout=%d|max=%d", root, c.config.PxarScanFanoutLevel, c.config.PxarScanMaxRoots)
	c.rootsCache[key] = []string{root}

	results, err := c.sampleFiles(context.Background(), root, nil, nil, 3, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected results from nested walk")
	}
}

func TestSampleFilesSkipsCollectorExcludeAndNonMatchingIncludeAndBrokenSymlinkInfo(t *testing.T) {
	root := t.TempDir()
	cfg := GetDefaultCollectorConfig()
	cfg.ExcludePatterns = []string{"excluded.txt"}

	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, cfg, t.TempDir(), types.ProxmoxBS, false)

	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write keep.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "excluded.txt"), []byte("no"), 0o644); err != nil {
		t.Fatalf("write excluded.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "skip.log"), []byte("log"), 0o644); err != nil {
		t.Fatalf("write skip.log: %v", err)
	}
	if err := os.Symlink("missing-target", filepath.Join(root, "broken")); err != nil {
		t.Fatalf("symlink broken: %v", err)
	}

	results, err := c.sampleFiles(context.Background(), root, []string{"*.txt"}, nil, 3, 10)
	if err != nil {
		t.Fatalf("sampleFiles error: %v", err)
	}
	if len(results) != 1 || results[0].RelativePath != "keep.txt" {
		t.Fatalf("expected only keep.txt, got %#v", results)
	}
}

func TestSampleFilesSkipsExcludedDirsAndRespectsMaxDepthInWorkerWalk(t *testing.T) {
	root := t.TempDir()
	cfg := GetDefaultCollectorConfig()
	cfg.ExcludePatterns = []string{"skip"}

	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, cfg, t.TempDir(), types.ProxmoxBS, false)

	if err := os.MkdirAll(filepath.Join(root, "skip", "inner"), 0o755); err != nil {
		t.Fatalf("mkdir skip/inner: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "skip", "inner", "skip.txt"), []byte("no"), 0o644); err != nil {
		t.Fatalf("write skip file: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(root, "deep", "inner"), 0o755); err != nil {
		t.Fatalf("mkdir deep/inner: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "deep", "ok.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write deep/ok.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "deep", "inner", "too-deep.txt"), []byte("no"), 0o644); err != nil {
		t.Fatalf("write deep/inner/too-deep.txt: %v", err)
	}

	key := fmt.Sprintf("%s|fanout=%d|max=%d", root, c.config.PxarScanFanoutLevel, c.config.PxarScanMaxRoots)
	c.rootsCache[key] = []string{root}

	results, err := c.sampleFiles(context.Background(), root, nil, nil, 1, 10)
	if err != nil {
		t.Fatalf("sampleFiles error: %v", err)
	}

	paths := make([]string, 0, len(results))
	for _, r := range results {
		paths = append(paths, r.RelativePath)
	}
	if !reflect.DeepEqual(paths, []string{"deep/ok.txt"}) {
		t.Fatalf("expected only deep/ok.txt due to exclusions and maxDepth, got %v", paths)
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

func TestComputePxarWorkerRootsFallbackToIntermediateLevelAndDownsamples(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{
		"a/a1",
		"a/a2",
		"b/b1",
		"c/c1",
		"d/d1",
	} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PxarScanFanoutLevel = 3
	cfg.PxarScanMaxRoots = 2
	cfg.PxarEnumWorkers = 1

	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, cfg, t.TempDir(), types.ProxmoxBS, false)

	ctx := context.Background()
	roots, err := c.computePxarWorkerRoots(ctx, root, "fallback-test")
	if err != nil {
		t.Fatalf("computePxarWorkerRoots error: %v", err)
	}
	if len(roots) != 2 {
		t.Fatalf("expected downsampled roots len 2, got %d (%v)", len(roots), roots)
	}
	for _, r := range roots {
		if _, err := os.Stat(r); err != nil {
			t.Fatalf("expected root to exist (%s): %v", r, err)
		}
		rel, err := filepath.Rel(root, r)
		if err != nil {
			t.Fatalf("rel error: %v", err)
		}
		if strings.Count(rel, string(filepath.Separator)) != 1 {
			t.Fatalf("expected fallback roots at depth 2, got %s (rel=%s)", r, rel)
		}
	}
}

func TestSampleDirectoriesLimitZeroReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, GetDefaultCollectorConfig(), root, types.ProxmoxVE, false)

	results, err := c.sampleDirectories(context.Background(), root, 2, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty result, got %d", len(results))
	}
}

func TestSampleDirectoriesReturnsWhenNoWorkerRoots(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, GetDefaultCollectorConfig(), root, types.ProxmoxVE, false)

	results, err := c.sampleDirectories(context.Background(), root, 2, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty result, got %d", len(results))
	}
}

func TestSampleDirectoriesStopsAtLimit(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, GetDefaultCollectorConfig(), root, types.ProxmoxVE, false)

	for _, d := range []string{"a", "b", "c"} {
		if err := os.MkdirAll(filepath.Join(root, d, "x"), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	key := fmt.Sprintf("%s|fanout=%d|max=%d", root, c.config.PxarScanFanoutLevel, c.config.PxarScanMaxRoots)
	c.rootsCache[key] = []string{root}

	dirs, err := c.sampleDirectories(context.Background(), root, 3, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dirs) != 1 {
		t.Fatalf("expected 1 result due to limit, got %d", len(dirs))
	}
}

func TestSampleDirectoriesReturnsErrorWhenWorkerStartDirMissing(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxVE, false)

	key := fmt.Sprintf("%s|fanout=%d|max=%d", root, c.config.PxarScanFanoutLevel, c.config.PxarScanMaxRoots)
	c.rootsCache[key] = []string{filepath.Join(root, "missing")}

	_, err := c.sampleDirectories(context.Background(), root, 2, 10)
	if err == nil {
		t.Fatalf("expected error when startDir is missing")
	}
}

func TestSampleDirectoriesUsesDefaultWorkerLimitAndSkipsExcludedDirs(t *testing.T) {
	root := t.TempDir()
	cfg := GetDefaultCollectorConfig()
	cfg.PxarIntraConcurrency = 0
	// shouldExclude() tests patterns against multiple "candidates" including the basename,
	// so using "skip" reliably excludes the directory itself and thus its subtree via SkipDir.
	cfg.ExcludePatterns = []string{"skip"}

	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, cfg, t.TempDir(), types.ProxmoxVE, false)

	for _, d := range []string{"keep/inner", "skip/inner"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	key := fmt.Sprintf("%s|fanout=%d|max=%d", root, c.config.PxarScanFanoutLevel, c.config.PxarScanMaxRoots)
	c.rootsCache[key] = []string{root}

	dirs, err := c.sampleDirectories(context.Background(), root, 3, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, d := range dirs {
		if strings.HasPrefix(d, "skip") {
			t.Fatalf("expected excluded directories to be skipped, got %v", dirs)
		}
	}
}

func TestSampleDirectoriesReturnsNilOnCanceledContextWithoutStartingWorkers(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxVE, false)

	if err := os.MkdirAll(filepath.Join(root, "keep"), 0o755); err != nil {
		t.Fatalf("mkdir keep: %v", err)
	}
	key := fmt.Sprintf("%s|fanout=%d|max=%d", root, c.config.PxarScanFanoutLevel, c.config.PxarScanMaxRoots)
	c.rootsCache[key] = []string{root}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dirs, err := c.sampleDirectories(ctx, root, 2, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dirs) != 0 {
		t.Fatalf("expected empty result, got %v", dirs)
	}
}

func TestSampleDirectoriesReturnsContextErrorWhenNotCanceled(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxVE, false)

	if err := os.MkdirAll(filepath.Join(root, "keep"), 0o755); err != nil {
		t.Fatalf("mkdir keep: %v", err)
	}
	key := fmt.Sprintf("%s|fanout=%d|max=%d", root, c.config.PxarScanFanoutLevel, c.config.PxarScanMaxRoots)
	c.rootsCache[key] = []string{root}

	errBoom := fmt.Errorf("boom")
	_, err := c.sampleDirectories(newClosedDoneContext(errBoom), root, 2, 10)
	if err == nil || err.Error() != errBoom.Error() {
		t.Fatalf("expected %v, got %v", errBoom, err)
	}
}

func TestSampleDirectoriesSkipsExcludedFiles(t *testing.T) {
	root := t.TempDir()
	cfg := GetDefaultCollectorConfig()
	cfg.ExcludePatterns = []string{"skip.txt"}

	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, cfg, t.TempDir(), types.ProxmoxVE, false)

	if err := os.MkdirAll(filepath.Join(root, "keep"), 0o755); err != nil {
		t.Fatalf("mkdir keep: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "skip.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatalf("write skip.txt: %v", err)
	}
	key := fmt.Sprintf("%s|fanout=%d|max=%d", root, c.config.PxarScanFanoutLevel, c.config.PxarScanMaxRoots)
	c.rootsCache[key] = []string{root}

	dirs, err := c.sampleDirectories(context.Background(), root, 2, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	foundKeep := false
	for _, d := range dirs {
		if d == "keep" {
			foundKeep = true
		}
	}
	if !foundKeep {
		t.Fatalf("expected keep in results, got %v", dirs)
	}
}

func TestComputePxarWorkerRootsNormalizesDefaults(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatalf("mkdir a: %v", err)
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PxarScanFanoutLevel = 0
	cfg.PxarScanMaxRoots = 0
	cfg.PxarEnumWorkers = 0
	cfg.PxarEnumBudgetMs = 1

	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, cfg, t.TempDir(), types.ProxmoxBS, false)

	roots, err := c.computePxarWorkerRoots(context.Background(), root, "defaults")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(roots) == 0 {
		t.Fatalf("expected some roots, got %v", roots)
	}
}

func TestComputePxarWorkerRootsReturnsNilWhenNoDirsFound(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxBS, false)

	roots, err := c.computePxarWorkerRoots(context.Background(), root, "empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if roots != nil {
		t.Fatalf("expected nil roots, got %v", roots)
	}
}

func TestComputePxarWorkerRootsCapsAndSkipsExcludedChildren(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"keep1", "keep2", "skip"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	cfg := GetDefaultCollectorConfig()
	cfg.ExcludePatterns = []string{"skip"}
	cfg.PxarScanFanoutLevel = 1
	cfg.PxarScanMaxRoots = 1
	cfg.PxarEnumWorkers = 1
	cfg.PxarStopOnCap = false

	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, cfg, t.TempDir(), types.ProxmoxBS, false)

	roots, err := c.computePxarWorkerRoots(context.Background(), root, "cap-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("expected 1 root due to cap, got %v", roots)
	}
	for _, r := range roots {
		if strings.Contains(r, string(filepath.Separator)+"skip") || filepath.Base(r) == "skip" {
			t.Fatalf("expected excluded dir not to be returned, got %v", roots)
		}
	}
}

func TestComputePxarWorkerRootsBudgetExceededReturnsNil(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PxarScanFanoutLevel = 2
	cfg.PxarEnumBudgetMs = 1

	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, cfg, t.TempDir(), types.ProxmoxBS, false)

	roots, err := c.computePxarWorkerRoots(newClosedDoneContext(context.DeadlineExceeded), t.TempDir(), "budget-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if roots != nil {
		t.Fatalf("expected nil roots, got %v", roots)
	}
}

func TestComputePxarWorkerRootsDebugProgressStopsOnChannelClose(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatalf("mkdir a: %v", err)
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PxarScanFanoutLevel = 1
	cfg.PxarScanMaxRoots = 1
	cfg.PxarEnumWorkers = 1

	logger := logging.New(types.LogLevelDebug, false)
	c := NewCollector(logger, cfg, t.TempDir(), types.ProxmoxBS, false)

	roots, err := c.computePxarWorkerRoots(context.Background(), root, "debug-progress")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(roots) == 0 {
		t.Fatalf("expected some roots, got %v", roots)
	}
}

func TestComputePxarWorkerRootsDebugProgressStopsOnCtxDone(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PxarScanFanoutLevel = 1

	logger := logging.New(types.LogLevelDebug, false)
	c := NewCollector(logger, cfg, t.TempDir(), types.ProxmoxBS, false)

	roots, err := c.computePxarWorkerRoots(newClosedDoneContext(context.DeadlineExceeded), t.TempDir(), "debug-ctxdone")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if roots != nil {
		t.Fatalf("expected nil roots, got %v", roots)
	}
}

func TestSampleFilesReturnsErrorWhenWorkerStartDirMissing(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)
	c := NewCollector(logger, GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxBS, false)

	key := fmt.Sprintf("%s|fanout=%d|max=%d", root, c.config.PxarScanFanoutLevel, c.config.PxarScanMaxRoots)
	c.rootsCache[key] = []string{filepath.Join(root, "missing")}

	_, err := c.sampleFiles(context.Background(), root, nil, nil, 3, 10)
	if err == nil {
		t.Fatalf("expected error when startDir is missing")
	}
}
