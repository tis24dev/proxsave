package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (c *Collector) pbsConfigPath() string {
	if c.config != nil && c.config.PBSConfigPath != "" {
		return c.systemPath(c.config.PBSConfigPath)
	}
	return c.systemPath("/etc/proxmox-backup")
}

// collectPBSConfigFile collects a single PBS configuration file with detailed logging
func (c *Collector) collectPBSConfigFile(ctx context.Context, root, filename, description string, enabled bool) ManifestEntry {
	if !enabled {
		c.logger.Debug("Skipping %s: disabled by configuration", filename)
		c.logger.Info("  %s: disabled", description)
		return ManifestEntry{Status: StatusDisabled}
	}

	srcPath := filepath.Join(root, filename)
	destPath := filepath.Join(c.tempDir, "etc/proxmox-backup", filename)

	if c.shouldExclude(srcPath) || c.shouldExclude(destPath) {
		c.logger.Debug("Skipping %s: excluded by pattern", filename)
		c.logger.Info("  %s: skipped (excluded)", description)
		c.incFilesSkipped()
		return ManifestEntry{Status: StatusSkipped}
	}

	c.logger.Debug("Checking %s: %s", filename, srcPath)

	info, err := os.Stat(srcPath)
	if os.IsNotExist(err) {
		c.incFilesNotFound()
		c.logger.Debug("  File not found: %v", err)
		c.logger.Info("  %s: not configured", description)
		return ManifestEntry{Status: StatusNotFound}
	}
	if err != nil {
		c.incFilesFailed()
		c.logger.Debug("  Stat error: %v", err)
		c.logger.Warning("  %s: failed - %v", description, err)
		return ManifestEntry{Status: StatusFailed, Error: err.Error()}
	}

	// Log file details in debug mode
	c.logger.Debug("  File exists, size=%d, mode=%s, mtime=%s",
		info.Size(), info.Mode(), info.ModTime().Format(time.RFC3339))
	c.logger.Debug("  Copying to %s", destPath)

	if err := c.safeCopyFile(ctx, srcPath, destPath, description); err != nil {
		c.logger.Warning("  %s: failed - %v", description, err)
		return ManifestEntry{Status: StatusFailed, Error: err.Error()}
	}

	c.logger.Info("  %s: collected (%s)", description, FormatBytes(info.Size()))
	return ManifestEntry{Status: StatusCollected, Size: info.Size()}
}

// CollectPBSConfigs collects Proxmox Backup Server specific configurations
func (c *Collector) CollectPBSConfigs(ctx context.Context) error {
	c.logger.Info("Collecting PBS configurations")
	c.logger.Debug("Validating PBS environment before collection")

	// Check if we're actually on PBS
	pbsConfigPath := c.pbsConfigPath()
	if _, err := os.Stat(pbsConfigPath); os.IsNotExist(err) {
		return fmt.Errorf("not a PBS system: %s not found", pbsConfigPath)
	}
	c.logger.Debug("Detected %s, proceeding with PBS collection", pbsConfigPath)

	// Collect PBS directories
	c.logger.Debug("Collecting PBS configuration directories")
	if err := c.collectPBSDirectories(ctx, pbsConfigPath); err != nil {
		return fmt.Errorf("failed to collect PBS directories: %w", err)
	}
	c.logger.Debug("PBS directory collection completed")

	datastores, err := c.getDatastoreList(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return fmt.Errorf("failed to detect PBS datastores: %w", err)
	}
	c.logger.Debug("Detected %d PBS datastores", len(datastores))

	if len(datastores) == 0 {
		c.logger.Info("Found 0 PBS datastore(s) via auto-detection")
	} else {
		summary := make([]string, 0, len(datastores))
		for _, ds := range datastores {
			if ds.Path != "" {
				summary = append(summary, fmt.Sprintf("%s (%s)", ds.Name, ds.Path))
			} else {
				summary = append(summary, ds.Name)
			}
		}
		c.logger.Info("Found %d PBS datastore(s) via auto-detection: %s", len(datastores), strings.Join(summary, ", "))
	}

	// Collect PBS commands output
	c.logger.Debug("Collecting PBS command outputs and state")
	if err := c.collectPBSCommands(ctx, datastores); err != nil {
		return fmt.Errorf("failed to collect PBS commands: %w", err)
	}
	c.logger.Debug("PBS command output collection completed")

	// Collect datastore inventory (mounts, paths, config snapshots)
	c.logger.Debug("Collecting PBS datastore inventory report")
	if err := c.collectPBSDatastoreInventory(ctx, datastores); err != nil {
		c.logger.Warning("Failed to collect PBS datastore inventory report: %v", err)
	} else {
		c.logger.Debug("PBS datastore inventory report completed")
	}

	// Collect datastore configurations
	if c.config.BackupDatastoreConfigs {
		c.logger.Debug("Collecting datastore configuration files and namespaces")
		if err := c.collectDatastoreConfigs(ctx, datastores); err != nil {
			c.logger.Warning("Failed to collect datastore configs: %v", err)
			// Non-fatal, continue
		} else {
			c.logger.Debug("Datastore configuration collection completed")
		}
	} else {
		c.logger.Skip("PBS datastore configuration backup disabled.")
	}

	// Collect user/ACL configurations
	if c.config.BackupUserConfigs {
		c.logger.Debug("Collecting PBS user and ACL configurations")
		if err := c.collectUserConfigs(ctx); err != nil {
			c.logger.Warning("Failed to collect user configs: %v", err)
			// Non-fatal, continue
		} else {
			c.logger.Debug("User configuration collection completed")
		}
	} else {
		c.logger.Skip("PBS user/ACL backup disabled.")
	}

	if c.config.BackupPxarFiles {
		c.logger.Debug("Collecting PXAR metadata for datastores")
		if err := c.collectPBSPxarMetadata(ctx, datastores); err != nil {
			c.logger.Warning("Failed to collect PBS PXAR metadata: %v", err)
		} else {
			c.logger.Debug("PXAR metadata collection completed")
		}
	} else {
		c.logger.Skip("PBS PXAR metadata collection disabled.")
	}

	// Print collection summary
	c.logger.Info("PBS collection summary:")
	c.logger.Info("  Files collected: %d", c.stats.FilesProcessed)
	c.logger.Info("  Files not found: %d", c.stats.FilesNotFound)
	if c.stats.FilesFailed > 0 {
		c.logger.Warning("  Files failed: %d", c.stats.FilesFailed)
	}
	c.logger.Debug("  Files skipped: %d", c.stats.FilesSkipped)
	c.logger.Debug("  Bytes collected: %d", c.stats.BytesCollected)

	c.logger.Info("PBS configuration collection completed")
	return nil
}

// collectPBSDirectories collects PBS-specific directories
func (c *Collector) collectPBSDirectories(ctx context.Context, root string) error {
	c.logger.Debug("Collecting PBS directories (source=%s, dest=%s)",
		root, filepath.Join(c.tempDir, "etc/proxmox-backup"))

	// Even though we keep a full snapshot of /etc/proxmox-backup (or PBS_CONFIG_PATH),
	// treat per-feature flags as exclusions so users can selectively omit sensitive files
	// while still capturing unknown/new PBS config files.
	//
	// NOTE: These patterns are applied only for the duration of the directory snapshot to
	// avoid impacting other collectors.
	var extraExclude []string
	if !c.config.BackupDatastoreConfigs {
		extraExclude = append(extraExclude, "datastore.cfg")
	}
	if !c.config.BackupUserConfigs {
		// User-related configs are intentionally excluded together.
		extraExclude = append(extraExclude, "user.cfg", "acl.cfg", "domains.cfg")
	}
	if !c.config.BackupRemoteConfigs {
		extraExclude = append(extraExclude, "remote.cfg")
	}
	if !c.config.BackupSyncJobs {
		extraExclude = append(extraExclude, "sync.cfg")
	}
		if !c.config.BackupVerificationJobs {
			extraExclude = append(extraExclude, "verification.cfg")
		}
		if !c.config.BackupTapeConfigs {
			extraExclude = append(extraExclude, "tape.cfg", "tape-job.cfg", "media-pool.cfg", "tape-encryption-keys.json")
		}
		if !c.config.BackupNetworkConfigs {
			extraExclude = append(extraExclude, "network.cfg")
		}
	if !c.config.BackupPruneSchedules {
		extraExclude = append(extraExclude, "prune.cfg")
	}

	// PBS main configuration directory (full backup)
	if len(extraExclude) > 0 {
		c.logger.Debug("PBS config exclusions enabled (disabled features): %s", strings.Join(extraExclude, ", "))
	}
	if err := c.withTemporaryExcludes(extraExclude, func() error {
		return c.safeCopyDir(ctx,
			root,
			filepath.Join(c.tempDir, "etc/proxmox-backup"),
			"PBS configuration")
	}); err != nil {
		return err
	}

	// Initialize manifest for PBS configs
	c.pbsManifest = make(map[string]ManifestEntry)

	c.logger.Info("Collecting PBS configuration files:")

		// Datastore configuration
		c.pbsManifest["datastore.cfg"] = c.collectPBSConfigFile(ctx, root, "datastore.cfg",
			"Datastore configuration", c.config.BackupDatastoreConfigs)

		// S3 endpoint configuration (used by S3 datastores)
		c.pbsManifest["s3.cfg"] = c.collectPBSConfigFile(ctx, root, "s3.cfg",
			"S3 endpoints", c.config.BackupDatastoreConfigs)

		// Node configuration (global PBS settings)
		c.pbsManifest["node.cfg"] = c.collectPBSConfigFile(ctx, root, "node.cfg",
			"Node configuration", true)

		// ACME configuration (accounts/plugins)
		c.pbsManifest["acme/accounts.cfg"] = c.collectPBSConfigFile(ctx, root, filepath.Join("acme", "accounts.cfg"),
			"ACME accounts", true)
		c.pbsManifest["acme/plugins.cfg"] = c.collectPBSConfigFile(ctx, root, filepath.Join("acme", "plugins.cfg"),
			"ACME plugins", true)

		// External metric servers
		c.pbsManifest["metricserver.cfg"] = c.collectPBSConfigFile(ctx, root, "metricserver.cfg",
			"External metric servers", true)

		// Traffic control
		c.pbsManifest["traffic-control.cfg"] = c.collectPBSConfigFile(ctx, root, "traffic-control.cfg",
			"Traffic control rules", true)

		// User configuration
		c.pbsManifest["user.cfg"] = c.collectPBSConfigFile(ctx, root, "user.cfg",
			"User configuration", c.config.BackupUserConfigs)

	// ACL configuration (under user configs flag)
	c.pbsManifest["acl.cfg"] = c.collectPBSConfigFile(ctx, root, "acl.cfg",
		"ACL configuration", c.config.BackupUserConfigs)

	// Remote configuration (for sync jobs)
	c.pbsManifest["remote.cfg"] = c.collectPBSConfigFile(ctx, root, "remote.cfg",
		"Remote configuration", c.config.BackupRemoteConfigs)

	// Sync jobs configuration
	c.pbsManifest["sync.cfg"] = c.collectPBSConfigFile(ctx, root, "sync.cfg",
		"Sync jobs", c.config.BackupSyncJobs)

	// Verification jobs configuration
	c.pbsManifest["verification.cfg"] = c.collectPBSConfigFile(ctx, root, "verification.cfg",
		"Verification jobs", c.config.BackupVerificationJobs)

		// Tape backup configuration
		c.pbsManifest["tape.cfg"] = c.collectPBSConfigFile(ctx, root, "tape.cfg",
			"Tape configuration", c.config.BackupTapeConfigs)

		// Tape jobs (under tape configs flag)
		c.pbsManifest["tape-job.cfg"] = c.collectPBSConfigFile(ctx, root, "tape-job.cfg",
			"Tape jobs", c.config.BackupTapeConfigs)

		// Media pool configuration (under tape configs flag)
		c.pbsManifest["media-pool.cfg"] = c.collectPBSConfigFile(ctx, root, "media-pool.cfg",
			"Media pool configuration", c.config.BackupTapeConfigs)

		// Tape encryption keys (under tape configs flag)
		c.pbsManifest["tape-encryption-keys.json"] = c.collectPBSConfigFile(ctx, root, "tape-encryption-keys.json",
			"Tape encryption keys", c.config.BackupTapeConfigs)

	// Network configuration
	c.pbsManifest["network.cfg"] = c.collectPBSConfigFile(ctx, root, "network.cfg",
		"Network configuration", c.config.BackupNetworkConfigs)

	// Prune/GC schedules
	c.pbsManifest["prune.cfg"] = c.collectPBSConfigFile(ctx, root, "prune.cfg",
		"Prune schedules", c.config.BackupPruneSchedules)

	c.logger.Debug("PBS directory collection finished")
	return nil
}

// collectPBSCommands collects output from PBS commands
func (c *Collector) collectPBSCommands(ctx context.Context, datastores []pbsDatastore) error {
	commandsDir := c.proxsaveCommandsDir("pbs")
	if err := c.ensureDir(commandsDir); err != nil {
		return fmt.Errorf("failed to create commands directory: %w", err)
	}
	c.logger.Debug("Collecting PBS command outputs into %s", commandsDir)

	// PBS version (CRITICAL)
	if err := c.collectCommandMulti(ctx,
		"proxmox-backup-manager version",
		filepath.Join(commandsDir, "pbs_version.txt"),
		"PBS version",
		true); err != nil {
		return fmt.Errorf("failed to get PBS version (critical): %w", err)
	}

	// Node configuration
	c.safeCmdOutput(ctx,
		"proxmox-backup-manager node show --output-format=json",
		filepath.Join(commandsDir, "node_config.json"),
		"Node configuration",
		false)

	// Datastore status
	if err := c.collectCommandMulti(ctx,
		"proxmox-backup-manager datastore list --output-format=json",
		filepath.Join(commandsDir, "datastore_list.json"),
		"Datastore list",
		false); err != nil {
		return err
	}

	// Datastore usage details
	if c.config.BackupDatastoreConfigs && len(datastores) > 0 {
		for _, ds := range datastores {
			c.safeCmdOutput(ctx,
				fmt.Sprintf("proxmox-backup-manager datastore show %s --output-format=json", ds.Name),
				filepath.Join(commandsDir, fmt.Sprintf("datastore_%s_status.json", ds.Name)),
				fmt.Sprintf("Datastore %s status", ds.Name),
				false)
		}
	}

	// ACME (accounts, plugins)
	c.collectPBSAcmeSnapshots(ctx, commandsDir)

	// Notifications (targets, matchers, endpoints)
	c.collectPBSNotificationSnapshots(ctx, commandsDir)

	// User list
	if c.config.BackupUserConfigs {
		if err := c.collectCommandMulti(ctx,
			"proxmox-backup-manager user list --output-format=json",
			filepath.Join(commandsDir, "user_list.json"),
			"User list",
			false); err != nil {
			return err
		}

		// Authentication realms (LDAP/AD/OpenID)
		c.collectPBSRealmSnapshots(ctx, commandsDir)

		// ACL list
		if err := c.collectCommandMulti(ctx,
			"proxmox-backup-manager acl list --output-format=json",
			filepath.Join(commandsDir, "acl_list.json"),
			"ACL list",
			false); err != nil {
			return err
		}
	}

	// Remote list (sync sources)
	if c.config.BackupRemoteConfigs {
		if err := c.collectCommandMulti(ctx,
			"proxmox-backup-manager remote list --output-format=json",
			filepath.Join(commandsDir, "remote_list.json"),
			"Remote list",
			false); err != nil {
			return err
		}
	}

	// Sync jobs status
	if c.config.BackupSyncJobs {
		if err := c.collectCommandMulti(ctx,
			"proxmox-backup-manager sync-job list --output-format=json",
			filepath.Join(commandsDir, "sync_jobs.json"),
			"Sync jobs",
			false); err != nil {
			return err
		}
	}

	// Verification jobs status
	if c.config.BackupVerificationJobs {
		if err := c.collectCommandMulti(ctx,
			"proxmox-backup-manager verify-job list --output-format=json",
			filepath.Join(commandsDir, "verification_jobs.json"),
			"Verification jobs",
			false); err != nil {
			return err
		}
	}

	// Prune jobs
	if c.config.BackupPruneSchedules {
		if err := c.collectCommandMulti(ctx,
			"proxmox-backup-manager prune-job list --output-format=json",
			filepath.Join(commandsDir, "prune_jobs.json"),
			"Prune jobs",
			false); err != nil {
			return err
		}
	}

	// GC jobs
	c.safeCmdOutput(ctx,
		"proxmox-backup-manager garbage-collection list --output-format=json",
		filepath.Join(commandsDir, "gc_jobs.json"),
		"Garbage collection jobs",
		false)

	// Tape backup status (if configured)
	if c.config.BackupTapeConfigs {
		if hasTape, err := c.hasTapeSupport(ctx); err != nil {
			if ctx.Err() != nil {
				return err
			}
			c.logger.Debug("Skipping tape details collection: %v", err)
		} else if hasTape {
			c.safeCmdOutput(ctx,
				"proxmox-tape drive list --output-format=json",
				filepath.Join(commandsDir, "tape_drives.json"),
				"Tape drives",
				false)

			c.safeCmdOutput(ctx,
				"proxmox-tape changer list --output-format=json",
				filepath.Join(commandsDir, "tape_changers.json"),
				"Tape changers",
				false)

			c.safeCmdOutput(ctx,
				"proxmox-tape pool list --output-format=json",
				filepath.Join(commandsDir, "tape_pools.json"),
				"Tape pools",
				false)
		}
	}

	// Network configuration
	if c.config.BackupNetworkConfigs {
		c.safeCmdOutput(ctx,
			"proxmox-backup-manager network list --output-format=json",
			filepath.Join(commandsDir, "network_list.json"),
			"Network configuration",
			false)
	}

	// Disk usage
	c.safeCmdOutput(ctx,
		"proxmox-backup-manager disk list --output-format=json",
		filepath.Join(commandsDir, "disk_list.json"),
		"Disk list",
		false)

	if err := c.collectCommandMulti(ctx,
		"proxmox-backup-manager cert info",
		filepath.Join(commandsDir, "cert_info.txt"),
		"Certificate information",
		false); err != nil {
		return err
	}

	// Traffic control rules
	c.safeCmdOutput(ctx,
		"proxmox-backup-manager traffic-control list --output-format=json",
		filepath.Join(commandsDir, "traffic_control.json"),
		"Traffic control rules",
		false)

	// Task log summary
	c.safeCmdOutput(ctx,
		"proxmox-backup-manager task list --limit 50 --output-format=json",
		filepath.Join(commandsDir, "recent_tasks.json"),
		"Recent tasks",
		false)

	// S3 endpoints (optional, may be unavailable on older PBS versions)
	c.collectPBSS3Snapshots(ctx, commandsDir)

	return nil
}

func (c *Collector) collectPBSAcmeSnapshots(ctx context.Context, commandsDir string) {
	accountsPath := filepath.Join(commandsDir, "acme_accounts.json")
	if err := c.collectCommandMulti(ctx,
		"proxmox-backup-manager acme account list --output-format=json",
		accountsPath,
		"ACME accounts",
		false,
	); err != nil {
		c.logger.Debug("ACME accounts snapshot skipped: %v", err)
	}

	pluginsPath := filepath.Join(commandsDir, "acme_plugins.json")
	if err := c.collectCommandMulti(ctx,
		"proxmox-backup-manager acme plugin list --output-format=json",
		pluginsPath,
		"ACME plugins",
		false,
	); err != nil {
		c.logger.Debug("ACME plugins snapshot skipped: %v", err)
	}

	type acmeAccount struct {
		Name string `json:"name"`
	}
	if raw, err := os.ReadFile(accountsPath); err == nil && len(raw) > 0 {
		var accounts []acmeAccount
		if err := json.Unmarshal(raw, &accounts); err == nil {
			for _, account := range accounts {
				name := strings.TrimSpace(account.Name)
				if name == "" {
					continue
				}
				out := filepath.Join(commandsDir, fmt.Sprintf("acme_account_%s_info.json", sanitizeFilename(name)))
				_ = c.collectCommandMulti(ctx,
					fmt.Sprintf("proxmox-backup-manager acme account info %s --output-format=json", name),
					out,
					fmt.Sprintf("ACME account info (%s)", name),
					false)
			}
		}
	}

	type acmePlugin struct {
		ID string `json:"id"`
	}
	if raw, err := os.ReadFile(pluginsPath); err == nil && len(raw) > 0 {
		var plugins []acmePlugin
		if err := json.Unmarshal(raw, &plugins); err == nil {
			for _, plugin := range plugins {
				id := strings.TrimSpace(plugin.ID)
				if id == "" {
					continue
				}
				out := filepath.Join(commandsDir, fmt.Sprintf("acme_plugin_%s_config.json", sanitizeFilename(id)))
				_ = c.collectCommandMulti(ctx,
					fmt.Sprintf("proxmox-backup-manager acme plugin config %s --output-format=json", id),
					out,
					fmt.Sprintf("ACME plugin config (%s)", id),
					false)
			}
		}
	}
}

func (c *Collector) collectPBSNotificationSnapshots(ctx context.Context, commandsDir string) {
	_ = c.collectCommandMulti(ctx,
		"proxmox-backup-manager notification target list --output-format=json",
		filepath.Join(commandsDir, "notification_targets.json"),
		"Notification targets",
		false)

	_ = c.collectCommandMulti(ctx,
		"proxmox-backup-manager notification matcher list --output-format=json",
		filepath.Join(commandsDir, "notification_matchers.json"),
		"Notification matchers",
		false)

	for _, typ := range []string{"smtp", "sendmail", "gotify", "webhook"} {
		_ = c.collectCommandMulti(ctx,
			fmt.Sprintf("proxmox-backup-manager notification endpoint %s list --output-format=json", typ),
			filepath.Join(commandsDir, fmt.Sprintf("notification_endpoints_%s.json", typ)),
			fmt.Sprintf("Notification endpoints (%s)", typ),
			false)
	}
}

func (c *Collector) collectPBSRealmSnapshots(ctx context.Context, commandsDir string) {
	for _, realm := range []struct {
		cmd  string
		out  string
		desc string
	}{
		{
			cmd:  "proxmox-backup-manager ldap list --output-format=json",
			out:  "realms_ldap.json",
			desc: "LDAP realms",
		},
		{
			cmd:  "proxmox-backup-manager ad list --output-format=json",
			out:  "realms_ad.json",
			desc: "Active Directory realms",
		},
		{
			cmd:  "proxmox-backup-manager openid list --output-format=json",
			out:  "realms_openid.json",
			desc: "OpenID realms",
		},
	} {
		_ = c.collectCommandMulti(ctx,
			realm.cmd,
			filepath.Join(commandsDir, realm.out),
			realm.desc,
			false)
	}
}

func (c *Collector) collectPBSS3Snapshots(ctx context.Context, commandsDir string) {
	endpointsPath := filepath.Join(commandsDir, "s3_endpoints.json")
	if err := c.collectCommandMulti(ctx,
		"proxmox-backup-manager s3 endpoint list --output-format=json",
		endpointsPath,
		"S3 endpoints",
		false,
	); err != nil {
		c.logger.Debug("S3 endpoints snapshot skipped: %v", err)
	}

	type s3Endpoint struct {
		ID string `json:"id"`
	}
	raw, err := os.ReadFile(endpointsPath)
	if err != nil || len(raw) == 0 {
		return
	}
	var endpoints []s3Endpoint
	if err := json.Unmarshal(raw, &endpoints); err != nil {
		return
	}

	for _, endpoint := range endpoints {
		id := strings.TrimSpace(endpoint.ID)
		if id == "" {
			continue
		}
		// Best-effort: may require network and may not exist on older versions.
		out := filepath.Join(commandsDir, fmt.Sprintf("s3_endpoint_%s_buckets.json", sanitizeFilename(id)))
		_ = c.collectCommandMulti(ctx,
			fmt.Sprintf("proxmox-backup-manager s3 endpoint list-buckets %s --output-format=json", id),
			out,
			fmt.Sprintf("S3 endpoint buckets (%s)", id),
			false)
	}
}

// collectUserConfigs collects user and ACL configurations
func (c *Collector) collectUserConfigs(ctx context.Context) error {
	c.logger.Debug("Collecting PBS user and ACL information")
	usersDir := c.proxsaveInfoDir("pbs", "access-control")
	if err := c.ensureDir(usersDir); err != nil {
		return fmt.Errorf("failed to create users directory: %w", err)
	}

	c.collectUserTokens(ctx, usersDir)

	c.logger.Debug("PBS user information collection completed")
	return nil
}

func (c *Collector) collectUserTokens(ctx context.Context, usersDir string) {
	c.logger.Debug("Collecting PBS API tokens for configured users")
	userListPath := filepath.Join(c.proxsaveCommandsDir("pbs"), "user_list.json")
	data, err := os.ReadFile(userListPath)
	if err != nil {
		c.logger.Debug("User list not available for token export: %v", err)
		return
	}

	var entries []struct {
		UserID string `json:"userid"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		c.logger.Debug("Failed to parse user list for token export: %v", err)
		return
	}

	aggregated := make(map[string]json.RawMessage)
	for _, entry := range entries {
		id := strings.TrimSpace(entry.UserID)
		if id == "" {
			continue
		}

		tokenPath := filepath.Join(usersDir, fmt.Sprintf("%s_tokens.json", sanitizeFilename(id)))
		cmd := fmt.Sprintf("proxmox-backup-manager user list-tokens %s --output-format=json", id)
		if err := c.safeCmdOutput(ctx, cmd, tokenPath, fmt.Sprintf("API tokens for %s", id), false); err != nil {
			c.logger.Debug("Token export skipped for %s: %v", id, err)
			continue
		}

		if payload, err := os.ReadFile(tokenPath); err == nil && len(payload) > 0 {
			aggregated[id] = json.RawMessage(payload)
		}
	}

	if len(aggregated) == 0 {
		c.logger.Debug("No PBS user tokens exported")
		return
	}

	buffer, err := json.MarshalIndent(aggregated, "", "  ")
	if err != nil {
		c.logger.Debug("Failed to serialize aggregated token data: %v", err)
		return
	}

	target := filepath.Join(usersDir, "tokens.json")
	if c.shouldExclude(target) {
		c.incFilesSkipped()
		return
	}
	if err := c.writeReportFile(target, buffer); err != nil {
		c.logger.Debug("Failed to write aggregated tokens.json: %v", err)
	}
	c.logger.Debug("Aggregated PBS token export completed (%d users)", len(aggregated))
}

// hasTapeSupport checks if PBS has tape backup support configured
func (c *Collector) hasTapeSupport(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	c.logger.Debug("Checking PBS tape support configuration")

	tapeCfg := filepath.Join(c.pbsConfigPath(), "tape.cfg")
	if _, err := c.depStat(tapeCfg); err == nil {
		c.logger.Debug("Detected %s, tape support enabled", tapeCfg)
		return true, nil
	}

	if _, err := c.depLookPath("proxmox-tape"); err != nil {
		c.logger.Debug("proxmox-tape CLI not available, tape support disabled")
		return false, nil
	}

	output, err := c.depRunCommand(ctx, "proxmox-tape", "drive", "list")
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, fmt.Errorf("proxmox-tape drive list failed: %w", err)
	}

	hasDrives := len(strings.TrimSpace(string(output))) > 0
	c.logger.Debug("Tape drive inventory detected=%v", hasDrives)
	return hasDrives, nil
}
