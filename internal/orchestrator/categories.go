package orchestrator

import (
	"path"
	"path/filepath"
	"strings"
)

// CategoryType represents the type of category
type CategoryType string

const (
	CategoryTypePVE    CategoryType = "pve"
	CategoryTypePBS    CategoryType = "pbs"
	CategoryTypeCommon CategoryType = "common"
)

// Category represents a backup category with its metadata
type Category struct {
	ID          string       // Unique identifier
	Name        string       // Display name
	Description string       // User-friendly description
	Type        CategoryType // PVE, PBS, or Common
	Paths       []string     // File/directory paths in the archive
	IsAvailable bool         // Whether this category is present in the backup
	ExportOnly  bool         // If true, never restored directly to system paths
}

// RestoreMode represents the pre-defined restore modes
type RestoreMode string

const (
	RestoreModeFull    RestoreMode = "full"
	RestoreModeStorage RestoreMode = "storage"
	RestoreModeBase    RestoreMode = "base"
	RestoreModeCustom  RestoreMode = "custom"
)

// GetAllCategories returns all available categories
func GetAllCategories() []Category {
	return []Category{
		{
			ID:          "pve_config_export",
			Name:        "PVE Config Export",
			Description: "Export-only copy of /etc/pve (never written to system paths)",
			Type:        CategoryTypePVE,
			Paths: []string{
				"./etc/pve/",
				"./etc/pve/jobs.cfg",
				"./etc/pve/vzdump.cron",
			},
			ExportOnly: true,
		},
		// PVE Categories
		{
			ID:          "pve_cluster",
			Name:        "PVE Cluster Configuration",
			Description: "Proxmox VE cluster configuration and database",
			Type:        CategoryTypePVE,
			Paths: []string{
				"./var/lib/pve-cluster/",
			},
		},
		{
			ID:          "storage_pve",
			Name:        "PVE Storage Configuration",
			Description: "Storage definitions (applied via API) and VZDump configuration",
			Type:        CategoryTypePVE,
			Paths: []string{
				"./etc/pve/storage.cfg",
				"./etc/pve/datacenter.cfg",
				"./etc/vzdump.conf",
			},
		},
		{
			ID:          "pve_jobs",
			Name:        "PVE Backup Jobs",
			Description: "Scheduled backup job definitions (applied via API)",
			Type:        CategoryTypePVE,
			Paths: []string{
				"./etc/pve/jobs.cfg",
				"./etc/pve/vzdump.cron",
			},
		},
		{
			ID:          "pve_notifications",
			Name:        "PVE Notifications",
			Description: "Datacenter notification targets and matchers (applied via API)",
			Type:        CategoryTypePVE,
			Paths: []string{
				"./etc/pve/notifications.cfg",
				"./etc/pve/priv/notifications.cfg",
			},
		},
		{
			ID:          "pve_access_control",
			Name:        "PVE Access Control",
			Description: "Users, roles, groups, ACLs and realms (applied via API)",
			Type:        CategoryTypePVE,
			Paths: []string{
				"./etc/pve/user.cfg",
				"./etc/pve/domains.cfg",
				"./etc/pve/priv/shadow.cfg",
				"./etc/pve/priv/token.cfg",
				"./etc/pve/priv/tfa.cfg",
			},
		},
		{
			ID:          "pve_firewall",
			Name:        "PVE Firewall",
			Description: "Firewall rules and options (staged; applied with rollback safety)",
			Type:        CategoryTypePVE,
			Paths: []string{
				"./etc/pve/firewall/",
				"./etc/pve/nodes/*/host.fw",
			},
		},
		{
			ID:          "pve_ha",
			Name:        "PVE High Availability (HA)",
			Description: "HA resources/groups/rules (staged; applied with rollback safety)",
			Type:        CategoryTypePVE,
			Paths: []string{
				"./etc/pve/ha/resources.cfg",
				"./etc/pve/ha/groups.cfg",
				"./etc/pve/ha/rules.cfg",
			},
		},
		{
			ID:          "pve_sdn",
			Name:        "PVE SDN",
			Description: "Software-defined networking configuration (staged; applied to pmxcfs)",
			Type:        CategoryTypePVE,
			Paths: []string{
				"./etc/pve/sdn/",
				"./etc/pve/sdn.cfg",
			},
		},
		{
			ID:          "corosync",
			Name:        "Corosync Configuration",
			Description: "Cluster communication and quorum settings",
			Type:        CategoryTypePVE,
			Paths: []string{
				"./etc/corosync/",
			},
		},
		{
			ID:          "ceph",
			Name:        "Ceph Configuration",
			Description: "Ceph storage cluster configuration",
			Type:        CategoryTypePVE,
			Paths: []string{
				"./etc/ceph/",
			},
		},

		// PBS Categories
		{
			ID:          "pbs_config",
			Name:        "PBS Config Export",
			Description: "Export-only copy of /etc/proxmox-backup (never written to system paths)",
			Type:        CategoryTypePBS,
			Paths: []string{
				"./etc/proxmox-backup/",
			},
			ExportOnly: true,
		},
		{
			ID:          "pbs_host",
			Name:        "PBS Host & Integrations",
			Description: "Node settings, ACME configuration, proxy, external metric servers and traffic control rules",
			Type:        CategoryTypePBS,
			Paths: []string{
				"./etc/proxmox-backup/node.cfg",
				"./etc/proxmox-backup/proxy.cfg",
				"./etc/proxmox-backup/acme/accounts.cfg",
				"./etc/proxmox-backup/acme/plugins.cfg",
				"./etc/proxmox-backup/metricserver.cfg",
				"./etc/proxmox-backup/traffic-control.cfg",
			},
		},
		{
			ID:          "datastore_pbs",
			Name:        "PBS Datastore Configuration",
			Description: "Datastore definitions and settings (including S3 endpoint definitions)",
			Type:        CategoryTypePBS,
			Paths: []string{
				"./etc/proxmox-backup/datastore.cfg",
				"./etc/proxmox-backup/s3.cfg",
			},
		},
		{
			ID:          "maintenance_pbs",
			Name:        "PBS Maintenance",
			Description: "Maintenance settings (restore only if environment matches)",
			Type:        CategoryTypePBS,
			Paths: []string{
				"./etc/proxmox-backup/maintenance.cfg",
			},
		},
		{
			ID:          "pbs_jobs",
			Name:        "PBS Jobs",
			Description: "Sync, verify, and prune job configurations",
			Type:        CategoryTypePBS,
			Paths: []string{
				"./etc/proxmox-backup/sync.cfg",
				"./etc/proxmox-backup/verification.cfg",
				"./etc/proxmox-backup/prune.cfg",
			},
		},
		{
			ID:          "pbs_remotes",
			Name:        "PBS Remotes",
			Description: "Remote definitions for sync/verify jobs (may include credentials)",
			Type:        CategoryTypePBS,
			Paths: []string{
				"./etc/proxmox-backup/remote.cfg",
			},
		},
		{
			ID:          "pbs_notifications",
			Name:        "PBS Notifications",
			Description: "Notification targets and matchers",
			Type:        CategoryTypePBS,
			Paths: []string{
				"./etc/proxmox-backup/notifications.cfg",
				"./etc/proxmox-backup/notifications-priv.cfg",
			},
		},
		{
			ID:          "pbs_access_control",
			Name:        "PBS Access Control",
			Description: "Users, realms and permissions",
			Type:        CategoryTypePBS,
			Paths: []string{
				"./etc/proxmox-backup/user.cfg",
				"./etc/proxmox-backup/domains.cfg",
				"./etc/proxmox-backup/acl.cfg",
				"./etc/proxmox-backup/token.cfg",
				"./etc/proxmox-backup/shadow.json",
				"./etc/proxmox-backup/token.shadow",
				"./etc/proxmox-backup/tfa.json",
			},
		},
		{
			ID:          "pbs_tape",
			Name:        "PBS Tape Backup",
			Description: "Tape jobs, pools, changers and tape encryption keys",
			Type:        CategoryTypePBS,
			Paths: []string{
				"./etc/proxmox-backup/tape.cfg",
				"./etc/proxmox-backup/tape-job.cfg",
				"./etc/proxmox-backup/media-pool.cfg",
				"./etc/proxmox-backup/tape-encryption-keys.json",
			},
		},

		// Common Categories
		{
			ID:          "filesystem",
			Name:        "Filesystem Configuration",
			Description: "Mount points and filesystems (/etc/fstab) - WARNING: Critical for boot",
			Type:        CategoryTypeCommon,
			Paths: []string{
				"./etc/fstab",
			},
		},
		{
			ID:          "storage_stack",
			Name:        "Storage Stack (Mounts/Targets)",
			Description: "Storage stack configuration used by mounts (iSCSI/LVM/MDADM/multipath/autofs/crypttab)",
			Type:        CategoryTypeCommon,
			Paths: []string{
				"./etc/crypttab",
				"./etc/iscsi/",
				"./var/lib/iscsi/",
				"./etc/multipath/",
				"./etc/multipath.conf",
				"./etc/mdadm/",
				"./etc/lvm/backup/",
				"./etc/lvm/archive/",
				"./etc/autofs.conf",
				"./etc/auto.master",
				"./etc/auto.master.d/",
				"./etc/auto.*",
			},
		},
		{
			ID:          "network",
			Name:        "Network Configuration",
			Description: "Network interfaces and routing",
			Type:        CategoryTypeCommon,
			Paths: []string{
				"./etc/network/",
				"./etc/netplan/",
				"./etc/systemd/network/",
				"./etc/NetworkManager/system-connections/",
				"./etc/hosts",
				"./etc/hostname",
				"./etc/resolv.conf",
				"./etc/cloud/cloud.cfg.d/99-disable-network-config.cfg",
				"./etc/dnsmasq.d/lxc-vmbr1.conf",
			},
		},
		{
			ID:          "ssl",
			Name:        "SSL Certificates",
			Description: "SSL/TLS certificates and private keys",
			Type:        CategoryTypeCommon,
			Paths: []string{
				"./etc/ssl/",
				"./etc/proxmox-backup/proxy.pem",
				"./etc/proxmox-backup/proxy.key",
				"./etc/proxmox-backup/ssl/",
			},
		},
		{
			ID:          "ssh",
			Name:        "SSH Configuration",
			Description: "SSH keys and authorized_keys",
			Type:        CategoryTypeCommon,
			Paths: []string{
				"./root/.ssh/",
				"./etc/ssh/",
			},
		},
		{
			ID:          "scripts",
			Name:        "Custom Scripts",
			Description: "User scripts and custom tools",
			Type:        CategoryTypeCommon,
			Paths: []string{
				"./usr/local/bin/",
				"./usr/local/sbin/",
			},
		},
		{
			ID:          "crontabs",
			Name:        "Scheduled Tasks",
			Description: "Cron jobs and systemd timers",
			Type:        CategoryTypeCommon,
			Paths: []string{
				"./etc/cron.d/",
				"./etc/crontab",
				"./var/spool/cron/",
			},
		},
		{
			ID:          "services",
			Name:        "System Services",
			Description: "Systemd service configurations",
			Type:        CategoryTypeCommon,
			Paths: []string{
				"./etc/systemd/system/",
				"./etc/default/",
				"./etc/udev/rules.d/",
				"./etc/apt/",
				"./etc/logrotate.d/",
				"./etc/timezone",
				"./etc/sysctl.conf",
				"./etc/sysctl.d/",
				"./etc/modprobe.d/",
				"./etc/modules",
				"./etc/iptables/",
				"./etc/nftables.conf",
				"./etc/nftables.d/",
			},
		},
		{
			ID:          "user_data",
			Name:        "User Data (Home Directories)",
			Description: "Root and user home directories (/root and /home)",
			Type:        CategoryTypeCommon,
			Paths: []string{
				"./root/",
				"./home/",
			},
		},
		{
			ID:          "zfs",
			Name:        "ZFS Configuration",
			Description: "ZFS pool cache and configuration files",
			Type:        CategoryTypeCommon,
			Paths: []string{
				"./etc/zfs/",
				"./etc/hostid",
			},
		},
		{
			ID:          "proxsave_info",
			Name:        "ProxSave Diagnostics (Export Only)",
			Description: "ProxSave command outputs and inventory reports (export-only; never written to system paths)",
			Type:        CategoryTypeCommon,
			Paths: []string{
				"./var/lib/proxsave-info/",
				"./manifest.json",
			},
			ExportOnly: true,
		},
	}
}

// GetPVECategories returns only PVE-specific categories
func GetPVECategories() []Category {
	all := GetAllCategories()
	var pve []Category
	for _, cat := range all {
		if cat.Type == CategoryTypePVE {
			pve = append(pve, cat)
		}
	}
	return pve
}

// GetPBSCategories returns only PBS-specific categories
func GetPBSCategories() []Category {
	all := GetAllCategories()
	var pbs []Category
	for _, cat := range all {
		if cat.Type == CategoryTypePBS {
			pbs = append(pbs, cat)
		}
	}
	return pbs
}

// GetCommonCategories returns categories common to both PVE and PBS
func GetCommonCategories() []Category {
	all := GetAllCategories()
	var common []Category
	for _, cat := range all {
		if cat.Type == CategoryTypeCommon {
			common = append(common, cat)
		}
	}
	return common
}

// GetCategoriesForSystem returns categories appropriate for the system type
func GetCategoriesForSystem(systemType string) []Category {
	all := GetAllCategories()
	var categories []Category

	for _, cat := range all {
		if systemType == "pve" {
			// PVE system: include PVE and common categories
			if cat.Type == CategoryTypePVE || cat.Type == CategoryTypeCommon {
				categories = append(categories, cat)
			}
		} else if systemType == "pbs" {
			// PBS system: include PBS and common categories
			if cat.Type == CategoryTypePBS || cat.Type == CategoryTypeCommon {
				categories = append(categories, cat)
			}
		}
	}

	return categories
}

// PathMatchesCategory checks if a given file path matches any path in a category
func PathMatchesCategory(filePath string, category Category) bool {
	// Normalize the file path
	normalized := filePath
	if !strings.HasPrefix(normalized, "./") && !strings.HasPrefix(normalized, "../") {
		normalized = "./" + normalized
	}
	normalized = filepath.ToSlash(normalized)

	for _, catPath := range category.Paths {
		if catPath == "" {
			continue
		}
		normalizedCat := catPath
		if !strings.HasPrefix(normalizedCat, "./") && !strings.HasPrefix(normalizedCat, "../") {
			normalizedCat = "./" + normalizedCat
		}
		normalizedCat = filepath.ToSlash(normalizedCat)

		if strings.ContainsAny(normalizedCat, "*?[") && !strings.HasSuffix(normalizedCat, "/") {
			if ok, err := path.Match(normalizedCat, normalized); err == nil && ok {
				return true
			}
		}
		// Check for exact match
		if normalized == normalizedCat {
			return true
		}

		// Check if the file is under a category directory
		if strings.HasSuffix(normalizedCat, "/") {
			// Handle directory path both with and without trailing slash
			dirPath := strings.TrimSuffix(normalizedCat, "/")
			if normalized == dirPath {
				return true
			}

			if strings.HasPrefix(normalized, normalizedCat) {
				return true
			}
		}
	}

	return false
}

// GetSelectedPaths returns all paths from selected categories
func GetSelectedPaths(categories []Category) []string {
	pathMap := make(map[string]bool)

	for _, cat := range categories {
		for _, path := range cat.Paths {
			pathMap[path] = true
		}
	}

	var paths []string
	for path := range pathMap {
		paths = append(paths, path)
	}

	return paths
}

// GetCategoryByID finds a category by its ID
func GetCategoryByID(id string, categories []Category) *Category {
	for i := range categories {
		if categories[i].ID == id {
			return &categories[i]
		}
	}
	return nil
}

// GetStorageModeCategories returns categories for storage/datastore mode
func GetStorageModeCategories(systemType string) []Category {
	all := GetAllCategories()
	var categories []Category

	if systemType == "pve" {
		// PVE: cluster + storage + jobs + zfs + filesystem + storage stack
		for _, cat := range all {
			if cat.ID == "pve_cluster" || cat.ID == "storage_pve" || cat.ID == "pve_jobs" || cat.ID == "zfs" || cat.ID == "filesystem" || cat.ID == "storage_stack" {
				categories = append(categories, cat)
			}
		}
	} else if systemType == "pbs" {
		// PBS: config export + datastore + maintenance + jobs + remotes + zfs + filesystem + storage stack
		for _, cat := range all {
			if cat.ID == "pbs_config" || cat.ID == "datastore_pbs" || cat.ID == "maintenance_pbs" || cat.ID == "pbs_jobs" || cat.ID == "pbs_remotes" || cat.ID == "zfs" || cat.ID == "filesystem" || cat.ID == "storage_stack" {
				categories = append(categories, cat)
			}
		}
	}

	return categories
}

// GetBaseModeCategories returns categories for system base mode
func GetBaseModeCategories() []Category {
	all := GetAllCategories()
	var categories []Category

	// Base mode: network, SSL, SSH, services, filesystem
	for _, cat := range all {
		if cat.ID == "network" || cat.ID == "ssl" || cat.ID == "ssh" || cat.ID == "services" || cat.ID == "filesystem" {
			categories = append(categories, cat)
		}
	}

	return categories
}
