package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestOptimizationConfigEnabled(t *testing.T) {
	cfg := OptimizationConfig{}
	if cfg.Enabled() {
		t.Fatal("expected disabled config when all stages are false")
	}
	cfg.EnableDeduplication = true
	if !cfg.Enabled() {
		t.Fatal("expected Enabled() to return true when a stage is active")
	}
}

func TestApplyOptimizationsRunsAllStages(t *testing.T) {
	root := t.TempDir()

	mustWriteFile := func(rel, content string) string {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return path
	}

	mustWriteFile(filepath.Join("dup", "one.txt"), "identical data")
	dupB := mustWriteFile(filepath.Join("dup", "two.txt"), "identical data")

	logFile := mustWriteFile(filepath.Join("logs", "app.log"), "line one\r\nline two\r\n")
	confOriginal := "# comment\nkey=value\n\n;ignored\nalpha=beta\n"
	confFile := mustWriteFile(filepath.Join("conf", "settings.conf"), confOriginal)
	jsonFile := mustWriteFile(filepath.Join("meta", "data.json"), "{\n  \"a\": 1,\n  \"b\": 2\n}\n")

	logger := logging.New(types.LogLevelError, false)
	cfg := OptimizationConfig{
		EnableDeduplication:       true,
		EnablePrefilter:           true,
		PrefilterMaxFileSizeBytes: 1024,
	}

	if err := ApplyOptimizations(context.Background(), logger, root, cfg); err != nil {
		t.Fatalf("ApplyOptimizations: %v", err)
	}

	// Deduplication should replace the duplicate with a symlink that still resolves.
	info, err := os.Lstat(dupB)
	if err != nil {
		t.Fatalf("stat duplicate: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected %s to be a symlink after deduplication", dupB)
	}
	data, err := os.ReadFile(dupB)
	if err != nil {
		t.Fatalf("read dedup symlink: %v", err)
	}
	if string(data) != "identical data" {
		t.Fatalf("symlink data mismatch, got %q", data)
	}

	// Prefilter should strip CR characters and keep config files semantically intact.
	logContents, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if bytes.Contains(logContents, []byte("\r")) {
		t.Fatalf("expected CR characters removed from %s", logFile)
	}
	confContents, err := os.ReadFile(confFile)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if string(confContents) != confOriginal {
		t.Fatalf("unexpected config contents: %q", confContents)
	}
	jsonContents, err := os.ReadFile(jsonFile)
	if err != nil {
		t.Fatalf("read json file: %v", err)
	}
	if bytes.Contains(jsonContents, []byte(" ")) || bytes.Contains(jsonContents, []byte("\n")) {
		t.Fatalf("expected minified JSON, got %q", jsonContents)
	}
}

func TestDedupDoesNotReplaceCriticalFilesWithSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatalf("mkdir etc: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "commands"), 0o755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}

	resolvPath := filepath.Join(root, "etc", "resolv.conf")
	resolvContent := []byte("nameserver 1.1.1.1\n")
	if err := os.WriteFile(resolvPath, resolvContent, 0o644); err != nil {
		t.Fatalf("write resolv.conf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "commands", "resolv_conf.txt"), resolvContent, 0o644); err != nil {
		t.Fatalf("write commands/resolv_conf.txt: %v", err)
	}

	logger := logging.New(types.LogLevelError, false)
	cfg := OptimizationConfig{
		EnableDeduplication: true,
	}
	if err := ApplyOptimizations(context.Background(), logger, root, cfg); err != nil {
		t.Fatalf("ApplyOptimizations: %v", err)
	}

	info, err := os.Lstat(resolvPath)
	if err != nil {
		t.Fatalf("lstat resolv.conf: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected %s to remain a regular file (critical path), got symlink", resolvPath)
	}
	got, err := os.ReadFile(resolvPath)
	if err != nil {
		t.Fatalf("read resolv.conf: %v", err)
	}
	if string(got) != string(resolvContent) {
		t.Fatalf("resolv.conf content mismatch: got %q want %q", got, resolvContent)
	}
}

// TestReplaceWithSymlinkPreservesFileOnFailure verifies the dedup replacement is
// fail-closed: if the symlink cannot be created the original staged file is kept,
// instead of being removed first and lost (issue #71).
// TestApplyOptimizationsFailsFatallyOnDedupError guards the #70 safety contract: an
// unsafe deduplication state must NOT be swallowed to a warning; ApplyOptimizations
// must return the error so the backup run aborts rather than ship a damaged tree.
func TestApplyOptimizationsFailsFatallyOnDedupError(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	// A non-existent dedup root makes deduplicateFiles fail (os.OpenRoot error); the
	// happy/fully-reverted paths return nil, so a returned error here can only come
	// from an unsafe state that must abort.
	err := ApplyOptimizations(context.Background(), logger, "/proxsave-nonexistent-root-xyz", OptimizationConfig{EnableDeduplication: true})
	if err == nil {
		t.Fatal("ApplyOptimizations must return (not swallow) a deduplication error so the backup aborts")
	}
	if !strings.Contains(err.Error(), "deduplication") {
		t.Fatalf("error should identify the deduplication stage, got: %v", err)
	}
}

// TestDeduplicationRevertsSymlinksWhenManifestUnwritable guards #70: if the dedup
// manifest cannot be written, the symlinks are reverted to regular files so the
// archive never ships unrecorded (unrecoverable) links.
func TestDeduplicationRevertsSymlinksWhenManifestUnwritable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("same"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("same"), 0o640); err != nil {
		t.Fatal(err)
	}
	// Make the manifest path a DIRECTORY so writeDedupManifest's WriteFile fails.
	manifestAsDir := filepath.Join(root, filepath.FromSlash(DedupManifestRelPath))
	if err := os.MkdirAll(manifestAsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	logger := logging.New(types.LogLevelError, false)
	if err := deduplicateFiles(context.Background(), logger, root); err != nil {
		t.Fatalf("deduplicateFiles should succeed (revert) when the manifest cannot be written: %v", err)
	}

	for _, name := range []string{"a.txt", "b.txt"} {
		p := filepath.Join(root, name)
		info, err := os.Lstat(p)
		if err != nil {
			t.Fatalf("lstat %s: %v", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("%s must be reverted to a regular file when the manifest is unwritable, got symlink", name)
		}
		if data, err := os.ReadFile(p); err != nil || string(data) != "same" {
			t.Fatalf("%s content lost after revert: %q err=%v", name, data, err)
		}
	}
}

// TestReplaceWithSymlinkDoesNotClobberSuffixFile guards #71: using a unique temp
// name must never destroy a real staged file that happens to carry a dedup temp
// suffix.
func TestReplaceWithSymlinkDoesNotClobberSuffixFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	duplicate := filepath.Join(root, "b.txt")
	suffixFile := duplicate + ".dedup.tmp" // a legitimate backed-up file, not our temp
	if err := os.WriteFile(target, []byte("data"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(duplicate, []byte("data"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(suffixFile, []byte("precious"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := replaceWithSymlink(target, duplicate); err != nil {
		t.Fatalf("replaceWithSymlink: %v", err)
	}

	if info, err := os.Lstat(duplicate); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("duplicate should be a symlink after dedup: info=%v err=%v", info, err)
	}
	// The pre-existing .dedup.tmp file must be intact (the old fixed-name temp would
	// have removed/clobbered it).
	if got, err := os.ReadFile(suffixFile); err != nil || string(got) != "precious" {
		t.Fatalf("a real *.dedup.tmp file must not be clobbered: got %q err=%v", got, err)
	}
}

// TestReplaceWithSymlinkFailClosedOnRenameFailure verifies that when the rename
// over the duplicate fails, the original duplicate is left untouched (fail-closed,
// issue #71).
func TestReplaceWithSymlinkFailClosedOnRenameFailure(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	duplicate := filepath.Join(root, "dup")
	if err := os.WriteFile(target, []byte("data"), 0o640); err != nil {
		t.Fatal(err)
	}
	// Make the duplicate a NON-EMPTY directory so renaming a file over it fails.
	if err := os.MkdirAll(filepath.Join(duplicate, "child"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := replaceWithSymlink(target, duplicate); err == nil {
		t.Fatal("expected replaceWithSymlink to fail when rename over the duplicate cannot succeed")
	}

	info, err := os.Lstat(duplicate)
	if err != nil {
		t.Fatalf("duplicate must still exist after a failed dedup: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("duplicate must be left untouched (still a directory) after a failed dedup")
	}
	if _, err := os.Stat(filepath.Join(duplicate, "child")); err != nil {
		t.Fatalf("duplicate contents must be preserved on failure: %v", err)
	}
}

// TestMinifyJSONIsLossless verifies json.Compact preserves number precision,
// duplicate keys and key order (issue #72) while still stripping whitespace.
func TestMinifyJSONIsLossless(t *testing.T) {
	root := t.TempDir()
	const name = "data.json"
	input := `{ "id": 123456789012345678, "b": 1, "b": 2, "ratio": 1.0 }`
	if err := os.WriteFile(filepath.Join(root, name), []byte(input), 0o640); err != nil {
		t.Fatal(err)
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rootFS.Close() }()

	changed, err := minifyJSON(rootFS, name)
	if err != nil {
		t.Fatalf("minifyJSON: %v", err)
	}
	if !changed {
		t.Fatal("expected JSON whitespace to be stripped")
	}
	got, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":123456789012345678,"b":1,"b":2,"ratio":1.0}`
	if string(got) != want {
		t.Fatalf("minifyJSON is not lossless:\n got %q\nwant %q", got, want)
	}
}

// TestPrefilterSkipsStructuredConfigJSON verifies JSON under sensitive config
// directories is left untouched by the prefilter (issue #72 defense-in-depth).
func TestPrefilterSkipsStructuredConfigJSON(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "etc", "proxmox-backup", "shadow.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "{\n  \"user\": \"x\"\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}

	logger := logging.New(types.LogLevelError, false)
	if err := prefilterFiles(context.Background(), logger, root, 1024); err != nil {
		t.Fatalf("prefilterFiles: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("structured-config JSON must not be modified, got %q", got)
	}
}

// TestDeduplicationWritesManifest verifies dedup records each created symlink in
// the manifest the restore uses to materialize them back (issue #70).
func TestDeduplicationWritesManifest(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join("a", "one.cfg"), "same content here", 0o640)
	write(filepath.Join("a", "two.cfg"), "same content here", 0o600)

	logger := logging.New(types.LogLevelError, false)
	if err := ApplyOptimizations(context.Background(), logger, root, OptimizationConfig{EnableDeduplication: true}); err != nil {
		t.Fatalf("ApplyOptimizations: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(DedupManifestRelPath)))
	if err != nil {
		t.Fatalf("read dedup manifest: %v", err)
	}
	var entries []DedupManifestEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("unmarshal dedup manifest: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 dedup entry, got %d (%+v)", len(entries), entries)
	}
	// WalkDir visits a/one.cfg before a/two.cfg, so two.cfg is the one symlinked.
	if entries[0].Path != "a/two.cfg" {
		t.Fatalf("unexpected dedup path %q", entries[0].Path)
	}
	if entries[0].Mode != uint32(0o600) {
		t.Fatalf("expected recorded mode 0600, got %o", entries[0].Mode)
	}
}
