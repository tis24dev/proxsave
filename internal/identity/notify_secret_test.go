package identity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotifySecretPath(t *testing.T) {
	got := NotifySecretPath("/var/lib/proxsave")
	want := filepath.Join("/var/lib/proxsave", "identity", ".notify_secret")
	if got != want {
		t.Fatalf("NotifySecretPath() = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, filepath.Join("identity", ".notify_secret")) {
		t.Fatalf("NotifySecretPath() = %q; expected to end in identity/.notify_secret", got)
	}
}

func TestPersistAndLoadNotifySecret(t *testing.T) {
	baseDir := t.TempDir()
	const secret = "3h64-dyi8-q3d6-wcm5"

	if err := PersistNotifySecret(context.Background(), baseDir, secret, nil); err != nil {
		t.Fatalf("PersistNotifySecret() error = %v", err)
	}

	got, err := LoadNotifySecret(baseDir)
	if err != nil {
		t.Fatalf("LoadNotifySecret() error = %v", err)
	}
	if got != secret {
		t.Fatalf("LoadNotifySecret() = %q, want %q", got, secret)
	}

	// The file is created 0600 (immutable attribute is best-effort and skipped
	// on filesystems/environments that do not support chattr).
	info, err := os.Stat(NotifySecretPath(baseDir))
	if err != nil {
		t.Fatalf("stat notify secret file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("notify secret file mode = %o, want 0600", perm)
	}
}

func TestPersistNotifySecretOverwrites(t *testing.T) {
	baseDir := t.TempDir()
	if err := PersistNotifySecret(context.Background(), baseDir, "aaaa-bbbb", nil); err != nil {
		t.Fatalf("first PersistNotifySecret() error = %v", err)
	}
	if err := PersistNotifySecret(context.Background(), baseDir, "cccc-dddd", nil); err != nil {
		t.Fatalf("second PersistNotifySecret() error = %v", err)
	}
	got, err := LoadNotifySecret(baseDir)
	if err != nil {
		t.Fatalf("LoadNotifySecret() error = %v", err)
	}
	if got != "cccc-dddd" {
		t.Fatalf("LoadNotifySecret() = %q, want cccc-dddd", got)
	}
}

func TestLoadNotifySecretMissing(t *testing.T) {
	baseDir := t.TempDir()
	got, err := LoadNotifySecret(baseDir)
	if err != nil {
		t.Fatalf("LoadNotifySecret() error = %v", err)
	}
	if got != "" {
		t.Fatalf("LoadNotifySecret() = %q, want empty string when absent", got)
	}
}

func TestLoadNotifySecretIgnoresJunk(t *testing.T) {
	baseDir := t.TempDir()
	dir := filepath.Join(baseDir, "identity")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".notify_secret"), []byte("not a secret!!"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := LoadNotifySecret(baseDir)
	if err != nil {
		t.Fatalf("LoadNotifySecret() error = %v", err)
	}
	if got != "" {
		t.Fatalf("LoadNotifySecret() = %q, want empty string for malformed content", got)
	}
}

func TestPersistNotifySecretRejectsEmpty(t *testing.T) {
	baseDir := t.TempDir()
	if err := PersistNotifySecret(context.Background(), baseDir, "   ", nil); err == nil {
		t.Fatalf("PersistNotifySecret() expected an error for an empty secret")
	}
	// Nothing must have been written.
	if _, err := os.Stat(NotifySecretPath(baseDir)); !os.IsNotExist(err) {
		t.Fatalf("expected no notify secret file, stat err = %v", err)
	}
}

func TestPersistNotifySecretRejectsMalformed(t *testing.T) {
	baseDir := t.TempDir()
	// Outside notifySecretFormat (uppercase + space + punctuation): the load path
	// would drop it, so persist must refuse it to keep the read/write contract
	// symmetric (a persisted secret always reloads).
	if err := PersistNotifySecret(context.Background(), baseDir, "Bad Secret!!", nil); err == nil {
		t.Fatalf("PersistNotifySecret() expected an error for a malformed secret")
	}
	if _, err := os.Stat(NotifySecretPath(baseDir)); !os.IsNotExist(err) {
		t.Fatalf("expected no notify secret file written, stat err = %v", err)
	}
}
