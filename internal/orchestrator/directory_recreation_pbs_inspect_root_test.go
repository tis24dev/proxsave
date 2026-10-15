package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

// The two directory readers open the directory through os.Root: the answers are the
// ones a plain open gave, empty, scaffold only, and anything else.
func TestDatastoreDirectoryReadersThroughRoot(t *testing.T) {
	dir := t.TempDir()
	if has, err := dirHasAnyEntry(dir); err != nil || has {
		t.Fatalf("empty dir: dirHasAnyEntry = %v, %v", has, err)
	}
	if unexpected, err := datastoreContainsUnexpectedEntries(dir); err != nil || unexpected {
		t.Fatalf("empty dir: datastoreContainsUnexpectedEntries = %v, %v", unexpected, err)
	}

	for _, name := range []string{".chunks", ".lock"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if has, err := dirHasAnyEntry(dir); err != nil || !has {
		t.Fatalf("scaffold: dirHasAnyEntry = %v, %v", has, err)
	}
	if unexpected, err := datastoreContainsUnexpectedEntries(dir); err != nil || unexpected {
		t.Fatalf("scaffold only: datastoreContainsUnexpectedEntries = %v, %v", unexpected, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "vm"), nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if unexpected, err := datastoreContainsUnexpectedEntries(dir); err != nil || !unexpected {
		t.Fatalf("with vm: datastoreContainsUnexpectedEntries = %v, %v", unexpected, err)
	}

	if _, err := dirHasAnyEntry(filepath.Join(dir, "absent")); err == nil {
		t.Fatalf("a missing directory must be an error")
	}
}
