package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestSampleDirectoriesBoundedRespectsDepthAndLimit(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		filepath.Join("a", "b"),
		"c",
		filepath.Join("d", "e", "f"),
	} {
		if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
	}

	c := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxVE, false)

	dirs, err := c.sampleDirectoriesBounded(context.Background(), root, 1, 10, 0)
	if err != nil {
		t.Fatalf("sampleDirectoriesBounded error: %v", err)
	}
	if len(dirs) != 3 {
		t.Fatalf("expected 3 top-level dirs, got %v", dirs)
	}
	for _, d := range dirs {
		if strings.Contains(d, "/") {
			t.Fatalf("expected top-level dir, got %q", d)
		}
	}

	dirs, err = c.sampleDirectoriesBounded(context.Background(), root, 2, 20, 0)
	if err != nil {
		t.Fatalf("sampleDirectoriesBounded error: %v", err)
	}
	want := map[string]bool{
		"a":   true,
		"a/b": true,
		"c":   true,
		"d":   true,
		"d/e": true,
	}
	for _, got := range dirs {
		delete(want, got)
		if got == "d/e/f" {
			t.Fatalf("unexpected deep dir %q in results: %v", got, dirs)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing expected directories: %#v (got %v)", want, dirs)
	}

	limited, err := c.sampleDirectoriesBounded(context.Background(), root, 1, 2, 0)
	if err != nil {
		t.Fatalf("sampleDirectoriesBounded error: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("expected limit=2 results, got %v", limited)
	}
}

func TestSampleFilesBoundedRespectsPatternsExcludeAndDepth(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(rel), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	write("keep1.txt", "data")
	write("excluded.txt", "data")
	write("skip_me.txt", "data")
	write(filepath.Join("nested", "keep2.txt"), "data")
	write(filepath.Join("nested", "deep", "keep3.txt"), "data")

	cfg := GetDefaultCollectorConfig()
	cfg.ExcludePatterns = []string{"excluded.txt"}
	c := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)

	include := []string{"*.txt"}
	exclude := []string{"skip*"}
	results, err := c.sampleFilesBounded(context.Background(), root, include, exclude, 1, 50, 0)
	if err != nil {
		t.Fatalf("sampleFilesBounded error: %v", err)
	}

	got := map[string]FileSummary{}
	for _, r := range results {
		got[r.RelativePath] = r
		if strings.Contains(r.RelativePath, `\`) {
			t.Fatalf("expected forward-slash relative path, got %q", r.RelativePath)
		}
		if r.SizeHuman == "" || r.SizeBytes <= 0 {
			t.Fatalf("expected populated size fields, got %+v", r)
		}
	}

	if _, ok := got["keep1.txt"]; !ok {
		t.Fatalf("expected keep1.txt in results: %v", results)
	}
	if _, ok := got["nested/keep2.txt"]; !ok {
		t.Fatalf("expected nested/keep2.txt in results: %v", results)
	}
	if _, ok := got["excluded.txt"]; ok {
		t.Fatalf("expected excluded.txt to be skipped: %v", results)
	}
	if _, ok := got["skip_me.txt"]; ok {
		t.Fatalf("expected skip_me.txt to be excluded by pattern: %v", results)
	}
	if _, ok := got["nested/deep/keep3.txt"]; ok {
		t.Fatalf("expected nested/deep/keep3.txt to be skipped due to maxDepth: %v", results)
	}
}

func TestSampleFilesBoundedLimitZeroReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	c := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxBS, false)
	results, err := c.sampleFilesBounded(context.Background(), root, nil, nil, 2, 0, 0)
	if err != nil {
		t.Fatalf("sampleFilesBounded error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty results, got %v", results)
	}
}
