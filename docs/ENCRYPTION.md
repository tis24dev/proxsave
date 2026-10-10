# Encryption Guide

<!-- site-region: encrypt-backups:start -->

## Encrypt backups and preserve recovery keys

AGE encryption protects the archives delivered to primary, secondary and rclone
storage. Any private identity matching one of the configured recipients can decrypt
the archive. Keep that identity, or the original passphrase, independently of the host.
ProxSave does not store your private key or passphrase for you.

Native PBS snapshots use the PVE storage encryption key, not AGE recipients. Enabling
AGE archive encryption does not encrypt a PBS snapshot whose storage has no key.
See [native PBS encryption](STORAGE.md#check-the-actual-encryption-key).

### Enable archive encryption

Run `proxsave` without arguments on an interactive terminal and choose
**Maintenance** > **Install** > **Edit install**. Enable `Backup encryption (AGE)`.
The wizard writes `ENCRYPT_ARCHIVE=true` and runs recipient setup when needed.
To configure recipients separately, choose **Maintenance** > **New key**.

Before changing an existing setup, inspect `AGE_RECIPIENT` and `AGE_RECIPIENT_FILE`
in the active `configs/backup.env`. The recipient wizard rewrites the recipient file
only; it does not clear inline recipients or change the configured file path.
Inline recipients and file recipients are merged and deduplicated, so an old inline
recipient can remain authorized after you replace the file.

The normal recipient file is `${BASE_DIR}/identity/age/recipient.txt`. It contains
public recipients, not private recovery keys. `AGE_RECIPIENTS` is a fallback alias used
only when `AGE_RECIPIENT` is empty. See [recipient configuration](#configure-recipients)
for supported formats, separators and permissions.

### Choose recovery material you can retain

The recipient setup accepts an AGE public recipient, an AGE private identity from
which it derives the public recipient, or a passphrase from which it derives an identity.
It supports multiple recipients. Preserve each needed private identity outside the
host; entering a private identity during setup does not make ProxSave retain it.

For passphrases, setup requires at least 12 characters and three of four character
classes: lowercase, uppercase, digit and symbol. Known weak passphrases are rejected.
Strength validation does not replace a safely retained recovery secret.

A passphrase-derived recipient is an X25519 identity, not age's native passphrase
format. Setup uses a random per-installation salt. Each backup manifest carries the
salt needed for later passphrase recovery. Keep the bundle intact or retain the raw
archive with its matching manifest. The same passphrase entered into a new setup with
a new salt creates a different recipient.

SSH public recipients are accepted for encryption, but ProxSave's built-in recovery
prompt does not accept SSH private keys. Use X25519 recovery material for dashboard
recovery, or explicitly plan and test the external AGE procedure in the
[recipient reference](#configure-recipients).

### Understand plaintext exposure

Collection happens before encryption. Sensitive files are staged in a root-owned
`0700` per-run directory under `/tmp/proxsave`. The compressed archive is streamed into
AGE without creating a plaintext archive, but the staging tree remains plaintext.
Provision enough temporary space for an uncompressed collection as well as the output.
On tmpfs, plaintext may reach swap; on disk it reaches persistent storage.

The shared `/tmp/proxsave` root may be `0755`. Its guard rejects unsafe ownership or
write permissions but does not tighten every world-readable root. Review it and use
private permissions if your security policy requires them. The location is compiled
in; `TMPDIR` does not move the staging tree.

Normal return and the first interrupt clean up backup staging. SIGKILL, power loss or
a second interrupt can leave it behind. Give the first interrupt time to unwind.
After an unclean shutdown inspect leftovers and establish that no backup, decrypt or
restore is using them before removing a specific directory. Never delete by a broad
glob on a working host. The next backup's sweep does not guarantee cleanup after a
reboot or for restore staging. See [Plaintext staging](#plaintext-staging) for exact
cleanup scope and the external inspection procedure.

### Verify encryption and recovery

Choose **Backup** in the dashboard. Check the encryption result and the named artifact:
a raw encrypted archive ends in `.age`; the default bundled artifact ends in
`.age.bundle.tar`. Keep the archive's sidecars together when bundling is disabled.

Choose **Tools** > **Decrypt** and prove the saved archive opens with your independently
retained private identity or passphrase. A successful encrypted upload does not prove
that you retained a working recovery secret. Follow
[Decrypting Backups](#decrypting-backups) to inspect the result safely.

### Rotate keys without losing old recovery points

For a gradual rotation, add the new recipient alongside the old one, create and test
a backup with the new identity, then remove the old recipient from both file and inline
settings. Replacing recipients changes future backups only. Existing backups still
need an identity that matched their original recipients.

Keep old private identities and passphrases until all backups that require them have
expired. The wizard backs up an existing recipient file before overwriting it, but
that public file cannot decrypt your old backups. Review the
[rotation reference](#key-rotation) for replacement and multi-recipient behavior.

When migrating a setup, preserve the entire `identity/age` directory, especially
`passphrase.salt`, alongside configuration. The setup wizard reads the salt file;
the comment inside `recipient.txt` is not its substitute. Existing backup decryption
uses the manifest salt and remains possible even if local salt files are lost.
See [lost-salt recovery](#rebuilding-a-lost-salt) before recreating future recipients.
Command-line equivalents are in [CLI_REFERENCE.md](CLI_REFERENCE.md).

<!-- site-region: encrypt-backups:end -->

<!-- site-region: decrypt-backups:start -->

## Decrypting Backups

Decryption creates a plaintext bundle for inspection or transfer; it does not apply
configuration to the host. This workflow accepts AGE archives from primary, secondary
or rclone storage. Native PBS snapshots require the
[PBS recovery procedure](STORAGE.md#recover-a-native-pbs-snapshot).

### Prepare a safe destination and recovery secret

Keep the original encrypted bundle, or the archive with its matching manifest and
checksum. You need a matching `AGE-SECRET-KEY-...` private identity or the original
passphrase. A public recipient cannot decrypt a backup.

Passphrase recovery uses the salt recorded in that backup's manifest. It does not
read `recipient.txt` or the host's `passphrase.salt`, so losing local setup files does
not prevent recovery of a complete existing backup. A different salt in newly created
setup does not change the identity needed by older archives.

Provide enough destination and temporary space. Decryption stages plaintext under
`/tmp/proxsave`; normal completion removes temporary staging, while an abrupt kill or
power loss can leave plaintext behind. The saved output is deliberately plaintext.
Choose a private destination with restrictive permissions and control access for as
long as the output exists. Follow [staging cleanup guidance](#plaintext-staging) after
an unclean shutdown, checking for live operations before deleting anything.

### Decrypt through the dashboard

1. Run `proxsave` without arguments on an interactive terminal and choose
   **Tools** > **Decrypt**.
2. Select the configured primary, secondary or cloud archive source and then the
   exact encrypted backup. Confirm its hostname and date.
3. Choose the destination directory. The suggested path is `./decrypt` or
   `${BASE_DIR}/decrypt`, depending on the resolved installation context.
4. Enter the matching AGE private identity or original passphrase when prompted.
   If it does not match, check the selected backup and secret before trying again.
5. Confirm successful completion and record the output path.

The output is `*.decrypted.bundle.tar`, an outer plain tar containing the decrypted
archive plus metadata and checksum. It is not itself the configuration archive.
Inspect its member names before selecting the inner archive:

```bash
tar -tf /private/path/backup.decrypted.bundle.tar
```

To extract it for inspection, use a new private directory and the exact output path:

```bash
install -d -m 700 /root/proxsave-inspection
tar -xf /private/path/backup.decrypted.bundle.tar -C /root/proxsave-inspection
```

Verify the inner archive against the matching checksum from that bundle, and inspect
expected configuration members before relying on it. Keep the original encrypted copy.
Remove plaintext inspection copies when finished; deletion is not guaranteed secure
erasure from the underlying storage.

### Recover with external AGE tools

When ProxSave is unavailable, the official AGE client can decrypt with a suitable
private identity. First unwrap the encrypted bundle into a private directory; feeding
the outer `.bundle.tar` directly to age is not the same as decrypting its `.age` member.
The complete [emergency decryption procedure](#emergency-decryption-without-configuration)
preserves the extraction order, checksum validation and cleanup steps.

A passphrase-derived ProxSave recipient is not age's native scrypt passphrase stanza.
The stock age client cannot reproduce it from the passphrase alone. Use the dashboard
with the original passphrase and manifest salt, or an independently retained matching
private identity for external recovery. Do not enter an SSH private key into the
ProxSave prompt: it is treated as a passphrase and derives the wrong identity. SSH
recipient recovery requires the external AGE procedure.

### Apply configuration only through a planned restore

If the goal is host recovery, choose **Tools** > **Restore** instead. That workflow can
verify and decrypt the original encrypted backup before category selection and plan
review; there is no need to decrypt first merely to restore it. Follow the
[restore guide](RESTORE_GUIDE.md#restore-modes) and target-specific cluster, storage,
network and boot precautions. Do not copy the extracted tree over `/`.

For a replacement host, restore remote credentials independently, make the complete
backup available through a configured source and retain the original recovery secret.
If a checksum fails, stop and retrieve another verified copy. If the key does not match,
check the original recipients and recovery records; new recipient setup does not
unlock an old backup. See [Emergency Scenarios](#emergency-scenarios) for missing salt
or setup files. Automation entry points belong in [CLI_REFERENCE.md](CLI_REFERENCE.md).

<!-- site-region: decrypt-backups:end -->

## Related Documentation

### Configuration
- **[Configuration Guide](CONFIGURATION.md)** - Complete variable reference including all AGE settings
- **[Cloud Storage Guide](CLOUD_STORAGE.md)** - rclone integration with encrypted cloud backups

### Restore Operations
- **[Restore Guide](RESTORE_GUIDE.md)** - Complete restore workflows (all modes)
- **[Restore Technical](RESTORE_TECHNICAL.md)** - Technical implementation details
- **[Cluster Recovery](CLUSTER_RECOVERY.md)** - Disaster recovery procedures

### Reference
- **[Dashboard](DASHBOARD.md)** - The interactive menu: New key, Backup, Decrypt, Restore
- **[Daemon](DAEMON.md)** - The resident scheduler that runs the encrypted backups
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

See [CLI_REFERENCE.md](CLI_REFERENCE.md) for encryption, decryption and restore command equivalents.

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


## Implementation appendix

### Encryption Implementation

- **Algorithm**: ChaCha20-Poly1305 (AEAD) with X25519 ECDH
- **Key derivation**: scrypt (N=2^15, r=8, p=1) for passphrases. The current scheme uses a **per-installation random salt** (v2), generated once, stored `0600` at `identity/age/passphrase.salt`, mirrored as the `# passphrase-salt:` line inside the recipient file (every backup rewrites that comment from the sibling before reading it back, so the sibling wins whenever the two differ; once the sibling is gone the comment still supplies the salt stamped into new manifests, but it can **not** re-derive the recipient, because the setup wizard reads the sibling alone and mints a fresh random salt when it is missing, yielding a different recipient), and embedded in each manifest as `passphrase_salt` so the passphrase alone can re-derive the recipient on any host. At decrypt ProxSave tries salts in order: the manifest's per-install salt first, then two fixed legacy namespaces (`proxsave/age-passphrase/v1`, then the pre-rebrand `proxmox-backup-go/age-passphrase/v1`), so archives from older versions and from before the rename stay decryptable.
- **Random nonces**: Unique per encryption operation
- **Authentication**: Poly1305 MAC prevents tampering



## Encryption settings and security reference

The task above is the operator procedure. The following material preserves detailed behavior, limits and implementation context.

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
a decrypt or a restore in progress.

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
#      restore-stage-*      restore staging: plaintext shadow and pve priv, from a killed restore
rm -rf /tmp/proxsave/restore-stage-20260803-120000_1
```

Two practical consequences. `/tmp` needs room for a full uncompressed copy of everything
being backed up, on top of the archive itself. And if `/tmp` is a tmpfs the staged plaintext
is in RAM and can reach swap; if it is on disk, it is written to persistent storage.

The same applies in reverse. `proxsave --decrypt` stages under
`/tmp/proxsave/proxmox-decrypt-*` and removes it at the end of a normal run. `proxsave
--restore` extracts the sensitive categories in the clear into
`/tmp/proxsave/restore-stage-<timestamp>_<seq>/`, which holds material such as `/etc/shadow`,
`/etc/gshadow` and `/etc/pve/priv/*.cfg`, and deletes it when the restore ends, on success or
failure. It is not registered, so a restore killed with `SIGKILL` or by a power loss leaves it
behind and nothing sweeps it.

What a restore keeps on purpose is not under `/tmp`: it goes into the restore's own directory,
`<BASE_DIR>/restore/<timestamp>/` (`/opt/proxsave/restore/<timestamp>/` by default), created **mode 0700**, one directory per restore. It
holds the rollback and safety tarballs (`restore_backup_`, `network_rollback_backup_`,
`firewall_rollback_backup_`, `ha_rollback_backup_`, `pve_access_control_rollback_backup_`,
each `_<timestamp>.tar.gz`), their `*_location.txt` files, the restore session log, the
detailed restore logs and the rollback logs, all **mode 0600**, plus what the restore hands to
you on the network and PBS side (`nic_repair_*`, `network_apply_*`, `datastore.cfg.deferred.*`,
`datastore.cfg.pre-normalize.*`). They are the rollback and the record of what the
restore did, and they survive the reboot the restore recommends, including on a host whose
`/tmp` is a tmpfs. ProxSave never deletes them. Clean them up yourself once a restore has
settled.

If you decrypt by hand with the `age` CLI, your own output is plaintext too: pipe it rather
than land it on a shared filesystem.

---

## Configure Recipients

Recipients identify the public keys used to encrypt an archive. Any matching private
identity can decrypt an archive encrypted for multiple recipients. ProxSave's
passphrase setup derives an X25519 identity and recipient using the installation's
salt; a public recipient alone cannot decrypt a backup.

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

Setup requirements and supported input forms follow. Use the [encryption task](#encrypt-backups-and-preserve-recovery-keys) for the dashboard procedure and [CLI_REFERENCE.md](CLI_REFERENCE.md) for text-mode entry points.

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

Then run the recipient setup, from the dashboard (**Maintenance** > **New key**) or the
flag:

Choose **Maintenance** > **New key** in the dashboard.

If the target recipient file already exists, `--newkey` asks for confirmation and copies the
old file to `<path>.bak-<timestamp>` itself before overwriting. If it does not exist, for
example when your only recipient was inline, it writes the new file with no prompt and no
warning.

The shipped template leaves `AGE_RECIPIENT` empty and nothing in the installer or the wizard
ever writes a value into it, so if you never hand-edited `backup.env` and set no environment
variable, `--newkey` does replace the effective recipient set.

---

## Security Notes

### Security Best Practices

| Practice | Implementation |
|----------|----------------|
| **Passphrase handling** | CLI entry uses `term.ReadPassword` (no echo); the TUI uses its password input |
| **Memory security** | Explicit buffers are cleared where implemented, but immutable Go strings and other copies cannot be guaranteed to be overwritten |
| **Streaming encryption** | No plaintext **archive** on disk during backup. Backup and restore staging are removed on normal completion; abrupt termination can leave plaintext behind. Restore safety archives are retained separately: see [Plaintext staging](#plaintext-staging) |
| **File permissions & ownership** | Enforced 0700/0600 and root:root on recipient/identity files (auto-fixed with `AUTO_FIX_PERMISSIONS`, otherwise warned) |
| **Private key storage** | **Keep offline** (password manager, hardware token, printed backup) |
| **Backup separation** | Store keys separately from backup media |
| **Access control** | Limit who has decryption keys |

### Private Key Protection

A private identity unlocks every backup encrypted to its matching recipient. Protect it as carefully as the data it can reveal.

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
- Store private keys on the same server as backups
- Commit private keys to git repositories
- Email private keys (even encrypted)
- Store in cloud drives without additional encryption

### Threat Model

**Protected against**:
- Backup media theft (encrypted at rest)
- Unauthorized access to backup storage
- Archive tampering (authenticated encryption)
- Network interception (if using rclone with encryption)

**Not protected against**:
- Compromise of the server during a backup. While a backup runs, the collected files sit unencrypted under `/tmp/proxsave` (see [Plaintext staging](#plaintext-staging)); a root-level compromise in that window sees everything in the clear
- Private key theft from offline storage
- Weak passphrase brute-force
- Advanced persistent threats on backup server

**Mitigation strategies**:
- Run backups on isolated systems
- Use hardware security modules (HSM) for production
- Implement key splitting (Shamir's Secret Sharing)
- Regular security audits

### Compliance Considerations

Archive encryption is one technical control. ProxSave does not establish or certify
compliance with GDPR, HIPAA, PCI DSS or SOC 2. Any assessment must also cover the
deployment, access controls, key management, plaintext staging and operational procedures.

**Diagnostics**: `DEBUG_LEVEL=advanced`, `DEBUG_LEVEL=extreme` or `--log-level debug`
records additional operational details. Review diagnostic output before sharing it;
it is not a compliance audit trail.

---


## Recovery reference

The task above is the operator procedure. The following material preserves detailed behavior, limits and implementation context.

## Emergency Scenarios

| Scenario | Solution |
|----------|----------|
| **Lost passphrase/private key** | **No recovery possible**. Keep 2+ offline copies (password manager, printed paper). |
| **Migrating to new server** | Copy the whole `identity/age/` directory byte for byte, **`passphrase.salt` included**, plus your `configs/backup.env`. `passphrase.salt` is the file that matters: the setup wizard reads only that one, and if it is missing it mints a new random salt and derives a **different** recipient without warning. The `# passphrase-salt:` copy inside `recipient.txt` only feeds the manifest, so do not rely on it alone and do not retype the recipient file. Alternatively choose **Maintenance** > **New key** on the new host and accept a new recipient. Keep private keys offline. |
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
  the dashboard **Tools** > **Decrypt** workflow, which reads the bundle directly. It only lists backups found under
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

## External AGE extraction reference

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
> passphrase alone, use **Tools** > **Decrypt**. ProxSave re-derives the identity from
> the passphrase plus the **per-installation random salt** generated at setup, which
> is stored next to the recipient (`identity/age/passphrase.salt`, mirrored as the
> `# passphrase-salt:` line in the recipient file) and embedded in every backup manifest
> (`passphrase_salt`) so recovery works on any host. The emergency `age` CLI path above
> therefore needs an `AGE-SECRET-KEY-...` identity file; a passphrase-only holder must use
> the dashboard **Tools** > **Decrypt** workflow, which reads the bundle directly.
>
> **Decryption never reads `recipient.txt` or `passphrase.salt`.** The salt travels inside
> each archive's manifest, so an existing backup stays openable with its passphrase even if
> both local copies are gone. See [Emergency
> Scenarios](#emergency-scenarios) for how to rebuild them from an archive.

---


## Earlier guide entry points

## Features

See the complete [operator procedure](#encrypt-backups-and-preserve-recovery-keys). Detailed settings and implementation material remain in the reference sections above.

## Overview

See the complete [operator procedure](#encrypt-backups-and-preserve-recovery-keys). Detailed settings and implementation material remain in the reference sections above.

## Quick Start

See the complete [operator procedure](#encrypt-backups-and-preserve-recovery-keys). Detailed settings and implementation material remain in the reference sections above.

## Restoring Encrypted Backups

See the complete [operator procedure](#decrypting-backups). Detailed settings and implementation material remain in the reference sections above.

## Running Encrypted Backups

See the complete [operator procedure](#encrypt-backups-and-preserve-recovery-keys). Detailed settings and implementation material remain in the reference sections above.
