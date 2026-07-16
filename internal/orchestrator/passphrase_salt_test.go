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

// countSaltCommentLines counts "# passphrase-salt:" comment lines in a
// recipient-file body.
func countSaltCommentLines(content string) int {
	n := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), passphraseSaltCommentPrefix) {
			n++
		}
	}
	return n
}

// TestSetupCoLocatesPassphraseSaltInRecipientFile drives the passphrase setup
// path end-to-end and asserts the salt is co-located inside recipient.txt as a
// single "# passphrase-salt: <salt>" comment. Deleting the standalone
// passphrase.salt sibling then no longer loses the salt: passphraseSaltForManifest
// still returns it, sourced from the recipient file.
func TestSetupCoLocatesPassphraseSaltInRecipientFile(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{EncryptArchive: true, BaseDir: tmp}
	o := newEncryptionTestOrchestrator(cfg)

	ui := &mockAgeSetupUI{
		AbortErr: ErrAgeRecipientSetupAborted,
		Drafts: []*AgeRecipientDraft{
			{Kind: AgeRecipientInputPassphrase, Passphrase: testStrongPassphrase},
		},
		AddMore: []bool{false},
	}
	if err := o.EnsureAgeRecipientsReadyWithUI(context.Background(), ui); err != nil {
		t.Fatalf("EnsureAgeRecipientsReadyWithUI: %v", err)
	}

	target := filepath.Join(tmp, "identity", "age", "recipient.txt")
	// The persisted sibling salt is the ground truth (idempotent read).
	wantSalt, err := o.getOrCreatePassphraseSalt(target)
	if err != nil {
		t.Fatalf("getOrCreatePassphraseSalt: %v", err)
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read recipient file: %v", err)
	}
	if n := countSaltCommentLines(string(content)); n != 1 {
		t.Fatalf("recipient file has %d salt comment lines, want exactly 1:\n%s", n, content)
	}
	if wantLine := passphraseSaltCommentPrefix + " " + wantSalt; !strings.Contains(string(content), wantLine+"\n") {
		t.Fatalf("recipient file missing %q:\n%s", wantLine, content)
	}

	// The recipient line must survive alongside the comment and still parse
	// (the comment is skipped by readRecipientFile).
	wantRec, err := deriveDeterministicRecipientFromPassphraseWithSalt(testStrongPassphrase, wantSalt)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := readRecipientFile(target)
	if err != nil {
		t.Fatalf("readRecipientFile: %v", err)
	}
	if len(recs) != 1 || recs[0] != wantRec {
		t.Fatalf("readRecipientFile = %v, want [%s]", recs, wantRec)
	}

	// Delete the sibling: the co-located salt must keep the manifest salt available.
	if err := os.Remove(passphraseSaltFilePath(target)); err != nil {
		t.Fatalf("remove sibling salt: %v", err)
	}
	got, err := o.passphraseSaltForManifest()
	if err != nil {
		t.Fatalf("passphraseSaltForManifest after sibling deletion: %v", err)
	}
	if got != wantSalt {
		t.Fatalf("passphraseSaltForManifest = %q, want %q (co-located salt lost)", got, wantSalt)
	}
}

// TestCoLocatedSaltDecryptsAfterSiblingDeleted proves the BACKUP-SAFETY goal:
// an archive encrypted to the passphrase recipient still decrypts after the
// passphrase.salt sibling is deleted, because the salt is co-located in
// recipient.txt and flows into the manifest.
func TestCoLocatedSaltDecryptsAfterSiblingDeleted(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{EncryptArchive: true, BaseDir: tmp}
	o := newEncryptionTestOrchestrator(cfg)
	ui := &mockAgeSetupUI{
		AbortErr: ErrAgeRecipientSetupAborted,
		Drafts:   []*AgeRecipientDraft{{Kind: AgeRecipientInputPassphrase, Passphrase: testStrongPassphrase}},
		AddMore:  []bool{false},
	}
	if err := o.EnsureAgeRecipientsReadyWithUI(context.Background(), ui); err != nil {
		t.Fatalf("setup: %v", err)
	}
	target := filepath.Join(tmp, "identity", "age", "recipient.txt")

	recs, err := readRecipientFile(target)
	if err != nil || len(recs) != 1 {
		t.Fatalf("readRecipientFile = %v, %v", recs, err)
	}
	plaintext := []byte("passphrase archive payload")
	ciphertext := encryptToRecipient(t, recs[0], plaintext)

	// Simulate the live data-loss: delete the standalone salt file.
	if err := os.Remove(passphraseSaltFilePath(target)); err != nil {
		t.Fatalf("remove sibling: %v", err)
	}

	// The manifest salt is recovered from recipient.txt.
	salt, err := o.passphraseSaltForManifest()
	if err != nil || salt == "" {
		t.Fatalf("passphraseSaltForManifest = (%q, %v), want non-empty salt", salt, err)
	}
	ids, err := parseIdentityInputWithSalts(testStrongPassphrase, []string{salt})
	if err != nil {
		t.Fatalf("parseIdentityInputWithSalts: %v", err)
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), ids...)
	if err != nil {
		t.Fatalf("decrypt after sibling deletion failed: %v", err)
	}
	gotPlain, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPlain, plaintext) {
		t.Fatalf("decrypted = %q, want %q", gotPlain, plaintext)
	}
}

// TestSetupRecipientOnlyHasNoSaltComment ensures recipient-only (X25519) setups
// never gain a salt comment: there is no passphrase.salt sibling to co-locate.
func TestSetupRecipientOnlyHasNoSaltComment(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}
	tmp := t.TempDir()
	cfg := &config.Config{EncryptArchive: true, BaseDir: tmp}
	o := newEncryptionTestOrchestrator(cfg)
	ui := &mockAgeSetupUI{
		AbortErr: ErrAgeRecipientSetupAborted,
		Drafts:   []*AgeRecipientDraft{{Kind: AgeRecipientInputExisting, PublicKey: id.Recipient().String()}},
		AddMore:  []bool{false},
	}
	if err := o.EnsureAgeRecipientsReadyWithUI(context.Background(), ui); err != nil {
		t.Fatalf("setup: %v", err)
	}
	target := filepath.Join(tmp, "identity", "age", "recipient.txt")
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read recipient file: %v", err)
	}
	if n := countSaltCommentLines(string(content)); n != 0 {
		t.Fatalf("recipient-only setup gained %d salt comment(s):\n%s", n, content)
	}
	if _, err := os.Stat(passphraseSaltFilePath(target)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recipient-only setup created a passphrase.salt sibling: %v", err)
	}
}

// TestCoLocatePassphraseSaltIdempotent proves the helper is safe to re-run (as
// Task 6 does at backup time for backfill): a second call neither duplicates the
// comment nor rewrites the file, and it is a no-op when no sibling salt exists.
func TestCoLocatePassphraseSaltIdempotent(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "identity", "age", "recipient.txt")
	o := &Orchestrator{}

	salt, err := o.getOrCreatePassphraseSalt(target)
	if err != nil {
		t.Fatalf("getOrCreatePassphraseSalt: %v", err)
	}
	rec, err := deriveDeterministicRecipientFromPassphraseWithSalt(testStrongPassphrase, salt)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRecipientFile(target, []string{rec}); err != nil {
		t.Fatalf("writeRecipientFile: %v", err)
	}

	if err := o.coLocatePassphraseSalt(target); err != nil {
		t.Fatalf("coLocatePassphraseSalt (1): %v", err)
	}
	first, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if n := countSaltCommentLines(string(first)); n != 1 {
		t.Fatalf("after first co-locate: %d salt comments, want 1:\n%s", n, first)
	}

	// Second call must be a byte-for-byte no-op (single comment, no duplication).
	if err := o.coLocatePassphraseSalt(target); err != nil {
		t.Fatalf("coLocatePassphraseSalt (2): %v", err)
	}
	second, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("second co-locate changed the file:\nfirst=%q\nsecond=%q", first, second)
	}
	if info, err := os.Stat(target); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("recipient file perm = %v, want 0600", perm)
	}

	// No sibling -> no-op even though recipient.txt exists.
	if err := os.Remove(passphraseSaltFilePath(target)); err != nil {
		t.Fatal(err)
	}
	recipientOnly := filepath.Join(t.TempDir(), "recipient.txt")
	if err := writeRecipientFile(recipientOnly, []string{rec}); err != nil {
		t.Fatal(err)
	}
	if err := o.coLocatePassphraseSalt(recipientOnly); err != nil {
		t.Fatalf("coLocatePassphraseSalt recipient-only: %v", err)
	}
	content, err := os.ReadFile(recipientOnly)
	if err != nil {
		t.Fatal(err)
	}
	if n := countSaltCommentLines(string(content)); n != 0 {
		t.Fatalf("recipient-only gained %d salt comment(s):\n%s", n, content)
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

// recipientWriteErrFS wraps a FakeFS and fails the atomic write of one target
// path (its ".proxsave.tmp.<nano>" OpenFile), so a test can force
// coLocatePassphraseSalt's recipient-file rewrite to fail while every read still
// succeeds.
type recipientWriteErrFS struct {
	*FakeFS
	target string
	err    error
}

func (r *recipientWriteErrFS) OpenFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	if strings.HasPrefix(path, r.target+".proxsave.tmp.") {
		return nil, r.err
	}
	return r.FakeFS.OpenFile(path, flag, perm)
}

// TestWriteArchiveManifestBackfillsCoLocatedSalt drives an existing passphrase
// install (salt only in the passphrase.salt sibling, recipient.txt without the
// co-located comment) through writeArchiveManifest and asserts the salt is
// backfilled into recipient.txt, so a later sibling deletion no longer loses it.
func TestWriteArchiveManifestBackfillsCoLocatedSalt(t *testing.T) {
	tmp := t.TempDir()
	recipientPath := filepath.Join(tmp, "identity", "age", "recipient.txt")
	o := newEncryptionTestOrchestrator(&config.Config{EncryptArchive: true, AgeRecipientFile: recipientPath})

	// Old install: sibling salt present, recipient.txt carries only the recipient
	// line (no co-located comment).
	salt, err := o.getOrCreatePassphraseSalt(recipientPath)
	if err != nil {
		t.Fatalf("getOrCreatePassphraseSalt: %v", err)
	}
	rec, err := deriveDeterministicRecipientFromPassphraseWithSalt(testStrongPassphrase, salt)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRecipientFile(recipientPath, []string{rec}); err != nil {
		t.Fatalf("writeRecipientFile: %v", err)
	}
	if content, _ := os.ReadFile(recipientPath); countSaltCommentLines(string(content)) != 0 {
		t.Fatalf("precondition: recipient.txt already carries a salt comment:\n%s", content)
	}

	archiveDir := t.TempDir()
	run := &backupRunContext{ctx: context.Background(), stats: &BackupStats{}}
	artifacts := &backupArtifacts{archivePath: filepath.Join(archiveDir, "archive.tar.zst")}
	if err := o.writeArchiveManifest(run, artifacts, "deadbeef"); err != nil {
		t.Fatalf("writeArchiveManifest: %v", err)
	}

	// The salt is now co-located in recipient.txt (exactly one comment, matching
	// the sibling).
	content, err := os.ReadFile(recipientPath)
	if err != nil {
		t.Fatal(err)
	}
	if n := countSaltCommentLines(string(content)); n != 1 {
		t.Fatalf("recipient.txt has %d salt comment lines after backfill, want 1:\n%s", n, content)
	}
	if wantLine := passphraseSaltCommentPrefix + " " + salt; !strings.Contains(string(content), wantLine+"\n") {
		t.Fatalf("recipient.txt missing backfilled %q:\n%s", wantLine, content)
	}

	// Deleting the sibling no longer loses the salt: it resolves from recipient.txt.
	if err := os.Remove(passphraseSaltFilePath(recipientPath)); err != nil {
		t.Fatalf("remove sibling: %v", err)
	}
	got, err := o.passphraseSaltForManifest()
	if err != nil || got != salt {
		t.Fatalf("passphraseSaltForManifest after sibling deletion = (%q, %v), want (%q, nil)", got, err, salt)
	}
}

// TestWriteArchiveManifestBackfillFailureIsNonFatal proves the backfill is
// best-effort: when co-locating the salt into recipient.txt fails (FS seam write
// error), writeArchiveManifest still succeeds and the salt still resolves from
// the passphrase.salt sibling for this run.
func TestWriteArchiveManifestBackfillFailureIsNonFatal(t *testing.T) {
	fake := NewFakeFS()
	t.Cleanup(func() { _ = fake.Cleanup() })
	recipientPath := filepath.Join("identity", "age", "recipient.txt")

	// Old install state inside the fake FS: sibling salt present, recipient.txt
	// with only the recipient line (no co-located comment).
	salt := randomSaltNamespaceV2 + "00112233445566778899aabbccddeeff"
	if err := fake.WriteFile(passphraseSaltFilePath(recipientPath), []byte(salt+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := deriveDeterministicRecipientFromPassphraseWithSalt(testStrongPassphrase, salt)
	if err != nil {
		t.Fatal(err)
	}
	if err := fake.WriteFile(recipientPath, []byte(rec+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	errFS := &recipientWriteErrFS{FakeFS: fake, target: recipientPath, err: os.ErrPermission}
	o := newEncryptionTestOrchestrator(&config.Config{EncryptArchive: true, AgeRecipientFile: recipientPath})
	o.fs = errFS

	run := &backupRunContext{ctx: context.Background(), stats: &BackupStats{}}
	artifacts := &backupArtifacts{archivePath: filepath.Join(fake.Root, "archive.tar.zst")}
	if err := o.writeArchiveManifest(run, artifacts, "deadbeef"); err != nil {
		t.Fatalf("writeArchiveManifest must not fail when the backfill write fails (best-effort): %v", err)
	}

	// The backfill write failed: recipient.txt still carries no comment.
	if content, _ := fake.ReadFile(recipientPath); countSaltCommentLines(string(content)) != 0 {
		t.Fatalf("recipient.txt unexpectedly gained a salt comment despite the write failure:\n%s", content)
	}
	// The salt still resolves from the sibling this run.
	got, err := o.passphraseSaltForManifest()
	if err != nil || got != salt {
		t.Fatalf("passphraseSaltForManifest = (%q, %v), want (%q, nil) from the sibling", got, err, salt)
	}
	if run.stats.ManifestPath == "" {
		t.Fatalf("manifest path must be recorded on success")
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
