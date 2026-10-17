package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

// pbsRealDatastoreLayout builds the root of a datastore the way PBS leaves it
// (measured on PBS 4.2.8 and 3.4.9): .chunks with its hash directories, .lock,
// .gc-status after a garbage collection, and a backup type directory. There is
// no .index directory.
func pbsRealDatastoreLayout(t *testing.T) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "ds")
	for _, dir := range []string{".chunks/0000", ".chunks/ffff", "vm/100"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for _, file := range []string{".lock", ".gc-status"} {
		if err := os.WriteFile(filepath.Join(base, file), nil, 0o644); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
	}
	return base
}

func TestValidatePBSDatastoreReadOnlyAcceptsTheRealPBSLayout(t *testing.T) {
	base := pbsRealDatastoreLayout(t)
	if warn := validatePBSDatastoreReadOnly(base); warn != "" {
		t.Fatalf("a datastore laid out by PBS must pass the preflight, got %q", warn)
	}
}

func TestValidatePBSDatastoreReadOnlyStillReportsAMissingChunkStore(t *testing.T) {
	base := pbsRealDatastoreLayout(t)
	if err := os.RemoveAll(filepath.Join(base, ".chunks")); err != nil {
		t.Fatal(err)
	}
	if warn := validatePBSDatastoreReadOnly(base); warn == "" {
		t.Fatal("a datastore without .chunks must fail the preflight")
	}
}

func TestPBSDatastoreHasDataLooksOnlyAtTheChunkStore(t *testing.T) {
	base := pbsRealDatastoreLayout(t)
	has, err := pbsDatastoreHasData(base)
	if err != nil || !has {
		t.Fatalf("pbsDatastoreHasData=%v err=%v; want true for a chunk store with hash directories", has, err)
	}

	// The scaffold earlier ProxSave versions created: an empty .chunks, an
	// .index that PBS never uses, .lock. Something inside .index is not data.
	legacy := filepath.Join(t.TempDir(), "legacy")
	for _, dir := range []string{".chunks", ".index/stray"} {
		if err := os.MkdirAll(filepath.Join(legacy, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(legacy, ".lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	has, err = pbsDatastoreHasData(legacy)
	if err != nil || has {
		t.Fatalf("pbsDatastoreHasData=%v err=%v; want false for the legacy scaffold", has, err)
	}
	unexpected, err := pbsDatastoreHasUnexpectedEntries(legacy)
	if err != nil || unexpected {
		t.Fatalf("pbsDatastoreHasUnexpectedEntries=%v err=%v; the legacy scaffold is not unrelated data", unexpected, err)
	}
}

func TestInitializePBSDatastoreDoesNotCreateAnIndexDirectory(t *testing.T) {
	base := filepath.Join(t.TempDir(), "datastore")
	if _, err := initializePBSDatastore(base, "ds", newDirTestLogger()); err != nil {
		t.Fatalf("initializePBSDatastore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, ".index")); !os.IsNotExist(err) {
		t.Fatalf(".index must not be created, stat err=%v", err)
	}
	if info, err := os.Stat(filepath.Join(base, ".chunks")); err != nil || !info.IsDir() {
		t.Fatalf(".chunks must be created, err=%v", err)
	}
}
