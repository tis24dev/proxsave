package wizard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAgeRecipient(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "recipient.txt")

	if err := SaveAgeRecipient(target, "age1abcd"); err != nil {
		t.Fatalf("SaveAgeRecipient error: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("failed to read recipient file: %v", err)
	}
	if string(data) != "age1abcd\n" {
		t.Fatalf("unexpected file content: %q", string(data))
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat error: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected permissions 0600, got %v", info.Mode().Perm())
	}
}
