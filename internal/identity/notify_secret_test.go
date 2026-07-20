package identity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestRemoveNotifySecret(t *testing.T) {
	baseDir := t.TempDir()
	const secret = "3h64-dyi8-q3d6-wcm5"
	if err := PersistNotifySecret(context.Background(), baseDir, secret, nil); err != nil {
		t.Fatalf("PersistNotifySecret() error = %v", err)
	}
	if err := RemoveNotifySecret(baseDir); err != nil {
		t.Fatalf("RemoveNotifySecret() error = %v", err)
	}
	if _, err := os.Stat(NotifySecretPath(baseDir)); !os.IsNotExist(err) {
		t.Fatalf("secret file must be gone after remove, stat err = %v", err)
	}
	if got, _ := LoadNotifySecret(baseDir); got != "" {
		t.Fatalf("LoadNotifySecret after remove = %q, want empty", got)
	}
	// Idempotent: removing an already-absent secret is a no-op.
	if err := RemoveNotifySecret(baseDir); err != nil {
		t.Fatalf("RemoveNotifySecret() (idempotent) error = %v", err)
	}
}

func TestPersistNotifySecretRejectsTooShort(t *testing.T) {
	baseDir := t.TempDir()
	// A single lowercase char passes notifySecretFormat but is below notifySecretMinLen (6):
	// it would be UNMASKABLE in logs (redact.go secretMinRegister), so persist must refuse it.
	if err := PersistNotifySecret(context.Background(), baseDir, "a", nil); err == nil {
		t.Fatalf("PersistNotifySecret() expected an error for a sub-threshold secret")
	}
	if _, err := os.Stat(NotifySecretPath(baseDir)); !os.IsNotExist(err) {
		t.Fatalf("expected no notify secret file written, stat err = %v", err)
	}
	// A 5-rune (still < 6) format-valid value is also refused; a 6-rune one is accepted.
	if err := PersistNotifySecret(context.Background(), baseDir, "abcde", nil); err == nil {
		t.Fatalf("PersistNotifySecret() expected an error for a 5-rune secret")
	}
	if err := PersistNotifySecret(context.Background(), baseDir, "abcdef", nil); err != nil {
		t.Fatalf("PersistNotifySecret() rejected a 6-rune secret: %v", err)
	}
}

func TestRemoveNotifySecretIfMatchesRemovesOnMatch(t *testing.T) {
	baseDir := t.TempDir()
	const secret = "3h64-dyi8-q3d6-wcm5"
	if err := PersistNotifySecret(context.Background(), baseDir, secret, nil); err != nil {
		t.Fatalf("PersistNotifySecret() error = %v", err)
	}
	removed, err := RemoveNotifySecretIfMatches(baseDir, secret)
	if err != nil {
		t.Fatalf("RemoveNotifySecretIfMatches() error = %v", err)
	}
	if !removed {
		t.Fatalf("RemoveNotifySecretIfMatches() removed = false, want true on an exact match")
	}
	if got, _ := LoadNotifySecret(baseDir); got != "" {
		t.Fatalf("LoadNotifySecret after guarded remove = %q, want empty", got)
	}
}

func TestRemoveNotifySecretIfMatchesKeepsOnMismatch(t *testing.T) {
	baseDir := t.TempDir()
	// Emulate the TOCTOU the guard defends against: a concurrent provisioner replaced the
	// rejected S_old with a fresh confirmed S_new before the ErrHCAuth remediation ran. The
	// guard must leave S_new in place (comparand is S_old).
	const sNew = "cccc-dddd"
	const sOld = "aaaa-bbbb"
	if err := PersistNotifySecret(context.Background(), baseDir, sNew, nil); err != nil {
		t.Fatalf("PersistNotifySecret() error = %v", err)
	}
	removed, err := RemoveNotifySecretIfMatches(baseDir, sOld)
	if err != nil {
		t.Fatalf("RemoveNotifySecretIfMatches() error = %v", err)
	}
	if removed {
		t.Fatalf("RemoveNotifySecretIfMatches() removed a concurrently-provisioned secret")
	}
	if got, _ := LoadNotifySecret(baseDir); got != sNew {
		t.Fatalf("LoadNotifySecret after mismatch = %q, want the fresh secret %q", got, sNew)
	}
}

func TestRemoveNotifySecretIfMatchesEmptyRejectedKeeps(t *testing.T) {
	baseDir := t.TempDir()
	const secret = "3h64-dyi8-q3d6-wcm5"
	if err := PersistNotifySecret(context.Background(), baseDir, secret, nil); err != nil {
		t.Fatalf("PersistNotifySecret() error = %v", err)
	}
	// An empty comparand must never delete blindly (regression this guard replaces).
	removed, err := RemoveNotifySecretIfMatches(baseDir, "   ")
	if err != nil {
		t.Fatalf("RemoveNotifySecretIfMatches() error = %v", err)
	}
	if removed {
		t.Fatalf("RemoveNotifySecretIfMatches() removed with an empty comparand")
	}
	if got, _ := LoadNotifySecret(baseDir); got != secret {
		t.Fatalf("LoadNotifySecret after empty-comparand call = %q, want %q", got, secret)
	}
}

func TestRemoveNotifySecretAbsentDirIsNoOp(t *testing.T) {
	// No identity directory created yet: remove must be a silent no-op.
	if err := RemoveNotifySecret(t.TempDir()); err != nil {
		t.Fatalf("RemoveNotifySecret() on absent dir error = %v", err)
	}
	if err := RemoveNotifySecret(""); err != nil {
		t.Fatalf("RemoveNotifySecret() on empty baseDir error = %v", err)
	}
}

func TestLockNotifySecretSerializes(t *testing.T) {
	baseDir := t.TempDir()
	unlock, err := LockNotifySecret(baseDir)
	if err != nil {
		t.Fatalf("LockNotifySecret() error = %v", err)
	}
	// A second acquisition (separate open file description) must BLOCK until the first
	// releases, so exactly one provisioner runs at a time.
	acquired := make(chan struct{})
	go func() {
		u2, lerr := LockNotifySecret(baseDir)
		if lerr != nil {
			t.Errorf("second LockNotifySecret() error = %v", lerr)
			close(acquired)
			return
		}
		u2()
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatalf("second lock acquired while the first was still held")
	case <-time.After(150 * time.Millisecond):
		// expected: the second acquisition is blocked.
	}
	unlock()
	select {
	case <-acquired:
		// expected: the second acquisition proceeds once the first releases.
	case <-time.After(2 * time.Second):
		t.Fatalf("second lock did not acquire after release")
	}
}

func TestLockNotifySecretEmptyBaseDir(t *testing.T) {
	if _, err := LockNotifySecret(""); err == nil {
		t.Fatalf("LockNotifySecret(\"\") expected an error")
	}
}
