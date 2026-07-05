package installer

import "strings"

// postInstallComponentDescriptions maps a BACKUP_* collector key to a short,
// human explanation of what it captures and when it is safe to disable. It feeds
// the post-install "Unused components" detail pane. Keys not present here fall
// back to the raw dry-run warnings (see PostInstallComponentDescription).
var postInstallComponentDescriptions = map[string]string{
	"BACKUP_APT_SOURCES":            "APT repository sources (/etc/apt/sources.list and sources.list.d). Lets you restore your package repositories. Rarely worth disabling.",
	"BACKUP_CEPH_CONFIG":            "Ceph cluster configuration (/etc/ceph): keyrings and ceph.conf. Only relevant on hyper-converged nodes running Ceph; disable on nodes without Ceph.",
	"BACKUP_CLUSTER_CONFIG":         "Proxmox cluster configuration (corosync + the pmxcfs /etc/pve tree). Only meaningful on clustered nodes; disable on a standalone host.",
	"BACKUP_CRITICAL_FILES":         "A curated set of critical system files (fstab, hosts, resolv.conf and similar). Generally keep enabled.",
	"BACKUP_CRON_JOBS":              "System cron jobs (/etc/crontab, /etc/cron.* and per-user crontabs). Disable if this node has no custom cron jobs.",
	"BACKUP_DATASTORE_CONFIGS":      "Proxmox Backup Server datastore definitions (datastore.cfg). PBS-only; disable on a PVE host.",
	"BACKUP_FIREWALL_RULES":         "Host-level firewall rules. Disable if you do not manage a firewall on this node.",
	"BACKUP_INSTALLED_PACKAGES":     "The list of installed Debian packages, so the package set can be reproduced. Low cost; usually keep it.",
	"BACKUP_KERNEL_MODULES":         "Kernel module configuration (/etc/modules, modprobe.d). Disable if you have no custom module setup.",
	"BACKUP_NETWORK_CONFIGS":        "Network configuration (/etc/network/interfaces and related). Generally keep enabled.",
	"BACKUP_PBS_ACME_ACCOUNTS":      "PBS ACME (Let's Encrypt) account registrations. PBS-only; disable if PBS does not use ACME certificates.",
	"BACKUP_PBS_ACME_PLUGINS":       "PBS ACME DNS-challenge plugin definitions. PBS-only; disable if you do not use ACME DNS plugins.",
	"BACKUP_PBS_METRIC_SERVERS":     "PBS external metric servers (InfluxDB/Graphite). PBS-only; disable if you export no metrics.",
	"BACKUP_PBS_NETWORK_CONFIG":     "PBS-specific network configuration. PBS-only; disable on a PVE host.",
	"BACKUP_PBS_NODE_CONFIG":        "PBS node configuration (node.cfg). PBS-only; disable on a PVE host.",
	"BACKUP_PBS_NOTIFICATIONS":      "PBS notification targets and matchers. PBS-only; disable if PBS sends no notifications.",
	"BACKUP_PBS_NOTIFICATIONS_PRIV": "PBS notification secrets (private tokens for notification targets). PBS-only; disable if you configure no notification credentials.",
	"BACKUP_PBS_S3_ENDPOINTS":       "PBS S3 endpoint definitions (S3-backed datastores). PBS-only; disable if you use no S3 backend.",
	"BACKUP_PBS_TRAFFIC_CONTROL":    "PBS traffic-control (bandwidth-limit) rules. PBS-only; disable if you set no traffic limits.",
	"BACKUP_PRUNE_SCHEDULES":        "PBS prune schedules (retention). PBS-only; disable on a PVE host.",
	"BACKUP_PVE_ACL":                "Proxmox VE access-control lists: users, roles and permissions. PVE-only; keep it if you manage PVE users.",
	"BACKUP_PVE_BACKUP_FILES":       "The actual guest backup archives (vzdump .vma/.tar dumps) found on PVE storages. PVE-only; disable to skip copying large guest backup files (see MAX_PVE_BACKUP_SIZE).",
	"BACKUP_PVE_FIREWALL":           "Proxmox VE firewall configuration (cluster, host and guest levels). PVE-only; disable if the PVE firewall is unused.",
	"BACKUP_PVE_JOBS":               "Proxmox VE scheduled jobs (jobs.cfg). PVE-only; disable if you have no scheduled jobs.",
	"BACKUP_PVE_REPLICATION":        "Proxmox VE storage-replication configuration. PVE-only; disable if you use no replication.",
	"BACKUP_PVE_SCHEDULES":          "Proxmox VE job schedules. PVE-only; disable if you schedule no PVE jobs.",
	"BACKUP_REMOTE_CONFIGS":         "PBS remote definitions (remote.cfg) used as sync sources. PBS-only; disable if you configure no remotes.",
	"BACKUP_ROOT_HOME":              "The contents of root's home directory (/root). Disable if you keep nothing important there.",
	"BACKUP_SMALL_PVE_BACKUPS":      "Small PVE guest backups captured inline. PVE-only; disable if you do not want inline guest backups.",
	"BACKUP_SSH_KEYS":               "SSH host keys and authorized_keys. Keep enabled unless you regenerate keys on restore.",
	"BACKUP_SSL_CERTS":              "SSL/TLS certificates and keys used by the node's services. Generally keep enabled.",
	"BACKUP_SYNC_JOBS":              "PBS sync jobs (pull/push between datastores/remotes). PBS-only; disable if you run no sync jobs.",
	"BACKUP_SYSCTL_CONFIG":          "Kernel sysctl parameters (/etc/sysctl.conf and sysctl.d). Disable if you have no custom tuning.",
	"BACKUP_SYSTEMD_SERVICES":       "Custom systemd unit files. Disable if this node has no custom services.",
	"BACKUP_TAPE_CONFIGS":           "PBS tape-backup configuration (drives, changers, media pools). PBS-only; disable if you use no tape backend.",
	"BACKUP_USER_CONFIGS":           "PBS access control: user.cfg (users), acl.cfg (permissions), domains.cfg (auth realms) plus their credential files (token.cfg, shadow.json, TFA). PBS-only; disable only if you never need to restore PBS users/ACLs/credentials.",
	"BACKUP_VERIFICATION_JOBS":      "PBS verification jobs (datastore integrity checks). PBS-only; disable if you schedule no verification.",
	"BACKUP_VM_CONFIGS":             "Proxmox VE guest configuration files (VM/CT .conf). PVE-only; keep it if you run guests on this node.",
	"BACKUP_VZDUMP_CONFIG":          "The vzdump default configuration (/etc/vzdump.conf). PVE-only; disable if you never customized vzdump.",
	"BACKUP_ZFS_CONFIG":             "ZFS configuration (pool cache and /etc/zfs). Disable if this node uses no ZFS.",
}

// PostInstallComponentDescription returns a curated human description for a
// BACKUP_* component key (case-insensitive), or "" when none is catalogued (the
// caller then falls back to the raw dry-run warnings).
func PostInstallComponentDescription(key string) string {
	return postInstallComponentDescriptions[strings.ToUpper(strings.TrimSpace(key))]
}
