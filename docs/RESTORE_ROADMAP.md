# Restore roadmap (ProxSave)

This document tracks which Proxmox configuration areas are currently restored by ProxSave, and what still remains.

## Implemented

### Common (automatic)
- Filesystem mounts: **Smart `/etc/fstab` merge** (interactive) that adds only safe candidates and normalizes restored entries with `nofail` (and `_netdev` for network mounts). If ProxSave inventory is present in the backup, it can remap unstable `/dev/*` devices (e.g. `/dev/sdb1`) to stable `UUID=`/`PARTUUID=`/`LABEL=` references on the restore host.

### PBS (automatic)
- Datastores: staged apply (`/etc/proxmox-backup/datastore.cfg`) with safety checks. Datastore definitions are applied even if the underlying mount is offline (PBS will show them as unavailable). If a datastore path looks like a mount root but resolves to the root filesystem, ProxSave applies a temporary mount guard to prevent writes to `/`. Definitions are only deferred when the path is invalid or contains unexpected entries; deferred blocks are written to `/tmp/proxsave/datastore.cfg.deferred.*`.
- Host & Integrations: staged apply (`/etc/proxmox-backup/{node,s3,metricserver,traffic-control}.cfg` + `/etc/proxmox-backup/acme/{accounts,plugins}.cfg`).
- Proxy/SSL: restores PBS proxy config (`/etc/proxmox-backup/proxy.cfg`) and TLS assets (`/etc/proxmox-backup/proxy.{pem,key}` and `/etc/proxmox-backup/ssl/`) when the `ssl` and `pbs_host` categories are selected.
- Maintenance: file-based restore (`/etc/proxmox-backup/maintenance.cfg`).
- Jobs: staged apply (`/etc/proxmox-backup/{sync,verification,prune}.cfg`).
- Remotes: staged apply (`/etc/proxmox-backup/remote.cfg`).
- Tape Backup: staged apply (`/etc/proxmox-backup/{tape,tape-job,media-pool}.cfg` + `/etc/proxmox-backup/tape-encryption-keys.json`).
- Notifications (targets/matchers): staged apply (`/etc/proxmox-backup/notifications.cfg` + `/etc/proxmox-backup/notifications-priv.cfg`).
- Access control (users/realms/ACL + secrets): staged apply (`/etc/proxmox-backup/{user,domains,acl,token}.cfg` + `{shadow.json,token.shadow,tfa.json}` when present). Safety rail: `root@pam` is preserved from the fresh install and forced `Admin` on `/` (propagate).
- TFA/WebAuthn note: TFA is restored 1:1 as part of access control, but some methods (notably WebAuthn) may require re-enrollment if the UI origin (FQDN/hostname or port) changes. In CUSTOM mode, if access control is selected without `network`/`ssl`, ProxSave suggests adding them for best compatibility and logs detected WebAuthn-enrolled users.

### PVE (automatic)
- Cluster RECOVERY mode: restores full cluster database (`/var/lib/pve-cluster/config.db`).
- Cluster SAFE mode: exports `/etc/pve` and (optionally) applies datacenter objects via `pvesh`/`pveum`:
  - Resource mappings (`/cluster/mapping/{pci,usb,dir}`): applied via `pvesh` when present in the backup (recommended before VM/CT apply if guests use `mapping=<id>`).
  - Pools (resource pools): applied via `pveum` (merge: create/update definitions and add membership; optional allow-move for guests).
  - VM/CT configs (qemu-server + lxc)
  - `storage.cfg`
  - `datacenter.cfg`
- Offline mount safety: for mount-backed storages in `storage.cfg` (notably `nfs`, `cifs`, `cephfs`, `glusterfs`, and `dir` on dedicated mountpoints), ProxSave applies a temporary mount guard (read-only bind mount; fallback `chattr +i`) when the mountpoint resolves to the root filesystem at restore time (for network storages this is `/mnt/pve/<storageid>`). This prevents accidental writes to `/` while storage is offline.
- Notifications (targets/matchers): staged parse + apply via `pvesh` (SAFE mode).
- Access control (realms/roles/groups/users/ACL + secrets): staged file-based apply to pmxcfs (`/etc/pve/{user,domains}.cfg` + `/etc/pve/priv/{shadow,token,tfa}.cfg`) for full 1:1 restore. Safety rail: `root@pam` is preserved from the fresh install and forced `Administrator` on `/` (propagate). Cluster backups in SAFE mode: applying access control is opt-in (cluster-wide) with a rollback timer.
- Firewall: staged file-based apply to pmxcfs (`/etc/pve/firewall/*` + `/etc/pve/nodes/<node>/host.fw`) with an optional rollback timer to prevent lockouts (apply + commit; auto-rollback if not committed).
- HA: staged file-based apply to pmxcfs (`/etc/pve/ha/{resources,groups,rules}.cfg`) with an optional rollback timer (apply + commit; auto-rollback if not committed). Note: applying HA config can trigger immediate HA actions (start/stop/migrate); rollback restores config files but cannot undo actions already taken by the HA manager.
- SDN: staged file-based apply to pmxcfs (`/etc/pve/sdn/` and `sdn.cfg` when present). Note: restores SDN definitions only; applying network changes remains a separate, explicit SDN step (UI/CLI).
- TFA/WebAuthn note: TFA is restored 1:1 as part of access control, but some methods (notably WebAuthn) may require re-enrollment if the UI origin (FQDN/hostname or port) changes. In CUSTOM mode, if access control is selected without `network`/`ssl`, ProxSave suggests adding them for best compatibility and logs detected WebAuthn-enrolled users.

### Export-only (manual review)
- `pve_config_export`: full `/etc/pve` (never written to system paths).
- `pbs_config`: full `/etc/proxmox-backup` (never written to system paths).

## Known gaps / next candidates

### PBS
- (none currently tracked here)

### PVE
- Replication
