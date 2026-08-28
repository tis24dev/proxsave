package orchestrator

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"
)

// TestExtractHardlinkAcceptsASymlinkedDestinationRoot pins the one asymmetry the
// redundant containment check in extractHardlink can get wrong.
//
// resolvePathWithinRootFS builds its result out of the CANONICAL root: it walks
// outwards from canonicalRoot and never from the lexical path it was handed. The
// prefix test that follows compares against destRoot as written. The two agree
// only while the destination root carries no symlink of its own, and a restore
// target like /var/lib/vz frequently is one. When they disagree every hard link
// entry in the archive is rejected as an escape although none of them escapes,
// and the restore fails on a correct archive.
//
// This runs against osFS deliberately. FakeFS does not resolve a symlink standing
// at the root itself, so the same fixture on FakeFS reports canonical == lexical
// and hides the defect entirely.
func TestExtractHardlinkAcceptsASymlinkedDestinationRoot(t *testing.T) {
	orig := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = orig })

	base := t.TempDir()
	realRoot := filepath.Join(base, "real-root")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatalf("mkdir real root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "target.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write hardlink target: %v", err)
	}

	aliasRoot := filepath.Join(base, "alias-root")
	if err := os.Symlink("real-root", aliasRoot); err != nil {
		t.Fatalf("create the symlinked destination root: %v", err)
	}

	header := &tar.Header{
		Name:     "hardlink.txt",
		Linkname: "target.txt",
		Typeflag: tar.TypeLink,
	}

	if err := extractHardlink(filepath.Join(aliasRoot, header.Name), header, aliasRoot); err != nil {
		t.Fatalf("extractHardlink refused a target inside its own destination root: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(realRoot, header.Name)); err != nil {
		t.Fatalf("the hard link was not created under the canonical root: %v", err)
	}
}
