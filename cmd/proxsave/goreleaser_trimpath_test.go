package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The release build must set -trimpath so the shipped binary embeds no absolute
// builder paths and is path-reproducible.
func TestGoreleaserBuildSetsTrimpath(t *testing.T) {
	root := repoRootForVersionTest(t)
	data, err := os.ReadFile(filepath.Join(root, ".github", ".goreleaser.yml"))
	if err != nil {
		t.Fatalf("read .goreleaser.yml: %v", err)
	}
	if !strings.Contains(string(data), "-trimpath") {
		t.Fatalf(".goreleaser.yml build must set -trimpath:\n%s", data)
	}
}
