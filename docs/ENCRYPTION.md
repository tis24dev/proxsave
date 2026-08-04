# Encryption Guide

Complete guide to AGE encryption for Proxsave.

## Table of Contents

- [Overview](#overview)
- [Features](#features)
- [Plaintext staging](#plaintext-staging)
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
- **Streaming encryption**: the archive is encrypted as it is written, so no plaintext archive is ever created on disk. The files gathered for the backup are staged in the clear under `/tmp/proxsave` first: see [Plaintext staging](#plaintext-staging)
- **Multiple recipients**: Support for both passphrase and key-based encryption
- **Memory safety**: Sensitive data zeroed immediately after use
- **Standard format**: Compatible with standard AGE tools

---

## Features

| Feature | Description |
|---------|-------------|
| **Encryption algorithm** | ChaCha20-Poly1305 (AEAD) with X25519 key exchange |
| **Key types** | Passphrase or X25519 key pair. SSH public keys (`ssh-ed25519` / `ssh-rsa`) are accepted as recipients but ProxSave cannot decrypt with the matching SSH private key: see the warning below |
| **Multiple recipients** | Single backup can be decrypted with any configured recipient |
| **Interactive setup** | `--newkey` (or the first encrypted run) helps you configure recipients |
| **Streaming mode** | Encrypts during backup creation, so there is no temporary plaintext **archive**. The staging tree under `/tmp/proxsave` is plaintext |
| **Security** | Passphrases read with `term.ReadPassword`, buffers zeroed after use |
| **File permissions** | Recipient files are created 0700/0600; the security check verifies them and auto-fixes only when `AUTO_FIX_PERMISSIONS` is enabled (otherwise it warns) |

---

## Plaintext staging

Encryption applies to the **archive**, not to the collection step. Each run creates a
staging directory `/tmp/proxsave/proxsave-<hostname>-<timestamp>-<random>` and copies every
collected file into it in the clear, including `/etc/shadow`, `/etc/gshadow`,
`/etc/ssl/private` and `/etc/pve/priv`. The tar is then read back from that directory and
streamed through the compressor into the age writer, so the archive is never written in
plaintext, but its input is. There is no tar file anywhere: the tar is streamed through a
pipe, not held in memory and not landed on disk.

The **per-run** directory is created `0700` and owned by `root`, and that is what keeps other
local users out of the staged files, since staged files keep the owner and mode of their
originals. The shared root `/tmp/proxsave` is a different matter: several paths create it
`0755`, and the guard only refuses a symlink, a non-directory, a **group or world writable**
root, or one owned by another user. A world-readable `0755` root is accepted and never
tightened, so anything ProxSave writes directly into the root, rather than inside a `0700`
per-run directory, is readable by every local user. Run `chmod 700 /tmp/proxsave` if that
matters on your host. The location is not configurable: it is compiled in, and `TMPDIR` does
not move it.

The directory exists from the start of collection until the run finishes, which includes
archiving, verification, bundling and any upload to secondary or cloud storage. It is
deleted when the run returns, whether it succeeded or failed, and also on the **first**
Ctrl-C or `SIGTERM`. It is **not** deleted if the process is killed with `SIGKILL`, if the
machine loses power, or if you press **Ctrl-C a second time**: ProxSave un-registers its
signal handler after the first signal, so a second one terminates the process outright and
no cleanup runs. Give the first Ctrl-C time to unwind.

The next *backup* run sweeps leftovers (a restore or a status check does not), driven by a
registry at `/var/run/proxsave/temp-dirs.json`, overridden by `PROXMOX_TEMP_REGISTRY_PATH`
when set and falling back under `TMPDIR` when that directory cannot be created. An entry is
swept when its PID is gone, or unconditionally once the record is 24 hours old. On a stock
host `/var/run` is tmpfs, so a crash followed by a reboot loses the record and the leftover
is never swept. Check by hand after an unclean shutdown, and note that the sweep only ever
covers backup staging:

Every path below belongs to a **live** run until that run ends, so check first and delete by
name. A glob run against a working host takes the staging directory out from under a backup,
a decrypt or a restore in progress — and the safety tarballs are not leftovers at all, they
are the restore's rollback.

```bash
# 1. Is anything running? If this prints a PID, stop here.
pgrep -a -x proxsave

# 2. Look at what is actually there, with dates.
ls -la /tmp/proxsave/

# 3. Delete the specific entries you judged stale, substituting the real names from
#    step 2. The patterns identify what each one is; they are not meant to be run as
#    globs on a host that is still working.
#      proxsave-*           backup staging, from a killed backup
#      proxmox-decrypt-*    decrypt staging: a FULLY DECRYPTED archive
#      restore-stage-*      restore staging: plaintext shadow and pve priv
#      *_backup_*.tar.gz    the restore's rollback -- only once that restore has settled
rm -rf /tmp/proxsave/restore-stage-20260803-120000_1
```

Two practical consequences. `/tmp` needs room for a full uncompressed copy of everything
being backed up, on top of the archive itself. And if `/tmp` is a tmpfs the staged plaintext
is in RAM and can reach swap; if it is on disk, it is written to persistent storage.

The same applies in reverse, and the restore side is worse. `proxsave --decrypt` stages under
`/tmp/proxsave/proxmox-decrypt-*` and removes it at the end of a normal run. `proxsave
--restore` extracts the sensitive categories in the clear into
`/tmp/proxsave/restore-stage-<timestamp>_<seq>/`, which holds material such as `/etc/shadow`,
`/etc/gshadow` and `/etc/pve/priv/*.cfg`, and **never deletes it**, on success or failure.
Nothing sweeps it either, since it is not registered. A restore also leaves its rollback and
safety tarballs (`restore_backup_`, `network_rollback_backup_`, `firewall_rollback_backup_`,
`ha_rollback_backup_`, `pve_access_control_rollback_backup_`, each `_<timestamp>.tar.gz`)
deliberately in place — they are the rollback. Those are written **mode 0600**, so their
contents are not readable by other local users even though `/tmp/proxsave` itself is `0755`
and shared; their names and sizes still are. Clean them up yourself once a restore has
settled.

If you decrypt by hand with the `age` CLI, your own output is plaintext too: pipe it rather
than land it on a shared filesystem.

---

## Quick Start

### 1. Generate Recipients

**Option A: Interactive wizard** (recommended for beginners):

```bash
proxsave --newkey
```

**Option B: Manual generation** with standard AGE tools:

```bash
# Generate key pair (keep the private key offline if possible)
age-keygen -o age-keys.txt

# Extract the public recipient (starts with "age1...")
grep "# public key:" age-keys.txt | cut -d: -f2 | tr -d ' '

# Then paste the recipient into:
#   proxsave --newkey
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
proxsave
# Archive will be encrypted (archive ends with .age; if bundling is enabled, output ends with .age.bundle.tar)
```

### 4. Decrypt When Needed

```bash
# Interactive decryption
proxsave --decrypt
```

---

## Configure Recipients

Recipients are public keys or passphrases that can decrypt backups. A backup encrypted for **N recipients** can be decrypted by **any of the N private keys/passphrases**.

### Static Configuration

**File**: set by `AGE_RECIPIENT_FILE`. The shipped template sets it to `${BASE_DIR}/identity/age/recipient.txt`; the compiled-in default is empty, and ProxSave then resolves it to that same path. Check the key before assuming the default path: `--newkey` rewrites whatever `AGE_RECIPIENT_FILE` points at, not necessarily the default.

```plaintext
# AGE recipients (one per line)

# X25519 public recipients (recommended)
age1abc123def456ghi789jkl012mno345pqr678stu901vwx234yz567abc

# Recipient derived from a passphrase (still an "age1..." recipient; the passphrase is NOT stored)
age1def456ghi789jkl012mno345pqr678stu901vwx234yz567abc123def

# SSH public key recipient (encrypt to an existing SSH key; ssh-ed25519 or ssh-rsa)
ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleSSHpublicKeyForAgeRecipient
```

**Format**:
- One recipient per line
- Blank lines and `#` comments ignored
- Supported types: X25519 (`age1...`) and SSH public keys (`ssh-ed25519` / `ssh-rsa`); mix freely

> **One comment in this file is not decoration.** ProxSave writes its own line at the top:
>
> ```plaintext
> # passphrase-salt: proxsave/age-passphrase/v2:1a2b3c...
> age1abc123def456ghi789jkl012mno345pqr678stu901vwx234yz567abc
> ```
>
> The recipient parser ignores it, but ProxSave reads it back. It is a **copy** of the
> per-installation salt that gets stamped into every archive manifest.
> `identity/age/passphrase.salt` is the authoritative one: it is the only copy the setup
> wizard reads when it derives a recipient from a passphrase, and every backup rewrites
> this comment from it. The comment exists so that losing the sibling does not lose the
> salt for future manifests.
>
> Edit this file **in place** and leave the salt line alone. Do not retype the file from
> scratch and do not filter out comments. Losing both copies does not lock you out of
> existing backups, since the salt travels in each archive's manifest, but every backup
> taken afterwards is written without a salt and can never be opened with the passphrase.
> And if `passphrase.salt` is gone, re-running the wizard with the same passphrase mints a
> **new random salt** and derives a **different** recipient, without a warning: the comment
> is not consulted there.

SSH recipients carry their own asymmetry, in the opposite direction:

> **SSH keys encrypt, but ProxSave cannot decrypt with them.** `proxsave --decrypt` and `proxsave --restore` accept only an `AGE-SECRET-KEY-...` identity or a passphrase. Paste an SSH private key at the prompt and it is hashed as a passphrase, which derives the wrong identity and loops on "Provided key or passphrase does not match this archive."
>
> If you configure **only** SSH recipients, ProxSave cannot open its own archives. Always keep at least one `age1...` recipient or a passphrase alongside them.
>
> An archive encrypted to an SSH key can still be opened with the upstream `age` CLI, pointing `-i` at the SSH private key:
>
> ```bash
> # With bundling left at its default the raw .age is not on disk: untar the bundle first.
> # install -d -m 700, not mkdir -p: the decrypted archive lands in this directory, and
> # /tmp is shared, so a 0755 workspace hands the node's configuration to every local
> # account. Keep the plaintext inside it and delete it when you are done.
> install -d -m 700 /tmp/emergency
> tar -xf <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age.bundle.tar -C /tmp/emergency
> age -d -i ~/.ssh/id_ed25519 -o /tmp/emergency/backup.tar.xz /tmp/emergency/<HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age
> rm -rf /tmp/emergency   # once you have what you needed
> ```

### Interactive Wizard

You can create/update recipients in two ways:

```bash
# Dedicated wizard (TUI by default)
proxsave --newkey

# Use CLI prompts instead of TUI (useful for debugging or when TUI rendering is unavailable)
proxsave --newkey --cli
```

If `ENCRYPT_ARCHIVE=true` and no recipients are configured, proxsave will start an interactive setup automatically during the backup (only when running in a real terminal).

**Setup options** (TUI/CLI):
- Paste an existing AGE public recipient (`age1...`)
- Enter a passphrase to derive a deterministic AGE key (passphrase is **not stored**)
- Paste an AGE private key (`AGE-SECRET-KEY-...`) to derive its public recipient (key is **not stored**)

**Passphrase strength.** When you derive a recipient from a passphrase, ProxSave
enforces a minimum strength or rejects it with an error: at least **12 characters**, and
characters from at least **3 of 4 classes** (lowercase, uppercase, digit, symbol). A
short list of well-known weak passphrases (for example `password`, `123456`, `qwerty`)
is also rejected outright.

**Notes**:
- Proxsave stores **no private key and no passphrase**. Besides the recipients it does store the passphrase salt, a deliberately public value, in `identity/age/passphrase.salt` and as the `# passphrase-salt:` line inside the recipient file. Keep private keys and passphrases offline.
- `AGE_RECIPIENT` (inline) and `AGE_RECIPIENT_FILE` are **merged and de-duplicated**. `AGE_RECIPIENTS` (plural) is accepted as a fallback alias for `AGE_RECIPIENT`, used only when `AGE_RECIPIENT` is empty.
- Both TUI and CLI setup flows support multiple recipients and de-duplicate repeated entries before saving.

---

## Running Encrypted Backups

### Prerequisites

1. `ENCRYPT_ARCHIVE=true` in `configs/backup.env`
2. At least one recipient configured via `AGE_RECIPIENT` and/or `AGE_RECIPIENT_FILE`
3. File permissions and ownership checked automatically (0700 directory, 0600 recipient file, owned root:root; auto-fixed when `AUTO_FIX_PERMISSIONS=true`, otherwise a warning)

### Backup Execution

```bash
proxsave
```

**Encryption flow**:

```text
┌─────────────────────────────────────────────┐
│  Phase 1: Backup Collection                 │
│  - Gather PVE/PBS/System files               │
│  - Stage them IN THE CLEAR under             │
│    /tmp/proxsave/proxsave-<host>-<ts>-<rnd>  │
│    (mode 0700, root only)                    │
└─────────────┬───────────────────────────────┘
              │
              ▼
┌─────────────────────────────────────────────┐
│  Phase 2: Streaming Encryption              │
│  - Stream the staging tree as a tar through  │
│    the compressor into AGE (ChaCha20-Poly1305)│
│  - Write to <HOST>-backup-YYYYMMDD-HHMMSS.tar.<ext>.age │
│  - NO plaintext ARCHIVE on disk              │
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

```text
backup/
└── pve-node1-backup-20240115-023000.tar.xz.age.bundle.tar   # Typical (bundling enabled)
```

**If bundling is disabled** (`BUNDLE_ASSOCIATED_FILES=false`), proxsave keeps the raw artifacts:

```text
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
  "sha256": "...",
  "created_at": "2024-01-15T02:30:00Z",
  "compression_type": "xz",
  "hostname": "pve-node1",
  "encryption_mode": "age",
  "passphrase_salt": "proxsave/age-passphrase/v2:1a2b3c..."
}
```

`passphrase_salt` is omitted entirely for X25519 or SSH-only setups, and for legacy
fixed-salt archives.

---

## Decrypting Backups

The `--decrypt` workflow converts an encrypted backup into a decrypted bundle for inspection or transfer.

```bash
proxsave --decrypt
```

**High-level flow**:
1. Select backup source (primary/secondary/cloud)
2. Select an encrypted backup
3. Select destination folder (default: `./decrypt` or `${BASE_DIR}/decrypt`)
4. When prompted, enter:
   - an AGE private key (`AGE-SECRET-KEY-...`), or
   - the passphrase you used (proxsave derives the matching identity; the passphrase is not stored)

**Output**:
- A decrypted bundle saved as `*.decrypted.bundle.tar`. That is itself a plain tar holding
  the decrypted archive plus its `.metadata` and `.sha256`, so `tar -xf` it to get the
  archive out.

If you need fully scripted/non-interactive decryption with a **private key**, use the
official `age` CLI. With bundling left at its default the raw `.age` is not on disk, so
unwrap the bundle first (see [Emergency Decryption Without
Configuration](#emergency-decryption-without-configuration)):

```bash
# 0700 workspace: the decrypted archive below is plaintext and /tmp is shared.
install -d -m 700 /tmp/emergency
tar -xf <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age.bundle.tar -C /tmp/emergency
age --decrypt -i /path/to/age-keys.txt \
  /tmp/emergency/<HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age \
  > /tmp/emergency/<HOST>-backup-YYYYMMDD-HHMMSS.tar.xz
rm -rf /tmp/emergency   # once you have what you needed
```

> **Passphrase recipients are not native age passphrases.** A passphrase recipient
> is an X25519 key *derived* from the passphrase, so the raw `age --decrypt` (which
> only understands age's own scrypt passphrase stanza) cannot decrypt it from the
> passphrase alone, use `proxsave --decrypt`. proxsave re-derives the identity from
> the passphrase plus the **per-installation random salt** generated at setup, which
> is stored next to the recipient (`identity/age/passphrase.salt`, mirrored as the
> `# passphrase-salt:` line in the recipient file) and embedded in every backup manifest
> (`passphrase_salt`) so recovery works on any host. The emergency `age` CLI path above
> therefore needs an `AGE-SECRET-KEY-...` identity file; a passphrase-only holder must use
> `proxsave --decrypt`, which reads the bundle directly.
>
> **Decryption never reads `recipient.txt` or `passphrase.salt`.** The salt travels inside
> each archive's manifest, so an existing backup stays openable with its passphrase even if
> both local copies are gone. See [Emergency
> Scenarios](#emergency-scenarios) for how to rebuild them from an archive.

---

## Restoring Encrypted Backups

Encrypted backups can be restored using the standard `--restore` command. The decryption is handled automatically during the restore workflow.

### Quick Restore Summary

```bash
# Interactive restore (decrypts automatically)
proxsave --restore
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
- **Multiple recipients**: Any X25519 recipient that matches the archive can decrypt it. An SSH recipient cannot be used here; see the warning in the recipients section

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

2. Extract the new public recipient and append it to the file named by `AGE_RECIPIENT_FILE`
   (that is `${BASE_DIR}/identity/age/recipient.txt` only when the key is empty). ProxSave
   reads exactly one recipient file, so appending to the default path while the key points
   elsewhere is a silent no-op and the new key never enters the rotation:
   ```bash
   grep "# public key:" age-keys-2025.txt | cut -d: -f2 | tr -d ' ' >> /opt/proxsave/identity/age/recipient.txt
   ```

3. Run backups for a while: new backups can be decrypted with **either** the old or the new private key.

4. After retention deletes older backups, remove the old recipient line from that same file.

5. If the old recipient was **also** set inline, remove it from `AGE_RECIPIENT` (or `AGE_RECIPIENTS`) and from the environment. Otherwise deleting the file line changes nothing: the inline value is merged back on the next backup.

**Important**:
- Keep old private keys until you are sure all old backups are expired (or safely archived).
- Proxsave stores no private key and no passphrase. Besides the recipients it stores the passphrase salt (`identity/age/passphrase.salt` and the `# passphrase-salt:` line in the recipient file). Private keys and passphrases remain your responsibility.
- Before every backup ProxSave rebuilds the recipient list from scratch: inline values first, then the file's lines, then exact duplicates are dropped keeping the first occurrence. Nothing is remembered between runs, so any recipient still present in configuration comes back.

### Full Replacement (Reset Recipients)

`proxsave --newkey` rewrites the **recipient file only**. It never edits
`configs/backup.env`. A recipient configured anywhere else stays active and is merged back
into the next backup, so on its own this is not a way to drop a compromised key.

To really drop a compromised recipient, clear all of these:

1. **`AGE_RECIPIENT` in `configs/backup.env`.** This key accumulates: every `AGE_RECIPIENT=`
   line in the file is kept, and each line may hold several recipients separated by comma,
   semicolon, pipe or newline. Remove or empty all of them.
2. **`AGE_RECIPIENTS`** (plural) in the same file. It is read only when `AGE_RECIPIENT`
   yields nothing, so emptying `AGE_RECIPIENT` silently hands control to it.
3. **The `AGE_RECIPIENT` and `AGE_RECIPIENT_FILE` environment variables**, if a systemd
   unit, cron wrapper or shell profile sets them. Environment values override the file.
4. **The recipient file itself**, which `--newkey` rewrites. That is the path in
   `AGE_RECIPIENT_FILE`, which is only `${BASE_DIR}/identity/age/recipient.txt` when the key
   is empty.

Then run:

```bash
proxsave --newkey
```

If the target recipient file already exists, `--newkey` asks for confirmation and copies the
old file to `<path>.bak-<timestamp>` itself before overwriting. If it does not exist, for
example when your only recipient was inline, it writes the new file with no prompt and no
warning.

The shipped template leaves `AGE_RECIPIENT` empty and nothing in the installer or the wizard
ever writes a value into it, so if you never hand-edited `backup.env` and set no environment
variable, `--newkey` does replace the effective recipient set.

---

## Emergency Scenarios

| Scenario | Solution |
|----------|----------|
| **Lost passphrase/private key** | **No recovery possible**. Keep 2+ offline copies (password manager, printed paper). |
| **Migrating to new server** | Copy the whole `identity/age/` directory byte for byte, **`passphrase.salt` included**, plus your `configs/backup.env`. `passphrase.salt` is the file that matters: the setup wizard reads only that one, and if it is missing it mints a new random salt and derives a **different** recipient without warning. The `# passphrase-salt:` copy inside `recipient.txt` only feeds the manifest, so do not rely on it alone and do not retype the recipient file. Alternatively run `proxsave --newkey` on the new host and accept a new recipient. Keep private keys offline. |
| **Verifying integrity** | Periodically decrypt a backup (or run a restore in a test VM) to ensure keys and archives are valid. |
| **Automation** | Headless runs require recipients pre-configured (`AGE_RECIPIENT` and/or `AGE_RECIPIENT_FILE`). |
| **Recipient file overwritten** | Restore from `recipient.txt.bak-*`. ProxSave writes that copy itself whenever `--newkey` overwrites an existing recipient file. |
| **Passphrase salt lost** | Recover it from the manifest of an archive your current recipient still opens: see [Rebuilding a lost salt](#rebuilding-a-lost-salt). Existing backups are unaffected. Write back only a value that matches the recipient you are still using. |

### Emergency Decryption Without Configuration

If you have the private key but lost all configuration, start from what is actually on disk.
With bundling left at its default the **only** file on the backup path is
`<HOST>-backup-YYYYMMDD-HHMMSS.tar.<ext>.age.bundle.tar`. The raw `.age` archive, its
`.sha256`, its `.metadata` and its `.manifest.json` are deleted once the bundle is written,
and the bundle is also the only file copied to secondary and cloud destinations. Unwrap it
before anything else.

```bash
# 1. Read the member names from the bundle rather than typing them
tar -tf <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age.bundle.tar
# <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age.metadata
# <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age.sha256
# <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age

# 2. Unwrap. The bundle is an uncompressed tar with basename-only entries,
#    so extract it into a directory of your own. 0700, not the 0755 mkdir -p would
#    give it: everything from step 4 on is plaintext and /tmp is shared.
install -d -m 700 /tmp/emergency
tar -xf <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age.bundle.tar -C /tmp/emergency
cd /tmp/emergency

# 3. Optional: verify before spending time on it. The .sha256 names the .age file,
#    so run this from the extraction directory.
sha256sum -c <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age.sha256

# 4. Decrypt with the stock age CLI
age --decrypt -i /path/to/age-keys.txt \
  <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age > <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz

# 5. Extract the inner archive. This is the node's configuration in the clear --
#    /etc/shadow, /etc/pve/priv and the rest -- so the destination is 0700 too.
install -d -m 700 /tmp/emergency-restore
tar -xf <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz -C /tmp/emergency-restore

# 6. When you are done, remove both workspaces: nothing else will.
rm -rf /tmp/emergency /tmp/emergency-restore
```

Notes on this recipe:

- The `.age` file inside the bundle keeps exactly the name it had on disk, so step 4 is the
  ordinary `age` command, just after the untar.
- **Do not assume the extension is `.tar.xz`.** It follows the compression actually used, and
  ProxSave falls back to gzip when the configured compressor is not installed. Take the name
  from `tar -tf`.
- If `BUNDLE_ASSOCIATED_FILES=false` was set there is no bundle: the `.age` file is already
  on disk next to its sidecars, so skip steps 1 to 3.
- The `.metadata` member is a byte copy of the manifest JSON. Only `.metadata` is bundled, so
  do not look for a `.manifest.json` inside the bundle. Read it with
  `cat <name>.age.metadata` to recover `compression_type`, `sha256`, `encryption_mode` and
  `passphrase_salt` without touching the archive.
- This path needs an `AGE-SECRET-KEY-...` identity. A passphrase cannot be fed to the `age`
  CLI (see the note under [Decrypting Backups](#decrypting-backups)); use
  `proxsave --decrypt`, which reads the bundle directly. It only lists backups found under
  the configured primary, secondary or cloud path, so a bundle carried in on removable media
  has to be placed on one of those paths first.

### Rebuilding a lost salt

Decryption never reads `recipient.txt` or `passphrase.salt`, so an existing archive stays
openable even when both are gone. Use one to rebuild them.

```bash
# Raw layout
grep passphrase_salt <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age.manifest.json

# Bundled layout, where the manifest travels as the .metadata member
tar -xOf <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age.bundle.tar \
    <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age.metadata | grep passphrase_salt

# Write the value back, prefix included, and future backups record it again.
# BASE_DIR is not a shell variable: substitute your install root.
printf '%s\n' 'proxsave/age-passphrase/v2:1a2b3c...' > /opt/proxsave/identity/age/passphrase.salt
chmod 600 /opt/proxsave/identity/age/passphrase.salt
```

Write back only a salt that matches the recipient you are still using. The next backup copies
this file over the `# passphrase-salt:` line in `recipient.txt` and stamps it into every new
manifest, so a value from an older salt generation would make future archives unopenable with
the passphrase you have.

An archive whose manifest has no `passphrase_salt` at all was written either by a version
that used a fixed salt, which ProxSave still tries, or by an install that had already lost
the salt. For the second case there is no recovery.

### Testing Backup Recoverability

Periodically verify backups are decryptable. Land the plaintext first: `tar` auto-detects
compression only when it is given a **file name**, so piping a compressed stream into
`tar -t` fails with `Archive is compressed. Use -J option` no matter which compressor was
used.

```bash
# 0700: archive.inner below is the whole configuration in the clear, and although
# this recipe deletes it, /tmp is shared for as long as the check runs.
install -d -m 700 /tmp/emergency
tar -xOf <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age.bundle.tar \
    <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age \
  | age --decrypt -i /path/to/age-keys.txt > /tmp/emergency/archive.inner
tar -tf /tmp/emergency/archive.inner >/dev/null && echo "Archive valid"
rm -f /tmp/emergency/archive.inner
```

**Recommended schedule**: Monthly automated test + manual review.

---

## Security Notes

### Encryption Implementation

- **Algorithm**: ChaCha20-Poly1305 (AEAD) with X25519 ECDH
- **Key derivation**: scrypt (N=2^15, r=8, p=1) for passphrases. The current scheme uses a **per-installation random salt** (v2), generated once, stored `0600` at `identity/age/passphrase.salt`, mirrored as the `# passphrase-salt:` line inside the recipient file (every backup rewrites that comment from the sibling before reading it back, so the sibling wins whenever the two differ; once the sibling is gone the comment still supplies the salt stamped into new manifests, but it can **not** re-derive the recipient — the setup wizard reads the sibling alone and mints a fresh random salt when it is missing, yielding a different recipient), and embedded in each manifest as `passphrase_salt` so the passphrase alone can re-derive the recipient on any host. At decrypt ProxSave tries salts in order: the manifest's per-install salt first, then two fixed legacy namespaces (`proxsave/age-passphrase/v1`, then the pre-rebrand `proxmox-backup-go/age-passphrase/v1`), so archives from older versions and from before the rename stay decryptable.
- **Random nonces**: Unique per encryption operation
- **Authentication**: Poly1305 MAC prevents tampering

### Security Best Practices

| Practice | Implementation |
|----------|----------------|
| **Passphrase handling** | Read with `term.ReadPassword` (no echo) |
| **Memory security** | Buffers zeroed immediately after use |
| **Streaming encryption** | No plaintext **archive** on disk. The backup staging tree is removed when the run ends, but not on `SIGKILL`, power loss or a double Ctrl-C, and a restore leaves its own staging tree and safety tarballs behind: see [Plaintext staging](#plaintext-staging) |
| **File permissions & ownership** | Enforced 0700/0600 and root:root on recipient/identity files (auto-fixed with `AUTO_FIX_PERMISSIONS`, otherwise warned) |
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
- ❌ Compromise of the server during a backup. While a backup runs, the collected files sit unencrypted under `/tmp/proxsave` (see [Plaintext staging](#plaintext-staging)); a root-level compromise in that window sees everything in the clear
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
# The shipped template sets this; the compiled-in default is empty and resolves to the same path.
# --newkey rewrites whatever this points at, so check it before assuming the default.
AGE_RECIPIENT_FILE=${BASE_DIR}/identity/age/recipient.txt   # Public recipients (recommended)

# Optional: inline recipients (merged with file; supports comma/semicolon/pipe/newline)
# AGE_RECIPIENTS (plural) is accepted as a fallback alias, used only when AGE_RECIPIENT is empty
AGE_RECIPIENT=age1abc123...,age1def456...
```

### Common Commands

```bash
# Generate new keys
proxsave --newkey

# Run encrypted backup
proxsave

# Decrypt backup (interactive)
proxsave --decrypt

# Restore from encrypted backup
proxsave --restore

# Manual decryption (scriptable) with age CLI, straight out of the bundle
tar -xOf <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age.bundle.tar \
    <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz.age \
  | age --decrypt -i /path/to/age-keys.txt > <HOST>-backup-YYYYMMDD-HHMMSS.tar.xz
```

### File paths in these examples

`BASE_DIR` is auto-detected from the installed executable and is **not** a shell variable:
substitute your install root (typically `/opt/proxsave`) when pasting any path that uses it.

### File Locations

```text
configs/
└── backup.env                      # Environment variables

identity/
└── age/
    ├── recipient.txt               # Public recipients, plus the "# passphrase-salt:" line (0600)
    ├── passphrase.salt             # Per-installation passphrase salt (0600, passphrase setups only)
    └── recipient.txt.bak-*         # Written by --newkey when it overwrites an existing file

backup/
└── <HOST>-backup-*.tar.<ext>[.age][.bundle.tar]
```

### Key Formats

**Public key (X25519)**:
```text
age1abc123def456ghi789jkl012mno345pqr678stu901vwx234yz567abc
```

**Private key (X25519)**:
```text
AGE-SECRET-KEY-1ABC123DEF456GHI789JKL012MNO345PQR678STU901VWX234YZ567ABC
```

---

**For complete AGE specification**, see: https://age-encryption.org/v1
