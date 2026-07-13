package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	rootdocs "github.com/tis24dev/proxsave"
)

// Characterization for installSupportDocs, part of the --upgrade finalize block
// (refresh embedded docs). Pins that every embedded doc is written under the base
// dir at its own Name (creating nested directories), byte-for-byte, at mode 0600,
// so an upcoming finalize-phase extraction can be proven behaviour-preserving.
func TestInstallSupportDocs_WritesEmbeddedDocs(t *testing.T) {
	docs := rootdocs.InstallableDocs()
	if len(docs) == 0 {
		t.Fatal("expected embedded docs (at least README.md); got none")
	}

	dir := t.TempDir()
	if err := installSupportDocs(dir, nil); err != nil {
		t.Fatalf("installSupportDocs: %v", err)
	}

	sawNested := false
	for _, doc := range docs {
		target := filepath.Join(dir, doc.Name)
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("doc %q not written: %v", doc.Name, err)
		}
		if !bytes.Equal(got, doc.Data) {
			t.Errorf("doc %q content mismatch (%d bytes on disk vs %d embedded)", doc.Name, len(got), len(doc.Data))
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(target)
			if err != nil {
				t.Fatalf("stat %q: %v", target, err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("doc %q mode = %o, want 0600", doc.Name, info.Mode().Perm())
			}
		}
		if strings.Contains(doc.Name, "/") {
			sawNested = true
		}
	}
	if !sawNested {
		t.Log("note: no embedded doc had a nested path; the MkdirAll subdir branch was not exercised")
	}
}

// installSupportDocs surfaces a directory-creation failure as an error (it is
// best-effort at the call site, only warned, but the function itself must still
// report the failure). Pointing the base dir at a regular file makes the first
// MkdirAll fail, which characterizes the error path without needing root.
func TestInstallSupportDocs_ErrorsWhenBaseDirIsRegularFile(t *testing.T) {
	if len(rootdocs.InstallableDocs()) == 0 {
		t.Fatal("expected embedded docs (at least README.md); got none")
	}

	dir := t.TempDir()
	fileAsBase := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(fileAsBase, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := installSupportDocs(fileAsBase, nil); err == nil {
		t.Fatal("expected an error when the base dir is a regular file, got nil")
	}
}
