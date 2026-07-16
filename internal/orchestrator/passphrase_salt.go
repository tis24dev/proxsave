package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// randomSaltNamespaceV2 namespaces the per-installation random salt used to
// derive a passphrase-based AGE recipient. The "v2" generation replaces the
// fixed salts (recipientSaltV1 / legacyRecipientSalt), which
// remain accepted at decrypt time for backward compatibility with older archives.
const randomSaltNamespaceV2 = "proxsave/age-passphrase/v2:"

// passphraseSaltCommentPrefix marks the passphrase salt co-located inside the
// recipient file as "# passphrase-salt: <salt>". The line is age-parser-safe:
// readRecipientFile skips "#"-prefixed lines.
const passphraseSaltCommentPrefix = "# passphrase-salt:"

// passphraseSaltFilePath returns the salt file that sits next to a recipient file.
func passphraseSaltFilePath(recipientPath string) string {
	return filepath.Join(filepath.Dir(recipientPath), "passphrase.salt")
}

// getOrCreatePassphraseSalt returns the per-installation passphrase salt stored
// next to recipientPath, generating and persisting a fresh random one if absent.
// The salt is public (it only provides domain separation / anti-precomputation):
// it is stored 0600 next to the recipient and also embedded in each archive
// manifest so the passphrase alone can re-derive the identity on any host.
func (o *Orchestrator) getOrCreatePassphraseSalt(recipientPath string) (string, error) {
	if strings.TrimSpace(recipientPath) == "" {
		return "", fmt.Errorf("recipient path is required to resolve the passphrase salt")
	}
	fs := o.filesystem()
	saltPath := passphraseSaltFilePath(recipientPath)

	data, err := fs.ReadFile(saltPath)
	if err == nil {
		if salt := strings.TrimSpace(string(data)); salt != "" {
			return salt, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read passphrase salt %s: %w", saltPath, err)
	}

	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate passphrase salt: %w", err)
	}
	salt := randomSaltNamespaceV2 + hex.EncodeToString(raw)
	if err := fs.MkdirAll(filepath.Dir(saltPath), 0o700); err != nil {
		return "", fmt.Errorf("create passphrase salt directory: %w", err)
	}
	if err := writeFileAtomicWithDeps(fs, o.clock, saltPath, []byte(salt+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("persist passphrase salt %s: %w", saltPath, err)
	}
	return salt, nil
}

// coLocatePassphraseSalt embeds the per-installation passphrase salt as a
// "# passphrase-salt: <salt>" comment inside the recipient file, so deleting the
// standalone passphrase.salt sibling no longer loses the sole record needed to
// re-derive the passphrase identity. The salt is a KDF salt (public, not secret);
// the recipient file keeps its 0600 permission and its existing recipient lines.
//
// It is a no-op when there is no passphrase.salt sibling (recipient-only /
// X25519 setups never create one), when the sibling is empty, or when the
// recipient file already carries exactly the same salt comment (idempotent). It
// is best-effort and FS-seamed so it can be reused at setup and at backup-time
// backfill for existing installs.
func (o *Orchestrator) coLocatePassphraseSalt(recipientPath string) error {
	if strings.TrimSpace(recipientPath) == "" {
		return nil
	}
	fs := o.filesystem()

	// Passphrase-mode signal: getOrCreatePassphraseSalt persists the sibling for
	// passphrase setups and never for recipient-only ones. No sibling -> nothing
	// to co-locate.
	saltPath := passphraseSaltFilePath(recipientPath)
	saltData, err := fs.ReadFile(saltPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read passphrase salt %s: %w", saltPath, err)
	}
	salt := strings.TrimSpace(string(saltData))
	if salt == "" {
		return nil
	}

	// The recipient file must exist to encrypt; read it to preserve its lines.
	content, err := fs.ReadFile(recipientPath)
	if err != nil {
		return fmt.Errorf("read recipient file %s: %w", recipientPath, err)
	}

	// Drop any existing salt comment(s), keep every other line, then re-add a
	// single canonical salt comment at the top.
	body := make([]string, 0)
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), passphraseSaltCommentPrefix) {
			continue
		}
		body = append(body, line)
	}
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	saltComment := passphraseSaltCommentPrefix + " " + salt
	newContent := strings.Join(append([]string{saltComment}, body...), "\n") + "\n"
	if newContent == string(content) {
		return nil // already co-located and canonical: idempotent no-op
	}
	if err := writeFileAtomicWithDeps(fs, o.clock, recipientPath, []byte(newContent), 0o600); err != nil {
		return fmt.Errorf("co-locate passphrase salt in %s: %w", recipientPath, err)
	}
	return nil
}

// readCoLocatedPassphraseSalt returns the salt embedded as a
// "# passphrase-salt: <salt>" comment inside the recipient file, or "" when the
// file is missing/unreadable or carries no such comment. It never errors: a
// missing co-located salt falls through to the sibling read.
func (o *Orchestrator) readCoLocatedPassphraseSalt(recipientPath string) string {
	if strings.TrimSpace(recipientPath) == "" {
		return ""
	}
	data, err := o.filesystem().ReadFile(recipientPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, passphraseSaltCommentPrefix) {
			continue
		}
		if salt := strings.TrimSpace(strings.TrimPrefix(trimmed, passphraseSaltCommentPrefix)); salt != "" {
			return salt
		}
	}
	return ""
}

// readPassphraseSalt returns the persisted per-installation salt for
// recipientPath. It prefers the salt co-located inside the recipient file (which
// survives deletion of the standalone passphrase.salt sibling), then falls back
// to the sibling. An absent salt in both places (ENOENT) yields ("", nil) so
// recipient-only (X25519/SSH) and legacy fixed-salt setups keep succeeding with
// no manifest salt. A sibling that exists but is unreadable or empty (and no
// co-located salt) is fatal: emitting an archive with an omitted salt in that
// case would be permanently undecryptable.
func (o *Orchestrator) readPassphraseSalt(recipientPath string) (string, error) {
	if strings.TrimSpace(recipientPath) == "" {
		return "", nil
	}
	if salt := o.readCoLocatedPassphraseSalt(recipientPath); salt != "" {
		return salt, nil
	}
	path := passphraseSaltFilePath(recipientPath)
	data, err := o.filesystem().ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read passphrase salt %s: %w", path, err)
	}
	salt := strings.TrimSpace(string(data))
	if salt == "" {
		return "", fmt.Errorf("passphrase salt %s is empty", path)
	}
	return salt, nil
}

// passphraseSaltForManifest returns the per-installation salt to embed in an
// archive manifest, or "" when encryption is off or no passphrase salt exists
// (X25519/SSH-only setups, or legacy installs still on the fixed salt). It
// returns a non-nil error when encryption is on and the salt file exists but is
// unreadable or empty, so the caller can fail the backup closed.
func (o *Orchestrator) passphraseSaltForManifest() (string, error) {
	if o == nil || o.cfg == nil || !o.cfg.EncryptArchive {
		return "", nil
	}
	recipientPath := strings.TrimSpace(o.cfg.AgeRecipientFile)
	if recipientPath == "" {
		recipientPath = o.defaultAgeRecipientFile()
	}
	return o.readPassphraseSalt(recipientPath)
}
