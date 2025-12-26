# Encryption Guide

Complete guide to AGE encryption for Proxsave.

## Table of Contents

- [Overview](#overview)
- [Features](#features)
- [Quick Start](#quick-start)
- [Configure Recipients](#configure-recipients)
  - [Static Configuration](#static-configuration)
  - [Interactive Wizard](#interactive-wizard)
- [Running Encrypted Backups](#running-encrypted-backups)
- [Decrypting Backups](#decrypting-backups)
- [Restoring Encrypted Backups](#restoring-encrypted-backups)
- [Key Rotation](#key-rotation)
- [Emergency Scenarios](#emergency-scenarios)
- [Security Notes](#security-notes)
- [Related Documentation](#related-documentation)

---

## Overview

Proxsave uses the **[age](https://age-encryption.org/)** format (via `filippo.io/age`) for encryption. AGE is a modern, simple, and secure file encryption format designed to replace GPG for basic use cases.

**Key characteristics**:
- **Streaming encryption**: Backups encrypted as they're created (no plaintext on disk)
- **Multiple recipients**: Support for both passphrase and key-based encryption
- **Memory safety**: Sensitive data zeroed immediately after use
- **Standard format**: Compatible with standard AGE tools

---

## Features

| Feature | Description |
|---------|-------------|
| **Encryption algorithm** | ChaCha20-Poly1305 (AEAD) with X25519 key exchange |
| **Key types** | Passphrase or X25519 key pair |
| **Multiple recipients** | Single backup can be decrypted with any configured recipient |
| **Interactive setup** | `--newkey` (or the first encrypted run) helps you configure recipients |
| **Streaming mode** | Encrypts during backup creation (no temporary plaintext) |
| **Security** | Passphrases read with `term.ReadPassword`, buffers zeroed after use |
| **File permissions** | Enforces 0700/0600 on recipient files |

---

## Quick Start

### 1. Generate Recipients

**Option A: Interactive wizard** (recommended for beginners):

```bash
./build/proxsave --newkey
```

**Option B: Manual generation** with standard AGE tools:

```bash
# Generate key pair (keep the private key offline if possible)
age-keygen -o age-keys.txt

# Extract the public recipient (starts with "age1...")
grep "# public key:" age-keys.txt | cut -d: -f2 | tr -d ' '

# Then paste the recipient into:
#   ./build/proxsave --newkey
# or configure it via AGE_RECIPIENT / AGE_RECIPIENT_FILE in configs/backup.env
```

### 2. Configure Environment

Add to `configs/backup.env`:

```bash
# Enable encryption
ENCRYPT_ARCHIVE=true

# Recipients (public keys). You can use the inline list, the file, or both.
# Inline list supports separators: comma, semicolon, pipe, newline
AGE_RECIPIENT=

# Default recipient file (created by the wizard on first run)
AGE_RECIPIENT_FILE=${BASE_DIR}/identity/age/recipient.txt
```

### 3. Run Encrypted Backup

```bash
./build/proxsave
# Archive will be encrypted (archive ends with .age; if bundling is enabled, output ends with .age.bundle.tar)
```

### 4. Decrypt When Needed

```bash
# Interactive decryption
./build/proxsave --decrypt
```

---

## Configure Recipients

Recipients are public keys or passphrases that can decrypt backups. A backup encrypted for **N recipients** can be decrypted by **any of the N private keys/passphrases**.

### Static Configuration

**File** (default): `${BASE_DIR}/identity/age/recipient.txt`

```plaintext
# AGE recipients (one per line)

# X25519 public recipients (recommended)
age1abc123def456ghi789jkl012mno345pqr678stu901vwx234yz567abc

# Recipient derived from a passphrase (still an "age1..." recipient; the passphrase is NOT stored)
age1def456ghi789jkl012mno345pqr678stu901vwx234yz567abc123def
```

**Format**:
- One recipient per line
- Blank lines and `#` comments ignored
- Mix key types freely

### Interactive Wizard

You can create/update recipients in two ways:

```bash
# Dedicated wizard (TUI by default)
./build/proxsave --newkey

# Use CLI prompts instead of TUI (useful for debugging and multi-recipient setups)
./build/proxsave --newkey --cli
```

If `ENCRYPT_ARCHIVE=true` and no recipients are configured, proxsave will start an interactive setup automatically during the backup (only when running in a real terminal).

**Setup options** (TUI/CLI):
- Paste an existing AGE public recipient (`age1...`)
- Enter a passphrase to derive a deterministic AGE key (passphrase is **not stored**)
- Paste an AGE private key (`AGE-SECRET-KEY-...`) to derive its public recipient (key is **not stored**)

**Notes**:
- Proxsave stores **only recipients** (public keys) in `${BASE_DIR}/identity/age/recipient.txt`. Keep private keys and passphrases offline.
- `AGE_RECIPIENT` and `AGE_RECIPIENT_FILE` are **merged and de-duplicated**.
- The CLI setup supports multiple recipients; otherwise you can add multiple recipients by editing the file (one per line).

---

## Running Encrypted Backups

### Prerequisites

1. `ENCRYPT_ARCHIVE=true` in `configs/backup.env`
2. At least one recipient configured via `AGE_RECIPIENT` and/or `AGE_RECIPIENT_FILE`
3. File permissions checked automatically (0700 directory, 0600 recipient file)

### Backup Execution

```bash
./build/proxsave
```

**Encryption flow**:

```
┌─────────────────────────────────────────────┐
│  Phase 1: Backup Collection                 │
│  - Gather PVE/PBS/System files              │
│  - Create TAR archive in memory             │
└─────────────┬───────────────────────────────┘
              │
              ▼
┌─────────────────────────────────────────────┐
│  Phase 2: Streaming Encryption              │
│  - Read TAR stream                          │
│  - Encrypt with AGE (ChaCha20-Poly1305)     │
│  - Write to <HOST>-backup-YYYYMMDD-HHMMSS.tar.<ext>.age │
│  - NO plaintext on disk                     │
└─────────────┬───────────────────────────────┘
              │
              ▼
┌─────────────────────────────────────────────┐
│  Phase 3: Storage Distribution              │
│  - Local: BACKUP_PATH/                      │
│  - Secondary: SECONDARY_PATH/ (optional)    │
│  - Cloud: rclone (optional)                 │
└─────────────────────────────────────────────┘
```

**Output file format**:

```
backup/
└── pve-node1-backup-20240115-023000.tar.xz.age.bundle.tar   # Typical (bundling enabled)
```

**If bundling is disabled** (`BUNDLE_ASSOCIATED_FILES=false`), proxsave keeps the raw artifacts:

```
backup/
├── pve-node1-backup-20240115-023000.tar.xz.age
├── pve-node1-backup-20240115-023000.tar.xz.age.sha256
├── pve-node1-backup-20240115-023000.tar.xz.age.metadata
└── pve-node1-backup-20240115-023000.tar.xz.age.manifest.json
```

**Manifest structure** (used during restore):

```json
{
  "archive_path": "/opt/proxsave/backup/pve-node1-backup-20240115-023000.tar.xz.age",
  "archive_size": 1234567890,
  "sha256": "…",
  "created_at": "2024-01-15T02:30:00Z",
  "compression_type": "xz",
  "hostname": "pve-node1",
  "encryption_mode": "age"
}
```

---

## Decrypting Backups

The `--decrypt` workflow converts an encrypted backup into a decrypted bundle for inspection or transfer.

```bash
./build/proxsave --decrypt
```

**High-level flow**:
1. Select backup source (primary/secondary/cloud)
2. Select an encrypted backup
3. Select destination folder (default: `./decrypt` or `${BASE_DIR}/decrypt`)
4. When prompted, enter:
   - an AGE private key (`AGE-SECRET-KEY-...`), or
   - the passphrase you used (proxsave derives the matching identity; the passphrase is not stored)

**Output**:
- A decrypted bundle saved as: `*.decrypted.bundle.tar`

If you need fully scripted/non-interactive decryption, use the official `age` CLI tool:

```bash
age --decrypt -i /path/to/age-keys.txt host-backup-YYYYMMDD-HHMMSS.tar.xz.age > host-backup-YYYYMMDD-HHMMSS.tar.xz
```

---

## Restoring Encrypted Backups

Encrypted backups can be restored using the standard `--restore` command. The decryption is handled automatically during the restore workflow.

### Quick Restore Summary

```bash
# Interactive restore (decrypts automatically)
./build/proxsave --restore
```

**Restore workflow with encryption**:

1. **Select backup** → Choose encrypted `.age` file
2. **Decrypt** → Provide key or passphrase when prompted
3. **Choose restore mode** → Full/Storage/Base/Custom
4. **Select categories** → PVE configs, PBS datastores, etc.
5. **Review and confirm** → Safety checks before applying
6. **Extract files** → Categories restored to system

**Decryption options during restore**:
- **Key or passphrase**: Prompted interactively when needed
- **Multiple recipients**: Any recipient that matches the archive can decrypt it

### Detailed Restore Documentation

For complete restore workflows, category details, safety features, and cluster recovery procedures, see:

- **[Restore Guide](RESTORE_GUIDE.md)** - Complete user guide with all restore modes
- **[Restore Technical](RESTORE_TECHNICAL.md)** - Technical architecture and internals
- **[Cluster Recovery](CLUSTER_RECOVERY.md)** - Advanced cluster disaster recovery

**Key topics covered in restore docs**:
- 4 restore modes (Full/Storage/Base/Custom)
- 15+ category reference
- Service management for cluster databases
- Safety features and rollback procedures
- Post-restore verification
- Troubleshooting

---

## Key Rotation

Rotating encryption keys periodically improves security (recommended annually or after key compromise).

### Recommended Rotation (Multi-Recipient, No Downtime)

1. Generate a new AGE key pair (preferably on an offline machine):
   ```bash
   age-keygen -o age-keys-2025.txt
   ```

2. Extract the new public recipient and append it to the recipient file:
   ```bash
   grep "# public key:" age-keys-2025.txt | cut -d: -f2 | tr -d ' ' >> ${BASE_DIR}/identity/age/recipient.txt
   ```

3. Run backups for a while: new backups can be decrypted with **either** the old or the new private key.

4. After retention deletes older backups, remove the old recipient line from `${BASE_DIR}/identity/age/recipient.txt`.

**Important**:
- Keep old private keys until you are sure all old backups are expired (or safely archived).
- Proxsave stores only recipients; private keys/passphrases remain your responsibility.

### Full Replacement (Reset Recipients)

To replace recipients completely (for example after a key compromise), run:

```bash
./build/proxsave --newkey
```

This overwrites the recipient file after confirmation. Back up `${BASE_DIR}/identity/age/recipient.txt` first if you need rollback.

---

## Emergency Scenarios

| Scenario | Solution |
|----------|----------|
| **Lost passphrase/private key** | **No recovery possible**. Keep 2+ offline copies (password manager, printed paper). |
| **Migrating to new server** | Copy the recipient file (`${BASE_DIR}/identity/age/recipient.txt`) and your `configs/backup.env`. Keep private keys offline. |
| **Verifying integrity** | Periodically decrypt a backup (or run a restore in a test VM) to ensure keys and archives are valid. |
| **Automation** | Headless runs require recipients pre-configured (`AGE_RECIPIENT` and/or `AGE_RECIPIENT_FILE`). |
| **Recipient file overwritten** | Restore from your own backup copy (or from `recipient.txt.bak-*` if you created one). |

### Emergency Decryption Without Configuration

If you have the private key but lost all configuration:

```bash
# Manual decryption with age tools
age --decrypt -i /path/to/age-keys.txt host-backup-YYYYMMDD-HHMMSS.tar.xz.age > decrypted.tar.xz

# Extract archive
tar -xf decrypted.tar.xz -C /tmp/emergency-restore
```

### Testing Backup Recoverability

Periodically verify backups are decryptable:

```bash
# Example: decrypt with age CLI on a safe machine and list archive content
age --decrypt -i /path/to/age-keys.txt host-backup-YYYYMMDD-HHMMSS.tar.xz.age | tar -t >/dev/null && echo "✓ Archive valid"
```

**Recommended schedule**: Monthly automated test + manual review.

---

## Security Notes

### Encryption Implementation

- **Algorithm**: ChaCha20-Poly1305 (AEAD) with X25519 ECDH
- **Key derivation**: scrypt (N=2^15, r=8, p=1) for passphrases
- **Random nonces**: Unique per encryption operation
- **Authentication**: Poly1305 MAC prevents tampering

### Security Best Practices

| Practice | Implementation |
|----------|----------------|
| **Passphrase handling** | Read with `term.ReadPassword` (no echo) |
| **Memory security** | Buffers zeroed immediately after use |
| **Streaming encryption** | No plaintext on disk during backup |
| **File permissions** | Enforced 0700/0600 on recipient/identity files |
| **Private key storage** | **Keep offline** (password manager, hardware token, printed backup) |
| **Backup separation** | Store keys separately from backup media |
| **Access control** | Limit who has decryption keys |

### Private Key Protection

**⚠️ CRITICAL**: Private keys allow decryption of ALL backups. Protect them as you would the data itself.

**Storage recommendations** (choose 2+ for redundancy):

1. **Password manager** (1Password, Bitwarden, KeePassXC)
   - Encrypted vault with strong master password
   - Accessible from multiple devices
   - Regular backups

2. **Hardware token** (YubiKey, Nitrokey)
   - Physical device required for decryption
   - Resistant to remote attacks
   - Risk: device loss

3. **Printed paper backup**
   - QR code + text format
   - Store in safe or safety deposit box
   - Immune to digital attacks

4. **Offline encrypted USB**
   - LUKS/VeraCrypt encrypted volume
   - Store in secure physical location
   - Air-gapped from network

**Never**:
- ❌ Store private keys on the same server as backups
- ❌ Commit private keys to git repositories
- ❌ Email private keys (even encrypted)
- ❌ Store in cloud drives without additional encryption

### Threat Model

**Protected against**:
- ✅ Backup media theft (encrypted at rest)
- ✅ Unauthorized access to backup storage
- ✅ Archive tampering (authenticated encryption)
- ✅ Network interception (if using rclone with encryption)

**Not protected against**:
- ❌ Compromise of the server during backup (plaintext in memory)
- ❌ Private key theft from offline storage
- ❌ Weak passphrase brute-force
- ❌ Advanced persistent threats on backup server

**Mitigation strategies**:
- Run backups on isolated systems
- Use hardware security modules (HSM) for production
- Implement key splitting (Shamir's Secret Sharing)
- Regular security audits

### Compliance Considerations

AGE encryption meets requirements for:

- **GDPR**: Personal data encrypted at rest and in transit
- **HIPAA**: PHI encrypted with industry-standard algorithms
- **PCI DSS**: Cardholder data encrypted per Requirement 3.4
- **SOC 2**: Encryption controls for confidentiality

**Audit trail**: Use `DEBUG_LEVEL=advanced` (or `DEBUG_LEVEL=extreme`) and/or run with `--log-level debug` to log encryption-related operations (never keys/passphrases).

---

## Related Documentation

### Configuration
- **[Configuration Guide](CONFIGURATION.md)** - Complete variable reference including all AGE settings
- **[Cloud Storage Guide](CLOUD_STORAGE.md)** - rclone integration with encrypted cloud backups

### Restore Operations
- **[Restore Guide](RESTORE_GUIDE.md)** - Complete restore workflows (all modes)
- **[Restore Technical](RESTORE_TECHNICAL.md)** - Technical implementation details
- **[Cluster Recovery](CLUSTER_RECOVERY.md)** - Disaster recovery procedures

### Reference
- **[CLI Reference](CLI_REFERENCE.md)** - All command flags including `--decrypt`, `--newkey`
- **[Troubleshooting](TROUBLESHOOTING.md)** - Common encryption/decryption issues
- **[Examples](EXAMPLES.md)** - Real-world encrypted backup scenarios

### Main Documentation
- **[README](../README.md)** - Project overview and quick start

---

## Quick Reference

### Environment Variables

```bash
# Enable encryption
ENCRYPT_ARCHIVE=true                       # Master switch

# Recipient configuration
AGE_RECIPIENT_FILE=${BASE_DIR}/identity/age/recipient.txt   # Public recipients (recommended)

# Optional: inline recipients (merged with file; supports comma/semicolon/pipe/newline)
AGE_RECIPIENT=age1abc123...,age1def456...
```

### Common Commands

```bash
# Generate new keys
./build/proxsave --newkey

# Run encrypted backup
./build/proxsave

# Decrypt backup (interactive)
./build/proxsave --decrypt

# Restore from encrypted backup
./build/proxsave --restore

# Manual decryption (scriptable) with age CLI
age --decrypt -i /path/to/age-keys.txt host-backup-YYYYMMDD-HHMMSS.tar.xz.age > host-backup-YYYYMMDD-HHMMSS.tar.xz
```

### File Locations

```
configs/
└── backup.env                      # Environment variables

identity/
└── age/
    ├── recipient.txt               # Public recipients (0600)
    └── recipient.txt.bak-*         # Optional backups (if you made one)

backup/
└── <HOST>-backup-*.tar.<ext>[.age][.bundle.tar]
```

### Key Formats

**Public key (X25519)**:
```
age1abc123def456ghi789jkl012mno345pqr678stu901vwx234yz567abc
```

**Private key (X25519)**:
```
AGE-SECRET-KEY-1ABC123DEF456GHI789JKL012MNO345PQR678STU901VWX234YZ567ABC
```

---

**For complete AGE specification**, see: https://age-encryption.org/v1
