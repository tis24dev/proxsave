package backup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type pveStorageEntry struct {
	Name    string
	Path    string
	Type    string
	Content string
}

type pveRuntimeInfo struct {
	Nodes    []string
	Storages []pveStorageEntry
}

var defaultPVEBackupPatterns = []string{
	"*.vma",
	"*.vma.gz",
	"*.vma.lz4",
	"*.vma.zst",
	"*.tar",
	"*.tar.gz",
	"*.tar.lz4",
	"*.tar.zst",
	"*.log",
	"*.notes",
}

var errStopWalk = errors.New("stop walk")

// CollectPVEConfigs collects Proxmox VE specific configurations
func (c *Collector) CollectPVEConfigs(ctx context.Context) error {
	c.logger.Info("Collecting PVE configurations")
	c.logger.Debug("Validating PVE environment and cluster state prior to collection")

	pveConfigPath := c.effectivePVEConfigPath()
	if _, err := os.Stat(pveConfigPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("not a PVE system: %s not found", pveConfigPath)
		}
		return fmt.Errorf("failed to access PVE config path %s: %w", pveConfigPath, err)
	}
	c.logger.Debug("%s detected, continuing with PVE collection", pveConfigPath)

	clustered := false
	if isClustered, err := c.isClusteredPVE(ctx); err != nil {
		if ctx.Err() != nil {
			return err
		}
		c.logger.Debug("Cluster detection failed, assuming standalone node: %v", err)
	} else {
		clustered = isClustered
		c.logger.Debug("Cluster detection completed: clustered=%v", clustered)
	}
	c.clusteredPVE = clustered

	// Collect PVE directories
	c.logger.Debug("Collecting PVE directories (clustered=%v)", clustered)
	if err := c.collectPVEDirectories(ctx, clustered); err != nil {
		return fmt.Errorf("failed to collect PVE directories: %w", err)
	}
	c.logger.Debug("PVE directory collection completed")

	// Collect PVE commands output
	c.logger.Debug("Collecting PVE command outputs and runtime state")
	runtimeInfo, err := c.collectPVECommands(ctx, clustered)
	if err != nil {
		return fmt.Errorf("failed to collect PVE commands: %w", err)
	}
	c.logger.Debug("PVE command output collection completed")

	// Collect VM/CT configurations
	if c.config.BackupVMConfigs {
		c.logger.Info("Collecting VM and container configurations")
		c.logger.Debug("Collecting VM/CT configuration files")
		if err := c.collectVMConfigs(ctx); err != nil {
			c.logger.Warning("Failed to collect VM configs: %v", err)
			// Non-fatal, continue
		} else {
			c.logger.Debug("VM/CT configuration collection completed")
		}
	} else {
		c.logger.Skip("VM/container configuration backup disabled.")
	}

	if c.config.BackupPVEJobs {
		c.logger.Debug("Collecting PVE job definitions for nodes: %v", runtimeInfo.Nodes)
		if err := c.collectPVEJobs(ctx, runtimeInfo.Nodes); err != nil {
			c.logger.Warning("Failed to collect PVE job information: %v", err)
		} else {
			c.logger.Debug("PVE job collection completed")
		}
	}

	if c.config.BackupPVESchedules {
		c.logger.Debug("Collecting PVE schedule information")
		if err := c.collectPVESchedules(ctx); err != nil {
			c.logger.Warning("Failed to collect PVE schedules: %v", err)
		} else {
			c.logger.Debug("PVE schedule collection completed")
		}
	}

	if c.config.BackupPVEReplication {
		c.logger.Debug("Collecting PVE replication settings for nodes: %v", runtimeInfo.Nodes)
		if err := c.collectPVEReplication(ctx, runtimeInfo.Nodes); err != nil {
			c.logger.Warning("Failed to collect PVE replication info: %v", err)
		} else {
			c.logger.Debug("PVE replication collection completed")
		}
	}

	if c.config.BackupPVEBackupFiles {
		c.logger.Debug("Collecting datastore metadata for PVE backup files")
		if err := c.collectPVEStorageMetadata(ctx, runtimeInfo.Storages); err != nil {
			c.logger.Warning("Failed to collect PVE datastore metadata: %v", err)
		} else {
			c.logger.Debug("PVE datastore metadata collection completed")
		}
	}

	if c.config.BackupCephConfig {
		c.logger.Debug("Collecting Ceph configuration and status")
		if err := c.collectPVECephInfo(ctx); err != nil {
			c.logger.Warning("Failed to collect Ceph information: %v", err)
		} else {
			c.logger.Debug("Ceph information collection completed")
		}
	}

	c.logger.Debug("Creating PVE info aliases under /var/lib/pve-cluster/info")
	if err := c.createPVEInfoAliases(ctx); err != nil {
		c.logger.Warning("Failed to create PVE info aliases: %v", err)
	}

	c.populatePVEManifest()

	c.logger.Info("PVE configuration collection completed")
	return nil
}

func (c *Collector) populatePVEManifest() {
	if c == nil || c.config == nil {
		return
	}
	c.pveManifest = make(map[string]ManifestEntry)

	type manifestLogOpts struct {
		description   string
		disableHint   string
		log           bool
		countNotFound bool
		// suppressNotFoundLog suppresses any log output when a file is not found.
		// This is useful for optional files that may not exist on a default installation.
		suppressNotFoundLog bool
	}

	logEntry := func(opts manifestLogOpts, entry ManifestEntry) {
		if !opts.log || strings.TrimSpace(opts.description) == "" {
			return
		}
		switch entry.Status {
		case StatusCollected:
			if entry.Size > 0 {
				c.logger.Info("  %s: collected (%s)", opts.description, FormatBytes(entry.Size))
			} else {
				c.logger.Info("  %s: collected", opts.description)
			}
		case StatusDisabled:
			if strings.TrimSpace(opts.disableHint) != "" {
				c.logger.Info("  %s: disabled (%s=false)", opts.description, opts.disableHint)
			} else {
				c.logger.Info("  %s: disabled", opts.description)
			}
		case StatusSkipped:
			c.logger.Info("  %s: skipped (excluded)", opts.description)
		case StatusNotFound:
			if opts.suppressNotFoundLog {
				return
			}
			if strings.TrimSpace(opts.disableHint) != "" {
				c.logger.Warning("  %s: not configured. If unused, set %s=false to disable.", opts.description, opts.disableHint)
			} else {
				c.logger.Warning("  %s: not configured", opts.description)
			}
		case StatusFailed:
			if strings.TrimSpace(entry.Error) != "" {
				c.logger.Warning("  %s: failed - %s", opts.description, entry.Error)
			} else {
				c.logger.Warning("  %s: failed", opts.description)
			}
		default:
			c.logger.Warning("  %s: failed - unknown status %s", opts.description, entry.Status)
		}
	}

	record := func(src string, enabled bool, opts manifestLogOpts) {
		if src == "" {
			return
		}
		dest := c.targetPathFor(src)
		key := pveManifestKey(c.tempDir, dest)
		entry := c.describePathForManifest(src, dest, enabled)
		c.pveManifest[key] = entry
		if opts.countNotFound && entry.Status == StatusNotFound {
			c.incFilesNotFound()
		}
		logEntry(opts, entry)
	}

	pveConfigPath := c.effectivePVEConfigPath()
	if pveConfigPath == "" {
		return
	}

	c.logger.Info("Collecting PVE configuration files:")

	// VM/CT configuration directories.
	record(filepath.Join(pveConfigPath, "qemu-server"), c.config.BackupVMConfigs, manifestLogOpts{
		description:   "VM configurations",
		disableHint:   "BACKUP_VM_CONFIGS",
		log:           true,
		countNotFound: true,
	})
	record(filepath.Join(pveConfigPath, "lxc"), c.config.BackupVMConfigs, manifestLogOpts{
		description:   "Container configurations",
		disableHint:   "BACKUP_VM_CONFIGS",
		log:           true,
		countNotFound: true,
	})

	// Firewall configuration.
	record(filepath.Join(pveConfigPath, "firewall"), c.config.BackupPVEFirewall, manifestLogOpts{
		description:   "Firewall configuration",
		disableHint:   "BACKUP_PVE_FIREWALL",
		log:           true,
		countNotFound: true,
	})
	if c.config.BackupPVEFirewall {
		nodesDir := filepath.Join(pveConfigPath, "nodes")
		if entries, err := os.ReadDir(nodesDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				node := strings.TrimSpace(entry.Name())
				if node == "" {
					continue
				}
				record(filepath.Join(nodesDir, node, "host.fw"), true, manifestLogOpts{log: false})
			}
		}
	}

	// Access control configuration (PVE stores ACLs inside user.cfg).
	record(filepath.Join(pveConfigPath, "user.cfg"), c.config.BackupPVEACL, manifestLogOpts{
		description:   "User/ACL configuration",
		disableHint:   "BACKUP_PVE_ACL",
		log:           true,
		countNotFound: true,
	})
	record(filepath.Join(pveConfigPath, "domains.cfg"), c.config.BackupPVEACL, manifestLogOpts{
		description: "Domain configuration",
		disableHint: "BACKUP_PVE_ACL",
		log:         true,
		// domains.cfg may not exist on a default standalone PVE install (built-in realms only).
		// Don't warn or count as "missing" in that case.
		countNotFound:       false,
		suppressNotFoundLog: true,
	})

	// Scheduled jobs.
	record(filepath.Join(pveConfigPath, "jobs.cfg"), c.config.BackupPVEJobs, manifestLogOpts{
		description:   "Job configuration",
		disableHint:   "BACKUP_PVE_JOBS",
		log:           true,
		countNotFound: true,
	})
	record(filepath.Join(pveConfigPath, "vzdump.cron"), c.config.BackupPVEJobs, manifestLogOpts{
		description:   "VZDump cron",
		disableHint:   "BACKUP_PVE_JOBS",
		log:           true,
		countNotFound: true,
	})

	// Cluster configuration.
	record(c.effectiveCorosyncConfigPath(), c.config.BackupClusterConfig, manifestLogOpts{
		description:   "Corosync configuration",
		disableHint:   "BACKUP_CLUSTER_CONFIG",
		log:           true,
		countNotFound: true,
	})
	record(filepath.Join(c.effectivePVEClusterPath(), "config.db"), c.config.BackupClusterConfig, manifestLogOpts{
		description:   "PVE cluster database",
		disableHint:   "BACKUP_CLUSTER_CONFIG",
		log:           true,
		countNotFound: true,
	})
	record(c.systemPath("/etc/corosync/authkey"), c.config.BackupClusterConfig, manifestLogOpts{
		description:   "Corosync authkey",
		disableHint:   "BACKUP_CLUSTER_CONFIG",
		log:           true,
		countNotFound: true,
	})

	// VZDump configuration.
	vzdumpPath := c.config.VzdumpConfigPath
	if vzdumpPath == "" {
		vzdumpPath = "/etc/vzdump.conf"
	} else if !filepath.IsAbs(vzdumpPath) {
		vzdumpPath = filepath.Join(pveConfigPath, vzdumpPath)
	}
	record(vzdumpPath, c.config.BackupVZDumpConfig, manifestLogOpts{
		description:   "VZDump configuration",
		disableHint:   "BACKUP_VZDUMP_CONFIG",
		log:           true,
		countNotFound: true,
	})
}

func pveManifestKey(tempDir, dest string) string {
	if tempDir == "" || dest == "" {
		return filepath.ToSlash(dest)
	}
	rel, err := filepath.Rel(tempDir, dest)
	if err != nil {
		return filepath.ToSlash(dest)
	}
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." {
		return filepath.ToSlash(dest)
	}
	return filepath.ToSlash(rel)
}

func (c *Collector) describePathForManifest(src, dest string, enabled bool) ManifestEntry {
	if !enabled {
		return ManifestEntry{Status: StatusDisabled}
	}
	if c.shouldExclude(src) || c.shouldExclude(dest) {
		return ManifestEntry{Status: StatusSkipped}
	}

	info, err := os.Lstat(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ManifestEntry{Status: StatusNotFound}
		}
		return ManifestEntry{Status: StatusFailed, Error: err.Error()}
	}

	if c.dryRun {
		if info.Mode().IsRegular() {
			return ManifestEntry{Status: StatusCollected, Size: info.Size()}
		}
		return ManifestEntry{Status: StatusCollected}
	}

	if _, err := os.Lstat(dest); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ManifestEntry{Status: StatusFailed, Error: "not present in temp directory after collection"}
		}
		return ManifestEntry{Status: StatusFailed, Error: err.Error()}
	}

	if info.Mode().IsRegular() {
		return ManifestEntry{Status: StatusCollected, Size: info.Size()}
	}
	return ManifestEntry{Status: StatusCollected}
}

// collectPVEDirectories collects PVE-specific directories
func (c *Collector) collectPVEDirectories(ctx context.Context, clustered bool) error {
	c.logger.Debug("Snapshotting PVE directories (clustered=%v)", clustered)

	pveConfigPath := c.effectivePVEConfigPath()
	var extraExclude []string
	if !c.config.BackupVMConfigs {
		extraExclude = append(extraExclude, "qemu-server", "lxc")
	}
	if !c.config.BackupPVEFirewall {
		// Rules can exist both under /etc/pve/firewall and under /etc/pve/nodes/*.
		extraExclude = append(extraExclude, "firewall", "host.fw")
	}
	if !c.config.BackupPVEACL {
		extraExclude = append(extraExclude, "user.cfg", "domains.cfg")
	}
	if !c.config.BackupPVEJobs {
		extraExclude = append(extraExclude, "jobs.cfg", "vzdump.cron")
	}
	if !c.config.BackupClusterConfig {
		// Keep /etc/pve snapshot but omit cluster-specific config files when disabled.
		extraExclude = append(extraExclude, "corosync.conf")
	}

	if len(extraExclude) > 0 {
		c.logger.Debug("PVE config exclusions enabled (disabled features): %s", strings.Join(extraExclude, ", "))
	}
	if err := c.withTemporaryExcludes(extraExclude, func() error {
		return c.safeCopyDir(ctx,
			pveConfigPath,
			c.targetPathFor(pveConfigPath),
			"PVE configuration")
	}); err != nil {
		return err
	}

	// Cluster configuration (if clustered)
	clusterPath := c.effectivePVEClusterPath()
	if c.config.BackupClusterConfig {
		corosyncPath := c.config.CorosyncConfigPath
		if corosyncPath == "" {
			corosyncPath = filepath.Join(pveConfigPath, "corosync.conf")
		} else if !filepath.IsAbs(corosyncPath) {
			corosyncPath = filepath.Join(pveConfigPath, corosyncPath)
		}
		if err := c.safeCopyFile(ctx,
			corosyncPath,
			c.targetPathFor(corosyncPath),
			"Corosync configuration"); err != nil {
			c.logger.Warning("Failed to copy corosync.conf: %v", err)
		}

		if clustered {
			// Cluster directory
			if err := c.safeCopyDir(ctx,
				clusterPath,
				c.targetPathFor(clusterPath),
				"PVE cluster data"); err != nil {
				c.logger.Warning("Failed to copy cluster data: %v", err)
			}
		} else {
			c.logger.Debug("PVE cluster not configured (single node) - skipping cluster data directory snapshot")
		}
	} else {
		c.logger.Skip("PVE cluster backup disabled")
		c.logger.Skip("Corosync configuration")
	}

	if c.config.BackupClusterConfig {
		authkeySrc := c.systemPath("/etc/corosync/authkey")
		if err := c.safeCopyFile(ctx,
			authkeySrc,
			c.targetPathFor(authkeySrc),
			"Corosync authkey"); err != nil {
			c.logger.Warning("Failed to copy Corosync authkey: %v", err)
		}

		// Always attempt to capture config.db even on standalone nodes when cluster config is enabled.
		configDB := filepath.Join(clusterPath, "config.db")
		if info, err := os.Stat(configDB); err == nil && !info.IsDir() {
			target := c.targetPathFor(configDB)
			c.logger.Debug("Copying PVE cluster database %s to %s", configDB, target)
			if err := c.safeCopyFile(ctx, configDB, target, "PVE cluster database"); err != nil {
				c.logger.Warning("Failed to copy PVE cluster database %s: %v", configDB, err)
			}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			c.logger.Warning("Failed to stat PVE cluster database %s: %v", configDB, err)
		}
	} else {
		c.logger.Debug("Skipping PVE cluster database capture: BACKUP_CLUSTER_CONFIG=false")
	}

	// Firewall configuration
	if c.config.BackupPVEFirewall {
		firewallSrc := filepath.Join(pveConfigPath, "firewall")
		if info, err := os.Stat(firewallSrc); err == nil {
			if info.IsDir() {
				if err := c.safeCopyDir(ctx,
					firewallSrc,
					c.targetPathFor(firewallSrc),
					"PVE firewall directory"); err != nil {
					c.logger.Warning("Failed to copy firewall directory: %v", err)
				}
			} else {
				if err := c.safeCopyFile(ctx,
					firewallSrc,
					c.targetPathFor(firewallSrc),
					"PVE firewall configuration"); err != nil {
					c.logger.Warning("Failed to copy firewall file: %v", err)
				}
			}
		} else if errors.Is(err, os.ErrNotExist) {
			c.logger.Debug("PVE firewall configuration not found (no rules configured) - skipping")
		} else {
			c.logger.Warning("Failed to access firewall configuration %s: %v", firewallSrc, err)
		}
	} else {
		c.logger.Skip("PVE firewall backup disabled.")
	}

	// VZDump configuration
	if c.config.BackupVZDumpConfig {
		c.logger.Info("Collecting VZDump backup configuration")
		vzdumpPath := c.config.VzdumpConfigPath
		if vzdumpPath == "" {
			vzdumpPath = "/etc/vzdump.conf"
		} else if !filepath.IsAbs(vzdumpPath) {
			vzdumpPath = filepath.Join(pveConfigPath, vzdumpPath)
		}
		if err := c.safeCopyFile(ctx,
			vzdumpPath,
			c.targetPathFor(vzdumpPath),
			"VZDump configuration"); err != nil {
			c.logger.Warning("Failed to copy VZDump configuration: %v", err)
		}
	} else {
		c.logger.Skip("VZDump configuration backup disabled.")
	}

	c.logger.Debug("PVE directory snapshot completed")
	return nil
}

// collectPVECommands collects output from PVE commands and returns runtime info
func (c *Collector) collectPVECommands(ctx context.Context, clustered bool) (*pveRuntimeInfo, error) {
	commandsDir := c.proxsaveCommandsDir("pve")
	if err := c.ensureDir(commandsDir); err != nil {
		return nil, fmt.Errorf("failed to create commands directory: %w", err)
	}
	c.logger.Debug("Collecting PVE command outputs into %s", commandsDir)

	// PVE version (CRITICAL)
	if err := c.safeCmdOutput(ctx,
		"pveversion -v",
		filepath.Join(commandsDir, "pveversion.txt"),
		"PVE version",
		true); err != nil {
		return nil, fmt.Errorf("failed to get PVE version (critical): %w", err)
	}

	// Node configuration
	c.safeCmdOutput(ctx,
		"pvenode config get",
		filepath.Join(commandsDir, "node_config.txt"),
		"Node configuration",
		false)

	// API version
	c.safeCmdOutput(ctx,
		"pvesh get /version --output-format=json",
		filepath.Join(commandsDir, "api_version.json"),
		"API version",
		false)

	info := &pveRuntimeInfo{
		Nodes:    make([]string, 0),
		Storages: make([]pveStorageEntry, 0),
	}

	// Collect node list (used for subsequent per-node commands)
	if nodeData, err := c.captureCommandOutput(ctx,
		"pvesh get /nodes --output-format=json",
		filepath.Join(commandsDir, "nodes_status.json"),
		"node status",
		false); err != nil {
		return nil, fmt.Errorf("failed to get node status: %w", err)
	} else if len(nodeData) > 0 {
		var nodes []struct {
			Node string `json:"node"`
		}
		if err := json.Unmarshal(nodeData, &nodes); err != nil {
			c.logger.Debug("Failed to parse node status JSON: %v", err)
		} else {
			for _, n := range nodes {
				if trimmed := strings.TrimSpace(n.Node); trimmed != "" {
					info.Nodes = append(info.Nodes, trimmed)
				}
			}
		}
	}

	// Collect ACL information if enabled
	if c.config.BackupPVEACL {
		c.safeCmdOutput(ctx,
			"pveum user list --output-format=json",
			filepath.Join(commandsDir, "pve_users.json"),
			"PVE users",
			false)

		c.safeCmdOutput(ctx,
			"pveum group list --output-format=json",
			filepath.Join(commandsDir, "pve_groups.json"),
			"PVE groups",
			false)

		c.safeCmdOutput(ctx,
			"pveum role list --output-format=json",
			filepath.Join(commandsDir, "pve_roles.json"),
			"PVE roles",
			false)

		// Resource pools (datacenter-wide objects; may be useful for SAFE restore apply).
		c.safeCmdOutput(ctx,
			"pveum pool list --output-format=json",
			filepath.Join(commandsDir, "pools.json"),
			"PVE resource pools",
			false)
	}

	// Cluster commands (if clustered)
	if clustered && c.config.BackupClusterConfig {
		c.safeCmdOutput(ctx,
			"pvecm status",
			filepath.Join(commandsDir, "cluster_status.txt"),
			"Cluster status",
			false)

		c.safeCmdOutput(ctx,
			"pvecm nodes",
			filepath.Join(commandsDir, "cluster_nodes.txt"),
			"Cluster nodes",
			false)

		// HA status
		c.safeCmdOutput(ctx,
			"pvesh get /cluster/ha/status --output-format=json",
			filepath.Join(commandsDir, "ha_status.json"),
			"HA status",
			false)

		// Resource mappings (datacenter-wide objects; used by VM configs via mapping=<id>).
		c.safeCmdOutput(ctx,
			"pvesh get /cluster/mapping/pci --output-format=json",
			filepath.Join(commandsDir, "mapping_pci.json"),
			"PCI resource mappings",
			false)
		c.safeCmdOutput(ctx,
			"pvesh get /cluster/mapping/usb --output-format=json",
			filepath.Join(commandsDir, "mapping_usb.json"),
			"USB resource mappings",
			false)
		c.safeCmdOutput(ctx,
			"pvesh get /cluster/mapping/dir --output-format=json",
			filepath.Join(commandsDir, "mapping_dir.json"),
			"Directory resource mappings",
			false)
	} else if clustered && !c.config.BackupClusterConfig {
		c.logger.Debug("Skipping cluster runtime commands: BACKUP_CLUSTER_CONFIG=false (clustered=%v)", clustered)
	}

	// Storage status
	hostname, _ := os.Hostname()
	nodeName := shortHostname(hostname)
	if nodeName == "" {
		nodeName = hostname
	}
	c.safeCmdOutput(ctx,
		fmt.Sprintf("pvesh get /nodes/%s/storage --output-format=json", nodeName),
		filepath.Join(commandsDir, "storage_status.json"),
		"Storage status",
		false)

	// Disk list
	c.safeCmdOutput(ctx,
		fmt.Sprintf("pvesh get /nodes/%s/disks/list --output-format=json", nodeName),
		filepath.Join(commandsDir, "disks_list.json"),
		"Disks list",
		false)

	// Storage information
	storageJSONPath := filepath.Join(commandsDir, "storage_status.json")
	if storageData, err := c.captureCommandOutput(ctx,
		fmt.Sprintf("pvesh get /nodes/%s/storage --output-format=json", nodeName),
		storageJSONPath,
		"Storage status",
		false); err != nil {
		return nil, fmt.Errorf("failed to query storage status: %w", err)
	} else if len(storageData) > 0 {
		storages, err := parseNodeStorageList(storageData)
		if err != nil {
			c.logger.Debug("Failed to parse storage status JSON: %v", err)
		} else {
			info.Storages = append(info.Storages, storages...)
			sort.Slice(info.Storages, func(i, j int) bool {
				return info.Storages[i].Name < info.Storages[j].Name
			})
		}
	}

	// Storage manager status (text output kept for compatibility)
	c.safeCmdOutput(ctx,
		"pvesm status",
		filepath.Join(commandsDir, "pvesm_status.txt"),
		"Storage manager status",
		false)

	// Ensure we have at least one node reference
	if len(info.Nodes) == 0 {
		if short := shortHostname(hostname); short != "" {
			info.Nodes = append(info.Nodes, short)
		} else if hostname != "" {
			info.Nodes = append(info.Nodes, hostname)
		} else {
			info.Nodes = append(info.Nodes, "localhost")
		}
	} else {
		sort.Strings(info.Nodes)
	}

	c.logger.Debug("PVE command output collection finished: %d nodes, %d storages", len(info.Nodes), len(info.Storages))
	return info, nil
}

func parseNodeStorageList(data []byte) ([]pveStorageEntry, error) {
	var raw []struct {
		Storage string `json:"storage"`
		Name    string `json:"name"`
		Path    string `json:"path"`
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	entries := make([]pveStorageEntry, 0, len(raw))
	for _, item := range raw {
		name := strings.TrimSpace(item.Storage)
		if name == "" {
			name = strings.TrimSpace(item.Name)
		}
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, pveStorageEntry{
			Name:    name,
			Path:    strings.TrimSpace(item.Path),
			Type:    strings.TrimSpace(item.Type),
			Content: strings.TrimSpace(item.Content),
		})
	}
	return entries, nil
}

// collectVMConfigs collects VM and Container configurations
func (c *Collector) collectVMConfigs(ctx context.Context) error {
	c.logger.Debug("Collecting VM and container configuration directories")
	pveConfigPath := c.effectivePVEConfigPath()
	// QEMU VMs
	vmConfigDir := filepath.Join(pveConfigPath, "qemu-server")
	if stat, err := os.Stat(vmConfigDir); err == nil && stat.IsDir() {
		if err := c.safeCopyDir(ctx,
			vmConfigDir,
			c.targetPathFor(vmConfigDir),
			"VM configurations"); err != nil {
			return fmt.Errorf("failed to copy VM configs: %w", err)
		}
	}

	// LXC Containers
	lxcConfigDir := filepath.Join(pveConfigPath, "lxc")
	if stat, err := os.Stat(lxcConfigDir); err == nil && stat.IsDir() {
		if err := c.safeCopyDir(ctx,
			lxcConfigDir,
			c.targetPathFor(lxcConfigDir),
			"Container configurations"); err != nil {
			return fmt.Errorf("failed to copy container configs: %w", err)
		}
	}

	// Collect VMs/CTs list
	commandsDir := c.proxsaveCommandsDir("pve")
	hostname, _ := os.Hostname()
	nodeName := shortHostname(hostname)
	if nodeName == "" {
		nodeName = hostname
	}

	// QEMU VMs list
	c.safeCmdOutput(ctx,
		fmt.Sprintf("pvesh get /nodes/%s/qemu --output-format=json", nodeName),
		filepath.Join(commandsDir, "qemu_vms.json"),
		"QEMU VMs list",
		false)

	// LXC Containers list
	c.safeCmdOutput(ctx,
		fmt.Sprintf("pvesh get /nodes/%s/lxc --output-format=json", nodeName),
		filepath.Join(commandsDir, "lxc_containers.json"),
		"LXC containers list",
		false)

	c.logger.Debug("VM/CT configuration collection finished")
	return nil
}

func (c *Collector) collectPVEJobs(ctx context.Context, nodes []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.logger.Debug("Collecting PVE job definitions and histories for nodes: %v", nodes)

	jobsDir := filepath.Join(c.tempDir, "var/lib/pve-cluster/info/jobs")
	if err := c.ensureDir(jobsDir); err != nil {
		return fmt.Errorf("failed to create jobs directory: %w", err)
	}

	if _, err := c.captureCommandOutput(ctx,
		"pvesh get /cluster/backup --output-format=json",
		filepath.Join(jobsDir, "backup_jobs.json"),
		"backup jobs",
		false); err != nil {
		return err
	}

	seen := make(map[string]struct{})
	for _, node := range nodes {
		node = strings.TrimSpace(node)
		if node == "" {
			continue
		}
		if _, ok := seen[node]; ok {
			continue
		}
		seen[node] = struct{}{}
		outputPath := filepath.Join(jobsDir, fmt.Sprintf("%s_backup_history.json", node))
		c.captureCommandOutput(ctx,
			fmt.Sprintf("pvesh get /nodes/%s/tasks --output-format=json --typefilter=vzdump", node),
			outputPath,
			fmt.Sprintf("%s backup history", node),
			false)
	}

	// Copy vzdump cron schedule if present
	if err := c.safeCopyFile(ctx,
		"/etc/cron.d/vzdump",
		filepath.Join(c.tempDir, "etc/cron.d/vzdump"),
		"VZDump cron schedule"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	c.logger.Debug("PVE job collection completed (jobs dir: %s)", jobsDir)
	return nil
}

func (c *Collector) effectivePVEConfigPath() string {
	if path := strings.TrimSpace(c.config.PVEConfigPath); path != "" {
		return c.systemPath(path)
	}
	return c.systemPath("/etc/pve")
}

func (c *Collector) effectivePVEClusterPath() string {
	if path := strings.TrimSpace(c.config.PVEClusterPath); path != "" {
		return c.systemPath(path)
	}
	return c.systemPath("/var/lib/pve-cluster")
}

func (c *Collector) targetPathFor(src string) string {
	clean := filepath.Clean(src)
	if filepath.IsAbs(clean) {
		clean = strings.TrimPrefix(clean, string(os.PathSeparator))
	}
	clean = strings.Trim(clean, string(os.PathSeparator))
	if clean == "" || clean == "." {
		clean = "pve"
	}
	return filepath.Join(c.tempDir, clean)
}

func (c *Collector) collectPVESchedules(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.logger.Debug("Collecting schedule information (cron/systemd timers)")

	schedulesDir := filepath.Join(c.tempDir, "var/lib/pve-cluster/info/schedules")
	if err := c.ensureDir(schedulesDir); err != nil {
		return fmt.Errorf("failed to create schedules directory: %w", err)
	}

	c.captureCommandOutput(ctx,
		"crontab -l",
		filepath.Join(schedulesDir, "root_crontab.txt"),
		"root crontab",
		false)

	c.captureCommandOutput(ctx,
		"systemctl list-timers --all --no-pager",
		filepath.Join(schedulesDir, "systemd_timers.txt"),
		"systemd timers",
		false)

	cronDir := "/etc/cron.d"
	if entries, err := os.ReadDir(cronDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			lower := strings.ToLower(name)
			if strings.Contains(lower, "pve") || strings.Contains(lower, "proxmox") || strings.Contains(lower, "vzdump") {
				src := filepath.Join(cronDir, name)
				dest := filepath.Join(c.tempDir, "etc/cron.d", name)
				if err := c.safeCopyFile(ctx, src, dest, fmt.Sprintf("cron job %s", name)); err != nil {
					c.logger.Debug("Failed to copy cron job %s: %v", name, err)
				}
			}
		}
	}

	c.logger.Debug("PVE schedule collection completed (output dir: %s)", schedulesDir)
	return nil
}

func (c *Collector) collectPVEReplication(ctx context.Context, nodes []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.logger.Debug("Collecting replication jobs for nodes: %v", nodes)

	repDir := filepath.Join(c.tempDir, "var/lib/pve-cluster/info/replication")
	if err := c.ensureDir(repDir); err != nil {
		return fmt.Errorf("failed to create replication directory: %w", err)
	}

	if _, err := c.captureCommandOutput(ctx,
		"pvesh get /cluster/replication --output-format=json",
		filepath.Join(repDir, "replication_jobs.json"),
		"replication jobs",
		false); err != nil {
		return err
	}

	seen := make(map[string]struct{})
	for _, node := range nodes {
		node = strings.TrimSpace(node)
		if node == "" {
			continue
		}
		if _, ok := seen[node]; ok {
			continue
		}
		seen[node] = struct{}{}
		outputPath := filepath.Join(repDir, fmt.Sprintf("%s_replication_status.json", node))
		c.captureCommandOutput(ctx,
			fmt.Sprintf("pvesh get /nodes/%s/replication --output-format=json", node),
			outputPath,
			fmt.Sprintf("%s replication status", node),
			false)
	}

	c.logger.Debug("PVE replication collection completed (dir: %s)", repDir)
	return nil
}

func (c *Collector) collectPVEStorageMetadata(ctx context.Context, storages []pveStorageEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.logger.Info("Collecting PVE datastore information using auto-detection")
	c.logger.Debug("Collecting datastore metadata for %d storages", len(storages))

	storages = c.augmentStoragesWithConfig(storages)

	if len(storages) == 0 {
		c.logger.Info("Found 0 PVE datastore(s) via auto-detection")
		c.logger.Info("No PVE datastores detected - skipping metadata collection")
		return nil
	}

	c.logger.Info("Found %d PVE datastore(s) via auto-detection", len(storages))

	baseDir := filepath.Join(c.tempDir, "var/lib/pve-cluster/info/datastores")
	if err := c.ensureDir(baseDir); err != nil {
		return fmt.Errorf("failed to create datastore metadata directory: %w", err)
	}

	var summary strings.Builder
	summary.WriteString("# PVE datastores detected on ")
	summary.WriteString(time.Now().Format(time.RFC3339))
	summary.WriteString("\n# Format: NAME|PATH|TYPE|CONTENT\n\n")

	processed := 0
	for _, storage := range storages {
		if storage.Path == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if stat, err := os.Stat(storage.Path); err != nil || !stat.IsDir() {
			c.logger.Debug("Skipping datastore %s (path not accessible: %s)", storage.Name, storage.Path)
			continue
		}

		processed++
		summary.WriteString(fmt.Sprintf("%s|%s|%s|%s\n",
			storage.Type,
			storage.Name,
			storage.Path,
			storage.Content))

		metaDir := filepath.Join(baseDir, storage.Name)
		if err := c.ensureDir(metaDir); err != nil {
			c.logger.Warning("Failed to create metadata directory for %s: %v", storage.Name, err)
			continue
		}

		meta := struct {
			Name              string        `json:"name"`
			Path              string        `json:"path"`
			Type              string        `json:"type"`
			Content           string        `json:"content,omitempty"`
			ScannedAt         time.Time     `json:"scanned_at"`
			SampleDirectories []string      `json:"sample_directories,omitempty"`
			DiskUsage         string        `json:"disk_usage,omitempty"`
			SampleFiles       []FileSummary `json:"sample_files,omitempty"`
		}{
			Name:      storage.Name,
			Path:      storage.Path,
			Type:      storage.Type,
			Content:   storage.Content,
			ScannedAt: time.Now(),
		}

		dirSamples, dirSampleErr := c.sampleDirectories(ctx, storage.Path, 2, 20)
		if dirSampleErr != nil {
			c.logger.Debug("Directory sample for datastore %s failed: %v", storage.Name, dirSampleErr)
		}
		if len(dirSamples) > 0 {
			meta.SampleDirectories = dirSamples
		}

		diskUsageText, diskUsageErr := c.describeDiskUsage(storage.Path)
		if diskUsageErr != nil {
			c.logger.Debug("Disk usage summary for %s failed: %v", storage.Name, diskUsageErr)
		} else {
			meta.DiskUsage = diskUsageText
		}

		includePatterns := c.config.PxarFileIncludePatterns
		if len(includePatterns) == 0 {
			includePatterns = []string{
				"*.vma", "*.vma.gz", "*.vma.lz4", "*.vma.zst",
				"*.tar", "*.tar.gz", "*.tar.lz4", "*.tar.zst",
				"*.log", "*.notes",
			}
		}
		excludePatterns := c.config.PxarFileExcludePatterns

		fileSummaries, sampleFileErr := c.sampleFiles(ctx, storage.Path, includePatterns, excludePatterns, 3, 100)
		if sampleFileErr != nil {
			c.logger.Debug("Backup file sample for %s failed: %v", storage.Name, sampleFileErr)
		} else if len(fileSummaries) > 0 {
			meta.SampleFiles = fileSummaries
		}

		metaBytes, err := json.MarshalIndent(meta, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal metadata for %s: %w", storage.Name, err)
		}

		if err := c.writeReportFile(filepath.Join(metaDir, "metadata.json"), metaBytes); err != nil {
			return err
		}

		fileSampleLines, fileSampleErr := c.sampleMetadataFileStats(ctx, storage.Path, 3, 10)
		if fileSampleErr != nil {
			c.logger.Debug("General file sampling for %s failed: %v", storage.Name, fileSampleErr)
		}

		if err := c.writeDatastoreMetadataText(metaDir, storage, dirSamples, dirSampleErr, diskUsageText, diskUsageErr, fileSampleLines, fileSampleErr); err != nil {
			c.logger.Warning("Failed to write metadata.txt for %s: %v", storage.Name, err)
		}

		if c.config.BackupPVEBackupFiles {
			c.logger.Info("Analyzing PVE backup files in datastore: %s", storage.Name)
			if err := c.collectDetailedPVEBackups(ctx, storage, metaDir); err != nil {
				c.logger.Warning("Detailed backup analysis for %s failed: %v", storage.Name, err)
			}
		} else {
			c.logger.Debug("Detailed backup analysis disabled for datastore: %s", storage.Name)
		}
	}

	if processed > 0 {
		summary.WriteString(fmt.Sprintf("\n# Total datastores processed: %d\n", processed))
		if err := c.writeReportFile(filepath.Join(baseDir, "detected_datastores.txt"), []byte(summary.String())); err != nil {
			return err
		}
	}

	c.logger.Debug("PVE datastore metadata collection completed (%d processed)", processed)
	return nil
}

func (c *Collector) collectDetailedPVEBackups(ctx context.Context, storage pveStorageEntry, metaDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	analysisDir := filepath.Join(metaDir, "backup_analysis")
	if err := c.ensureDir(analysisDir); err != nil {
		return fmt.Errorf("failed to create backup analysis directory: %w", err)
	}

	patterns := c.config.PxarFileIncludePatterns
	if len(patterns) == 0 {
		patterns = defaultPVEBackupPatterns
	}

	writers := c.newPatternWriters(storage, analysisDir, patterns)
	if len(writers) == 0 {
		c.logger.Debug("No valid backup patterns for datastore %s", storage.Name)
		return nil
	}
	c.logger.Info("Scanning for PVE backup files in datastore: %s (optimized single scan)", storage.Name)
	defer func() {
		for _, w := range writers {
			if err := w.Close(); err != nil {
				c.logger.Debug("Failed to close writer for pattern %s: %v", w.pattern, err)
			}
		}
	}()

	var totalFiles int64
	var totalSize int64

	var smallDir string
	if c.config.BackupSmallPVEBackups && c.config.MaxPVEBackupSizeBytes > 0 {
		smallDir = filepath.Join(c.tempDir, "var/lib/pve-cluster/small_backups", storage.Name)
		if err := c.ensureDir(smallDir); err != nil {
			c.logger.Warning("Cannot create small backups directory %s: %v", smallDir, err)
			smallDir = ""
		}
	}

	includePattern := strings.TrimSpace(c.config.PVEBackupIncludePattern)
	var includeDir string
	if includePattern != "" {
		includeDir = filepath.Join(c.tempDir, "var/lib/pve-cluster/selected_backups", storage.Name)
		if err := c.ensureDir(includeDir); err != nil {
			c.logger.Warning("Cannot create selected backups directory %s: %v", includeDir, err)
			includeDir = ""
		}
	}

	walkErr := filepath.WalkDir(storage.Path, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			c.logger.Debug("Skipping %s: %v", path, walkErr)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			c.logger.Debug("Failed to stat %s: %v", path, err)
			return nil
		}

		base := filepath.Base(path)
		matched := false
		for _, w := range writers {
			if matchPattern(base, w.pattern) {
				matched = true
				if err := w.Write(path, info); err != nil {
					c.logger.Debug("Failed to log %s for pattern %s: %v", path, w.pattern, err)
				}
			}
		}

		if !matched {
			return nil
		}

		totalFiles++
		totalSize += info.Size()

		if smallDir != "" && info.Size() <= c.config.MaxPVEBackupSizeBytes {
			if err := c.copyBackupSample(ctx, path, smallDir, fmt.Sprintf("small PVE backup %s", filepath.Base(path))); err != nil {
				c.logger.Debug("Failed to copy small backup %s: %v", path, err)
			}
		}
		if includeDir != "" && strings.Contains(path, includePattern) {
			if err := c.copyBackupSample(ctx, path, includeDir, fmt.Sprintf("selected PVE backup %s", filepath.Base(path))); err != nil {
				c.logger.Debug("Failed to copy pattern backup %s: %v", path, err)
			}
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}

	if err := c.writePatternSummary(storage, analysisDir, writers, totalFiles, totalSize); err != nil {
		return err
	}
	for _, w := range writers {
		if w.count == 0 {
			continue
		}
		c.logger.Info("Found %d backup files (%s) in datastore: %s", w.count, describePatternForLog(w.pattern), storage.Name)
	}
	return nil
}

func (c *Collector) newPatternWriters(storage pveStorageEntry, analysisDir string, patterns []string) []*patternWriter {
	writers := make([]*patternWriter, 0, len(patterns))
	seen := make(map[string]struct{})
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		pw, err := newPatternWriter(storage.Name, storage.Path, analysisDir, pattern, c.dryRun)
		if err != nil {
			c.logger.Warning("Failed to prepare writer for pattern %s: %v", pattern, err)
			continue
		}
		writers = append(writers, pw)
	}
	return writers
}

type patternWriter struct {
	pattern     string
	storageName string
	storagePath string
	filePath    string
	file        *os.File
	writer      *bufio.Writer
	count       int64
	totalSize   int64
	errorCount  int64
}

func newPatternWriter(storageName, storagePath, analysisDir, pattern string, dryRun bool) (*patternWriter, error) {
	clean := cleanPatternName(pattern)
	filename := fmt.Sprintf("%s_%s_list.txt", storageName, clean)
	filePath := filepath.Join(analysisDir, filename)

	// In dry-run mode, create a writer without an actual file
	if dryRun {
		return &patternWriter{
			pattern:     pattern,
			storageName: storageName,
			storagePath: storagePath,
			filePath:    filePath,
			file:        nil,
			writer:      nil,
		}, nil
	}

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0640)
	if err != nil {
		return nil, err
	}
	writer := bufio.NewWriter(file)
	header := fmt.Sprintf("# PVE backup files matching pattern: %s\n# Datastore: %s (%s)\n# Generated on: %s\n# Format: permissions size date path\n\n",
		pattern,
		storageName,
		storagePath,
		time.Now().Format(time.RFC3339),
	)
	if _, err := writer.WriteString(header); err != nil {
		file.Close()
		return nil, err
	}
	return &patternWriter{
		pattern:     pattern,
		storageName: storageName,
		storagePath: storagePath,
		filePath:    filePath,
		file:        file,
		writer:      writer,
	}, nil
}

func (pw *patternWriter) Write(path string, info os.FileInfo) error {
	// In dry-run mode, writer will be nil - just count without writing
	if pw.writer == nil {
		pw.count++
		pw.totalSize += info.Size()
		return nil
	}

	rel, err := filepath.Rel(pw.storagePath, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = path
	}
	line := fmt.Sprintf("%s %10s %s %s\n",
		info.Mode().String(),
		FormatBytes(info.Size()),
		info.ModTime().Format(time.RFC3339),
		rel,
	)
	if _, err := pw.writer.WriteString(line); err != nil {
		pw.errorCount++
		return err
	}
	pw.count++
	pw.totalSize += info.Size()
	return nil
}

func (pw *patternWriter) Close() error {
	var err error
	if pw.writer != nil {
		if flushErr := pw.writer.Flush(); flushErr != nil && err == nil {
			err = flushErr
		}
	}
	if pw.file != nil {
		if closeErr := pw.file.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	pw.writer = nil
	pw.file = nil
	return err
}

func cleanPatternName(pattern string) string {
	clean := strings.ReplaceAll(pattern, "*", "")
	clean = strings.ReplaceAll(clean, ".", "_")
	clean = strings.ReplaceAll(clean, "/", "_")
	if clean == "" {
		return "all"
	}
	return clean
}

func describePatternForLog(pattern string) string {
	trimmed := strings.Trim(pattern, "*")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return pattern
	}
	return trimmed
}

func matchPattern(name, pattern string) bool {
	matched, err := filepath.Match(pattern, name)
	if err != nil {
		return false
	}
	return matched
}

func (c *Collector) copyBackupSample(ctx context.Context, src, destDir, description string) error {
	if err := c.ensureDir(destDir); err != nil {
		return err
	}
	dest := filepath.Join(destDir, filepath.Base(src))
	return c.safeCopyFile(ctx, src, dest, description)
}

func (c *Collector) writePatternSummary(storage pveStorageEntry, analysisDir string, writers []*patternWriter, totalFiles, totalSize int64) error {
	// Skip file creation in dry-run mode
	if c.dryRun {
		c.logger.Debug("[DRY RUN] Would write backup summary for datastore: %s", storage.Name)
		return nil
	}

	summaryPath := filepath.Join(analysisDir, fmt.Sprintf("%s_backup_summary.txt", storage.Name))
	file, err := os.OpenFile(summaryPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	fmt.Fprintf(writer, "# PVE Backup Files Summary for datastore: %s\n", storage.Name)
	fmt.Fprintf(writer, "# Path: %s\n", storage.Path)
	fmt.Fprintf(writer, "# Generated on: %s\n\n", time.Now().Format(time.RFC3339))

	for _, w := range writers {
		fmt.Fprintf(writer, "## Files matching pattern: %s\n", w.pattern)
		if w.count == 0 {
			fmt.Fprintln(writer, "  No files found")
			fmt.Fprintln(writer)
			continue
		}
		fmt.Fprintf(writer, "  Files: %d\n", w.count)
		if w.errorCount > 0 {
			fmt.Fprintf(writer, "  Successfully analyzed: %d\n", w.count-w.errorCount)
			fmt.Fprintf(writer, "  Files with errors: %d\n", w.errorCount)
		}
		fmt.Fprintf(writer, "  Total size: %s\n\n", FormatBytes(w.totalSize))
	}

	fmt.Fprintln(writer, "## Overall Summary")
	fmt.Fprintf(writer, "Total backup files: %d\n", totalFiles)
	fmt.Fprintf(writer, "Total backup size: %s\n", FormatBytes(totalSize))
	if err := writer.Flush(); err != nil {
		return err
	}
	return file.Close()
}

func (c *Collector) collectPVECephInfo(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if !c.isCephConfigured(ctx) {
		c.logger.Warning("Skipping Ceph collection: not detected. If unused, set BACKUP_CEPH_CONFIG=false to disable.")
		return nil
	}

	c.logger.Debug("Collecting Ceph cluster information")

	cephDir := filepath.Join(c.tempDir, "var/lib/pve-cluster/info/ceph")
	if err := c.ensureDir(cephDir); err != nil {
		return fmt.Errorf("failed to create ceph directory: %w", err)
	}

	for _, cephPath := range c.cephConfigPaths() {
		if info, err := os.Stat(cephPath); err == nil && info.IsDir() {
			if err := c.safeCopyDir(ctx,
				cephPath,
				c.targetPathFor(cephPath),
				fmt.Sprintf("Ceph configuration (%s)", cephPath)); err != nil {
				c.logger.Debug("Failed to copy Ceph configuration from %s: %v", cephPath, err)
			}
		}
	}

	if _, err := c.depLookPath("ceph"); err != nil {
		c.logger.Debug("Ceph CLI not available, skipping Ceph command outputs")
		return nil
	}

	commands := []struct {
		cmd  string
		file string
		desc string
	}{
		{"ceph -s", "ceph_status.txt", "Ceph status"},
		{"ceph osd df", "ceph_osd_df.txt", "Ceph OSD DF"},
		{"ceph osd tree", "ceph_osd_tree.txt", "Ceph OSD tree"},
		{"ceph mon stat", "ceph_mon_stat.txt", "Ceph mon stat"},
		{"ceph pg stat", "ceph_pg_stat.txt", "Ceph PG stat"},
		{"ceph health detail", "ceph_health.txt", "Ceph health"},
	}

	for _, command := range commands {
		c.captureCommandOutput(ctx,
			command.cmd,
			filepath.Join(cephDir, command.file),
			command.desc,
			false)
	}

	c.logger.Debug("Ceph information collection completed")
	return nil
}

func (c *Collector) createPVEInfoAliases(ctx context.Context) error {
	baseInfoDir := filepath.Join(c.tempDir, "var/lib/pve-cluster/info")
	if err := c.ensureDir(baseInfoDir); err != nil {
		return fmt.Errorf("failed to prepare PVE info directory: %w", err)
	}

	aliasMap := []struct {
		source string
		target string
	}{
		{
			source: filepath.Join(c.proxsaveCommandsDir("pve"), "nodes_status.json"),
			target: filepath.Join(baseInfoDir, "nodes_status.json"),
		},
		{
			source: filepath.Join(c.proxsaveCommandsDir("pve"), "storage_status.json"),
			target: filepath.Join(baseInfoDir, "storage_status.json"),
		},
		{
			source: filepath.Join(c.proxsaveCommandsDir("pve"), "pve_users.json"),
			target: filepath.Join(baseInfoDir, "user_list.json"),
		},
		{
			source: filepath.Join(c.proxsaveCommandsDir("pve"), "pve_groups.json"),
			target: filepath.Join(baseInfoDir, "group_list.json"),
		},
		{
			source: filepath.Join(c.proxsaveCommandsDir("pve"), "pve_roles.json"),
			target: filepath.Join(baseInfoDir, "role_list.json"),
		},
	}

	for _, entry := range aliasMap {
		if err := c.copyIfExists(entry.source, entry.target, "PVE info alias"); err != nil {
			c.logger.Debug("Skipping alias for %s: %v", entry.source, err)
		}
	}

	jobsDir := filepath.Join(baseInfoDir, "jobs")
	if err := c.ensureDir(jobsDir); err == nil {
		aggregatedHistory := filepath.Join(jobsDir, "backup_history.json")
		if err := c.aggregateBackupHistory(ctx, jobsDir, aggregatedHistory); err != nil {
			c.logger.Debug("Failed to aggregate backup history: %v", err)
		}
	} else {
		c.logger.Debug("Failed to prepare jobs directory for aliases: %v", err)
	}

	replicationDir := filepath.Join(baseInfoDir, "replication")
	if err := c.ensureDir(replicationDir); err == nil {
		aggregatedStatus := filepath.Join(replicationDir, "replication_status.json")
		if err := c.aggregateReplicationStatus(ctx, replicationDir, aggregatedStatus); err != nil {
			c.logger.Debug("Failed to aggregate replication status: %v", err)
		}
		// Ensure replication_jobs.json exists by copying the collected one if present
		sourceJobs := filepath.Join(replicationDir, "replication_jobs.json")
		if _, err := os.Stat(sourceJobs); err != nil {
			// replication_jobs.json was not yet created; attempt to copy from temp path used earlier
			backupJobsPath := filepath.Join(baseInfoDir, "jobs", "replication_jobs.json")
			_ = c.copyIfExists(backupJobsPath, sourceJobs, "replication jobs alias")
		}
	} else {
		c.logger.Debug("Failed to prepare replication directory for aliases: %v", err)
	}

	if err := c.writePVEVersionInfo(ctx, baseInfoDir); err != nil {
		c.logger.Debug("Failed to write pve_version.txt: %v", err)
	}

	return nil
}

func (c *Collector) copyIfExists(source, target, description string) error {
	if _, err := os.Stat(source); err != nil {
		return err
	}
	return c.safeCopyFile(context.Background(), source, target, description)
}

func (c *Collector) aggregateBackupHistory(ctx context.Context, jobsDir, target string) error {
	entries, err := os.ReadDir(jobsDir)
	if err != nil {
		return err
	}

	var buffers []json.RawMessage
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, "_backup_history.json") {
			continue
		}
		path := filepath.Join(jobsDir, name)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			c.logger.Debug("Failed to read %s: %v", path, readErr)
			continue
		}
		buffers = append(buffers, json.RawMessage(data))
	}

	var output []byte
	if len(buffers) == 0 {
		output = []byte("[]")
	} else if len(buffers) == 1 {
		output = buffers[0]
	} else {
		output = []byte("[\n")
		for i, buf := range buffers {
			output = append(output, buf...)
			if i < len(buffers)-1 {
				output = append(output, []byte(",\n")...)
			}
		}
		output = append(output, []byte("\n]")...)
	}

	return c.writeReportFile(target, output)
}

func (c *Collector) aggregateReplicationStatus(ctx context.Context, replicationDir, target string) error {
	entries, err := os.ReadDir(replicationDir)
	if err != nil {
		return err
	}

	var buffers []json.RawMessage
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, "_replication_status.json") {
			continue
		}
		path := filepath.Join(replicationDir, name)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			c.logger.Debug("Failed to read %s: %v", path, readErr)
			continue
		}
		buffers = append(buffers, json.RawMessage(data))
	}

	var output []byte
	if len(buffers) == 0 {
		output = []byte("[]")
	} else if len(buffers) == 1 {
		output = buffers[0]
	} else {
		output = []byte("[\n")
		for i, buf := range buffers {
			output = append(output, buf...)
			if i < len(buffers)-1 {
				output = append(output, []byte(",\n")...)
			}
		}
		output = append(output, []byte("\n]")...)
	}
	return c.writeReportFile(target, output)
}

func (c *Collector) writePVEVersionInfo(ctx context.Context, baseInfoDir string) error {
	versionFile := filepath.Join(baseInfoDir, "pve_version.txt")
	if err := c.safeCmdOutput(ctx, "pveversion", versionFile, "PVE version info", false); err != nil {
		return err
	}
	return nil
}

func (c *Collector) augmentStoragesWithConfig(storages []pveStorageEntry) []pveStorageEntry {
	configEntries := c.parseStorageConfigEntries()
	if len(configEntries) == 0 {
		return storages
	}

	merged := make(map[string]pveStorageEntry, len(storages)+len(configEntries))
	for _, entry := range storages {
		name := strings.ToLower(strings.TrimSpace(entry.Name))
		if name == "" {
			continue
		}
		merged[name] = entry
	}

	for _, entry := range configEntries {
		key := strings.ToLower(strings.TrimSpace(entry.Name))
		if key == "" {
			continue
		}
		existing, ok := merged[key]
		if !ok {
			merged[key] = entry
			continue
		}
		if strings.TrimSpace(existing.Path) == "" && strings.TrimSpace(entry.Path) != "" {
			existing.Path = entry.Path
		}
		if strings.TrimSpace(existing.Type) == "" && strings.TrimSpace(entry.Type) != "" {
			existing.Type = entry.Type
		}
		if strings.TrimSpace(existing.Content) == "" && strings.TrimSpace(entry.Content) != "" {
			existing.Content = entry.Content
		}
		merged[key] = existing
	}

	result := make([]pveStorageEntry, 0, len(merged))
	for _, entry := range merged {
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result
}

func (c *Collector) parseStorageConfigEntries() []pveStorageEntry {
	storageCfg := filepath.Join(c.effectivePVEConfigPath(), "storage.cfg")
	file, err := os.Open(storageCfg)
	if err != nil {
		return nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var (
		entries []pveStorageEntry
		current *pveStorageEntry
	)

	flushCurrent := func() {
		if current == nil {
			return
		}
		if strings.TrimSpace(current.Name) != "" {
			entries = append(entries, *current)
		}
		current = nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if idx := strings.Index(trimmed, ":"); idx > 0 {
			kind := strings.TrimSpace(trimmed[:idx])
			name := strings.TrimSpace(trimmed[idx+1:])
			if kind != "" && name != "" && !strings.Contains(kind, " ") {
				flushCurrent()
				current = &pveStorageEntry{
					Name: name,
					Type: kind,
				}
				continue
			}
		}

		if current == nil {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		key := fields[0]
		value := strings.TrimSpace(trimmed[len(key):])
		value = strings.Trim(value, "\"")
		switch key {
		case "path":
			current.Path = value
		case "content":
			current.Content = strings.ReplaceAll(value, " ", "")
		}
	}
	if err := scanner.Err(); err != nil {
		c.logger.Debug("Failed to parse storage.cfg: %v", err)
	}
	flushCurrent()
	return entries
}

func (c *Collector) describeDiskUsage(path string) (string, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return "", err
	}
	total := int64(stat.Blocks) * int64(stat.Bsize)
	available := int64(stat.Bavail) * int64(stat.Bsize)
	used := total - available
	if total <= 0 {
		return "", fmt.Errorf("invalid filesystem statistics for %s", path)
	}
	return fmt.Sprintf("Used: %s / Total: %s (Free: %s)",
		FormatBytes(used),
		FormatBytes(total),
		FormatBytes(available),
	), nil
}

func (c *Collector) sampleMetadataFileStats(ctx context.Context, root string, maxDepth, limit int) ([]string, error) {
	lines := make([]string, 0, limit)
	if limit <= 0 {
		return lines, nil
	}

	root = filepath.Clean(root)
	stopErr := errors.New("metadata sample limit reached")

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		depth := relativeDepth(root, path)
		if d.IsDir() {
			if depth >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		line := fmt.Sprintf("%s %d %s",
			info.ModTime().Format(time.RFC3339),
			info.Size(),
			path,
		)
		lines = append(lines, line)
		if len(lines) >= limit {
			return stopErr
		}
		return nil
	})

	if err != nil && !errors.Is(err, stopErr) {
		return lines, err
	}
	return lines, nil
}

func (c *Collector) writeDatastoreMetadataText(
	metaDir string,
	storage pveStorageEntry,
	dirSamples []string,
	dirErr error,
	diskUsage string,
	diskErr error,
	fileSamples []string,
	fileErr error,
) error {
	var builder strings.Builder
	metadataErrors := 0

	fmt.Fprintf(&builder, "# Datastore: %s\n", storage.Name)
	fmt.Fprintf(&builder, "# Path: %s\n", storage.Path)
	if storage.Type != "" {
		fmt.Fprintf(&builder, "# Type: %s\n", storage.Type)
	}
	if storage.Content != "" {
		fmt.Fprintf(&builder, "# Content: %s\n", storage.Content)
	}
	fmt.Fprintf(&builder, "# Scanned on: %s\n\n", time.Now().Format(time.RFC3339))

	builder.WriteString("## Directory Structure (max 2 levels):\n")
	if len(dirSamples) == 0 {
		metadataErrors++
		builder.WriteString("# Error: Unable to scan directory structure\n")
		if dirErr != nil {
			fmt.Fprintf(&builder, "# CAUSE: %v\n", dirErr)
		}
		builder.WriteString("\n")
	} else {
		limit := dirSamples
		if len(limit) > 20 {
			limit = limit[:20]
		}
		for _, rel := range limit {
			if rel == "" || rel == "." {
				builder.WriteString(storage.Path)
			} else {
				builder.WriteString(filepath.Join(storage.Path, filepath.FromSlash(rel)))
			}
			builder.WriteByte('\n')
		}
		if len(dirSamples) > len(limit) {
			builder.WriteString("# ... output truncated ...\n")
		}
		builder.WriteString("\n")
	}

	builder.WriteString("## Disk Usage:\n")
	if diskUsage == "" {
		metadataErrors++
		builder.WriteString("# Error: Unable to calculate disk usage\n")
		if diskErr != nil {
			fmt.Fprintf(&builder, "# CAUSE: %v\n", diskErr)
		}
		builder.WriteString("\n")
	} else {
		builder.WriteString(diskUsage)
		builder.WriteString("\n\n")
	}

	builder.WriteString("## File Types (sample):\n")
	if len(fileSamples) == 0 {
		metadataErrors++
		builder.WriteString("# No sample files available\n")
		if fileErr != nil {
			fmt.Fprintf(&builder, "# CAUSE: %v\n", fileErr)
		}
		builder.WriteString("\n")
	} else {
		for _, line := range fileSamples {
			builder.WriteString(line)
			builder.WriteByte('\n')
		}
		builder.WriteString("\n")
	}

	if metadataErrors > 0 {
		builder.WriteString("## Data Quality Notes\n")
		fmt.Fprintf(&builder, "WARNING: Metadata collection encountered %d issue(s)\n", metadataErrors)
		builder.WriteString("This datastore information may be incomplete\n")
	}

	return c.writeReportFile(filepath.Join(metaDir, "metadata.txt"), []byte(builder.String()))
}

func relativeDepth(root, path string) int {
	if root == path {
		return 0
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return 0
	}
	if rel == "." {
		return 0
	}
	rel = filepath.ToSlash(rel)
	return strings.Count(rel, "/") + 1
}

// isClusteredPVE checks if this is a clustered PVE system using multiple heuristics
func (c *Collector) isClusteredPVE(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	corosyncPath := c.effectiveCorosyncConfigPath()
	corosyncExists := false
	if corosyncPath != "" {
		if _, err := c.depStat(corosyncPath); err == nil {
			corosyncExists = true
		}
	}
	c.logger.Debug("Cluster detection: corosyncPath=%q exists=%v", corosyncPath, corosyncExists)

	if c.hasCorosyncClusterConfig() {
		c.logger.Debug("Detected cluster via corosync configuration")
		return true, nil
	}

	nodeCount, nodeErr := c.pveNodesDirCount()
	if nodeErr != nil {
		c.logger.Debug("Cluster detection: nodes directory count unavailable: %v", nodeErr)
	} else {
		c.logger.Debug("Cluster detection: nodes directory count=%d", nodeCount)
		if nodeCount > 1 {
			if corosyncExists {
				c.logger.Debug("Detected cluster via nodes directory count (corosync config present)")
				return true, nil
			}
			c.logger.Debug("Cluster detection: nodes directory suggests cluster, but corosync config missing; deferring to service/pvecm checks")
		}
	}

	corosyncActive := c.isServiceActive(ctx, "corosync.service")
	c.logger.Debug("Cluster detection: corosync.service active=%v", corosyncActive)
	if corosyncActive {
		if corosyncExists {
			c.logger.Debug("Detected cluster via corosync.service state (corosync config present)")
			return true, nil
		}
		c.logger.Debug("Cluster detection: corosync.service active but corosync config missing; deferring to pvecm check")
	}

	// Fallback to pvecm status
	if _, err := c.depLookPath("pvecm"); err == nil {
		output, err := c.depRunCommand(ctx, "pvecm", "status")
		if err != nil {
			outText := strings.TrimSpace(string(output))
			if isPvecmMissingCorosyncConfig(outText) {
				c.logger.Debug("Cluster detection: pvecm status indicates no corosync configuration; treating as standalone. Output: %s", summarizeCommandOutputText(outText))
				return false, nil
			}
			return false, fmt.Errorf("pvecm status failed: %w", err)
		}
		clustered := strings.Contains(string(output), "Cluster information")
		c.logger.Debug("pvecm status detected clustered=%v", clustered)
		if clustered {
			return true, nil
		}
	}

	c.logger.Debug("Cluster heuristics indicate standalone PVE node")
	return false, nil
}

func shortHostname(host string) string {
	if idx := strings.Index(host, "."); idx > 0 {
		return host[:idx]
	}
	return host
}

func (c *Collector) hasCorosyncClusterConfig() bool {
	corosyncPath := c.effectiveCorosyncConfigPath()
	data, err := os.ReadFile(corosyncPath)
	if err != nil {
		return false
	}
	content := strings.ToLower(string(data))
	for _, key := range []string{"cluster_name", "nodelist", "ring0_addr"} {
		if strings.Contains(content, key) {
			return true
		}
	}
	return false
}

func (c *Collector) effectiveCorosyncConfigPath() string {
	corosyncPath := strings.TrimSpace(c.config.CorosyncConfigPath)
	if corosyncPath == "" {
		return filepath.Join(c.effectivePVEConfigPath(), "corosync.conf")
	}
	if filepath.IsAbs(corosyncPath) {
		return corosyncPath
	}
	return filepath.Join(c.effectivePVEConfigPath(), corosyncPath)
}

func (c *Collector) hasMultiplePVENodes() bool {
	count, err := c.pveNodesDirCount()
	return err == nil && count > 1
}

func (c *Collector) pveNodesDirCount() (int, error) {
	nodesDir := filepath.Join(c.effectivePVEConfigPath(), "nodes")
	entries, err := os.ReadDir(nodesDir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			count++
		}
	}
	return count, nil
}

func isPvecmMissingCorosyncConfig(output string) bool {
	if output == "" {
		return false
	}
	lower := strings.ToLower(output)
	return strings.Contains(lower, "corosync config") && strings.Contains(lower, "does not exist")
}

func (c *Collector) isServiceActive(ctx context.Context, service string) bool {
	if service == "" {
		return false
	}
	if _, err := c.depLookPath("systemctl"); err != nil {
		return false
	}
	if _, err := c.depRunCommand(ctx, "systemctl", "is-active", service); err == nil {
		return true
	}
	return false
}

func (c *Collector) isCephConfigured(ctx context.Context) bool {
	for _, path := range c.cephConfigPaths() {
		if cephHasClusterConfig(path) || cephHasKeyring(path) {
			return true
		}
	}
	if c.cephServiceActive(ctx) {
		return true
	}
	if c.cephStorageConfigured(ctx) {
		return true
	}
	if c.cephStatusAvailable(ctx) {
		return true
	}
	if c.cephProcessesRunning(ctx) {
		return true
	}
	return false
}

func (c *Collector) cephConfigPaths() []string {
	paths := make([]string, 0, 3)
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		abs := path
		if !filepath.IsAbs(path) {
			abs = filepath.Clean(filepath.Join("/", path))
		}
		abs = c.systemPath(abs)
		for _, existing := range paths {
			if existing == abs {
				return
			}
		}
		paths = append(paths, abs)
	}

	if c.config.CephConfigPath != "" {
		add(c.config.CephConfigPath)
	}
	add(filepath.Join(c.effectivePVEConfigPath(), "ceph"))
	add("/etc/ceph")
	return paths
}

func cephHasClusterConfig(path string) bool {
	confPath := filepath.Join(path, "ceph.conf")
	data, err := os.ReadFile(confPath)
	if err != nil {
		return false
	}
	content := strings.ToLower(string(data))
	for _, key := range []string{"fsid", "mon_host", "mon_initial_members"} {
		if strings.Contains(content, key) {
			return true
		}
	}
	return false
}

func cephHasKeyring(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	found := false
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".keyring") {
			found = true
			return errStopWalk
		}
		return nil
	})
	if errors.Is(err, errStopWalk) {
		return true
	}
	return found
}

func (c *Collector) cephServiceActive(ctx context.Context) bool {
	hostname, _ := os.Hostname()
	short := shortHostname(hostname)
	services := []string{
		"ceph.target",
		"ceph-mon",
		"ceph-osd",
		"ceph-mds",
		"ceph-mgr",
	}
	if short != "" {
		services = append(services,
			fmt.Sprintf("ceph-mon@%s", short),
			fmt.Sprintf("ceph-osd@%s", short),
			fmt.Sprintf("ceph-mds@%s", short),
			fmt.Sprintf("ceph-mgr@%s", short))
	}
	if hostname != "" && hostname != short {
		services = append(services,
			fmt.Sprintf("ceph-mon@%s", hostname),
			fmt.Sprintf("ceph-osd@%s", hostname),
			fmt.Sprintf("ceph-mds@%s", hostname),
			fmt.Sprintf("ceph-mgr@%s", hostname))
	}
	for _, svc := range services {
		if c.isServiceActive(ctx, svc) {
			return true
		}
	}
	return false
}

func (c *Collector) cephStorageConfigured(ctx context.Context) bool {
	if _, err := c.depLookPath("pvesm"); err != nil {
		return false
	}
	output, err := c.depRunCommand(ctx, "pvesm", "status")
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(output))
	return strings.Contains(lower, "cephfs") || strings.Contains(lower, "rbd")
}

func (c *Collector) cephStatusAvailable(ctx context.Context) bool {
	if _, err := c.depLookPath("ceph"); err != nil {
		return false
	}
	statusCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := c.depRunCommand(statusCtx, "ceph", "-s"); err == nil {
		return true
	}
	return false
}

func (c *Collector) cephProcessesRunning(ctx context.Context) bool {
	if _, err := c.depLookPath("pgrep"); err != nil {
		return false
	}
	if _, err := c.depRunCommand(ctx, "pgrep", "-f", "ceph-"); err == nil {
		return true
	}
	return false
}
