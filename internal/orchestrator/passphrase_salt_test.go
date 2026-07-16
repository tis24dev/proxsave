package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
)

const testStrongPassphrase = "Str0ng-Passphrase!"

func encryptToRecipient(t *testing.T, recipientStr string, plaintext []byte) []byte {
	t.Helper()
	rec, err := age.ParseX25519Recipient(recipientStr)
	if err != nil {
		t.Fatalf("parse recipient: %v", err)
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rec)
	if err != nil {
		t.Fatalf("age.Encrypt: %v", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		t.Fatalf("write plaintext: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close age writer: %v", err)
	}
	return buf.Bytes()
}

func TestGetOrCreatePassphraseSalt(t *testing.T) {
	recipientPath := filepath.Join(t.TempDir(), "identity", "age", "recipient.txt")
	o := &Orchestrator{}

	salt, err := o.getOrCreatePassphraseSalt(recipientPath)
	if err != nil {
		t.Fatalf("getOrCreatePassphraseSalt: %v", err)
	}
	if !strings.HasPrefix(salt, randomSaltNamespaceV2) {
		t.Fatalf("salt %q missing prefix %q", salt, randomSaltNamespaceV2)
	}
	if salt == recipientSaltV1 || salt == legacyRecipientSalt {
		t.Fatalf("salt collided with a fixed salt: %q", salt)
	}

	saltPath := passphraseSaltFilePath(recipientPath)
	info, err := os.Stat(saltPath)
	if err != nil {
		t.Fatalf("salt file not persisted: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("salt file perm = %v, want 0600", perm)
	}

	// Idempotent: a second call returns the same persisted salt.
	salt2, err := o.getOrCreatePassphraseSalt(recipientPath)
	if err != nil {
		t.Fatalf("second getOrCreatePassphraseSalt: %v", err)
	}
	if salt2 != salt {
		t.Fatalf("salt not stable across calls: %q vs %q", salt, salt2)
	}
	if got, err := o.readPassphraseSalt(recipientPath); err != nil || got != salt {
		t.Fatalf("readPassphraseSalt = (%q, %v), want (%q, nil)", got, err, salt)
	}
}

func TestPassphraseSaltIsPerInstallation(t *testing.T) {
	o1, o2 := &Orchestrator{}, &Orchestrator{}
	r1 := filepath.Join(t.TempDir(), "recipient.txt")
	r2 := filepath.Join(t.TempDir(), "recipient.txt")

	salt1, err := o1.getOrCreatePassphraseSalt(r1)
	if err != nil {
		t.Fatal(err)
	}
	salt2, err := o2.getOrCreatePassphraseSalt(r2)
	if err != nil {
		t.Fatal(err)
	}
	if salt1 == salt2 {
		t.Fatalf("two installations produced the same salt: %q", salt1)
	}

	rec1, err := deriveDeterministicRecipientFromPassphraseWithSalt(testStrongPassphrase, salt1)
	if err != nil {
		t.Fatal(err)
	}
	rec2, err := deriveDeterministicRecipientFromPassphraseWithSalt(testStrongPassphrase, salt2)
	if err != nil {
		t.Fatal(err)
	}
	if rec1 == rec2 {
		t.Fatalf("same passphrase produced the same recipient across installs (correlatable): %q", rec1)
	}

	recConst, err := deriveDeterministicRecipientFromPassphrase(testStrongPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	if rec1 == recConst {
		t.Fatalf("random-salt recipient equals the fixed-salt recipient: %q", rec1)
	}
}

// TestPassphraseRandomSaltRoundTripAndIsolation proves the per-install salt is
// actually required to decrypt: with the salt the archive decrypts, and with the
// fixed/legacy salts alone (no manifest salt) it does not.
func TestPassphraseRandomSaltRoundTripAndIsolation(t *testing.T) {
	salt := randomSaltNamespaceV2 + "00112233445566778899aabbccddeeff"
	recStr, err := deriveDeterministicRecipientFromPassphraseWithSalt(testStrongPassphrase, salt)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("top secret backup bytes")
	ciphertext := encryptToRecipient(t, recStr, plaintext)

	// With the per-install salt (as carried in the manifest) → success.
	idsWith, err := parseIdentityInputWithSalts(testStrongPassphrase, []string{salt})
	if err != nil {
		t.Fatalf("parseIdentityInputWithSalts: %v", err)
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), idsWith...)
	if err != nil {
		t.Fatalf("decrypt with per-install salt failed: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read decrypted: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypted = %q, want %q", got, plaintext)
	}

	// Without the salt (only the fixed v1/legacy salts) → must NOT decrypt.
	idsConst, err := parseIdentityInput(testStrongPassphrase)
	if err != nil {
		t.Fatalf("parseIdentityInput: %v", err)
	}
	if _, err := age.Decrypt(bytes.NewReader(ciphertext), idsConst...); err == nil {
		t.Fatalf("archive decrypted WITHOUT the per-install salt; the salt is not actually required")
	}
}

// TestLegacyConstantSaltArchiveStillDecrypts guarantees backward compatibility:
// archives produced before the per-install salt (derived from the fixed salt and
// carrying no manifest salt) keep decrypting via the constant fallbacks.
func TestLegacyConstantSaltArchiveStillDecrypts(t *testing.T) {
	recStr, err := deriveDeterministicRecipientFromPassphrase(testStrongPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("legacy archive payload")
	ciphertext := encryptToRecipient(t, recStr, plaintext)

	ids, err := parseIdentityInput(testStrongPassphrase) // no manifest salt, constants only
	if err != nil {
		t.Fatalf("parseIdentityInput: %v", err)
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), ids...)
	if err != nil {
		t.Fatalf("legacy archive failed to decrypt: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypted = %q, want %q", got, plaintext)
	}
}

func TestManifestPassphraseSalts(t *testing.T) {
	if got := manifestPassphraseSalts(nil); got != nil {
		t.Fatalf("nil manifest: got %v, want nil", got)
	}
	if got := manifestPassphraseSalts(&backup.Manifest{}); got != nil {
		t.Fatalf("empty salt: got %v, want nil", got)
	}
	got := manifestPassphraseSalts(&backup.Manifest{PassphraseSalt: "  the-salt  "})
	if len(got) != 1 || got[0] != "the-salt" {
		t.Fatalf("manifestPassphraseSalts = %v, want [the-salt]", got)
	}
}

func TestPassphraseSaltForManifest(t *testing.T) {
	recipientPath := filepath.Join(t.TempDir(), "identity", "age", "recipient.txt")
	o := &Orchestrator{cfg: &config.Config{EncryptArchive: true, AgeRecipientFile: recipientPath}}

	// (c) salt absent (ENOENT) -> ("", nil): recipient-only setups keep succeeding.
	if got, err := o.passphraseSaltForManifest(); err != nil || got != "" {
		t.Fatalf("expected (%q, nil) before setup, got (%q, %v)", "", got, err)
	}

	salt, err := o.getOrCreatePassphraseSalt(recipientPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := o.passphraseSaltForManifest(); err != nil || got != salt {
		t.Fatalf("passphraseSaltForManifest = (%q, %v), want (%q, nil)", got, err, salt)
	}

	// (d) encryption disabled -> ("", nil).
	o.cfg.EncryptArchive = false
	if got, err := o.passphraseSaltForManifest(); err != nil || got != "" {
		t.Fatalf("expected (%q, nil) when encryption disabled, got (%q, %v)", "", got, err)
	}
}

// saltReadErrFS wraps a FakeFS and fails ReadFile for one path with a
// non-not-exist error, so a test can force a strict read failure on the
// passphrase salt file.
type saltReadErrFS struct {
	*FakeFS
	failPath string
	err      error
}

func (r *saltReadErrFS) ReadFile(path string) ([]byte, error) {
	if path == r.failPath {
		return nil, r.err
	}
	return r.FakeFS.ReadFile(path)
}

// TestPassphraseSaltForManifestEmptyFileFailsClosed: (a) EncryptArchive=true and
// the salt file exists but is empty -> non-nil error. Emitting an archive with an
// omitted manifest salt here would be permanently undecryptable, so fail closed.
func TestPassphraseSaltForManifestEmptyFileFailsClosed(t *testing.T) {
	recipientPath := filepath.Join(t.TempDir(), "identity", "age", "recipient.txt")
	saltPath := passphraseSaltFilePath(recipientPath)
	if err := os.MkdirAll(filepath.Dir(saltPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(saltPath, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := &Orchestrator{cfg: &config.Config{EncryptArchive: true, AgeRecipientFile: recipientPath}}

	got, err := o.passphraseSaltForManifest()
	if err == nil {
		t.Fatalf("empty salt file must fail closed, got (%q, nil)", got)
	}
	if got != "" {
		t.Fatalf("salt on error = %q, want %q", got, "")
	}
}

// TestPassphraseSaltForManifestReadErrorFailsClosed: (b) EncryptArchive=true and
// the salt read fails with a non-ENOENT error (EACCES) -> non-nil error.
func TestPassphraseSaltForManifestReadErrorFailsClosed(t *testing.T) {
	fake := NewFakeFS()
	t.Cleanup(func() { _ = fake.Cleanup() })
	recipientPath := filepath.Join("identity", "age", "recipient.txt")
	saltPath := passphraseSaltFilePath(recipientPath)
	errFS := &saltReadErrFS{FakeFS: fake, failPath: saltPath, err: os.ErrPermission}
	o := &Orchestrator{
		cfg: &config.Config{EncryptArchive: true, AgeRecipientFile: recipientPath},
		fs:  errFS,
	}

	got, err := o.passphraseSaltForManifest()
	if err == nil {
		t.Fatalf("unreadable salt must fail closed, got (%q, nil)", got)
	}
	if got != "" {
		t.Fatalf("salt on error = %q, want %q", got, "")
	}
}

// TestWriteArchiveManifestAbortsOnUnreadableSalt is the caller-level guard: when
// the salt is unreadable and encryption is on, the backup aborts with a
// BackupError (non-zero exit) instead of emitting a silent-success undecryptable
// archive.
func TestWriteArchiveManifestAbortsOnUnreadableSalt(t *testing.T) {
	fake := NewFakeFS()
	t.Cleanup(func() { _ = fake.Cleanup() })
	recipientPath := filepath.Join("identity", "age", "recipient.txt")
	saltPath := passphraseSaltFilePath(recipientPath)
	errFS := &saltReadErrFS{FakeFS: fake, failPath: saltPath, err: os.ErrPermission}
	o := &Orchestrator{
		cfg: &config.Config{EncryptArchive: true, AgeRecipientFile: recipientPath},
		fs:  errFS,
	}

	run := &backupRunContext{ctx: context.Background(), stats: &BackupStats{}}
	artifacts := &backupArtifacts{archivePath: filepath.Join(fake.Root, "archive.tar.zst")}

	err := o.writeArchiveManifest(run, artifacts, "deadbeef")
	if err == nil {
		t.Fatalf("writeArchiveManifest must abort when the salt is unreadable")
	}
	var be *BackupError
	if !errors.As(err, &be) {
		t.Fatalf("want *BackupError, got %T: %v", err, err)
	}
	if run.stats.ManifestPath != "" {
		t.Fatalf("manifest path must not be recorded on abort, got %q", run.stats.ManifestPath)
	}
	if artifacts.manifestPath != "" {
		t.Fatalf("artifacts manifest path must not be set on abort, got %q", artifacts.manifestPath)
	}
}
