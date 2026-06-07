# Security

## Threat model

ProxSave is a Proxmox (PVE/PBS) system backup & restore tool that runs **as root**
on the host it protects. Reading and writing arbitrary system paths — `/etc/pve`,
`/etc/proxmox-backup`, `/proc`, the configured backup destinations, and so on — is
its core function, not a vulnerability: an attacker able to influence those paths
would already need root on the host.

The one place where **untrusted content** becomes a filesystem path is **restore
archive extraction**, and that is contained independently:

- extracted entry targets are sanitized against the destination root, and
  symlink/hardlink targets that escape the root are rejected
  (`internal/orchestrator/restore_archive_entries.go`);
- process-owned staging/temp and download/install I/O is confined with `os.Root`
  (`internal/backup/optimizations.go`, `cmd/proxsave/upgrade.go`), so traversal via
  `..` or escaping symlinks fails at the syscall level.

Release artifacts are signed (ECDSA P-256) and the signature is verified by both
the installer and the self-upgrade, in addition to SLSA build-provenance
attestations — see [PROVENANCE_VERIFICATION.md](./PROVENANCE_VERIFICATION.md).

## Static-analysis policy (gosec)

CI runs gosec (`.github/workflows/security-ultimate.yml`). Two rules deserve a note:

- **G305** (archive-extraction traversal / zip-slip) — **kept enabled**; the restore
  extraction containment described above is what satisfies it.
- **G304** ("file path from a variable") — **kept enabled project-wide**, but the
  current findings on the collectors, config readers and backup-output writers are
  **dismissed as false positives** in Code Scanning: those paths are
  tool/system/config-controlled per the threat model. The rule stays enabled so any
  **new** variable-path I/O is still surfaced for review; genuinely contained I/O
  should use `os.Root` rather than a `#nosec` suppression.
