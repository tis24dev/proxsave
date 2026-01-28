package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (c *Collector) detectZFSUsage() (bool, string) {
	var indicators []string

	// Strong indicator: ZFS filesystems currently mounted.
	if data, err := os.ReadFile(c.systemPath("/proc/mounts")); err == nil && len(data) > 0 {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[2] == "zfs" {
				indicators = append(indicators, "mounted_zfs")
				break
			}
		}
	}

	// Strong indicator: ZFS pool cache exists.
	if _, err := os.Stat(c.systemPath("/etc/zfs/zpool.cache")); err == nil {
		indicators = append(indicators, "zpool_cache")
	}

	// Medium indicator: fstab references ZFS.
	if data, err := os.ReadFile(c.systemPath("/etc/fstab")); err == nil && len(data) > 0 {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[2] == "zfs" {
				indicators = append(indicators, "fstab_zfs")
				break
			}
		}
	}

	// Strong indicator (PVE): storage.cfg references zfspool.
	if data, err := os.ReadFile(c.systemPath("/etc/pve/storage.cfg")); err == nil && len(data) > 0 {
		if strings.Contains(strings.ToLower(string(data)), "zfspool") {
			indicators = append(indicators, "pve_storage_zfspool")
		}
	}

	if len(indicators) == 0 {
		return false, "none"
	}
	return true, strings.Join(indicators, ",")
}

// CollectSystemInfo collects common system information (both PVE and PBS)
func (c *Collector) CollectSystemInfo(ctx context.Context) error {
	c.logger.Info("Collecting system information")
	c.logger.Debug("Preparing filesystem context for system collection (tempDir=%s)", c.tempDir)

	ensureSystemPath()
	c.logger.Debug("System PATH verified for command execution")

	// Collect system directories
	c.logger.Debug("Collecting system directories (network, apt, cron, services, ssl, kernel, firewall, etc.)")
	if err := c.collectSystemDirectories(ctx); err != nil {
		return fmt.Errorf("failed to collect system directories: %w", err)
	}
	c.logger.Debug("System directories collection completed")

	// Collect system commands output
	c.logger.Debug("Collecting system command outputs and runtime state")
	if err := c.collectSystemCommands(ctx); err != nil {
		return fmt.Errorf("failed to collect system commands: %w", err)
	}
	c.logger.Debug("System command collection completed")

	// Collect kernel information
	c.logger.Debug("Collecting kernel information (uname/modules)")
	if err := c.collectKernelInfo(ctx); err != nil {
		c.logger.Warning("Failed to collect kernel info: %v", err)
		// Non-fatal, continue
	} else {
		c.logger.Debug("Kernel information collected successfully")
	}

	// Collect hardware information
	c.logger.Debug("Collecting hardware inventory (CPU/memory/devices)")
	if err := c.collectHardwareInfo(ctx); err != nil {
		c.logger.Warning("Failed to collect hardware info: %v", err)
		// Non-fatal, continue
	} else {
		c.logger.Debug("Hardware inventory collected successfully")
	}

	if c.config.BackupCriticalFiles {
		c.logger.Debug("Collecting critical files specified in configuration")
		if err := c.collectCriticalFiles(ctx); err != nil {
			c.logger.Warning("Failed to collect critical files: %v", err)
		} else {
			c.logger.Debug("Critical files collected successfully")
		}
	}

	if c.config.BackupConfigFile {
		c.logger.Debug("Collecting backup configuration file")
		if err := c.collectConfigFile(ctx); err != nil {
			c.logger.Warning("Failed to collect backup configuration file: %v", err)
		} else {
			c.logger.Debug("Backup configuration file collected successfully")
		}
	}

	if len(c.config.CustomBackupPaths) > 0 {
		c.logger.Debug("Collecting custom paths: %v", c.config.CustomBackupPaths)
		if err := c.collectCustomPaths(ctx); err != nil {
			c.logger.Warning("Failed to collect custom paths: %v", err)
		} else {
			c.logger.Debug("Custom paths collected successfully")
		}
	}

	if c.config.BackupScriptDir {
		c.logger.Debug("Collecting script directories (/usr/local/bin,/usr/local/sbin)")
		if err := c.collectScriptDirectories(ctx); err != nil {
			c.logger.Warning("Failed to collect script directories: %v", err)
		} else {
			c.logger.Debug("Script directories collected successfully")
		}
	}

	if c.config.BackupScriptRepository {
		c.logger.Debug("Collecting script repository from %s", c.config.ScriptRepositoryPath)
		if err := c.collectScriptRepository(ctx); err != nil {
			c.logger.Warning("Failed to collect script repository: %v", err)
		} else {
			c.logger.Debug("Script repository collected successfully")
		}
	}

	if c.config.BackupSSHKeys {
		c.logger.Debug("Collecting SSH keys for root and users")
		if err := c.collectSSHKeys(ctx); err != nil {
			c.logger.Warning("Failed to collect SSH keys: %v", err)
		} else {
			c.logger.Debug("SSH keys collected successfully")
		}
	}

	if c.config.BackupRootHome {
		c.logger.Debug("Collecting /root home directory")
		if err := c.collectRootHome(ctx); err != nil {
			c.logger.Warning("Failed to collect root home files: %v", err)
		} else {
			c.logger.Debug("Root home directory collected successfully")
		}
	}

	if c.config.BackupUserHomes {
		c.logger.Debug("Collecting user home directories under /home")
		if err := c.collectUserHomes(ctx); err != nil {
			c.logger.Warning("Failed to collect user home directories: %v", err)
		} else {
			c.logger.Debug("User home directories collected successfully")
		}
	}

	c.logger.Info("System information collection completed")
	return nil
}

// collectSystemDirectories collects system configuration directories
func (c *Collector) collectSystemDirectories(ctx context.Context) error {
	c.logger.Debug("Collecting system directories into %s", c.tempDir)
	// Network configuration
	if c.config.BackupNetworkConfigs {
		c.logger.Debug("Collecting network configuration files (/etc/network/*)")
		if err := c.safeCopyFile(ctx,
			c.systemPath("/etc/network/interfaces"),
			filepath.Join(c.tempDir, "etc/network/interfaces"),
			"Network interfaces"); err != nil {
			c.logger.Debug("No /etc/network/interfaces found")
		}

		// Additional network configs
		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/network/interfaces.d"),
			filepath.Join(c.tempDir, "etc/network/interfaces.d"),
			"Network interfaces.d"); err != nil {
			c.logger.Debug("No /etc/network/interfaces.d found")
		}

		// Additional network-related overrides frequently used on Proxmox hosts
		extraNetworkFiles := []struct {
			path string
			desc string
		}{
			{"/etc/cloud/cloud.cfg.d/99-disable-network-config.cfg", "Cloud-init network override"},
			{"/etc/dnsmasq.d/lxc-vmbr1.conf", "LXC bridge DNSMasq configuration"},
		}
		for _, file := range extraNetworkFiles {
			if err := c.safeCopyFile(ctx,
				c.systemPath(file.path),
				filepath.Join(c.tempDir, strings.TrimPrefix(file.path, "/")),
				file.desc); err != nil {
				c.logger.Debug("Failed to collect %s: %v", file.path, err)
			}
		}

		// Netplan configs (if present)
		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/netplan"),
			filepath.Join(c.tempDir, "etc/netplan"),
			"Netplan configuration"); err != nil {
			c.logger.Debug("No /etc/netplan found")
		}

		// systemd-networkd configs (if present)
		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/systemd/network"),
			filepath.Join(c.tempDir, "etc/systemd/network"),
			"systemd-networkd configuration"); err != nil {
			c.logger.Debug("No /etc/systemd/network found")
		}

		// NetworkManager connections (if present)
		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/NetworkManager/system-connections"),
			filepath.Join(c.tempDir, "etc/NetworkManager/system-connections"),
			"NetworkManager connections"); err != nil {
			c.logger.Debug("No NetworkManager system-connections found")
		}
	}

	// Hostname and hosts
	c.logger.Debug("Collecting hostname/hosts information")
	if err := c.safeCopyFile(ctx,
		c.systemPath("/etc/hostname"),
		filepath.Join(c.tempDir, "etc/hostname"),
		"Hostname"); err != nil {
		c.logger.Debug("No /etc/hostname found")
	}

	if err := c.safeCopyFile(ctx,
		c.systemPath("/etc/hosts"),
		filepath.Join(c.tempDir, "etc/hosts"),
		"Hosts file"); err != nil {
		c.logger.Debug("No /etc/hosts found")
	}

	// DNS configuration
	c.logger.Debug("Collecting DNS resolver configuration")
	if err := c.safeCopyFile(ctx,
		c.systemPath("/etc/resolv.conf"),
		filepath.Join(c.tempDir, "etc/resolv.conf"),
		"DNS resolver"); err != nil {
		c.logger.Debug("No /etc/resolv.conf found")
	}

	// Timezone configuration
	c.logger.Debug("Collecting timezone configuration")
	if err := c.safeCopyFile(ctx,
		c.systemPath("/etc/timezone"),
		filepath.Join(c.tempDir, "etc/timezone"),
		"Timezone configuration"); err != nil {
		c.logger.Debug("No /etc/timezone found")
	}

	// Apt sources
	if c.config.BackupAptSources {
		c.logger.Debug("Collecting APT sources and authentication data")
		if err := c.safeCopyFile(ctx,
			c.systemPath("/etc/apt/sources.list"),
			filepath.Join(c.tempDir, "etc/apt/sources.list"),
			"APT sources"); err != nil {
			c.logger.Debug("No /etc/apt/sources.list found")
		}

		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/apt/sources.list.d"),
			filepath.Join(c.tempDir, "etc/apt/sources.list.d"),
			"APT sources.list.d"); err != nil {
			c.logger.Debug("No /etc/apt/sources.list.d found")
		}

		// APT preferences
		if err := c.safeCopyFile(ctx,
			c.systemPath("/etc/apt/preferences"),
			filepath.Join(c.tempDir, "etc/apt/preferences"),
			"APT preferences"); err != nil {
			c.logger.Debug("No /etc/apt/preferences found")
		}

		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/apt/preferences.d"),
			filepath.Join(c.tempDir, "etc/apt/preferences.d"),
			"APT preferences.d"); err != nil {
			c.logger.Debug("No /etc/apt/preferences.d found")
		}

		// APT authentication keys
		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/apt/trusted.gpg.d"),
			filepath.Join(c.tempDir, "etc/apt/trusted.gpg.d"),
			"APT GPG keys"); err != nil {
			c.logger.Debug("No /etc/apt/trusted.gpg.d found")
		}

		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/apt/apt.conf.d"),
			filepath.Join(c.tempDir, "etc/apt/apt.conf.d"),
			"APT apt.conf.d"); err != nil {
			c.logger.Debug("No /etc/apt/apt.conf.d found")
		}

		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/apt/auth.conf.d"),
			filepath.Join(c.tempDir, "etc/apt/auth.conf.d"),
			"APT auth.conf.d"); err != nil {
			c.logger.Debug("No /etc/apt/auth.conf.d found")
		}

		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/apt/keyrings"),
			filepath.Join(c.tempDir, "etc/apt/keyrings"),
			"APT keyrings"); err != nil {
			c.logger.Debug("No /etc/apt/keyrings found")
		}

		if err := c.safeCopyFile(ctx,
			c.systemPath("/etc/apt/listchanges.conf"),
			filepath.Join(c.tempDir, "etc/apt/listchanges.conf"),
			"APT listchanges.conf"); err != nil {
			c.logger.Debug("No /etc/apt/listchanges.conf found")
		}

		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/apt/listchanges.conf.d"),
			filepath.Join(c.tempDir, "etc/apt/listchanges.conf.d"),
			"APT listchanges.conf.d"); err != nil {
			c.logger.Debug("No /etc/apt/listchanges.conf.d found")
		}
	}

	// Cron jobs
	if c.config.BackupCronJobs {
		c.logger.Debug("Collecting cron definitions (system and per-user)")
		if err := c.safeCopyFile(ctx,
			c.systemPath("/etc/crontab"),
			filepath.Join(c.tempDir, "etc/crontab"),
			"System crontab"); err != nil {
			c.logger.Debug("No /etc/crontab found")
		}

		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/cron.d"),
			filepath.Join(c.tempDir, "etc/cron.d"),
			"Cron.d directory"); err != nil {
			c.logger.Debug("No /etc/cron.d found")
		}

		// Cron scripts directories
		cronDirs := []string{
			"/etc/cron.daily",
			"/etc/cron.hourly",
			"/etc/cron.monthly",
			"/etc/cron.weekly",
		}
		for _, dir := range cronDirs {
			if err := c.safeCopyDir(ctx, c.systemPath(dir),
				filepath.Join(c.tempDir, dir[1:]), // Remove leading /
				filepath.Base(dir)); err != nil {
				c.logger.Debug("No %s found", dir)
			}
		}

		// Per-user crontabs
		if err := c.safeCopyDir(ctx,
			c.systemPath("/var/spool/cron/crontabs"),
			filepath.Join(c.tempDir, "var/spool/cron/crontabs"),
			"User crontabs"); err != nil {
			c.logger.Debug("No user crontabs found")
		}
	}

	// Systemd services
	if c.config.BackupSystemdServices {
		c.logger.Debug("Collecting systemd unit definitions")
		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/systemd/system"),
			filepath.Join(c.tempDir, "etc/systemd/system"),
			"Systemd services"); err != nil {
			c.logger.Debug("No /etc/systemd/system found")
		}
	}

	// SSL certificates
	if c.config.BackupSSLCerts {
		c.logger.Debug("Collecting SSL certificates and keys")
		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/ssl/certs"),
			filepath.Join(c.tempDir, "etc/ssl/certs"),
			"SSL certificates"); err != nil {
			c.logger.Debug("No /etc/ssl/certs found")
		}

		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/ssl/private"),
			filepath.Join(c.tempDir, "etc/ssl/private"),
			"SSL private keys"); err != nil {
			c.logger.Debug("No /etc/ssl/private found")
		}

		if err := c.safeCopyFile(ctx,
			c.systemPath("/etc/ssl/openssl.cnf"),
			filepath.Join(c.tempDir, "etc/ssl/openssl.cnf"),
			"OpenSSL configuration"); err != nil {
			c.logger.Debug("No /etc/ssl/openssl.cnf found")
		}
	}

	// Sysctl configuration
	if c.config.BackupSysctlConfig {
		c.logger.Debug("Collecting sysctl configuration files")
		if err := c.safeCopyFile(ctx,
			c.systemPath("/etc/sysctl.conf"),
			filepath.Join(c.tempDir, "etc/sysctl.conf"),
			"Sysctl configuration"); err != nil {
			c.logger.Debug("No /etc/sysctl.conf found")
		}

		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/sysctl.d"),
			filepath.Join(c.tempDir, "etc/sysctl.d"),
			"Sysctl.d directory"); err != nil {
			c.logger.Debug("No /etc/sysctl.d found")
		}
	}

	// Kernel modules
	if c.config.BackupKernelModules {
		c.logger.Debug("Collecting kernel module configuration")
		if err := c.safeCopyFile(ctx,
			c.systemPath("/etc/modules"),
			filepath.Join(c.tempDir, "etc/modules"),
			"Kernel modules"); err != nil {
			c.logger.Debug("No /etc/modules found")
		}

		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/modprobe.d"),
			filepath.Join(c.tempDir, "etc/modprobe.d"),
			"Modprobe.d directory"); err != nil {
			c.logger.Debug("No /etc/modprobe.d found")
		}
	}

	// ZFS configuration files
	if c.config.BackupZFSConfig {
		c.logger.Debug("Collecting ZFS configuration (/etc/zfs, /etc/hostid)")
		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/zfs"),
			filepath.Join(c.tempDir, "etc/zfs"),
			"ZFS configuration"); err != nil {
			c.logger.Warning("Failed to collect /etc/zfs: %v", err)
		}

		if err := c.safeCopyFile(ctx,
			c.systemPath("/etc/hostid"),
			filepath.Join(c.tempDir, "etc/hostid"),
			"ZFS host identifier"); err != nil {
			c.logger.Warning("Failed to collect /etc/hostid: %v", err)
		}
	}

	// Firewall rules (iptables/nftables)
	if c.config.BackupFirewallRules {
		c.logger.Debug("Collecting firewall rules (/etc/iptables, nftables)")
		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/iptables"),
			filepath.Join(c.tempDir, "etc/iptables"),
			"iptables rules"); err != nil {
			c.logger.Debug("No /etc/iptables found")
		}

		if err := c.safeCopyDir(ctx,
			c.systemPath("/etc/nftables.d"),
			filepath.Join(c.tempDir, "etc/nftables.d"),
			"nftables rules"); err != nil {
			c.logger.Debug("No /etc/nftables.d found")
		}

		if err := c.safeCopyFile(ctx,
			c.systemPath("/etc/nftables.conf"),
			filepath.Join(c.tempDir, "etc/nftables.conf"),
			"nftables configuration"); err != nil {
			c.logger.Debug("No /etc/nftables.conf found")
		}
	}

	// Logrotate configuration
	if err := c.safeCopyDir(ctx,
		c.systemPath("/etc/logrotate.d"),
		filepath.Join(c.tempDir, "etc/logrotate.d"),
		"logrotate configuration"); err != nil {
		c.logger.Debug("No /etc/logrotate.d found")
	}

	// DHCP leases (best effort)
	if err := c.safeCopyDir(ctx,
		c.systemPath("/var/lib/dhcp"),
		c.proxsaveRuntimeDir("var/lib/dhcp"),
		"DHCP leases (runtime snapshot)"); err != nil {
		c.logger.Debug("No /var/lib/dhcp found")
	}
	if err := c.safeCopyDir(ctx,
		c.systemPath("/var/lib/NetworkManager"),
		c.proxsaveRuntimeDir("var/lib/NetworkManager"),
		"NetworkManager leases (runtime snapshot)"); err != nil {
		c.logger.Debug("No /var/lib/NetworkManager leases found")
	}
	if err := c.safeCopyDir(ctx,
		c.systemPath("/run/systemd/netif/leases"),
		c.proxsaveRuntimeDir("run/systemd/netif/leases"),
		"systemd-networkd leases (runtime snapshot)"); err != nil {
		c.logger.Debug("No /run/systemd/netif/leases found")
	}

	c.logger.Debug("System directories collected")
	return nil
}

// collectSystemCommands collects output from system commands
func (c *Collector) collectSystemCommands(ctx context.Context) error {
	commandsDir := c.proxsaveCommandsDir("system")
	if err := c.ensureDir(commandsDir); err != nil {
		return fmt.Errorf("failed to create commands directory: %w", err)
	}
	c.logger.Debug("Collecting system command outputs into %s", commandsDir)

	// OS release information (CRITICAL)
	osReleasePath := c.systemPath("/etc/os-release")
	if err := c.collectCommandMulti(ctx,
		fmt.Sprintf("cat %s", osReleasePath),
		filepath.Join(commandsDir, "os_release.txt"),
		"OS release",
		true); err != nil {
		return fmt.Errorf("failed to get OS release (critical): %w", err)
	}

	// Kernel version (CRITICAL)
	if err := c.collectCommandMulti(ctx,
		"uname -a",
		filepath.Join(commandsDir, "uname.txt"),
		"Kernel version",
		true); err != nil {
		return fmt.Errorf("failed to get kernel version (critical): %w", err)
	}

	// Hostname
	c.safeCmdOutput(ctx,
		"hostname -f",
		filepath.Join(commandsDir, "hostname.txt"),
		"Hostname",
		false)

	// IP addresses
	if err := c.collectCommandMulti(ctx,
		"ip addr show",
		filepath.Join(commandsDir, "ip_addr.txt"),
		"IP addresses",
		false); err != nil {
		return err
	}
	c.collectCommandOptional(ctx,
		"ip -j addr show",
		filepath.Join(commandsDir, "ip_addr.json"),
		"IP addresses (json)")

	// Policy routing rules
	if err := c.collectCommandMulti(ctx,
		"ip rule show",
		filepath.Join(commandsDir, "ip_rule.txt"),
		"IP rules",
		false); err != nil {
		return err
	}
	c.collectCommandOptional(ctx,
		"ip -j rule show",
		filepath.Join(commandsDir, "ip_rule.json"),
		"IP rules (json)")

	// IP routes
	if err := c.collectCommandMulti(ctx,
		"ip route show",
		filepath.Join(commandsDir, "ip_route.txt"),
		"IP routes",
		false); err != nil {
		return err
	}
	c.collectCommandOptional(ctx,
		"ip -j route show",
		filepath.Join(commandsDir, "ip_route.json"),
		"IP routes (json)")

	// All routing tables (IPv4/IPv6)
	c.collectCommandOptional(ctx,
		"ip -4 route show table all",
		filepath.Join(commandsDir, "ip_route_all_v4.txt"),
		"IP routes (all tables v4)")
	c.collectCommandOptional(ctx,
		"ip -6 route show table all",
		filepath.Join(commandsDir, "ip_route_all_v6.txt"),
		"IP routes (all tables v6)")

	// IP link statistics
	c.collectCommandOptional(ctx,
		"ip -s link",
		filepath.Join(commandsDir, "ip_link.txt"),
		"IP link statistics")
	c.collectCommandOptional(ctx,
		"ip -j link",
		filepath.Join(commandsDir, "ip_link.json"),
		"IP links (json)")

	// Neighbors (ARP/NDP)
	c.safeCmdOutput(ctx,
		"ip neigh show",
		filepath.Join(commandsDir, "ip_neigh.txt"),
		"Neighbor table",
		false)
	c.safeCmdOutput(ctx,
		"ip -6 neigh show",
		filepath.Join(commandsDir, "ip6_neigh.txt"),
		"Neighbor table (IPv6)",
		false)

	// Bridge/VLAN/FDB/MDB state
	c.collectCommandOptional(ctx,
		"bridge -d link show",
		filepath.Join(commandsDir, "bridge_link.txt"),
		"Bridge links")
	c.collectCommandOptional(ctx,
		"bridge vlan show",
		filepath.Join(commandsDir, "bridge_vlan.txt"),
		"Bridge VLANs")
	c.collectCommandOptional(ctx,
		"bridge fdb show",
		filepath.Join(commandsDir, "bridge_fdb.txt"),
		"Bridge FDB")
	c.collectCommandOptional(ctx,
		"bridge mdb show",
		filepath.Join(commandsDir, "bridge_mdb.txt"),
		"Bridge MDB")

	if err := c.collectNetworkInventory(ctx, commandsDir, ""); err != nil {
		c.logger.Debug("Network inventory collection failed: %v", err)
	}

	// Bonding status (/proc/net/bonding/*)
	if entries, err := os.ReadDir(c.systemPath("/proc/net/bonding")); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			src := c.systemPath(filepath.Join("/proc/net/bonding", entry.Name()))
			dest := filepath.Join(commandsDir, "bonding_"+entry.Name()+".txt")
			if err := c.safeCopyFile(ctx, src, dest, "Bonding status"); err != nil && !errors.Is(err, os.ErrNotExist) {
				c.logger.Debug("Failed to copy bonding status for %s: %v", entry.Name(), err)
			}
		}
	} else {
		c.logger.Debug("No bonding interfaces found")
	}

	// DNS resolver
	resolvPath := c.systemPath("/etc/resolv.conf")
	c.safeCmdOutput(ctx,
		fmt.Sprintf("cat %s", resolvPath),
		filepath.Join(commandsDir, "resolv_conf.txt"),
		"DNS configuration",
		false)

	// Disk usage
	if err := c.collectCommandMulti(ctx,
		"df -h",
		filepath.Join(commandsDir, "df.txt"),
		"Disk usage",
		false); err != nil {
		return err
	}

	// Mounted filesystems
	c.safeCmdOutput(ctx,
		"mount",
		filepath.Join(commandsDir, "mount.txt"),
		"Mounted filesystems",
		false)

	// Block devices
	if err := c.collectCommandMulti(ctx,
		"lsblk -f",
		filepath.Join(commandsDir, "lsblk.txt"),
		"Block devices",
		false); err != nil {
		return err
	}

	// Block devices (JSON) - used for stable device mapping during restore (fstab remap).
	c.collectCommandOptional(ctx,
		"lsblk -J -O",
		filepath.Join(commandsDir, "lsblk_json.json"),
		"Block devices (JSON)")

	// Block device identifiers (UUID/PARTUUID/LABEL) - used for stable device mapping during restore.
	c.collectCommandOptional(ctx,
		"blkid",
		filepath.Join(commandsDir, "blkid.txt"),
		"Block device identifiers (blkid)")

	// Memory information
	if err := c.collectCommandMulti(ctx,
		"free -h",
		filepath.Join(commandsDir, "free.txt"),
		"Memory usage",
		false); err != nil {
		return err
	}

	// CPU information
	if err := c.collectCommandMulti(ctx,
		"lscpu",
		filepath.Join(commandsDir, "lscpu.txt"),
		"CPU information",
		false); err != nil {
		return err
	}

	// PCI devices
	if err := c.collectCommandMulti(ctx,
		"lspci -v",
		filepath.Join(commandsDir, "lspci.txt"),
		"PCI devices",
		false); err != nil {
		return err
	}

	// USB devices
	c.safeCmdOutput(ctx,
		"lsusb",
		filepath.Join(commandsDir, "lsusb.txt"),
		"USB devices",
		false)

	// Systemd services status
	if c.config.BackupSystemdServices {
		if err := c.collectCommandMulti(ctx,
			"systemctl list-units --type=service --all",
			filepath.Join(commandsDir, "systemctl_services.txt"),
			"Systemd services",
			false); err != nil {
			return err
		}

		c.safeCmdOutput(ctx, "systemctl list-unit-files --type=service",
			filepath.Join(commandsDir, "systemctl_service_files.txt"),
			"Systemd service files", false)
	}

	// Installed packages
	if c.config.BackupInstalledPackages {
		packagesDir := filepath.Join(commandsDir, "packages")
		if err := c.ensureDir(packagesDir); err != nil {
			return fmt.Errorf("failed to create packages directory: %w", err)
		}

		if err := c.collectCommandMulti(ctx,
			"dpkg -l",
			filepath.Join(packagesDir, "dpkg_list.txt"),
			"Installed packages",
			false); err != nil {
			return err
		}
	}

	// APT policy
	if c.config.BackupAptSources {
		c.safeCmdOutput(ctx,
			"apt-cache policy",
			filepath.Join(commandsDir, "apt_policy.txt"),
			"APT policy",
			false)
	}

	// Firewall status
	if c.config.BackupFirewallRules {
		if err := c.collectCommandMulti(ctx,
			"iptables-save",
			filepath.Join(commandsDir, "iptables.txt"),
			"iptables rules",
			false); err != nil {
			return err
		}

		c.collectCommandOptional(ctx,
			"iptables -t nat -vnL --line-numbers",
			filepath.Join(commandsDir, "iptables_nat.txt"),
			"iptables NAT table")

		// ip6tables
		if err := c.collectCommandMulti(ctx,
			"ip6tables-save",
			filepath.Join(commandsDir, "ip6tables.txt"),
			"ip6tables rules",
			false); err != nil {
			return err
		}

		c.collectCommandOptional(ctx,
			"ip6tables -t nat -vnL --line-numbers",
			filepath.Join(commandsDir, "ip6tables_nat.txt"),
			"ip6tables NAT table")

		// nftables
		c.safeCmdOutput(ctx,
			"nft list ruleset",
			filepath.Join(commandsDir, "nftables.txt"),
			"nftables rules",
			false)

		// UFW status
		c.collectCommandOptional(ctx,
			"ufw status verbose",
			filepath.Join(commandsDir, "ufw_status.txt"),
			"UFW status")

		// firewalld status
		c.collectCommandOptional(ctx,
			"firewall-cmd --state",
			filepath.Join(commandsDir, "firewalld_state.txt"),
			"firewalld state")
		c.collectCommandOptional(ctx,
			"firewall-cmd --list-all",
			filepath.Join(commandsDir, "firewalld_list_all.txt"),
			"firewalld rules")

		// Service state for ufw/firewalld (best effort)
		c.collectCommandOptional(ctx,
			"systemctl status --no-pager ufw",
			filepath.Join(commandsDir, "systemctl_ufw.txt"),
			"systemctl ufw")
		c.collectCommandOptional(ctx,
			"systemctl status --no-pager firewalld",
			filepath.Join(commandsDir, "systemctl_firewalld.txt"),
			"systemctl firewalld")
	}

	// Loaded kernel modules
	if c.config.BackupKernelModules {
		c.safeCmdOutput(ctx,
			"lsmod",
			filepath.Join(commandsDir, "lsmod.txt"),
			"Loaded kernel modules",
			false)
	}

	// Sysctl values
	if c.config.BackupSysctlConfig {
		c.safeCmdOutput(ctx,
			"sysctl -a",
			filepath.Join(commandsDir, "sysctl.txt"),
			"Sysctl values",
			false)
	}

	// ZFS pools (if ZFS is present)
	if c.config.BackupZFSConfig {
		usesZFS, indicators := c.detectZFSUsage()
		c.logger.Debug("ZFS usage detected=%t (indicators=%s)", usesZFS, indicators)
		if !usesZFS {
			c.logger.Warning("Skipping ZFS collection: not detected. Set BACKUP_ZFS_CONFIG=false to disable.")
		} else {
			zfsDir := filepath.Join(commandsDir, "zfs")
			if err := c.ensureDir(zfsDir); err != nil {
				return fmt.Errorf("failed to create zfs info directory: %w", err)
			}

			if _, err := c.depLookPath("zpool"); err == nil {
				c.collectCommandOptional(ctx,
					"zpool status",
					filepath.Join(zfsDir, "zpool_status.txt"),
					"ZFS pool status")

				c.collectCommandOptional(ctx,
					"zpool list",
					filepath.Join(zfsDir, "zpool_list.txt"),
					"ZFS pool list")
			}

			if _, err := c.depLookPath("zfs"); err == nil {
				c.collectCommandOptional(ctx,
					"zfs list",
					filepath.Join(zfsDir, "zfs_list.txt"),
					"ZFS filesystem list")

				c.collectCommandOptional(ctx,
					"zfs get all",
					filepath.Join(zfsDir, "zfs_get_all.txt"),
					"ZFS properties",
				)
			}
		}
	}

	// LVM information
	if _, err := c.depStat(c.systemPath("/sbin/pvs")); err == nil {
		c.safeCmdOutput(ctx,
			"pvs",
			filepath.Join(commandsDir, "lvm_pvs.txt"),
			"LVM physical volumes",
			false)

		c.safeCmdOutput(ctx,
			"vgs",
			filepath.Join(commandsDir, "lvm_vgs.txt"),
			"LVM volume groups",
			false)

		c.safeCmdOutput(ctx,
			"lvs",
			filepath.Join(commandsDir, "lvm_lvs.txt"),
			"LVM logical volumes",
			false)
	}

	c.logger.Debug("System command output collection finished")
	if err := c.buildNetworkReport(ctx, commandsDir); err != nil {
		c.logger.Debug("Network report generation failed: %v", err)
	}
	return nil
}

// buildNetworkReport composes a single human-readable network report by aggregating
// key command outputs and configuration files.
func (c *Collector) buildNetworkReport(ctx context.Context, commandsDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	reportPath := filepath.Join(commandsDir, "network_report.txt")

	var b strings.Builder
	now := time.Now().Format(time.RFC3339)
	hostname, _ := os.Hostname()
	b.WriteString("Proxsave Network Report\n")
	b.WriteString(fmt.Sprintf("Timestamp: %s\n", now))
	b.WriteString(fmt.Sprintf("Hostname: %s\n", hostname))
	b.WriteString("\n")

	appendFile := func(title, path string) {
		if path == "" {
			return
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			return
		}
		b.WriteString(fmt.Sprintf("## %s (%s)\n", title, path))
		b.Write(data)
		if !strings.HasSuffix(string(data), "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	appendGlob := func(title, pattern string) {
		matches, err := filepath.Glob(pattern)
		if err != nil || len(matches) == 0 {
			return
		}
		for _, m := range matches {
			appendFile(title, m)
		}
	}

	// Config files (best effort)
	appendFile("interfaces", c.systemPath("/etc/network/interfaces"))
	appendGlob("interfaces.d", filepath.Join(c.systemPath("/etc/network/interfaces.d"), "*"))
	appendFile("hostname", c.systemPath("/etc/hostname"))
	appendFile("hosts", c.systemPath("/etc/hosts"))
	appendFile("resolv.conf", c.systemPath("/etc/resolv.conf"))
	appendGlob("netplan", filepath.Join(c.systemPath("/etc/netplan"), "*.yaml"))
	appendGlob("systemd-networkd", filepath.Join(c.systemPath("/etc/systemd/network"), "*.network"))
	appendGlob("systemd-networkd", filepath.Join(c.systemPath("/etc/systemd/network"), "*.netdev"))
	appendGlob("systemd-networkd", filepath.Join(c.systemPath("/etc/systemd/network"), "*.link"))
	appendGlob("NetworkManager connection", filepath.Join(c.systemPath("/etc/NetworkManager/system-connections"), "*"))

	// Command outputs already collected
	commandFiles := []struct {
		title string
		name  string
	}{
		{"IP addresses", "ip_addr.txt"},
		{"IP routes", "ip_route.txt"},
		{"IP routes (all tables v4)", "ip_route_all_v4.txt"},
		{"IP routes (all tables v6)", "ip_route_all_v6.txt"},
		{"IP rules", "ip_rule.txt"},
		{"IP links (stats)", "ip_link.txt"},
		{"Network inventory", "network_inventory.json"},
		{"Neighbors (ARP/NDP)", "ip_neigh.txt"},
		{"Neighbors (IPv6)", "ip6_neigh.txt"},
		{"Bridge links", "bridge_link.txt"},
		{"Bridge VLANs", "bridge_vlan.txt"},
		{"Bridge FDB", "bridge_fdb.txt"},
		{"Bridge MDB", "bridge_mdb.txt"},
		{"Bonding status", "bonding.txt"},
		{"iptables-save", "iptables.txt"},
		{"iptables NAT table", "iptables_nat.txt"},
		{"ip6tables-save", "ip6tables.txt"},
		{"ip6tables NAT table", "ip6tables_nat.txt"},
		{"nftables ruleset", "nftables.txt"},
		{"UFW status", "ufw_status.txt"},
		{"firewalld state", "firewalld_state.txt"},
		{"firewalld rules", "firewalld_list_all.txt"},
		{"systemctl ufw", "systemctl_ufw.txt"},
		{"systemctl firewalld", "systemctl_firewalld.txt"},
	}

	for _, cf := range commandFiles {
		appendFile(cf.title, filepath.Join(commandsDir, cf.name))
	}

	// Bonding: include each collected bonding_* file
	if entries, err := os.ReadDir(commandsDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if strings.HasPrefix(name, "bonding_") {
				appendFile("Bonding status", filepath.Join(commandsDir, name))
			}
		}
	}

	reportData := []byte(b.String())
	if len(reportData) == 0 {
		return nil
	}

	if err := c.writeReportFile(reportPath, reportData); err != nil {
		return err
	}
	return nil
}

func ensureSystemPath() {
	current := os.Getenv("PATH")
	if current == "" {
		current = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}

	segments := strings.Split(current, string(os.PathListSeparator))
	seen := make(map[string]struct{}, len(segments))
	filtered := make([]string, 0, len(segments))

	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if _, ok := seen[seg]; ok {
			continue
		}
		seen[seg] = struct{}{}
		filtered = append(filtered, seg)
	}

	extras := []string{"/usr/local/sbin", "/usr/sbin", "/sbin"}
	for _, extra := range extras {
		if _, ok := seen[extra]; !ok {
			filtered = append(filtered, extra)
			seen[extra] = struct{}{}
		}
	}

	_ = os.Setenv("PATH", strings.Join(filtered, string(os.PathListSeparator)))
}

// collectKernelInfo collects kernel-specific information
func (c *Collector) collectKernelInfo(ctx context.Context) error {
	commandsDir := c.proxsaveCommandsDir("system")
	c.logger.Debug("Collecting kernel information into %s", commandsDir)

	// Kernel command line
	c.safeCmdOutput(ctx,
		fmt.Sprintf("cat %s", c.systemPath("/proc/cmdline")),
		filepath.Join(commandsDir, "kernel_cmdline.txt"),
		"Kernel command line",
		false)

	// Kernel version details
	c.safeCmdOutput(ctx,
		fmt.Sprintf("cat %s", c.systemPath("/proc/version")),
		filepath.Join(commandsDir, "kernel_version.txt"),
		"Kernel version details",
		false)

	c.logger.Debug("Kernel information snapshot completed")
	return nil
}

// collectHardwareInfo collects hardware information
func (c *Collector) collectHardwareInfo(ctx context.Context) error {
	commandsDir := c.proxsaveCommandsDir("system")
	c.logger.Debug("Collecting hardware inventory into %s", commandsDir)

	// DMI decode (requires root)
	c.safeCmdOutput(ctx,
		"dmidecode",
		filepath.Join(commandsDir, "dmidecode.txt"),
		"Hardware DMI information",
		false)

	// Hardware sensors (if available)
	if _, err := c.depStat(c.systemPath("/usr/bin/sensors")); err == nil {
		c.safeCmdOutput(ctx,
			"sensors",
			filepath.Join(commandsDir, "sensors.txt"),
			"Hardware sensors",
			false)
	}

	// SMART status for disks (if available)
	if _, err := c.depStat(c.systemPath("/usr/sbin/smartctl")); err == nil {
		// Get list of disks
		c.safeCmdOutput(ctx,
			"smartctl --scan",
			filepath.Join(commandsDir, "smartctl_scan.txt"),
			"SMART scan",
			false)
	}

	c.logger.Debug("Hardware information snapshot completed")
	return nil
}

func (c *Collector) collectCriticalFiles(ctx context.Context) error {
	c.logger.Debug("Collecting critical files (passwd/shadow/fstab/etc.)")
	criticalFiles := []string{
		"/etc/fstab",
		"/etc/crypttab",
		"/etc/passwd",
		"/etc/group",
		"/etc/shadow",
		"/etc/gshadow",
		"/etc/sudoers",
	}

	for _, file := range criticalFiles {
		if err := ctx.Err(); err != nil {
			return err
		}
		dest := filepath.Join(c.tempDir, strings.TrimPrefix(file, "/"))
		if err := c.safeCopyFile(ctx, c.systemPath(file), dest, fmt.Sprintf("critical file %s", filepath.Base(file))); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.logger.Debug("Failed to copy critical file %s: %v", file, err)
		}
	}

	c.logger.Debug("Critical file collection completed")
	return nil
}

func (c *Collector) collectConfigFile(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	configPath := strings.TrimSpace(c.config.ConfigFilePath)
	if configPath == "" {
		c.logger.Debug("Config file path not provided; skipping configuration file collection")
		return nil
	}

	dest := filepath.Join(c.tempDir, strings.TrimPrefix(configPath, "/"))
	src := configPath
	if filepath.IsAbs(src) {
		src = c.systemPath(src)
	}
	if err := c.safeCopyFile(ctx, src, dest, "backup configuration file"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}

func (c *Collector) collectCustomPaths(ctx context.Context) error {
	c.logger.Debug("Collecting custom paths defined in configuration")
	seen := make(map[string]struct{})

	for _, rawPath := range c.config.CustomBackupPaths {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := strings.TrimSpace(rawPath)
		if path == "" {
			continue
		}

		logicalPath := path
		if !filepath.IsAbs(logicalPath) {
			logicalPath = filepath.Join("/", path)
		}
		logicalPath = filepath.Clean(logicalPath)

		if _, ok := seen[logicalPath]; ok {
			continue
		}
		seen[logicalPath] = struct{}{}

		physicalPath := c.systemPath(logicalPath)
		info, err := os.Lstat(physicalPath)
		if err != nil {
			if !os.IsNotExist(err) {
				c.logger.Debug("Custom path %s not accessible: %v", physicalPath, err)
			}
			continue
		}

		dest := filepath.Join(c.tempDir, strings.TrimPrefix(logicalPath, "/"))
		if info.IsDir() {
			if err := c.safeCopyDir(ctx, physicalPath, dest, fmt.Sprintf("custom directory %s", logicalPath)); err != nil {
				c.logger.Debug("Failed to copy custom directory %s: %v", physicalPath, err)
			}
		} else {
			if err := c.safeCopyFile(ctx, physicalPath, dest, fmt.Sprintf("custom file %s", filepath.Base(logicalPath))); err != nil {
				c.logger.Debug("Failed to copy custom file %s: %v", physicalPath, err)
			}
		}
	}

	c.logger.Debug("Custom path collection completed")
	return nil
}

func (c *Collector) collectScriptDirectories(ctx context.Context) error {
	c.logger.Debug("Collecting system script directories")
	scriptDirs := []string{
		"/usr/local/bin",
		"/usr/local/sbin",
	}

	for _, dir := range scriptDirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		dest := filepath.Join(c.tempDir, strings.TrimPrefix(dir, "/"))
		if err := c.safeCopyDir(ctx, c.systemPath(dir), dest, fmt.Sprintf("scripts in %s", dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.logger.Debug("Failed to copy script directory %s: %v", dir, err)
		}
	}

	c.logger.Debug("System script directory collection completed")
	return nil
}

func (c *Collector) collectSSHKeys(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.logger.Debug("Collecting SSH configuration and keys for host, root and users")

	// Capture full SSH daemon configuration (sshd_config, host keys, etc.)
	if err := c.safeCopyDir(ctx,
		c.systemPath("/etc/ssh"),
		filepath.Join(c.tempDir, "etc/ssh"),
		"SSH configuration"); err != nil {
		c.logger.Debug("Failed to copy /etc/ssh: %v", err)
	}

	// Root SSH keys
	if err := c.safeCopyDir(ctx, c.systemPath("/root/.ssh"), filepath.Join(c.tempDir, "root/.ssh"), "root SSH keys"); err != nil && !errors.Is(err, os.ErrNotExist) {
		c.logger.Debug("Failed to copy root SSH keys: %v", err)
	}

	// User SSH keys
	homeEntries, err := os.ReadDir(c.systemPath("/home"))
	if err == nil {
		for _, entry := range homeEntries {
			if !entry.IsDir() {
				continue
			}
			userSSH := filepath.Join(c.systemPath("/home"), entry.Name(), ".ssh")
			if _, err := os.Stat(userSSH); err == nil {
				dest := filepath.Join(c.tempDir, "home", entry.Name(), ".ssh")
				if err := c.safeCopyDir(ctx, userSSH, dest, fmt.Sprintf("%s SSH keys", entry.Name())); err != nil {
					c.logger.Debug("Failed to copy SSH keys for user %s: %v", entry.Name(), err)
				}
			}
		}
	}

	c.logger.Debug("SSH key collection completed")
	return nil
}

func (c *Collector) collectScriptRepository(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	base := strings.TrimSpace(c.config.ScriptRepositoryPath)
	if base == "" {
		return nil
	}

	info, err := os.Stat(base)
	if err != nil || !info.IsDir() {
		return nil
	}

	target := c.proxsaveInfoDir("script-repository", filepath.Base(base))
	c.logger.Debug("Collecting script repository from %s", base)

	if err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == base {
			return nil
		}

		rel, err := filepath.Rel(base, path)
		if err != nil || rel == "." {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) > 0 {
			if parts[0] == "backup" || parts[0] == "log" {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		dest := filepath.Join(target, rel)
		if d.IsDir() {
			return c.ensureDir(dest)
		}
		return c.safeCopyFile(ctx, path, dest, fmt.Sprintf("script repository file %s", rel))
	}); err != nil {
		return err
	}

	c.logger.Debug("Script repository collection completed: %s -> %s", base, target)
	return nil
}

func (c *Collector) collectRootHome(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.logger.Debug("Collecting /root profile files and histories")

	if _, err := c.depStat(c.systemPath("/root")); err != nil {
		return nil
	}

	target := filepath.Join(c.tempDir, "root")
	if err := c.ensureDir(target); err != nil {
		return err
	}

	files := []string{
		".bashrc",
		".profile",
		".bash_logout",
		".lesshst",
		".selected_editor",
		".forward",
		".wget-hsts",
		"pkg-list.txt",
		"test-cron.log",
	}
	for _, name := range files {
		src := filepath.Join(c.systemPath("/root"), name)
		dest := filepath.Join(target, name)
		if err := c.safeCopyFile(ctx, src, dest, fmt.Sprintf("root file %s", name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.logger.Debug("Failed to copy root file %s: %v", name, err)
		}
	}

	historyPatterns := []string{".bash_history", ".bash_history-*"}
	for _, pattern := range historyPatterns {
		matches, err := filepath.Glob(filepath.Join(c.systemPath("/root"), pattern))
		if err != nil {
			continue
		}
		for _, match := range matches {
			name := filepath.Base(match)
			if err := c.safeCopyFile(ctx, match, filepath.Join(target, name), fmt.Sprintf("root history %s", name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				c.logger.Debug("Failed to copy root history %s: %v", match, err)
			}
		}
	}

	// Only copy security-critical directories; custom paths must be configured explicitly.
	// Respect BACKUP_SSH_KEYS to allow backing up /root without including SSH keys.
	if c.config.BackupSSHKeys {
		if err := c.safeCopyDir(ctx, c.systemPath("/root/.ssh"), filepath.Join(target, ".ssh"), "root SSH directory"); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.logger.Debug("Failed to copy root SSH directory: %v", err)
		}
	} else {
		c.logger.Debug("Skipping /root/.ssh in root home: BACKUP_SSH_KEYS=false")
	}

	// Copy full root .config directory (for CLI tools, editors, and other configs)
	if err := c.safeCopyDir(ctx, c.systemPath("/root/.config"), filepath.Join(target, ".config"), "root config directory"); err != nil && !errors.Is(err, os.ErrNotExist) {
		c.logger.Debug("Failed to copy root .config directory: %v", err)
	}

	wranglerLogs := filepath.Join(c.systemPath("/root"), ".config", ".wrangler", "logs")
	if err := c.safeCopyDir(ctx, wranglerLogs, filepath.Join(target, ".config", ".wrangler", "logs"), "wrangler logs"); err != nil && !errors.Is(err, os.ErrNotExist) {
		c.logger.Debug("Failed to copy wrangler logs: %v", err)
	}

	c.logger.Debug("Root home collection completed")
	return nil
}

func (c *Collector) collectUserHomes(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.logger.Debug("Collecting home directories under /home")

	entries, err := os.ReadDir(c.systemPath("/home"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if name == "" {
			continue
		}
		src := filepath.Join(c.systemPath("/home"), name)
		dest := filepath.Join(c.tempDir, "home", name)

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.IsDir() {
			extraExclude := []string(nil)
			if !c.config.BackupSSHKeys {
				extraExclude = append(extraExclude, ".ssh")
			}
			if err := c.withTemporaryExcludes(extraExclude, func() error {
				return c.safeCopyDir(ctx, src, dest, fmt.Sprintf("home directory for %s", name))
			}); err != nil && !errors.Is(err, os.ErrNotExist) {
				c.logger.Debug("Failed to copy home for %s: %v", name, err)
			}
			continue
		}

		if err := c.safeCopyFile(ctx, src, dest, fmt.Sprintf("home entry %s", name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.logger.Debug("Failed to copy home entry %s: %v", name, err)
		}
	}

	c.logger.Debug("User home collection completed")
	return nil
}
