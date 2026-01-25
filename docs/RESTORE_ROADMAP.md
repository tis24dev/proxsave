# Restore roadmap (ProxSave)

This document tracks which Proxmox configuration areas are currently restored by ProxSave, and what still remains.

## Implemented

### PBS (automatic)
- Datastores: staged apply (`/etc/proxmox-backup/datastore.cfg`) with safety checks (may defer unsafe definitions).
- Maintenance: file-based restore (`/etc/proxmox-backup/maintenance.cfg`).
- Jobs: staged apply (`/etc/proxmox-backup/{sync,verification,prune}.cfg`).
- Remotes: staged apply (`/etc/proxmox-backup/remote.cfg`).
- Notifications (targets/matchers): staged apply (`/etc/proxmox-backup/notifications.cfg` + `/etc/proxmox-backup/notifications-priv.cfg`).
- Access control (users/realms/ACL + secrets): staged apply (`/etc/proxmox-backup/{user,domains,acl,token}.cfg` + `{shadow.json,token.shadow,tfa.json}` when present).

### PVE (automatic)
- Cluster RECOVERY mode: restores full cluster database (`/var/lib/pve-cluster/config.db`).
- Cluster SAFE mode: exports `/etc/pve` and (optionally) applies via `pvesh`:
  - VM/CT configs (qemu-server + lxc)
  - `storage.cfg`
  - `datacenter.cfg`
- Notifications (targets/matchers): staged parse + apply via `pvesh` (SAFE mode).
- Access control (realms/roles/groups/users/ACL): staged parse + apply via `pvesh` (SAFE mode), with regenerated local user passwords (`*@pve`) and API token secrets (report file written with mode `0600`).

### Export-only (manual review)
- `pve_config_export`: full `/etc/pve` (never written to system paths).
- `pbs_config`: full `/etc/proxmox-backup` (never written to system paths).

## Known gaps / next candidates

### PBS
- S3 Endpoints
- Traffic Control
- Tape configuration
- Proxy/SSL handling remains intentionally guarded (regenerate vs restore policy)

### PVE
- Access control secrets in SAFE mode: passwords/tokens are regenerated and recorded; exact 1:1 import of password hashes/token secrets is not possible via API (full fidelity requires cluster RECOVERY via `config.db`). TFA enrollment secrets still require re-enrollment after restore.
- Firewall
- SDN
- HA
- Replication
- Pools and other datacenter-wide objects exposed via `pvesh`
