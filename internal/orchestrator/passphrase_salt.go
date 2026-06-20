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

// readPassphraseSalt returns the persisted per-installation salt next to
// recipientPath, or "" if it is absent/unreadable.
func (o *Orchestrator) readPassphraseSalt(recipientPath string) string {
	if strings.TrimSpace(recipientPath) == "" {
		return ""
	}
	data, err := o.filesystem().ReadFile(passphraseSaltFilePath(recipientPath))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// passphraseSaltForManifest returns the per-installation salt to embed in an
// archive manifest, or "" when encryption is off or no passphrase salt exists
// (X25519/SSH-only setups, or legacy installs still on the fixed salt).
func (o *Orchestrator) passphraseSaltForManifest() string {
	if o == nil || o.cfg == nil || !o.cfg.EncryptArchive {
		return ""
	}
	recipientPath := strings.TrimSpace(o.cfg.AgeRecipientFile)
	if recipientPath == "" {
		recipientPath = o.defaultAgeRecipientFile()
	}
	return o.readPassphraseSalt(recipientPath)
}
