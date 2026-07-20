package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCreateProfileFileRefusesPreexistingSymlink pins the CWE-59 / F02-06
// symlink protection: createProfileFile must never follow a pre-existing
// symlink at the requested path, and must never touch the symlink's target.
func TestCreateProfileFileRefusesPreexistingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("PRECIOUS"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "cpu.pprof")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f, err := createProfileFile(link)
	if err == nil {
		f.Close()
		t.Fatal("createProfileFile followed a pre-existing symlink")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "PRECIOUS" {
		t.Fatalf("target truncated/modified: %q", b)
	}
}

// TestCreateProfileFileCreatesFreshFile ensures the confinement change did not
// break the ordinary path: a fresh profile file must still be created.
func TestCreateProfileFileCreatesFreshFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cpu.pprof")
	f, err := createProfileFile(p)
	if err != nil {
		t.Fatalf("createProfileFile: %v", err)
	}
	f.Close()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("profile file not created: %v", err)
	}
}
