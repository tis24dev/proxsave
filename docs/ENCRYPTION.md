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

Proxsave uses **[age](https://age-encryption.org/)** (actually, rage) for encryption. AGE is a modern, simple, and secure file encryption format designed to replace GPG for basic use cases.

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
| **Interactive wizard** | `--newkey` generates keys with guided setup |
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
# Generate key pair
age-keygen -o configs/age-keys.txt

# Extract public key
grep "# public key:" configs/age-keys.txt | cut -d: -f2 | tr -d ' ' > configs/recipient.txt
```

### 2. Configure Environment

Add to `configs/backup.env`:

```bash
# Enable encryption
AGE_ENABLED=true

# Recipient file (public keys only)
AGE_RECIPIENT_FILE=configs/recipient.txt

# Private keys location (for decryption/restore)
# Keep this file OFFLINE and SECURE
AGE_IDENTITY_FILE=configs/age-keys.txt  # Optional, only needed for decrypt/restore
```

### 3. Run Encrypted Backup

```bash
./build/proxsave
# Backup will be encrypted with .age extension
```

### 4. Decrypt When Needed

```bash
# Interactive decryption
./build/proxsave --decrypt

# Automatic decryption (requires AGE_IDENTITY_FILE set)
./build/proxsave --decrypt --auto
```

---

## Configure Recipients

Recipients are public keys or passphrases that can decrypt backups. A backup encrypted for **N recipients** can be decrypted by **any of the N private keys/passphrases**.

### Static Configuration

**File**: `configs/recipient.txt`

```plaintext
# AGE recipients (one per line)

# X25519 public keys (generated via age-keygen)
age1abc123def456ghi789jkl012mno345pqr678stu901vwx234yz567abc

# Passphrases (scrypt-based)
age1scrypt1qyqgzjxy...
```

**Format**:
- One recipient per line
- Blank lines and `#` comments ignored
- Mix key types freely

### Interactive Wizard

The `--newkey` wizard guides you through key generation:

```bash
./build/proxsave --newkey
```

**Wizard flow**:

```
┌──────────────────────────────────────────────┐
│  AGE Key Generation Wizard                   │
├──────────────────────────────────────────────┤
│                                              │
│  Choose recipient type:                     │
│                                              │
│  1. X25519 key pair (recommended)           │
│     - Most secure                           │
│     - Requires private key file             │
│                                              │
│  2. Passphrase-based                        │
│     - Easier for manual recovery            │
│     - Slightly weaker security              │
│                                              │
│  3. Add multiple recipients                 │
│                                              │
│  Your choice: _                             │
└──────────────────────────────────────────────┘
```

**Option 1: X25519 Key Pair**

```
Enter path for private key: configs/age-keys.txt

✓ Generated key pair:
  Private key: configs/age-keys.txt (KEEP OFFLINE!)
  Public key:  age1abc123def456...

✓ Added to recipient.txt
```

**Private key format** (`age-keys.txt`):

```plaintext
# created: 2024-01-15T10:30:00Z
# public key: age1abc123def456ghi789jkl012mno345pqr678stu901vwx234yz567abc
AGE-SECRET-KEY-1ABC123DEF456GHI789JKL012MNO345PQR678STU901VWX234YZ567ABC
```

**⚠️ CRITICAL**: Keep private keys offline (password manager, hardware token, printed backup).

**Option 2: Passphrase-based**

```
Enter passphrase: ****************
Confirm passphrase: ****************

✓ Generated scrypt recipient:
  age1scrypt1qyqgzjxy...

✓ Added to recipient.txt
```

**Security notes**:
- Passphrase strength: 20+ random characters recommended
- Scrypt parameters: N=2^18, r=8, p=1 (16s on modern CPU)
- No recovery if passphrase lost

**Option 3: Multiple Recipients**

Allows adding several recipients in one session (useful for team access or backup strategies):

```
How many recipients? 3

Recipient 1/3:
  Type (1=key, 2=passphrase): 1
  Private key path: configs/age-keys-admin.txt
  ✓ age1abc123...

Recipient 2/3:
  Type (1=key, 2=passphrase): 1
  Private key path: configs/age-keys-backup.txt
  ✓ age1def456...

Recipient 3/3:
  Type (1=key, 2=passphrase): 2
  Passphrase: ****************
  ✓ age1scrypt1qyq...

✓ All 3 recipients added to recipient.txt
```

**Best practice**: Use multiple recipients for redundancy:
- **Admin key**: Primary operational key
- **Backup key**: Offline cold storage key
- **Emergency passphrase**: Last resort recovery (store in password manager)

---

## Running Encrypted Backups

### Prerequisites

1. `AGE_ENABLED=true` in `configs/backup.env`
2. Valid `recipient.txt` with at least one recipient
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
│  - Write to backup.YYYYMMDD_HHMMSS.tar.age  │
│  - NO plaintext on disk                     │
└─────────────┬───────────────────────────────┘
              │
              ▼
┌─────────────────────────────────────────────┐
│  Phase 3: Storage Distribution              │
│  - Local: backup/*.age                      │
│  - Secondary: SECONDARY_PATH/*.age          │
│  - Cloud: rclone copy *.age                 │
└─────────────────────────────────────────────┘
```

**Output file format**:

```
backup/
├── backup.20240115_023000.tar.age      # Encrypted archive
├── backup.20240115_023000.tar.age.sha256  # Checksum
└── manifest.20240115_023000.json       # Metadata (unencrypted)
```

**Manifest structure** (used during restore):

```json
{
  "timestamp": "2024-01-15T02:30:00Z",
  "hostname": "pve-node1",
  "archive": "backup.20240115_023000.tar.age",
  "encrypted": true,
  "compression": "xz",
  "size_bytes": 1234567890,
  "categories": ["pve_config", "pve_config_export", "pbs_datastores", ...]
}
```

---

## Decrypting Backups

The `--decrypt` command decrypts backups for inspection or manual recovery.

### Interactive Decryption

```bash
./build/proxsave --decrypt
```

**Workflow**:

**Step 1: Select Backup Source**

```
┌──────────────────────────────────────────────┐
│  Select Backup Source                        │
├──────────────────────────────────────────────┤
│  ○  Local (BACKUP_PATH)                      │
│  ○  Secondary (SECONDARY_PATH)               │
│  ●  Cloud (CLOUD_REMOTE)                     │
│                                              │
│  [Enter] to confirm                          │
└──────────────────────────────────────────────┘
```

**Step 2: Choose Backup**

Displays all available backups with metadata:

```
┌──────────────────────────────────────────────────────────────┐
│  Available Backups                                           │
├──────────────────────────────────────────────────────────────┤
│  1. 2024-01-15 02:30:00  (1.2 GB, XZ, Encrypted)            │
│  2. 2024-01-14 02:30:00  (1.1 GB, XZ, Encrypted)            │
│  3. 2024-01-13 02:30:00  (1.2 GB, XZ, Encrypted)            │
│                                                              │
│  Select backup: _                                           │
└──────────────────────────────────────────────────────────────┘
```

**Step 3: Provide Decryption Key**

```
┌──────────────────────────────────────────────┐
│  Decryption Method                           │
├──────────────────────────────────────────────┤
│  ○  Identity file (age-keys.txt)             │
│  ●  Passphrase                               │
│                                              │
│  Enter passphrase: ****************          │
└──────────────────────────────────────────────┘
```

**Options**:
1. **Identity file**: Path to private key (e.g., `configs/age-keys.txt`)
2. **Passphrase**: If backup encrypted with scrypt recipient

**Step 4: Decryption Output**

```
Decrypting backup.20240115_023000.tar.age...

✓ Decryption successful
  Output: /tmp/backup.20240115_023000.tar.xz
  Size: 856 MB (compressed)

Next steps:
  - Inspect:  tar -tf /tmp/backup.20240115_023000.tar.xz
  - Extract:  tar -xf /tmp/backup.20240115_023000.tar.xz -C /path/to/extract
  - Restore:  ./build/proxsave --restore
```

### Automatic Decryption

For scripted operations, use `--auto` flag:

```bash
# Requires AGE_IDENTITY_FILE set in environment
export AGE_IDENTITY_FILE=configs/age-keys.txt

# Decrypt latest backup automatically
./build/proxsave --decrypt --auto

# Decrypt specific backup
./build/proxsave --decrypt --auto --backup=backup.20240115_023000.tar.age
```

**Use cases**:
- Automated restore testing
- CI/CD backup verification
- Scheduled integrity checks

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
2. **Decrypt** → Provide identity file or passphrase
3. **Choose restore mode** → Full/Storage/Base/Custom
4. **Select categories** → PVE configs, PBS datastores, etc.
5. **Review and confirm** → Safety checks before applying
6. **Extract files** → Categories restored to system

**Decryption options during restore**:
- **Identity file**: Automatic if `AGE_IDENTITY_FILE` set
- **Passphrase**: Prompted interactively if needed
- **Multiple recipients**: Any valid recipient can decrypt

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

### Rotation Wizard

```bash
# Run key rotation wizard
./build/proxsave --newkey
```

**Process**:
1. Backs up existing `recipient.txt` → `recipient.txt.bak-YYYYMMDD-HHMMSS`
2. Launches wizard for new key
3. Updates `AGE_RECIPIENT_FILE`

**Best practice**: Keep old private keys until all old backups are purged by retention.

### Manual Rotation

**Step 1: Generate new key**

```bash
age-keygen -o configs/age-keys-2025.txt
```

**Step 2: Extract public key**

```bash
grep "# public key:" configs/age-keys-2025.txt | cut -d: -f2 | tr -d ' ' >> configs/recipient.txt
```

**Step 3: Archive old key**

```bash
mv configs/age-keys.txt configs/age-keys-2024-archived.txt
mv configs/age-keys-2025.txt configs/age-keys.txt
```

**Step 4: Update environment** (if using `AGE_IDENTITY_FILE`)

```bash
# configs/backup.env
AGE_IDENTITY_FILE=configs/age-keys.txt  # Now points to 2025 key
```

**Step 5: Run first backup with new key**

```bash
./build/proxsave
```

**Step 6: Verify decryption works**

```bash
./build/proxsave --decrypt --auto
```

**⚠️ Important**:
- **Keep old private keys** until retention deletes all backups encrypted with them
- **Document rotation date** in your runbook
- **Test decryption** with new key before archiving old key

### Multi-Recipient Strategy

For zero-downtime rotation, use multiple recipients:

**Step 1: Add new key to existing recipient list**

```bash
# Generate new key
age-keygen -o configs/age-keys-2025.txt

# Add public key to recipient.txt (append, don't replace)
grep "# public key:" configs/age-keys-2025.txt | cut -d: -f2 | tr -d ' ' >> configs/recipient.txt
```

**Step 2: Run backups with both keys**

```bash
# Backups now encrypted for BOTH 2024 and 2025 keys
./build/proxsave
```

**Step 3: After retention period, remove old key**

```bash
# Edit recipient.txt, remove old age1abc123... line
nano configs/recipient.txt
```

**Advantage**: All backups during transition period can be decrypted with either key.

---

## Emergency Scenarios

| Scenario | Solution |
|----------|----------|
| **Lost passphrase/private key** | **No recovery possible**. Keep 2+ offline copies (password manager, printed paper). |
| **Migrating to new server** | Copy `recipient.txt` (public key only). Keep private keys offline. |
| **Verifying integrity** | Run `--decrypt` periodically to ensure backups are valid. |
| **Automation** | Headless runs require `AGE_RECIPIENT` set (wizard won't run). |
| **Corrupted recipient file** | Restore from `recipient.txt.bak-*` backup (created by `--newkey`). |
| **Multiple identities** | Set `AGE_IDENTITY_FILE` to file containing multiple private keys (one per line). |

### Emergency Decryption Without Configuration

If you have the private key but lost all configuration:

```bash
# Manual decryption with age tools
age --decrypt -i /path/to/age-keys.txt backup.20240115_023000.tar.age > decrypted.tar.xz

# Extract archive
tar -xf decrypted.tar.xz -C /tmp/emergency-restore
```

### Testing Backup Recoverability

Periodically verify backups are decryptable:

```bash
# Test decryption (creates temporary file)
./build/proxsave --decrypt --auto

# Verify archive integrity
tar -tzf /tmp/backup.*.tar.xz > /dev/null && echo "✓ Archive valid"

# Cleanup
rm /tmp/backup.*.tar.xz
```

**Recommended schedule**: Monthly automated test + manual review.

---

## Security Notes

### Encryption Implementation

- **Algorithm**: ChaCha20-Poly1305 (AEAD) with X25519 ECDH
- **Key derivation**: scrypt (N=2^18, r=8, p=1) for passphrases
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

**Audit trail**: Enable `DEBUG_LEVEL=1` to log encryption operations (excludes keys/passphrases).

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
AGE_ENABLED=true                           # Master switch

# Recipient configuration
AGE_RECIPIENT_FILE=configs/recipient.txt   # Public keys (required)
AGE_IDENTITY_FILE=configs/age-keys.txt     # Private keys (optional, for decrypt/restore)

# Alternative: inline recipients (overrides file)
AGE_RECIPIENT=age1abc123...,age1def456...  # Comma-separated
```

### Common Commands

```bash
# Generate new keys
./build/proxsave --newkey

# Run encrypted backup
./build/proxsave

# Decrypt backup (interactive)
./build/proxsave --decrypt

# Decrypt backup (automatic)
./build/proxsave --decrypt --auto

# Restore from encrypted backup
./build/proxsave --restore

# Verify encryption status
grep "Encrypted: true" backup/manifest.*.json
```

### File Locations

```
configs/
├── recipient.txt              # Public keys (commit to git)
├── recipient.txt.bak-*        # Automatic backups
├── age-keys.txt               # Private keys (NEVER commit!)
└── backup.env                 # Environment variables

backup/
├── backup.*.tar.age           # Encrypted archives
├── backup.*.tar.age.sha256    # Checksums
└── manifest.*.json            # Metadata (unencrypted)
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

**Passphrase recipient (scrypt)**:
```
age1scrypt1qyqgzjxy...
```

---

**For complete AGE specification**, see: https://age-encryption.org/v1
