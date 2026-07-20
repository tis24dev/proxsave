package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadFileUnderRootReadsFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFileUnderRoot(p)
	if err != nil {
		t.Fatalf("ReadFileUnderRoot: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q want hello", got)
	}
}

func TestReadFileUnderRootMissingIsNotExist(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadFileUnderRoot(filepath.Join(dir, "nope.txt"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

func TestReadFileUnderRootRefusesEscapingSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("PRECIOUS"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFileUnderRoot(link); err == nil {
		t.Fatal("expected refusal reading through an escaping symlink")
	}
}

func TestOpenFileUnderRootExclRefusesPreexistingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "prof")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := OpenFileUnderRoot(link, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("want ErrExist over pre-existing symlink, got %v", err)
	}
	// target untouched
	b, _ := os.ReadFile(target)
	if string(b) != "x" {
		t.Fatalf("target was modified: %q", b)
	}
}

func TestOpenFileUnderRootCreatesAndWrites(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "new")
	f, err := OpenFileUnderRoot(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("OpenFileUnderRoot: %v", err)
	}
	if _, err := f.WriteString("data"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "data" {
		t.Fatalf("got %q want data", b)
	}
}

func TestReadFileInRootReadsPlainFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc/hostname"), []byte("host\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFileInRoot(root, "/etc/hostname")
	if err != nil {
		t.Fatalf("ReadFileInRoot: %v", err)
	}
	if string(got) != "host\n" {
		t.Fatalf("got %q want %q", got, "host\n")
	}
}

// TestReadFileInRootReanchorsAbsoluteSymlink is the regression for issue #255:
// on a Proxmox host /etc/ceph/ceph.conf is an absolute symlink to
// /etc/pve/ceph.conf. Under a prefix the target must re-anchor to
// PREFIX/etc/pve/ceph.conf, not escape to the container root.
func TestReadFileInRootReanchorsAbsoluteSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc/pve"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc/ceph"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc/pve/ceph.conf"), []byte("fsid=DEADBEEF\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/pve/ceph.conf", filepath.Join(root, "etc/ceph/ceph.conf")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFileInRoot(root, "/etc/ceph/ceph.conf")
	if err != nil {
		t.Fatalf("ReadFileInRoot: %v", err)
	}
	if string(got) != "fsid=DEADBEEF\n" {
		t.Fatalf("got %q want %q", got, "fsid=DEADBEEF\n")
	}
}

// TestReadFileInRootConfinesHostileAbsoluteTarget proves a symlink pointing at an
// absolute path outside the intended subtree re-anchors under root and can never
// read the real host file: a decoy planted at root/etc/shadow is returned, and
// with no decoy the read fails rather than reaching the machine's /etc/shadow.
func TestReadFileInRootConfinesHostileAbsoluteTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/shadow", filepath.Join(root, "etc/evil")); err != nil {
		t.Fatal(err)
	}
	// Without a decoy the confined target does not exist: must fail, never read the real /etc/shadow.
	if _, err := ReadFileInRoot(root, "/etc/evil"); err == nil {
		t.Fatal("expected error resolving hostile symlink with no in-root target")
	}
	// With a decoy inside root, the read is confined to it.
	if err := os.WriteFile(filepath.Join(root, "etc/shadow"), []byte("CONFINED\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFileInRoot(root, "/etc/evil")
	if err != nil {
		t.Fatalf("ReadFileInRoot: %v", err)
	}
	if string(got) != "CONFINED\n" {
		t.Fatalf("got %q want confined decoy", got)
	}
}

func TestReadFileInRootConfinesRelativeEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A relative target climbing above root is lexically collapsed by filepath.Clean
	// (the leading ".." above root are dropped) and re-anchored under root, so it
	// resolves to root/etc/passwd, never the machine's real /etc/passwd.
	if err := os.Symlink("../../../../etc/passwd", filepath.Join(root, "etc/climb")); err != nil {
		t.Fatal(err)
	}
	// With no in-root target the read fails rather than reaching the real file.
	if _, err := ReadFileInRoot(root, "/etc/climb"); err == nil {
		t.Fatal("expected error resolving relative escape with no in-root target")
	}
	// With a decoy inside root, the read is confined to it, proving containment.
	if err := os.WriteFile(filepath.Join(root, "etc/passwd"), []byte("CONFINED\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFileInRoot(root, "/etc/climb")
	if err != nil {
		t.Fatalf("ReadFileInRoot: %v", err)
	}
	if string(got) != "CONFINED\n" {
		t.Fatalf("got %q want confined decoy", got)
	}
}

// TestReadFileInRootRefusesIntermediateAbsoluteSymlinkDir documents that an
// intermediate directory component that is itself an absolute symlink is not
// re-anchored: os.Root refuses it and the read fails closed (safe). The real
// Proxmox layout uses a final-component symlink, which is handled.
func TestReadFileInRootRefusesIntermediateAbsoluteSymlinkDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc/pve"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc/pve/ceph.conf"), []byte("fsid=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// etc/cephdir is an absolute-symlink DIRECTORY; the file under it is a real file.
	if err := os.Symlink("/etc/pve", filepath.Join(root, "etc/cephdir")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFileInRoot(root, "/etc/cephdir/ceph.conf"); err == nil {
		t.Fatal("expected fail-closed error for an intermediate absolute-symlink directory")
	}
}

func TestReadFileInRootDetectsLoop(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/loop", filepath.Join(root, "etc/loop")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFileInRoot(root, "/etc/loop"); err == nil {
		t.Fatal("expected error for symlink loop")
	}
}
