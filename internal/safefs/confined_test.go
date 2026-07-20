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
