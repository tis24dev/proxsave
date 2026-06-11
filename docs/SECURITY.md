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
- **G101** ("potential hardcoded credentials") — **kept enabled**. With one
  intentional exception (below), every current finding is a **name-only false
  positive**: a constant or identifier whose name matches `token`/`secret`/`salt`
  but whose value is a file path (`/etc/pve/priv/token.cfg`), a KDF
  domain-separation salt (public by construction), a token *name* (`backup@pbs!go-client`,
  not its secret), or an enum id (`pbs_runtime_access_user_tokens`). These are
  dismissed as false positives in Code Scanning rather than renamed, since the names
  are correct as written.

### Hardcoded relay credential (G101)

The cloud email relay ships a hardcoded `WorkerToken` and `HMACSecret`
(`notify.DefaultCloudRelayConfig`, mirrored in `config.applyDefaults`). These are a
**shared, public anti-abuse credential**, not a confidential secret: the same value
is compiled into every distributed binary and published in this open-source
repository, so it cannot be kept secret on the client side. The token name is
literally `v1_public_...`. It only gates the free shared relay worker, whose real
protection is server-side (rate limiting, per-request `server_mac`/`server_id`); it
grants no access to user data or the host. The two sites carry a `#nosec G101`
documenting this, and the Code Scanning alert is dismissed accordingly. Operators
who want a private relay can point `CLOUDFLARE_*` / the relay config at their own
worker with their own secret.
