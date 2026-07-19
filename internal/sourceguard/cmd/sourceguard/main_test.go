package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestRunCleanFileExitsZero(t *testing.T) {
	var out, errb bytes.Buffer
	p := write(t, "clean.go", "package x\n\nfunc F() {}\n")
	if code := run([]string{p}, &out, &errb); code != 0 {
		t.Fatalf("clean file: code=%d, out=%q err=%q", code, out.String(), errb.String())
	}
	if out.Len() != 0 {
		t.Fatalf("clean file produced output: %q", out.String())
	}
}

func TestRunBidiFileExitsOneAndReports(t *testing.T) {
	var out, errb bytes.Buffer
	// The temp file on disk holds a real U+202E; this test SOURCE holds only the
	// escape. t.TempDir() is outside the tracked tree, so the whole-tree scan
	// never sees it.
	p := write(t, "bad.go", "package x\n// a\u202eb\n")
	if code := run([]string{p}, &out, &errb); code != 1 {
		t.Fatalf("bad file: want code 1, got %d (out=%q err=%q)", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "U+202E") {
		t.Fatalf("bad file output missing U+202E: %q", out.String())
	}
}

func TestRunHomoglyphOnlyForGo(t *testing.T) {
	var out, errb bytes.Buffer
	// A Cyrillic small a (U+0430) is a homoglyph only checked for .go.
	md := write(t, "doc.md", "id\u0430\n")
	if code := run([]string{md}, &out, &errb); code != 0 {
		t.Fatalf(".md with confusable: want 0, got %d (%q)", code, out.String())
	}
	out.Reset()
	errb.Reset()
	gofile := write(t, "code.go", "id\u0430\n")
	if code := run([]string{gofile}, &out, &errb); code != 1 {
		t.Fatalf(".go with confusable: want 1, got %d (%q)", code, out.String())
	}
}

func TestRunReadErrorFailsClosed(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{filepath.Join(t.TempDir(), "does-not-exist.go")}, &out, &errb); code != 1 {
		t.Fatalf("missing file: want fail-closed code 1, got %d", code)
	}
}
