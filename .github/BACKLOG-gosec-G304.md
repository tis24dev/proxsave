# Backlog: remaining gosec G304 findings

`G304` ("potential file inclusion via variable") fires wherever a path that is not a
compile-time constant reaches `os.Open`/`os.ReadFile`. 30 sites remain, across
16 files. They are pre-existing, not introduced by the retention work.

`internal/storage` is already clear, and the fix used there is the one to repeat:

```go
file, err := safefs.OpenFileUnderRoot(path, os.O_RDONLY, 0)
```

`safefs.OpenFileUnderRoot` opens through an `*os.Root` on the path's parent directory,
so the confinement holds at the syscall level and a final component that is an absolute
symlink - or one escaping that directory - is refused rather than followed. That answers
the finding structurally. **`#nosec` is not an accepted resolution here.**

Two reasons each site still needs its own judgement rather than a blanket sweep:

- it requires read access to the parent directory, not just execute/search, which not
  every caller has;
- it refuses an absolute-symlink final component that `os.Open` would have followed. On a
  Proxmox host that is a real pattern (`/etc/ceph/ceph.conf` -> `/etc/pve/ceph.conf`), and
  those callers want `safefs.ReadFileInRoot` instead, which re-anchors the target.

## Sites

| File | Lines |
|---|---|
| `internal/backup/checksum.go` | 198, 317 |
| `internal/backup/collector.go` | 79 |
| `internal/backup/collector_network_inventory.go` | 256 |
| `internal/backup/collector_pbs.go` | 768, 785, 799 |
| `internal/backup/collector_pbs_datastore_inventory.go` | 454, 607 |
| `internal/backup/collector_pbs_notifications_summary.go` | 130 |
| `internal/backup/collector_pve.go` | 1946, 1995, 2078, 2399 |
| `internal/backup/collector_system.go` | 1138 |
| `internal/config/config.go` | 1498, 1691, 1803 |
| `internal/config/upgrade.go` | 162 |
| `internal/identity/identity.go` | 1153 |
| `internal/metrics/prometheus.go` | 78 |
| `internal/notify/email.go` | 1255, 704 |
| `internal/orchestrator/deps.go` | 89, 90, 92, 94 |
| `internal/security/procscan.go` | 34, 40 |
| `internal/security/security.go` | 739 |

Regenerate with:

```bash
gosec -include=G304 -quiet ./internal/... ./cmd/...
```
