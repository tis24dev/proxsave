package orchestrator

import (
	"bytes"
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
	if !strings.HasPrefix(salt, passphraseRandomSaltPrefix) {
		t.Fatalf("salt %q missing prefix %q", salt, passphraseRandomSaltPrefix)
	}
	if salt == passphraseRecipientSalt || salt == legacyPassphraseRecipientSalt {
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
	if got := o.readPassphraseSalt(recipientPath); got != salt {
		t.Fatalf("readPassphraseSalt = %q, want %q", got, salt)
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
	salt := passphraseRandomSaltPrefix + "00112233445566778899aabbccddeeff"
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

	if got := o.passphraseSaltForManifest(); got != "" {
		t.Fatalf("expected empty salt before setup, got %q", got)
	}

	salt, err := o.getOrCreatePassphraseSalt(recipientPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := o.passphraseSaltForManifest(); got != salt {
		t.Fatalf("passphraseSaltForManifest = %q, want %q", got, salt)
	}

	o.cfg.EncryptArchive = false
	if got := o.passphraseSaltForManifest(); got != "" {
		t.Fatalf("expected empty salt when encryption disabled, got %q", got)
	}
}
