# Troubleshooting Guide

Complete troubleshooting guide for Proxsave with common issues, solutions, and debugging procedures.

## Table of Contents

- [Overview](#overview)
- [Common Issues](#common-issues)
  - [Build Failures](#1-build-failures)
  - [Configuration Issues](#2-configuration-issues)
  - [Cloud Storage Issues](#3-cloud-storage-issues)
  - [Encryption Issues](#4-encryption-issues)
  - [Disk Space Issues](#5-disk-space-issues)
  - [Email Notification Issues](#6-email-notification-issues)
  - [Restore Issues](#7-restore-issues)
  - [Backup Monitoring Issues](#8-backup-monitoring-issues)
- [Debug Procedures](#debug-procedures)
- [Getting Help](#getting-help)
- [Exit Codes](#exit-codes)
- [Related Documentation](#related-documentation)

---

## Overview

This guide covers the most common issues encountered when using Proxsave, along with step-by-step solutions and debugging procedures.

**Before troubleshooting**:
1. Check you're running the latest version: `proxsave --version`
2. Try dry-run mode first: `proxsave --dry-run --log-level debug`
3. Review the newest log in `LOG_PATH` (default `/opt/proxsave/log`; use your own directory if `LOG_PATH` or `LOCAL_LOG_PATH` is set in `configs/backup.env`). The filename carries the FQDN, so glob the directory rather than guessing: `ls -t /opt/proxsave/log/backup-*.log | head -1`

---

## Common Issues

### 1. Build Failures

#### Error: `go: cannot find main module`

**Cause**: Not running from the project root directory (where `go.mod` lives).

**Solution**:
```bash
cd /opt/proxsave  # Ensure you're in project root (go.mod ships with the repo)
go mod tidy
make build
```

**Verification**:
```bash
ls -la go.mod go.sum
# Both files should exist
```

---

#### Error: `package xxx not found`

**Cause**: Missing dependencies.

**Solution**:
```bash
go mod tidy  # Download dependencies
make build
```

**Alternative (clean rebuild)**:
```bash
rm -rf go.sum vendor/
go mod tidy
go mod vendor  # Optional: vendor dependencies
make build
```

---

#### Error: `build fails with permission denied`

**Cause**: Insufficient permissions on build directory.

**Solution**:
```bash
# Fix build directory permissions
chmod 755 /opt/proxsave/build
chown $(whoami):$(whoami) /opt/proxsave/build

# Rebuild
make clean
make build
```

---

### 2. Configuration Issues

#### Error: `Configuration file not found: configs/backup.env`

**Cause**: Configuration file doesn't exist or is in wrong location.

**Solution**:
```bash
# Run installer to create config
proxsave --install
# For a clean reinstall (keeps build/, env/, and identity/), run:
# proxsave --new-install

# Or copy template manually
cp internal/config/templates/backup.env configs/backup.env
nano configs/backup.env
```

**Using custom path**:
```bash
# Specify custom config location
proxsave --config /etc/pbs/prod.env
```

---

#### Error: `Security check failed: Permission denied`

**Cause**: Incorrect file/directory permissions.

**Solution**:
```bash
# Fix permissions manually. 755 is what the security check expects for these two:
# setting 700 makes every run warn and, with AUTO_FIX_PERMISSIONS on (the default),
# chmod them straight back.
chmod 755 /opt/proxsave/backup
chmod 755 /opt/proxsave/log
chmod 600 /opt/proxsave/configs/backup.env

# Or enable auto-fix in config
nano configs/backup.env
AUTO_FIX_PERMISSIONS=true
```

**Recommended permissions**:
```text
/opt/proxsave/           755 (drwxr-xr-x)
├── backup/                    755 (drwxr-xr-x)
├── log/                       755 (drwxr-xr-x)
├── configs/
│   └── backup.env             600 (-rw-------)
├── identity/                  700 (drwx------)
│   └── age/
│       ├── recipient.txt      600 (-rw-------)
└── build/
    └── proxsave               755 (-rwxr-xr-x)
```

**CIFS/SMB or Windows-backed shares**:
Linux permission modes and `root:root` ownership are often synthetic on CIFS/SMB mounts, especially when the server is Windows. ProxSave detects non-POSIX backup/log filesystems and skips POSIX permission/ownership warnings for `BACKUP_PATH`, `LOG_PATH`, `SECONDARY_PATH`, and `SECONDARY_LOG_PATH`. If warnings persist, confirm the share is mounted before ProxSave starts and that the mount type appears as `cifs`/`smb` in `/proc/mounts`.

---

#### Error: `Invalid configuration value for COMPRESSION_TYPE`

**Cause**: Typo or unsupported compression algorithm.

**Solution**:
```bash
# Check valid values
nano configs/backup.env
COMPRESSION_TYPE=xz    # Valid: gzip, bzip2, xz, lzma, zstd (also pigz; none to disable)
```

**Test configuration**:
```bash
proxsave --dry-run --log-level debug
# Check for configuration validation errors
```

---

#### Notice: `SKIP ... Expected with limited privileges` (containers/non-root)

**Symptoms**:
- Running ProxSave in an environment with **limited privileges** (for example a container or non-root execution) can produce log lines like:
  - `SKIP Skipping Hardware DMI information: DMI tables not accessible (Expected with limited privileges).`
  - `SKIP Skipping Block device identifiers (blkid): block devices not accessible (restore hint: fstab remap may be limited) (Expected with limited privileges).`

**Cause**: With limited privileges, access to low-level system interfaces is intentionally restricted (for example `/dev/mem` and most block devices). Some inventory commands can fail even though the backup itself is working correctly.

**Behavior**:
- ProxSave still attempts the collection.
- Only a small allowlist of **privilege-sensitive** commands is downgraded from `WARNING` to `SKIP` when failure is expected in this environment (`dmidecode`, `blkid`, `sensors`, `smartctl`).
- Other failures are **not** downgraded and still appear as warnings/errors.

**Impact**:
- Hardware inventory output may be missing/empty.
- If `blkid` is skipped, ProxSave restore may have **limited** ability to automatically remap `/etc/fstab` devices (UUID/PARTUUID/LABEL). You may need to review mounts manually during restore.

**How to verify**:
- Check the startup log line: `INFO Privilege context: ...`
- If you suspect an unprivileged/shifted user namespace mapping:
```bash
cat /proc/self/uid_map
cat /proc/self/gid_map
# If the second column is non-zero (e.g. "0 100000 65536"), you're in a shifted/unprivileged mapping.
```

**Optional**: If you want to hide `SKIP` lines on the console, run with `--log-level warning` (this also hides normal info logs).

---

#### A personal pre/post script seems not to run, and nothing is logged

**Symptoms**:
- `PERSONAL_SCRIPT_PRE_RUN` or `PERSONAL_SCRIPT_POST_RUN` is set, but the script's effect never
  happens, and no log line, warning, notification or ping mentions it.

**Cause**:
- Silence is by design: ProxSave starts these scripts and reports nothing about them, so a
  script that never started looks exactly like one that ran and did nothing. The usual reasons
  it never starts are a path that does not exist, a missing
  shebang, a value that is a command line rather than a bare path (no shell is used, so
  arguments, pipes and redirections are not interpreted), a path edited in `backup.env` without
  restarting the daemon, or a run that was not the daemon's (a manual `proxsave --backup` and a
  cron-mode run start neither script).
- The one cause that DOES log: the trusted-path gate at daemon start. A path that traverses a
  symlink, is not executable, is writable by group or others, or sits under a directory not
  owned by root (or writable by others without the sticky bit) is disabled for that daemon
  with a `WARNING` naming the variable, the path and the reason.

**Resolution**:
```bash
proxsave --daemon-status --log-level debug      # first: READY/REFUSED plus UID and path evidence
journalctl -u proxsave-daemon.service | grep PERSONAL_SCRIPT   # a gate refusal names the reason
namei -l /path/to/my-pre-run.sh         # inspect ownership and mode of every path component
ls -l /usr/local/bin/my-pre-run.sh      # owned by root, mode 0700 or 0755, no group/other write
head -1 /usr/local/bin/my-pre-run.sh    # a shebang, e.g. #!/bin/sh
/usr/local/bin/my-pre-run.sh; echo $?   # runs by hand, as root
grep PERSONAL_SCRIPT /opt/proxsave/configs/backup.env
systemctl restart proxsave-daemon.service   # the paths are read at daemon start
```
- The status command is read-only: it does not execute either script, start a backup, acquire the
  daemon lock, or send a healthcheck ping. `READY` means the path passes the same gate used by the
  daemon; `REFUSED` gives the exact reason; `NOT CONFIGURED` means the setting is empty. Debug output
  says whether the UID came from the live daemon or from the current-process fallback and lists the
  owner/mode evidence. A script refusal does not change the command's daemon-health exit code.
- Do not change a user's home directory to `root:root`. Copy or move the script to a fully
  root-owned path such as `/usr/local/bin`, update `backup.env`, then restart the daemon.
- Have the script write its own log if you want a record: ProxSave will not write one for you.
- A script still running after 10 minutes is killed, silently, and the daemon carries on. The
  one exception is the abandoned-child unwind, where the post script is started and left to
  systemd's cgroup teardown (see [DAEMON.md](DAEMON.md)).

---

### 3. Cloud Storage Issues

#### Error: `rclone not found in PATH`

**Cause**: rclone not installed or not in PATH.

**Solution**:
```bash
# Install rclone
curl https://rclone.org/install.sh | sudo bash

# Verify
rclone version
which rclone
```

**Manual installation**:
```bash
wget https://downloads.rclone.org/rclone-current-linux-amd64.zip
unzip rclone-current-linux-amd64.zip
sudo cp rclone-*/rclone /usr/local/bin/
sudo chmod 755 /usr/local/bin/rclone
```

---

#### Error: `Cloud remote gdrive not accessible: couldn't find configuration section`

**Cause**: rclone remote not configured.

**Solution**:
```bash
# Configure rclone remote
rclone config
# n > gdrive > drive > ... > y > q

# Test remote
rclone listremotes
# Should show: gdrive:

# Test access
rclone lsf gdrive:
```

**Verify remote in config**:
```bash
# Check rclone config
rclone config show gdrive

# Verify backup.env points to correct remote
grep CLOUD_REMOTE configs/backup.env
grep CLOUD_REMOTE_PATH configs/backup.env
# Should match:
#   CLOUD_REMOTE=gdrive
#   CLOUD_REMOTE_PATH=/pbs-backups
```

---

#### Error: `401 unauthorized`

**Cause**: Expired OAuth token or invalid credentials.

**Solution for Google Drive**:
```bash
# Reconnect OAuth
rclone config reconnect gdrive

# Or reconfigure from scratch
rclone config delete gdrive
rclone config  # Create new
```

**Solution for S3/B2**:
```bash
# Regenerate API keys from provider console
# Delete old remote and create new with fresh keys
rclone config delete s3backup
rclone config  # Create new with updated credentials
```

---

#### Error: `connection timeout (30s)`

**Cause**: Slow network or firewall blocking connection.

**Solution**:
```bash
# Increase connection timeout
nano configs/backup.env
RCLONE_TIMEOUT_CONNECTION=60

# Test connectivity
ping -c 4 google.com
curl -I https://www.googleapis.com
```

**Firewall check**:
```bash
# Test HTTPS connectivity
curl -v https://www.googleapis.com 2>&1 | grep -i "connected\|timeout"

# Check firewall rules (if applicable)
iptables -L -n | grep -i drop
```

---

#### Restore/Decrypt: stuck on "Scanning backup path..." or timeout (cloud/rclone)

**Cause**: ProxSave scans cloud backups by listing the remote (`rclone lsf`) and inspecting each candidate by reading the manifest/metadata (`rclone cat`). Each rclone call is protected by `RCLONE_TIMEOUT_CONNECTION` (the timer resets per command). On slow remotes or very large directories this can time out.

**Solution**:
```bash
# Increase scan timeout
nano configs/backup.env
RCLONE_TIMEOUT_CONNECTION=120

# Ensure you selected the remote directory that contains the backups (scan is non-recursive),
# then re-run restore with debug logs (restore log path is printed on start)
proxsave --restore --log-level debug

# Or use support mode to capture full diagnostics
proxsave --restore --support
```

If it still fails, run the equivalent manual checks:
```bash
rclone lsf <remote:path>
rclone cat <remote:path>/<backup>.bundle.tar | head
```

---

#### Error: `operation timeout (300s exceeded)`

**Cause**: Large backup file + slow upload speed.

**Solution 1: Increase timeout**:
```bash
nano configs/backup.env
RCLONE_TIMEOUT_OPERATION=900  # 15 minutes
```

**Solution 2: Reduce backup size**:
```bash
# Use faster compression
COMPRESSION_TYPE=zstd
COMPRESSION_LEVEL=3
COMPRESSION_MODE=fast

# Or reduce backup scope
BACKUP_CLUSTER_CONFIG=false
BACKUP_ROOT_HOME=false
```

> Note: `BACKUP_CLUSTER_CONFIG=false` also skips cluster runtime collection (`pvecm status`, `pvecm nodes`, HA status), which helps avoid non-critical cluster warnings on standalone nodes.

---

#### Error: `429 Too Many Requests` (API rate limiting)

**Cause**: Exceeding cloud provider API rate limits.

**Solution**:
```bash
# Reduce parallel transfers
nano configs/backup.env
RCLONE_TRANSFERS=2
CLOUD_BATCH_SIZE=10
CLOUD_BATCH_PAUSE=3  # Wait 3 seconds between batches
```

**Provider-specific tuning**:

**Google Drive**:
```bash
RCLONE_TRANSFERS=2
CLOUD_BATCH_SIZE=10
CLOUD_BATCH_PAUSE=2
```

**Backblaze B2**:
```bash
RCLONE_TRANSFERS=2
CLOUD_BATCH_SIZE=20
CLOUD_BATCH_PAUSE=2
```

---

#### Error: `directory not found` or `403 forbidden` during connectivity check

**Cause**: API token lacks list/about permissions. Common with:
- **Cloudflare R2** restricted tokens
- **S3-compatible providers** with minimal permissions
- **Backblaze B2**, **Wasabi** write-only tokens

**Solution**:
```bash
# Use write test instead of list test
nano configs/backup.env
CLOUD_WRITE_HEALTHCHECK=true
```

This creates a temporary test file (`.pbs-backup-healthcheck-<timestamp>`) and deletes it, requiring only write/delete permissions instead of list operations.

**Alternative**: Grant list permissions to your API token if possible.

**Verify permissions**:
```bash
# Test write access
echo "test" > /tmp/test.txt
rclone copy /tmp/test.txt gdrive:pbs-backups/
rclone deletefile gdrive:pbs-backups/test.txt
rm /tmp/test.txt
```

---

### 4. Encryption Issues

#### Error: `Encryption setup requires interaction but terminal unavailable`

**Cause**: Trying to run encryption wizard in non-interactive environment (cron, systemd).

**Solution 1: Pre-generate keys**:
```bash
# Run key generation interactively first
proxsave --newkey

# Then run backup (uses existing keys)
proxsave
```

**Solution 2: Set recipient directly**:
```bash
# Generate key manually
age-keygen -o configs/age-keys.txt

# Extract public key
grep "# public key:" configs/age-keys.txt | cut -d: -f2 | tr -d ' '

# Add to config
nano configs/backup.env
AGE_RECIPIENT="age1abc123def456..."
```

---

#### Error: `Failed to decrypt: incorrect passphrase`

**Cause**: Wrong passphrase, wrong key, or corrupted backup.

**Solution**:
- **Verify passphrase** is correct (case-sensitive, no trailing spaces)
- If using private key, paste full `AGE-SECRET-KEY-1...` string (not public key!)
- Try alternative recipients if multiple were configured
- **No recovery possible** if passphrase/key is lost

**Test decryption manually**:
```bash
# With private key file
age --decrypt -i configs/age-keys.txt backup.tar.xz.age > test.tar.xz
```

> **Passphrase-based backups**: the archive is encrypted to an X25519 recipient
> *derived* from your passphrase, not with age's native passphrase (scrypt) mode,
> so `age --decrypt` will not prompt for a passphrase and cannot decrypt it on its
> own. Use `proxsave --decrypt` and enter the passphrase when prompted: proxsave
> re-derives the matching identity using the per-installation salt recorded in the
> backup manifest (`passphrase_salt`).

**Verify backup integrity**:
```bash
# Check SHA256 checksum
sha256sum -c backup.*.sha256

# If checksum fails, backup is corrupted
```

---

#### Error: `age: no identity matched any of the recipients`

**Cause**: Using wrong private key (doesn't match any recipient used during encryption).

**Solution**:
```bash
# Check which public key was used during encryption
# (stored in backup.env at backup time)

# Verify your private key matches
age-keygen -y configs/age-keys.txt
# Output should match AGE_RECIPIENT in backup.env

# If mismatch, find correct private key or use passphrase
```

---

### 5. Disk Space Issues

#### Error: `Insufficient disk space: 0.5 GB available, 1 GB required`

**Cause**: Not enough free space for backup creation.

**Solution 1: Clean old backups**:
```bash
# Check disk usage
df -h /opt/proxsave

# List backups by size
ls -lh /opt/proxsave/backup/

# Clean old backups manually
rm /opt/proxsave/backup/backup.*.tar.xz
```

**Solution 2: Adjust retention**:
```bash
nano configs/backup.env
MAX_LOCAL_BACKUPS=5  # Keep fewer backups

# Or use GFS with aggressive pruning
RETENTION_POLICY=gfs
RETENTION_DAILY=3
RETENTION_WEEKLY=2
RETENTION_MONTHLY=6
RETENTION_YEARLY=2
```

**Solution 3: Reduce backup size**:
```bash
# Disable large collectors
BACKUP_ROOT_HOME=false
BACKUP_CRITICAL_FILES=false

# Use faster/smaller compression
COMPRESSION_TYPE=zstd
COMPRESSION_LEVEL=3
```

---

#### Warning: `retention ignored N backup(s) that do not belong to <host>`

**Symptoms**: the run logs the line above, prefixed by the location it applies to (`Local storage:`, `Secondary storage:` or `Cloud storage:`), often followed by `N of those carry this host's short name under a different spelling`. The location keeps growing past `MAX_LOCAL_BACKUPS` / `MAX_SECONDARY_BACKUPS` / `MAX_CLOUD_BACKUPS`, and the run prunes nothing. A separate INFO line, `retention left N backup(s) alone because nothing names the host that wrote them`, is a different case and is covered as cause 3 below.

**Cause**: retention only prunes what this host owns. The owner of an archive is the `hostname` recorded in its manifest, or the host token its filename carries (`<host>-backup-<timestamp>`) when no manifest can be read. A host answers to the name the kernel reports (`hostname`) and to the name it stamps into what it writes (usually the FQDN from `hostname -f`), and to nothing else. There are three reasons an archive lands outside that set:

1. **It belongs to another machine** sharing the directory or the remote prefix. The filter is doing its job and nothing needs doing.
2. **It is this machine's own work, written under a name it no longer resolves.** The location then holds both spellings: the short-named archives rotate, the FQDN-named ones do not.

   Archives written from this version on also record the **server identity** of the machine that produced them, and that identity survives a hostname change. A host that has LOST the ability to resolve its own qualified name recognises those archives as its own again and keeps rotating them automatically: no action is needed and the warning stops. The run says so on an INFO line, `retention brought N backup(s) back into rotation`.

   Four limits on that, all deliberate:

   - **It is not retroactive.** Archives written before this version carry no server identity and can never be adopted. A host that is stranded today still needs solution 1 or solution 2 below for its existing backlog.
   - **A host that still resolves a qualified form of its own name is not auto-claimed.** If `hostname -f` still returns `pve.home.arpa` and the archives say `pve.siteB.example`, this host answers to two spellings of `pve` already, so a third one is indistinguishable from a second machine, or from a clone of this one that inherited the same server identity. Retention leaves those alone and says why: `N of them also carry this host's own server identity`.
   - **Adoption only ever looks under the first label of the name the KERNEL reports.** If this host is called `pve` but `hostname -f` answers `nas` (a rewritten `/etc/hosts` line such as `127.0.1.1 nas pve` does exactly that), archives named `nas.lan` are NOT auto-claimed even when they carry this host's identity, because retention reports its spelling mismatches under `pve` and may only ever claim from inside what it reports. Use solution 1 or solution 2 for those.
   - **A mismatching server identity never stops a host rotating its own archives.** If an archive names a host this machine answers to, it is this machine's, whatever identity it records. Reinstalling ProxSave or restoring `BASE_DIR` from elsewhere mints a NEW server identity, and the run reports the divergence on an INFO line, `N backup(s) this host owns by name record a different server identity`, without changing what it prunes.
3. **Nothing names the machine that wrote it.** Pre-Go archives are called `proxmox-backup-<timestamp>`, and that leading label is the product name, not a host, so the filename carries no host token. If the `.metadata` beside such an archive is missing, unreadable, or carries no `HOSTNAME=` line, nothing anywhere can say which machine wrote it, so retention leaves it alone on every host: on a shared directory or remote prefix, claiming it means deleting another machine's backup. This case is reported at INFO, not as a warning, so it does not by itself change the run's exit code: it is a fixed backlog you clear by hand, not something that went wrong on the run. One sub-case is already noisy for a separate, older reason: an archive with no `.metadata` beside it at all makes the local listing log `Missing .metadata for X - using filename metadata` at WARNING on every pass, which promotes the run to exit 1. That was already true before retention stopped claiming these archives; what changes is that it now repeats until you remove them. A pre-Go archive whose `.metadata` DOES carry a `HOSTNAME=` line is attributed normally and keeps rotating on the machine it names.

**Tell the two apart**:
```bash
# What this host calls itself, both ways
hostname
hostname -f

# What the archives are named (adjust the path to the location that warned)
ls -1 /opt/proxsave/backup/*-backup-*.tar* | sed 's#.*/##'

# What the newest run decided
grep -E "retention ignored|different spelling|Simple retention" \
  "$(ls -t /opt/proxsave/log/backup-*.log | head -1)"
```
If the names before `-backup-` are spellings of THIS host, you are in case 2. Compare their first label against plain `hostname`, not against `hostname -f`: automatic adoption keys on the kernel name, so archives whose first label matches only what `hostname -f` prints need solution 1 or solution 2 like any other case-2 backlog.

The two INFO lines above are only in the log of a run made at INFO level or below, which is the default. To see which archives each decision covers, run one backup with `--log-level debug`: the per-file `retention out of scope` lines then name the archive's server identity and which check refused it.

**Solution 1 (case 2, preferred): make the host resolve its own name again**:
```bash
# hostname -f reads /etc/hosts and then DNS. The usual regression is a rewritten
# hosts line with the short name first, which makes hostname -f print the short name.
grep -n "$(hostname)" /etc/hosts

# Wrong: the short name comes first, so hostname -f returns "pve"
# 192.168.1.10 pve pve.home.arpa
# Right: the FQDN comes first
# 192.168.1.10 pve.home.arpa pve

# It must now print the spelling the archives carry, so compare it with the
# prefix the filenames above carry. A dry run cannot confirm this: it stops
# before storage dispatch, so retention never executes.
hostname -f
```
The next real run recognises the stranded archives and brings the location down to its configured limit in a single pass, deleting nothing that belongs to another host.

**Solution 2 (case 2, when the old name is gone for good): remove the stranded archives by hand**:
```bash
# List first, and read the list
ls -1 /opt/proxsave/backup/pve.home.arpa-backup-*.tar*

# Then delete the ones you chose, by name, with a trailing glob so the .sha256,
# .metadata and .manifest.json sidecars go with the archive
rm -f /opt/proxsave/backup/pve.home.arpa-backup-20260101-000000.tar.xz*
```
Never pipe the listing straight into `rm`: the same directory may hold another machine's archives.

> **Renaming the archives does not get them rotating again**, whichever way it is done. Rename the archive alone and its `.sha256` is left behind, so the run stops treating the archive as complete and reports it as `ignored by retention (no manifest/checksum)`. Rename the sidecars with it and the `.metadata` travels too, and the `hostname` recorded inside it is what attribution reads, so the archive is still attributed to the old spelling. A `.bundle.tar` carries that manifest inside the archive, where a rename cannot reach it at all. Restore the name resolution, or delete the archives.

**Case 1: give every host its own location**. Point each host at its own directory (`BACKUP_PATH`, `SECONDARY_PATH`) or its own `CLOUD_REMOTE_PATH` prefix. See the "Give every host its own prefix" note in [CLOUD_STORAGE.md](CLOUD_STORAGE.md) for the cloud side.

**Case 3: remove the pre-Go archives when you no longer need them**. Nothing will ever rotate them, so the only way the location comes back down is by hand.
```bash
# Name the files the run left alone. The per-file lines are DEBUG, so they are only
# in the log of a run made at that level: set LOG_LEVEL=debug in backup.env, or run
# one backup with --log-level debug, then read the newest log.
grep "retention out of scope" "$(ls -t /opt/proxsave/log/backup-*.log | head -1)"

# Check first whether the sidecar names a host. If it prints a name, that archive
# still rotates on THAT machine and you should leave it where it is.
grep -H "^HOSTNAME=" /opt/proxsave/backup/proxmox-backup-*.tar.*.metadata

# List, read the list, then delete the ones you chose by name, with a trailing
# glob so the .sha256 and .metadata sidecars go with the archive
ls -1 /opt/proxsave/backup/proxmox-backup-*.tar.*
rm -f /opt/proxsave/backup/proxmox-backup-20240101-000000.tar.gz*
```
Never pipe the listing straight into `rm`: the same directory may hold another machine's archives.

> A host whose kernel hostname is literally `proxmox` writes archives with the same name shape (`proxmox-backup-<timestamp>`), so on that one host these are current backups, not a pre-Go backlog. Check the `HOSTNAME=` line before deleting anything there.

**Verification**:
```bash
grep -E "retention ignored|Simple retention" \
  "$(ls -t /opt/proxsave/log/backup-*.log | head -1)"
```
The warning is gone and the run reports `Simple retention -> current: N, limit: M, to_delete: K` with a `to_delete` that matches what you expect.

---

### 6. Email Notification Issues

#### Symptom: No email notifications received

First, confirm which delivery method you are using:

```bash
# configs/backup.env
EMAIL_DELIVERY_METHOD=relay   # cloud relay (outbound HTTPS)
# or
EMAIL_DELIVERY_METHOD=sendmail # /usr/sbin/sendmail (local MTA required)
# or
EMAIL_DELIVERY_METHOD=pmf     # Proxmox Notifications via proxmox-mail-forward
```

If Email is enabled but you don't see it being dispatched, ensure `EMAIL_DELIVERY_METHOD` is one of: `pmf`, `relay`, `sendmail` (aliases such as `proxmox-notifications` normalize to `pmf`; typos will skip Email with a warning like: `Email: enabled but not initialized (...)`).

##### If `EMAIL_DELIVERY_METHOD=pmf`

This mode uses Proxmox Notifications via `proxmox-mail-forward`. It is the recommended mode on Proxmox hosts when you expected SMTP settings in ProxSave: configure SMTP targets/matchers in Proxmox, then let ProxSave hand the message to Proxmox.

- `EMAIL_RECIPIENT` is optional in this mode and is only used for the `To:` header.
- If PMF fails, ProxSave tries the shared cloud relay next. That leg is **not** gated on `EMAIL_FALLBACK_SENDMAIL`: it runs whenever the recipient is set and is not a `root@` address, so choosing `pmf` with the flag off does not keep the report inside Proxmox. Only the final local-sendmail leg consults the flag. To avoid the relay entirely use `EMAIL_DELIVERY_METHOD=sendmail`.
- Verify `proxmox-mail-forward` exists:
  ```bash
  test -x /usr/libexec/proxmox-mail-forward && echo "proxmox-mail-forward OK" || echo "proxmox-mail-forward not found"
  ```
- Verify Proxmox Notifications configuration in the UI (`Datacenter -> Notifications` on PVE, or the PBS notification UI/config).
- Direct handoff test:
  ```bash
  printf "To: root\nSubject: proxsave test\n\nHello from proxsave\n" | sudo /usr/libexec/proxmox-mail-forward
  ```

##### If `EMAIL_DELIVERY_METHOD=relay`

- Ensure outbound HTTPS works from the node (the relay needs network access).
- Ensure the recipient is configured:
  - Set `EMAIL_RECIPIENT=...`, or
  - Leave it empty and set an email for `root@pam` inside Proxmox (auto-detect).
- Recipient auto-detection details (when `EMAIL_RECIPIENT` is empty):
  - **PVE**: `pvesh get /access/users/root@pam` → fallback to `pveum user list` → fallback to `/etc/pve/user.cfg`
  - **PBS**: `proxmox-backup-manager user list` → fallback to `/etc/proxmox-backup/user.cfg`
  - **Dual**: intentionally reuses the **PVE** path for `root@pam` email discovery
- Relay blocks `root@...` recipients; use a real non-root mailbox for `EMAIL_RECIPIENT`.
- If `EMAIL_FALLBACK_SENDMAIL=true`, ProxSave will fall back to local `/usr/sbin/sendmail` when relay delivery fails. If relay cannot start because no recipient is available, sendmail cannot help either; configure `EMAIL_RECIPIENT` or the `root@pam` email in Proxmox.
- Check the proxsave logs for `email-relay` warnings/errors.
- `Email relay accepted request ...` means the relay accepted the submission. It does **not** guarantee final inbox delivery; later provider-side failures/bounces are outside the ProxSave process.

Common relay auth/forbidden errors:

- `authentication failed (HTTP 401): missing bearer token`: the relay did not receive the `Authorization: Bearer ...` header.
- `authentication failed (HTTP 401): missing signature`: the relay did not receive the `X-Signature` header.
- `forbidden (HTTP 403): invalid token`: the bearer token is wrong or not allowed by the worker.
- `forbidden (HTTP 403): HMAC signature validation failed`: the request body and `X-Signature` do not match the worker's `HMAC_SECRET`.
- `forbidden (HTTP 403): missing or invalid script version`: the relay rejected `X-Script-Version` (it must be semantic-version-like, e.g. `1.2.3`).
- `forbidden (HTTP 403): from address override not allowed`: the client attempted to override the worker-managed sender address.

If you operate your own relay worker:

- The worker-side env var `MAC_LIMIT_IP_WHITELIST` can bypass the per-server daily MAC quota for trusted source IPs.
- Example: `MAC_LIMIT_IP_WHITELIST=86.56.17.99`
- This bypass affects only the daily MAC quota. It does **not** disable bearer-token checks, HMAC validation, IP burst limits, or token limits.

Quick checks for auto-detect:

```bash
# Run only the relevant block for your platform (PVE vs PBS).

# PVE (preferred: API via pvesh)
pvesh get /access/users/root@pam --output-format json

# PVE (fallback: legacy CLI)
pveum user list --output-format json

# PVE (last resort: config file)
grep -n '^user:root@pam:' /etc/pve/user.cfg

# PBS (CLI)
proxmox-backup-manager user list --output-format json

# PBS (config file)
grep -n '^user:root@pam:' /etc/proxmox-backup/user.cfg
```

##### If `EMAIL_DELIVERY_METHOD=sendmail`

This mode uses `/usr/sbin/sendmail`, so your node must have a working local MTA (e.g. postfix).

- Ensure a recipient is available:
  - Set `EMAIL_RECIPIENT=...`, or
  - Leave it empty and set an email for `root@pam` inside Proxmox (auto-detect).
- If auto-detection fails, run the quick checks above and review proxsave debug logs (they include diagnostic output from failed Proxmox CLI/API calls).
- Verify `sendmail` exists:
  ```bash
  test -x /usr/sbin/sendmail && echo "sendmail OK" || echo "sendmail not found"
  ```
- Check your MTA status and queue (`systemctl status postfix`, `mailq`, `/var/log/mail.log`).

---

#### Warning: `... storage may fail due to insufficient space`

**Cause**: a non-critical destination (secondary or cloud) is below its required free space, so the run warns and carries on. The primary destination is critical instead: if it is short of space the run stops with a disk-space error rather than warning.

**Solution**:
```bash
# The required space is max(MIN_DISK_SPACE_<TIER>_GB, estimated size x SAFETY_FACTOR),
# so the MIN_DISK_SPACE_* keys are a FLOOR, not a warning threshold. Raising one makes
# the check stricter. The template ships 1 GB for the primary tier.
nano configs/backup.env
MIN_DISK_SPACE_SECONDARY_GB=1

# Or free up space on that destination
```

---
### 7. Restore Issues

#### Mountpoint is read-only / `Operation not permitted` after a restore (NFS/storage not restored)

**Symptoms**:
- After a restore, a storage mountpoint (e.g. `/mnt/pve/<id>` or a PBS datastore path) is read-only, or writes fail with `Operation not permitted`.
- An NFS/CIFS/network share "wasn't restored" and the storage shows as unavailable.

**Explanation**:
- The storage *definition* is restored, but the share was offline/unreachable at restore time, so ProxSave applied a **read-only bind-mount guard** on the mountpoint to stop Proxmox from silently writing into the root filesystem (`/`). If the bind mount could not be created, ProxSave instead logged a warning and left the mountpoint unguarded (older versions set a persistent `chattr +i` immutable flag here; that was removed because it survived reboots and could re-block the mountpoint later).
- Bringing the share back online and mounting it is enough to *use* it again (a real mount stacks on top of the guard automatically). The guard is not deleted, only shadowed: a bind-mount guard clears on reboot or via cleanup. A **legacy** `chattr +i` flag (from an older version) persists across reboots until cleared.

**Resolution**:
```bash
# 1. Bring the share online and mount/activate the storage
pvesm status
mount -t nfs <server>:<export> /mnt/pve/<id>   # or: pvesm activate <id>

# 2. Remove the leftover guards (run as root; preview first)
proxsave --cleanup-guards --dry-run
proxsave --cleanup-guards
```
- `--cleanup-guards` unmounts bind-mount guards and clears any **legacy** `chattr +i` flags, but only on mountpoints that are **not currently mounted**; it prints a summary of what was cleared vs left pending. To clear a flag while the storage is mounted: unmount it, run `--cleanup-guards` again (or `chattr -i <mountpoint>`), then remount.
- If you already deleted `/var/lib/proxsave/guards` by hand and a mountpoint is still read-only, ProxSave has no record left to clear. Check for the immutable flag and remove it manually while the storage is unmounted:
```bash
lsattr -d /mnt/pve/<id>        # an 'i' in the flags means immutable
chattr -i /mnt/pve/<id>
```

#### Restore drops SSH / IP changes during network restore

**Symptoms**:
- SSH/Web UI disconnects during restore when the `network` category is applied live
- You see a `NETWORK ROLLBACK` block in the footer (especially after Ctrl+C)

**Explanation**:
- Live network apply can change IP/routes immediately.
- ProxSave protects access by arming a rollback timer that can revert network-related files automatically if `COMMIT` is not received in time.

**What to do**:
- Prefer running restore from the **local console/IPMI**, not over SSH.
- If the footer says **ARMED**, reconnect using the **pre-apply IP** once rollback runs.
- If it says **EXECUTED**, reconnect using the **pre-apply IP** (rollback already ran).
- If it says **DISARMED/CLEARED**, reconnect using the **post-apply IP** (new config remains active).
- Check the rollback log path printed in the footer for details.

#### PBS UI/API fails after restore: `proxmox-backup-proxy` permission denied

**Symptoms**:
- PBS web UI login fails or API requests return authentication errors
- `systemctl status proxmox-backup-proxy` shows a restart loop
- Journal contains errors like:
  - `unable to read "/etc/proxmox-backup/user.cfg" - Permission denied (os error 13)`
  - `unable to read "/etc/proxmox-backup/authkey.pub" - Permission denied (os error 13)`
  - `configuration directory '/etc/proxmox-backup' permission problem`

**Cause**:
- PBS services (notably `proxmox-backup-proxy`) run as user `backup` and require specific ownership/permissions under `/etc/proxmox-backup`.
- If staged restore (or manual file copy) rewrites these files with the wrong owner/group/mode, the services cannot read them and may refuse to start.

**What to do**:
1. Ensure you're running the latest ProxSave build and rerun restore for the staged PBS categories you selected (e.g. `pbs_access_control`, `pbs_jobs`, `pbs_remotes`, `pbs_host`, `pbs_notifications`, `datastore_pbs`). ProxSave applies staged files atomically and enforces final permissions/ownership (not left to `umask`).
2. If PBS is already broken and you need a quick recovery:
   - Identify the blocking path component with `namei -l /etc/proxmox-backup/user.cfg`.
   - Restore package defaults (recommended): reinstall `proxmox-backup-server` and restart services. Example:
     ```bash
     apt-get update
     apt-get install --reinstall proxmox-backup-server
     systemctl restart proxmox-backup proxmox-backup-proxy
     ```
   - Or fix ownership/permissions to match a clean install of your PBS version (verify `/etc/proxmox-backup` and the files referenced in the journal are readable by user `backup`).

#### Error during network preflight: `addr_add_dry_run() got an unexpected keyword argument 'nodad'`

**Symptoms**:
- Restore networking preflight fails when running `ifup -n -a`
- Log contains: `NetlinkListenerWithCache.addr_add_dry_run() got an unexpected keyword argument 'nodad'`

**Cause**:
- A Proxmox-packaged `ifupdown2` version may ship a Python signature mismatch between `addr_add()` and `addr_add_dry_run()` (dry-run path), which crashes `ifup -n` when `nodad` is used.

**What ProxSave does**:
- During restore, ProxSave can apply a guarded hotfix (only when needed) by patching `/usr/share/ifupdown2/lib/nlcache.py` and writing a timestamped `.bak.*` backup first.

**Recovery / rollback**:
- To revert the hotfix, restore the `.bak.*` copy back onto `nlcache.py`, or upgrade `ifupdown2` when Proxmox publishes a fixed build.

---

### 8. Backup Monitoring Issues

The full status vocabulary, both the check-screen keywords and the per-sensor states, is documented in [HEALTHCHECKS.md](HEALTHCHECKS.md#troubleshooting). This section covers the cases that send people looking for a fix.

#### The monitoring check never leaves `PROVISIONING`

**Symptoms**:
- The install screen or the dashboard `Healthchecks` check keeps reporting `PROVISIONING`.

**Cause**:
- Centralized monitoring provisions this host's credential automatically on a daemon run. No Telegram pairing and no API key are involved. A state that never advances means the request is not getting through.

**Resolution**:
```bash
# 1. Is the daemon actually running? It is what provisions.
proxsave --daemon-status
systemctl status proxsave-daemon.service

# 2. Watch it try
journalctl -u proxsave-daemon.service -f
```
- Provisioning retries are throttled to roughly every 15 minutes, so give it one interval before concluding anything.
- If the daemon reports the server is unreachable, this is outbound connectivity from the host, not a ProxSave setting.

#### No monitoring portal link is shown

**Symptoms**:
- The dashboard check or the end of a run shows a `Healthchecks Portal:` address and a `Healthchecks Login:` identity instead of a single-use link, or shows nothing about the portal at all.

**Cause**:
- The server stops minting links once you have set your own portal password. That is the expected steady state, and it is why you see an address plus an identity: sign in there with the password you chose. Opening the link is not what retires it, only choosing a password is.
- If nothing at all is printed, the mint did not succeed. It is best effort and stays quiet when it fails. ProxSave will not fall back to the address form on its own: it shows that only when the server confirms a password exists, so it never sends you to a sign-in page you have no password for.
- A link or address that is not a clean http(s) URL on the monitoring server's own domain is dropped on purpose, so a tampered response cannot show you a phishing address.

**Resolution**:
- If you see the address and identity, sign in with the password you set.
- If you see nothing, open the dashboard `Healthchecks` check again to request a fresh link. Repeat failures mean the monitoring server could not be reached; check outbound connectivity from the host.

#### Every backup reports warnings and the monitor went red right after an upgrade

**Symptoms**:
- Backups that clearly succeeded finish with "completed with warnings", exit `1` instead of `0`, and the `proxsave-backup` check goes down.
- It started right after an upgrade and repeats on every scheduled run.
- The run log carries `ProxSave <version> has unseen release notes. Open proxsave to view the new features.`

**Cause**:
- After a version bump ProxSave has release notes waiting for you. Until someone acknowledges them, every run logs that line at WARNING level. A warning promotes a clean run from exit `0` to exit `1`, and the daemon pings the backup check with that code, so `/1` takes the check down on a backup that was fine.
- It only bites unattended upgrades. `--upgrade` shows the screen for you on an interactive terminal, which clears the flag; an upgrade run from a script or over a pipe skips it.

**Resolution**:
- Run `proxsave --show-whatsnew` once on the host, or open the dashboard and page through the release-notes screen. Either clears the flag and the next run is green again.

#### A backup ran but `proxsave-backup` stayed silent

**Symptoms**:
- You ran a backup by hand or from the dashboard, and the monitor recorded nothing.

**Cause**:
- Only the resident daemon pings. A standalone run hands its outcome off to the daemon, which then pings. The handoff needs a live daemon and is discarded when older than 15 minutes.

**Resolution**:
- Start the daemon before running standalone backups, or let the daemon run the scheduled backup.

#### Self mode: `UNREACHABLE`

**Symptoms**:
- The self-mode check cannot reach your ping URL.

**Resolution**:
```bash
# The check uses /log, which records a ping without changing check state
curl -fsS -X POST "https://your-monitor.example/<uuid>/log" && echo OK
```
- Confirm the URL is the **full** ping URL of that check. When you configure IDs instead, the daemon builds `<HEALTHCHECK_PING_ENDPOINT>/<HEALTHCHECK_PING_KEY>/<id>`, or `<endpoint>/<id>` with no key. A full `*_URL` always wins over the matching `*_ID`.

#### Sensors read `not provisioned` or `transmit failed`

**Symptoms**:
- The `Sensors:` list shows those states instead of `ok`.

**Cause**:
- `not provisioned` means the daemon tried to report but has no URL for that check yet. In centralized mode the URLs are still being fetched; in self mode that check is simply not configured, which is fine if you did not want it.
- `transmit failed` means the ping did not reach the monitor. It is usually transient.

**Resolution**:
- The daemon warns once when it cannot reach the monitor, then drops to debug so a recurring failure does not flood the journal. Raise `DEBUG_LEVEL` to see each attempt.

---

## Debug Procedures

### Enable Debug Logging

```bash
# Run with debug level
proxsave --log-level debug

# Or set in config
nano configs/backup.env
DEBUG_LEVEL=extreme

# Logs include:
# - Detailed command execution
# - rclone stdout/stderr
# - File operations
# - Retry attempts
```

**Debug levels**:

| Level | Detail | Use Case |
|-------|--------|----------|
| `standard` | Basic operations | Normal production |
| `advanced` | Command execution, file ops | Troubleshooting |
| `extreme` | Full verbose, all internals | Deep debugging |

---

### Test rclone Manually

```bash
# Test upload
echo "test" > /tmp/test.txt
rclone copy /tmp/test.txt gdrive:pbs-backups/ --verbose

# Verify
rclone lsl gdrive:pbs-backups/test.txt

# Test download
rclone copy gdrive:pbs-backups/test.txt /tmp/test-download.txt
cat /tmp/test-download.txt

# Cleanup
rclone deletefile gdrive:pbs-backups/test.txt
rm /tmp/test*.txt
```

**Check rclone configuration**:
```bash
# List remotes
rclone listremotes

# Show remote details
rclone config show gdrive

# Test connectivity
rclone about gdrive:
```

---

### Verify Configuration Loading

```bash
# Check parsed configuration
grep -E "^CLOUD_|^RCLONE_" /opt/proxsave/configs/backup.env

# Test with dry-run
proxsave --dry-run --log-level debug
# Check output for loaded config values
```

Dry-run runs the full workflow at the chosen log level but makes no changes (no archive
written, no upload, no retention delete). With `--log-level debug` the loaded
configuration and the actions ProxSave *would* take are written to the log, so use it to
confirm the config parsed as expected before a real run.

---

### Analyze Log Files

```bash
# Find latest log
ls -lt /opt/proxsave/log/

# View log
cat /opt/proxsave/log/backup-$(hostname)-*.log

# Filter errors
grep -i "error\|fail\|warning" /opt/proxsave/log/backup-*.log

# Filter cloud issues
grep -i "cloud.*error\|cloud.*fail\|cloud.*warning" /opt/proxsave/log/backup-*.log
```

**Log analysis patterns**:
```bash
# Check backup duration
grep "Backup completed" /opt/proxsave/log/backup-*.log

# Check compression ratio
grep "Compression" /opt/proxsave/log/backup-*.log

# Check upload speed
grep -i "upload.*speed\|transfer.*rate" /opt/proxsave/log/backup-*.log

# Check retention operations
grep "Retention" /opt/proxsave/log/backup-*.log
```

---

### Test Individual Components

#### Test Compression

```bash
# Test xz compression
echo "test data" > /tmp/test.txt
xz -zv -6 /tmp/test.txt
ls -lh /tmp/test.txt.xz

# Test zstd compression
echo "test data" > /tmp/test2.txt
zstd -v -3 /tmp/test2.txt
ls -lh /tmp/test2.txt.zst
```

#### Test Encryption

```bash
# Generate test key
age-keygen -o /tmp/test-key.txt

# Encrypt test file
echo "sensitive data" | age -r $(grep "public key:" /tmp/test-key.txt | cut -d: -f2) > /tmp/encrypted.age

# Decrypt test file
age --decrypt -i /tmp/test-key.txt /tmp/encrypted.age
```

#### Test Backup Archive Creation

```bash
# Create test TAR archive
tar -czf /tmp/test-backup.tar.gz /etc/hostname /etc/hosts
tar -tzf /tmp/test-backup.tar.gz
```

---

## Getting Help

### Check Documentation

Before reporting issues, review:

- **[README](../README.md)** - Project overview
- **[Configuration Guide](CONFIGURATION.md)** - All config variables
- **[Cloud Storage Guide](CLOUD_STORAGE.md)** - rclone troubleshooting
- **[Encryption Guide](ENCRYPTION.md)** - AGE encryption issues
- **[Restore Guide](RESTORE_GUIDE.md)** - Restore troubleshooting

---

### Enable Verbose Logging

```bash
# Capture full debug output
proxsave --log-level debug 2>&1 | tee /tmp/pbs-debug.log
```

---

### Report Issues

If problem persists:

**1. Gather information**:
```bash
proxsave --version
rclone version
go version
uname -a
```

**2. Collect logs**:
```bash
tar -czf /tmp/pbs-debug.tar.gz \
    /opt/proxsave/log/backup-*.log \
    /tmp/pbs-debug.log
```

**3. Sanitize config** (remove credentials):
```bash
cp configs/backup.env /tmp/backup.env.sanitized
nano /tmp/backup.env.sanitized
# Remove: TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID, EMAIL_*, WEBHOOK_*_URL, AGE_RECIPIENT
```

**4. Create GitHub issue**:
- **Repository**: https://github.com/tis24dev/proxsave/issues
- **Include**:
  - Version information
  - Sanitized configuration
  - Relevant log excerpts
  - Steps to reproduce
- **Describe**:
  - Expected behavior
  - Actual behavior
  - Environment details

**Issue template**:
```markdown
**Version**:
proxsave --version output here

**Environment**:
- OS: Proxmox VE 8.x / PBS 3.x / Debian 12
- Go version:
- rclone version:

**Configuration** (sanitized):
```bash
CLOUD_ENABLED=true
CLOUD_REMOTE=gdrive
CLOUD_REMOTE_PATH=/pbs-backups
COMPRESSION_TYPE=xz
# ... other relevant settings
```text

**Issue description**:
Brief description of the problem...

**Steps to reproduce**:
1. Configure X
2. Run Y
3. Observe error Z

**Expected behavior**:
What should happen...

**Actual behavior**:
What actually happens...

**Logs** (relevant excerpts):
```
[ERROR] Cloud upload failed: connection timeout
...
```text

**Additional context**:
Any other relevant information...
```

---

### Use Support Mode

For complex issues requiring developer assistance:

```bash
# Run in support mode (sends debug log to developer)
proxsave --support
```

**Support mode workflow**:
1. Requests GitHub username and issue number
2. Runs backup with DEBUG logging
3. Emails the log to the maintainer address baked into the build (injected at build time, not hardcoded; a dev build without it skips the email and logs a warning)
4. Requires existing GitHub issue for tracking

**Note**: Logs may contain file paths and hostnames. Credentials are never logged.

---

## Exit Codes

`proxsave` returns a specific exit code so scripts and the daemon can react to the
failure class. Four of them are not failures: `1` is also what a backup that succeeded
with warnings returns, `16` means no backup was performed for a benign reason, `17`
means a guard cleanup ran fine but the storage is still locked, and `130` means the run
was cancelled by hand. Only `2` through `15` are unambiguous failures. A wrapper that
treats "non-zero" as "page someone" raises false alarms on warning-only runs, on
overlapping runs, on paused hosts, on a datastore that is simply still mounted, and on
anything you Ctrl+C.

| Code | Name | Meaning |
|------|------|---------|
| `0` | success | Execution completed successfully |
| `1` | generic error | Unspecified error |
| `2` | configuration error | Invalid or missing configuration |
| `3` | environment error | Invalid or unsupported Proxmox environment |
| `4` | backup error | Error during the backup operation (generic) |
| `5` | storage error | Error during storage operations |
| `6` | network error | Network error (upload, notifications, and so on) |
| `7` | permission error | Permission error |
| `8` | verification error | Integrity verification failed |
| `9` | collection error | Error collecting configuration files |
| `10` | archive error | Error creating the archive |
| `11` | compression error | Error during compression |
| `12` | disk space error | Insufficient disk space |
| `13` | panic error | An unhandled panic was caught |
| `14` | security error | The security check reported errors |
| `15` | encryption error | Error during encryption setup or processing |
| `16` | backup skipped | No backup was performed, for a benign reason: another backup already held the lock, or `BACKUP_ENABLED=false`. Not a failure |
| `17` | guards still in place | `--cleanup-guards` only. The cleanup ran without error but guard mounts or immutable flags are left behind, or the remaining count could not be confirmed. Also returned by `--cleanup-guards --dry-run` when it finds guards. Not a failure: unmount the datastore and retry. A `1` from that mode means the cleanup itself failed |
| `130` | interrupted | The run was cancelled with Ctrl+C (128 plus SIGINT) |

A backup that finishes with warnings (no errors) is promoted from `0` to exit `1`
(generic error) before the notification phase; a run that raised errors becomes `4`
(backup error) when its code is still `0`/`1`, otherwise it keeps its more specific code.

---

## Related Documentation

### Configuration & Setup
- **[Configuration Guide](CONFIGURATION.md)** - Complete variable reference
- **[CLI Reference](CLI_REFERENCE.md)** - All command-line flags

### Operations
- **[Cloud Storage Guide](CLOUD_STORAGE.md)** - rclone configuration and troubleshooting
- **[Encryption Guide](ENCRYPTION.md)** - AGE encryption setup
- **[Restore Guide](RESTORE_GUIDE.md)** - Restore operations
- **[Backup Monitoring](HEALTHCHECKS.md)** - Monitored checks, the monitoring portal, and status vocabulary
- **[Resident Daemon](DAEMON.md)** - Scheduler engines, watchdog, and service management

### Reference
- **[Examples](EXAMPLES.md)** - Real-world scenarios
- **[Developer Guide](DEVELOPER_GUIDE.md)** - Contributing and development

### Main Documentation
- **[README](../README.md)** - Project overview and quick start

---

## Quick Diagnostic Checklist

Use this checklist for rapid troubleshooting:

```bash
# 1. Check binary exists and is executable
ls -lh /opt/proxsave/build/proxsave
# Should show: -rwxr-xr-x ... proxsave

# 2. Check configuration file exists
ls -lh /opt/proxsave/configs/backup.env
# Should show: -rw------- ... backup.env

# 3. Test configuration loading
proxsave --dry-run
# Should NOT error on config parsing

# 4. Check disk space
df -h /opt/proxsave
# Should have >2GB free

# 5. Check permissions
ls -la /opt/proxsave/backup /opt/proxsave/log
# backup: drwxr-xr-x
# log: drwxr-xr-x

# 6. Test rclone (if cloud enabled)
rclone listremotes
rclone lsf gdrive:
# Should list remote without errors

# 7. Check latest log
tail -50 /opt/proxsave/log/backup-*.log
# Review for obvious errors

# 8. Run debug mode
proxsave --dry-run --log-level debug 2>&1 | less
# Review detailed output for issues
```

---

## FAQ - Common Questions

**Q: Backup succeeds but cloud upload fails - is this a problem?**
A: No. Cloud uploads are non-critical. Local backup succeeded, which is the primary goal. Review cloud configuration and retry.

**Q: How do I test my configuration without running a real backup?**
A: Use `--dry-run` mode: `proxsave --dry-run --log-level debug`

**Q: Logs show warnings about deprecated variables?**
A: Update your configuration: `proxsave --upgrade-config`

**Q: Can I run backup while another backup is in progress?**
A: No. Proxsave uses a lock file (`.backup.lock` under `LOCK_PATH`, default `<BASE_DIR>/lock/.backup.lock`) to prevent concurrent runs. The lock stores `pid/host/time`; on the same host, proxsave checks PID liveness to avoid "stuck" locks after an interrupted run.

**Q: Backup hangs during PVE datastore detection when a network storage is unreachable.**
A: Set `FS_IO_TIMEOUT` to cap how long proxsave waits on any individual filesystem syscall (stat/readdir/open/read/write/close/glob/copy/hash) across the preflight, logging, storage, cloud and restore paths, and `PVESH_TIMEOUT` to cap `pvesh` calls. This reduces the likelihood of indefinite hangs when a storage becomes unreachable mid-run.

**Q: How do I recover from a failed backup?**
A: Delete the incomplete backup file and re-run. The system automatically handles cleanup.

**Q: Encryption is slow - how can I speed it up?**
A: AGE encryption is streaming and shouldn't significantly impact speed. Check compression settings instead.

**Q: Cloud upload is very slow - how can I speed it up?**
A: Increase `RCLONE_TRANSFERS`, use `parallel` mode, check network bandwidth, try different compression.
