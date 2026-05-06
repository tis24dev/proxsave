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
func (c *Collector) collectPBSConfigFile(ctx context.Context, root, filename, description string, enabled bool, disableHint string) ManifestEntry {
	if !enabled {
		c.logger.Debug("Skipping %s: disabled by configuration", filename)
		if strings.TrimSpace(disableHint) != "" {
			c.logger.Info("  %s: disabled (%s=false)", description, disableHint)
		} else {
			c.logger.Info("  %s: disabled", description)
		}
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
		if strings.TrimSpace(disableHint) != "" {
			c.logger.Warning("  %s: not configured. If unused, set %s=false to disable.", description, disableHint)
		} else {
			c.logger.Warning("  %s: not configured", description)
		}
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
	state := newCollectionState(c)
	if err := runRecipe(ctx, newPBSRecipe(), state); err != nil {
		return err
	}
	if err := runRecipe(ctx, newPBSUserConfigRecipe(), state); err != nil {
		return err
	}

	c.logger.Info("PBS configuration collection completed")
	return nil
}

func (c *Collector) collectPBSConfigSnapshot(ctx context.Context, root string) error {
	c.logger.Debug("Collecting PBS directories (source=%s, dest=%s)",
		root, filepath.Join(c.tempDir, "etc/proxmox-backup"))

	var extraExclude []string
	if !c.config.BackupDatastoreConfigs {
		extraExclude = append(extraExclude, "datastore.cfg")
	}
	if !c.config.BackupDatastoreConfigs || !c.config.BackupPBSS3Endpoints {
		extraExclude = append(extraExclude, "s3.cfg")
	}
	if !c.config.BackupPBSNodeConfig {
		extraExclude = append(extraExclude, "node.cfg")
	}
	if !c.config.BackupPBSAcmeAccounts {
		extraExclude = append(extraExclude, "**/acme/accounts.cfg")
	}
	if !c.config.BackupPBSAcmePlugins {
		extraExclude = append(extraExclude, "**/acme/plugins.cfg")
	}
	if !c.config.BackupPBSMetricServers {
		extraExclude = append(extraExclude, "metricserver.cfg")
	}
	if !c.config.BackupPBSTrafficControl {
		extraExclude = append(extraExclude, "traffic-control.cfg")
	}
	if !c.config.BackupPBSNotifications {
		extraExclude = append(extraExclude, "notifications.cfg", "notifications-priv.cfg")
	} else if !c.config.BackupPBSNotificationsPriv {
		extraExclude = append(extraExclude, "notifications-priv.cfg")
	}
	if !c.config.BackupUserConfigs {
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
	if !c.config.BackupPBSNetworkConfig {
		extraExclude = append(extraExclude, "network.cfg")
	}
	if !c.config.BackupPruneSchedules {
		extraExclude = append(extraExclude, "prune.cfg")
	}

	if len(extraExclude) > 0 {
		c.logger.Debug("PBS config exclusions enabled (disabled features): %s", strings.Join(extraExclude, ", "))
	}
	return c.withTemporaryExcludes(extraExclude, func() error {
		return c.safeCopyDir(ctx,
			root,
			filepath.Join(c.tempDir, "etc/proxmox-backup"),
			"PBS configuration")
	})
}

func (c *Collector) initPBSManifest() {
	c.pbsManifest = make(map[string]ManifestEntry)
}

func (c *Collector) setPBSManifestEntry(ctx context.Context, root, key, description string, enabled bool, disableHint string) {
	c.pbsManifest[key] = c.collectPBSConfigFile(ctx, root, key, description, enabled, disableHint)
}

func (c *Collector) collectPBSManifestDatastore(ctx context.Context, root string) error {
	c.logger.Info("Collecting PBS configuration files:")
	c.setPBSManifestEntry(ctx, root, "datastore.cfg", "Datastore configuration", c.config.BackupDatastoreConfigs, "BACKUP_DATASTORE_CONFIGS")
	return nil
}

func (c *Collector) collectPBSManifestS3(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "s3.cfg", "S3 endpoints", c.config.BackupDatastoreConfigs && c.config.BackupPBSS3Endpoints, "BACKUP_PBS_S3_ENDPOINTS")
	return nil
}

func (c *Collector) collectPBSManifestNode(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "node.cfg", "Node configuration", c.config.BackupPBSNodeConfig, "BACKUP_PBS_NODE_CONFIG")
	return nil
}

func (c *Collector) collectPBSManifestACMEAccounts(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, filepath.Join("acme", "accounts.cfg"), "ACME accounts", c.config.BackupPBSAcmeAccounts, "BACKUP_PBS_ACME_ACCOUNTS")
	return nil
}

func (c *Collector) collectPBSManifestACMEPlugins(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, filepath.Join("acme", "plugins.cfg"), "ACME plugins", c.config.BackupPBSAcmePlugins, "BACKUP_PBS_ACME_PLUGINS")
	return nil
}

func (c *Collector) collectPBSManifestMetricServers(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "metricserver.cfg", "External metric servers", c.config.BackupPBSMetricServers, "BACKUP_PBS_METRIC_SERVERS")
	return nil
}

func (c *Collector) collectPBSManifestTrafficControl(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "traffic-control.cfg", "Traffic control rules", c.config.BackupPBSTrafficControl, "BACKUP_PBS_TRAFFIC_CONTROL")
	return nil
}

func (c *Collector) collectPBSManifestNotifications(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "notifications.cfg", "Notifications configuration", c.config.BackupPBSNotifications, "BACKUP_PBS_NOTIFICATIONS")
	return nil
}

func (c *Collector) collectPBSManifestNotificationsPriv(ctx context.Context, root string) error {
	privEnabled := c.config.BackupPBSNotifications && c.config.BackupPBSNotificationsPriv
	privDisableHint := "BACKUP_PBS_NOTIFICATIONS_PRIV"
	if !c.config.BackupPBSNotifications {
		privDisableHint = "BACKUP_PBS_NOTIFICATIONS"
	}
	c.setPBSManifestEntry(ctx, root, "notifications-priv.cfg", "Notifications secrets", privEnabled, privDisableHint)
	return nil
}

func (c *Collector) collectPBSManifestUserCfg(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "user.cfg", "User configuration", c.config.BackupUserConfigs, "BACKUP_USER_CONFIGS")
	return nil
}

func (c *Collector) collectPBSManifestACLCfg(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "acl.cfg", "ACL configuration", c.config.BackupUserConfigs, "BACKUP_USER_CONFIGS")
	return nil
}

func (c *Collector) collectPBSManifestDomainsCfg(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "domains.cfg", "Auth realm configuration", c.config.BackupUserConfigs, "BACKUP_USER_CONFIGS")
	return nil
}

func (c *Collector) collectPBSManifestRemote(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "remote.cfg", "Remote configuration", c.config.BackupRemoteConfigs, "BACKUP_REMOTE_CONFIGS")
	return nil
}

func (c *Collector) collectPBSManifestSyncJobs(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "sync.cfg", "Sync jobs", c.config.BackupSyncJobs, "BACKUP_SYNC_JOBS")
	return nil
}

func (c *Collector) collectPBSManifestVerificationJobs(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "verification.cfg", "Verification jobs", c.config.BackupVerificationJobs, "BACKUP_VERIFICATION_JOBS")
	return nil
}

func (c *Collector) collectPBSManifestTapeCfg(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "tape.cfg", "Tape configuration", c.config.BackupTapeConfigs, "BACKUP_TAPE_CONFIGS")
	return nil
}

func (c *Collector) collectPBSManifestTapeJobs(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "tape-job.cfg", "Tape jobs", c.config.BackupTapeConfigs, "BACKUP_TAPE_CONFIGS")
	return nil
}

func (c *Collector) collectPBSManifestMediaPools(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "media-pool.cfg", "Media pool configuration", c.config.BackupTapeConfigs, "BACKUP_TAPE_CONFIGS")
	return nil
}

func (c *Collector) collectPBSManifestTapeEncryptionKeys(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "tape-encryption-keys.json", "Tape encryption keys", c.config.BackupTapeConfigs, "BACKUP_TAPE_CONFIGS")
	return nil
}

func (c *Collector) collectPBSManifestNetwork(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "network.cfg", "Network configuration", c.config.BackupPBSNetworkConfig, "BACKUP_PBS_NETWORK_CONFIG")
	return nil
}

func (c *Collector) collectPBSManifestPrune(ctx context.Context, root string) error {
	c.setPBSManifestEntry(ctx, root, "prune.cfg", "Prune schedules", c.config.BackupPruneSchedules, "BACKUP_PRUNE_SCHEDULES")
	return nil
}

func (c *Collector) collectPBSCoreRuntime(ctx context.Context, commandsDir string) error {
	if err := c.collectCommandMulti(ctx,
		commandSpec("proxmox-backup-manager", "version"),
		filepath.Join(commandsDir, "pbs_version.txt"),
		"PBS version",
		true); err != nil {
		return fmt.Errorf("failed to get PBS version (critical): %w", err)
	}
	return nil
}

func (c *Collector) collectPBSNodeRuntime(ctx context.Context, commandsDir string) error {
	if c.config.BackupPBSNodeConfig {
		c.safeCmdOutput(ctx,
			commandSpec("proxmox-backup-manager", "node", "show", "--output-format=json"),
			filepath.Join(commandsDir, "node_config.json"),
			"Node configuration",
			false)
	}
	return nil
}

func (c *Collector) collectPBSNetworkRuntime(ctx context.Context, commandsDir string) error {
	if c.config.BackupPBSNetworkConfig {
		c.safeCmdOutput(ctx,
			commandSpec("proxmox-backup-manager", "network", "list", "--output-format=json"),
			filepath.Join(commandsDir, "network_list.json"),
			"Network configuration",
			false)
	}
	return nil
}

func (c *Collector) collectPBSDatastoreListRuntime(ctx context.Context, commandsDir string) error {
	return c.collectCommandMulti(ctx,
		commandSpec("proxmox-backup-manager", "datastore", "list", "--output-format=json"),
		filepath.Join(commandsDir, "datastore_list.json"),
		"Datastore list",
		false)
}

func (c *Collector) collectPBSDatastoreStatusRuntime(ctx context.Context, commandsDir string, datastores []pbsDatastore) error {
	if !c.config.BackupDatastoreConfigs || len(datastores) == 0 {
		return nil
	}
	for _, ds := range datastores {
		if ds.isOverride() {
			c.logger.Debug("Skipping datastore status for %s (path=%s): no PBS datastore identity", ds.Name, ds.Path)
			continue
		}
		cliName := ds.cliName()
		if cliName == "" {
			c.logger.Debug("Skipping datastore status for %s (path=%s): empty PBS datastore identity", ds.Name, ds.Path)
			continue
		}
		dsKey := ds.pathKey()
		c.safeCmdOutput(ctx,
			commandSpec("proxmox-backup-manager", "datastore", "show", cliName, "--output-format=json"),
			filepath.Join(commandsDir, fmt.Sprintf("datastore_%s_status.json", dsKey)),
			fmt.Sprintf("Datastore %s status", ds.Name),
			false)
	}
	return nil
}

func (c *Collector) collectPBSAcmeAccountsListRuntime(ctx context.Context, commandsDir string) ([]string, error) {
	if !c.config.BackupPBSAcmeAccounts {
		return nil, nil
	}
	raw, err := c.captureCommandOutput(ctx,
		commandSpec("proxmox-backup-manager", "acme", "account", "list", "--output-format=json"),
		filepath.Join(commandsDir, "acme_accounts.json"),
		"ACME accounts",
		false)
	if err != nil {
		return nil, err
	}
	names, parseErr := parsePBSStringFieldList(raw, "name")
	if parseErr != nil {
		c.logger.Debug("Failed to parse ACME account list: %v", parseErr)
	}
	return names, nil
}

func (c *Collector) collectPBSAcmeAccountInfoRuntime(ctx context.Context, commandsDir string, accountNames []string) error {
	if !c.config.BackupPBSAcmeAccounts {
		return nil
	}
	for _, name := range uniqueSortedStrings(accountNames) {
		out := filepath.Join(commandsDir, fmt.Sprintf("acme_account_%s_info.json", sanitizeFilename(name)))
		c.collectCommandOptional(ctx,
			commandSpec("proxmox-backup-manager", "acme", "account", "info", name, "--output-format=json"),
			out,
			fmt.Sprintf("ACME account info (%s)", name))
	}
	return nil
}

func (c *Collector) collectPBSAcmePluginsListRuntime(ctx context.Context, commandsDir string) ([]string, error) {
	if !c.config.BackupPBSAcmePlugins {
		return nil, nil
	}
	raw, err := c.captureCommandOutput(ctx,
		commandSpec("proxmox-backup-manager", "acme", "plugin", "list", "--output-format=json"),
		filepath.Join(commandsDir, "acme_plugins.json"),
		"ACME plugins",
		false)
	if err != nil {
		return nil, err
	}
	ids, parseErr := parsePBSStringFieldList(raw, "id")
	if parseErr != nil {
		c.logger.Debug("Failed to parse ACME plugin list: %v", parseErr)
	}
	return ids, nil
}

func (c *Collector) collectPBSAcmePluginConfigRuntime(ctx context.Context, commandsDir string, pluginIDs []string) error {
	if !c.config.BackupPBSAcmePlugins {
		return nil
	}
	for _, id := range uniqueSortedStrings(pluginIDs) {
		out := filepath.Join(commandsDir, fmt.Sprintf("acme_plugin_%s_config.json", sanitizeFilename(id)))
		c.collectCommandOptional(ctx,
			commandSpec("proxmox-backup-manager", "acme", "plugin", "config", id, "--output-format=json"),
			out,
			fmt.Sprintf("ACME plugin config (%s)", id))
	}
	return nil
}

func (c *Collector) collectPBSNotificationTargetsRuntime(ctx context.Context, commandsDir string) error {
	if !c.config.BackupPBSNotifications {
		return nil
	}
	return c.collectCommandMulti(ctx,
		commandSpec("proxmox-backup-manager", "notification", "target", "list", "--output-format=json"),
		filepath.Join(commandsDir, "notification_targets.json"),
		"Notification targets",
		false)
}

func (c *Collector) collectPBSNotificationMatchersRuntime(ctx context.Context, commandsDir string) error {
	if !c.config.BackupPBSNotifications {
		return nil
	}
	return c.collectCommandMulti(ctx,
		commandSpec("proxmox-backup-manager", "notification", "matcher", "list", "--output-format=json"),
		filepath.Join(commandsDir, "notification_matchers.json"),
		"Notification matchers",
		false)
}

func (c *Collector) collectPBSNotificationEndpointRuntime(ctx context.Context, commandsDir, typ string) error {
	if !c.config.BackupPBSNotifications {
		return nil
	}
	return c.collectCommandMulti(ctx,
		commandSpec("proxmox-backup-manager", "notification", "endpoint", typ, "list", "--output-format=json"),
		filepath.Join(commandsDir, fmt.Sprintf("notification_endpoints_%s.json", typ)),
		fmt.Sprintf("Notification endpoints (%s)", typ),
		false)
}

func (c *Collector) collectPBSNotificationEndpointSMTPRuntime(ctx context.Context, commandsDir string) error {
	return c.collectPBSNotificationEndpointRuntime(ctx, commandsDir, "smtp")
}

func (c *Collector) collectPBSNotificationEndpointSendmailRuntime(ctx context.Context, commandsDir string) error {
	return c.collectPBSNotificationEndpointRuntime(ctx, commandsDir, "sendmail")
}

func (c *Collector) collectPBSNotificationEndpointGotifyRuntime(ctx context.Context, commandsDir string) error {
	return c.collectPBSNotificationEndpointRuntime(ctx, commandsDir, "gotify")
}

func (c *Collector) collectPBSNotificationEndpointWebhookRuntime(ctx context.Context, commandsDir string) error {
	return c.collectPBSNotificationEndpointRuntime(ctx, commandsDir, "webhook")
}

func (c *Collector) collectPBSAccessUsersRuntime(ctx context.Context, commandsDir string) ([]string, error) {
	if !c.config.BackupUserConfigs {
		return nil, nil
	}
	raw, err := c.captureCommandOutput(ctx,
		commandSpec("proxmox-backup-manager", "user", "list", "--output-format=json"),
		filepath.Join(commandsDir, "user_list.json"),
		"User list",
		false)
	if err != nil {
		return nil, err
	}
	ids, parseErr := parsePBSStringFieldList(raw, "userid")
	if parseErr != nil {
		c.logger.Debug("Failed to parse PBS user list: %v", parseErr)
	}
	return ids, nil
}

func (c *Collector) collectPBSAccessRealmRuntime(ctx context.Context, commandsDir string, spec CommandSpec, out, desc string) error {
	if !c.config.BackupUserConfigs {
		return nil
	}
	return c.collectCommandMulti(ctx, spec, filepath.Join(commandsDir, out), desc, false)
}

func (c *Collector) collectPBSAccessRealmLDAPRuntime(ctx context.Context, commandsDir string) error {
	return c.collectPBSAccessRealmRuntime(ctx, commandsDir, commandSpec("proxmox-backup-manager", "ldap", "list", "--output-format=json"), "realms_ldap.json", "LDAP realms")
}

func (c *Collector) collectPBSAccessRealmADRuntime(ctx context.Context, commandsDir string) error {
	return c.collectPBSAccessRealmRuntime(ctx, commandsDir, commandSpec("proxmox-backup-manager", "ad", "list", "--output-format=json"), "realms_ad.json", "Active Directory realms")
}

func (c *Collector) collectPBSAccessRealmOpenIDRuntime(ctx context.Context, commandsDir string) error {
	return c.collectPBSAccessRealmRuntime(ctx, commandsDir, commandSpec("proxmox-backup-manager", "openid", "list", "--output-format=json"), "realms_openid.json", "OpenID realms")
}

func (c *Collector) collectPBSAccessACLRuntime(ctx context.Context, commandsDir string) error {
	if !c.config.BackupUserConfigs {
		return nil
	}
	return c.collectCommandMulti(ctx,
		commandSpec("proxmox-backup-manager", "acl", "list", "--output-format=json"),
		filepath.Join(commandsDir, "acl_list.json"),
		"ACL list",
		false)
}

func (c *Collector) ensurePBSAccessControlDir() (string, error) {
	usersDir := c.proxsaveInfoDir("pbs", "access-control")
	if err := c.ensureDir(usersDir); err != nil {
		return "", fmt.Errorf("failed to create users directory: %w", err)
	}
	return usersDir, nil
}

func (c *Collector) collectPBSAccessUserTokensRuntime(ctx context.Context, usersDir string, userIDs []string) error {
	_, err := c.collectPBSUserTokensForIDs(ctx, usersDir, userIDs)
	return err
}

func (c *Collector) collectPBSAccessTokensAggregateRuntime(usersDir string, userIDs []string) error {
	return c.writePBSAggregatedTokensFromUserFiles(usersDir, userIDs)
}

func (c *Collector) collectPBSRemotesRuntime(ctx context.Context, commandsDir string) error {
	if !c.config.BackupRemoteConfigs {
		return nil
	}
	return c.collectCommandMulti(ctx,
		commandSpec("proxmox-backup-manager", "remote", "list", "--output-format=json"),
		filepath.Join(commandsDir, "remote_list.json"),
		"Remote list",
		false)
}

func (c *Collector) collectPBSSyncJobsRuntime(ctx context.Context, commandsDir string) error {
	if !c.config.BackupSyncJobs {
		return nil
	}
	return c.collectCommandMulti(ctx,
		commandSpec("proxmox-backup-manager", "sync-job", "list", "--output-format=json"),
		filepath.Join(commandsDir, "sync_jobs.json"),
		"Sync jobs",
		false)
}

func (c *Collector) collectPBSVerificationJobsRuntime(ctx context.Context, commandsDir string) error {
	if !c.config.BackupVerificationJobs {
		return nil
	}
	return c.collectCommandMulti(ctx,
		commandSpec("proxmox-backup-manager", "verify-job", "list", "--output-format=json"),
		filepath.Join(commandsDir, "verification_jobs.json"),
		"Verification jobs",
		false)
}

func (c *Collector) collectPBSPruneJobsRuntime(ctx context.Context, commandsDir string) error {
	if !c.config.BackupPruneSchedules {
		return nil
	}
	return c.collectCommandMulti(ctx,
		commandSpec("proxmox-backup-manager", "prune-job", "list", "--output-format=json"),
		filepath.Join(commandsDir, "prune_jobs.json"),
		"Prune jobs",
		false)
}

func (c *Collector) collectPBSGCJobsRuntime(ctx context.Context, commandsDir string) error {
	return c.collectCommandMulti(ctx,
		commandSpec("proxmox-backup-manager", "garbage-collection", "list", "--output-format=json"),
		filepath.Join(commandsDir, "gc_jobs.json"),
		"Garbage collection jobs",
		false)
}

func (c *Collector) detectPBSTapeSupport(ctx context.Context) (bool, error) {
	return c.hasTapeSupport(ctx)
}

func (c *Collector) collectPBSTapeDrivesRuntime(ctx context.Context, commandsDir string, enabled bool) error {
	if !enabled {
		return nil
	}
	c.safeCmdOutput(ctx,
		commandSpec("proxmox-tape", "drive", "list", "--output-format=json"),
		filepath.Join(commandsDir, "tape_drives.json"),
		"Tape drives",
		false)
	return nil
}

func (c *Collector) collectPBSTapeChangersRuntime(ctx context.Context, commandsDir string, enabled bool) error {
	if !enabled {
		return nil
	}
	c.safeCmdOutput(ctx,
		commandSpec("proxmox-tape", "changer", "list", "--output-format=json"),
		filepath.Join(commandsDir, "tape_changers.json"),
		"Tape changers",
		false)
	return nil
}

func (c *Collector) collectPBSTapePoolsRuntime(ctx context.Context, commandsDir string, enabled bool) error {
	if !enabled {
		return nil
	}
	c.safeCmdOutput(ctx,
		commandSpec("proxmox-tape", "pool", "list", "--output-format=json"),
		filepath.Join(commandsDir, "tape_pools.json"),
		"Tape pools",
		false)
	return nil
}

func (c *Collector) collectPBSDisksRuntime(ctx context.Context, commandsDir string) error {
	c.safeCmdOutput(ctx,
		commandSpec("proxmox-backup-manager", "disk", "list", "--output-format=json"),
		filepath.Join(commandsDir, "disk_list.json"),
		"Disk list",
		false)
	return nil
}

func (c *Collector) collectPBSCertInfoRuntime(ctx context.Context, commandsDir string) error {
	return c.collectCommandMulti(ctx,
		commandSpec("proxmox-backup-manager", "cert", "info"),
		filepath.Join(commandsDir, "cert_info.txt"),
		"Certificate information",
		false)
}

func (c *Collector) collectPBSTrafficControlRuntime(ctx context.Context, commandsDir string) error {
	if !c.config.BackupPBSTrafficControl {
		return nil
	}
	c.safeCmdOutput(ctx,
		commandSpec("proxmox-backup-manager", "traffic-control", "list", "--output-format=json"),
		filepath.Join(commandsDir, "traffic_control.json"),
		"Traffic control rules",
		false)
	return nil
}

func (c *Collector) collectPBSRecentTasksRuntime(ctx context.Context, commandsDir string) error {
	c.safeCmdOutput(ctx,
		commandSpec("proxmox-backup-manager", "task", "list", "--limit", "50", "--output-format=json"),
		filepath.Join(commandsDir, "recent_tasks.json"),
		"Recent tasks",
		false)
	return nil
}

func (c *Collector) collectPBSS3EndpointsRuntime(ctx context.Context, commandsDir string) ([]string, error) {
	if !(c.config.BackupDatastoreConfigs && c.config.BackupPBSS3Endpoints) {
		return nil, nil
	}
	raw, err := c.captureCommandOutput(ctx,
		commandSpec("proxmox-backup-manager", "s3", "endpoint", "list", "--output-format=json"),
		filepath.Join(commandsDir, "s3_endpoints.json"),
		"S3 endpoints",
		false)
	if err != nil {
		return nil, err
	}
	ids, parseErr := parsePBSStringFieldList(raw, "id")
	if parseErr != nil {
		c.logger.Debug("Failed to parse S3 endpoint list: %v", parseErr)
	}
	return ids, nil
}

func (c *Collector) collectPBSS3EndpointBucketsRuntime(ctx context.Context, commandsDir string, endpointIDs []string) error {
	if !(c.config.BackupDatastoreConfigs && c.config.BackupPBSS3Endpoints) {
		return nil
	}
	for _, id := range uniqueSortedStrings(endpointIDs) {
		out := filepath.Join(commandsDir, fmt.Sprintf("s3_endpoint_%s_buckets.json", sanitizeFilename(id)))
		c.collectCommandOptional(ctx,
			commandSpec("proxmox-backup-manager", "s3", "endpoint", "list-buckets", id, "--output-format=json"),
			out,
			fmt.Sprintf("S3 endpoint buckets (%s)", id))
	}
	return nil
}

func parsePBSListPayload(raw []byte) ([]map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if envelope, ok := payload.(map[string]any); ok {
		if data, exists := envelope["data"]; exists {
			payload = data
		}
	}
	items, ok := payload.([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected json shape")
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func parsePBSStringFieldList(raw []byte, field string) ([]string, error) {
	rows, err := parsePBSListPayload(raw)
	if err != nil {
		return nil, err
	}
	values := make([]string, 0, len(rows))
	for _, row := range rows {
		value, ok := row[field].(string)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		values = append(values, value)
	}
	return uniqueSortedStrings(values), nil
}

func (c *Collector) loadPBSUserIDsFromCommandFile(commandsDir string) ([]string, error) {
	userListPath := filepath.Join(commandsDir, "user_list.json")
	data, err := os.ReadFile(userListPath)
	if err != nil {
		return nil, err
	}
	return parsePBSStringFieldList(data, "userid")
}

func (c *Collector) collectPBSUserTokensForIDs(ctx context.Context, usersDir string, userIDs []string) (map[string]json.RawMessage, error) {
	aggregated := make(map[string]json.RawMessage)
	for _, id := range uniqueSortedStrings(userIDs) {
		tokenPath := filepath.Join(usersDir, fmt.Sprintf("%s_tokens.json", sanitizeFilename(id)))
		spec := commandSpec("proxmox-backup-manager", "user", "list-tokens", id, "--output-format=json")
		if err := c.safeCmdOutput(ctx, spec, tokenPath, fmt.Sprintf("API tokens for %s", id), false); err != nil {
			c.logger.Debug("Token export skipped for %s: %v", id, err)
			continue
		}

		if payload, err := os.ReadFile(tokenPath); err == nil && len(payload) > 0 {
			aggregated[id] = json.RawMessage(payload)
		}
	}
	if len(aggregated) == 0 {
		c.logger.Debug("No PBS user tokens exported")
	}
	return aggregated, nil
}

func (c *Collector) writePBSAggregatedTokensFromUserFiles(usersDir string, userIDs []string) error {
	aggregated := make(map[string]json.RawMessage)
	for _, id := range uniqueSortedStrings(userIDs) {
		tokenPath := filepath.Join(usersDir, fmt.Sprintf("%s_tokens.json", sanitizeFilename(id)))
		payload, err := os.ReadFile(tokenPath)
		if err != nil || len(payload) == 0 {
			continue
		}
		aggregated[id] = json.RawMessage(payload)
	}
	if len(aggregated) == 0 {
		return nil
	}
	buffer, err := json.MarshalIndent(aggregated, "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(usersDir, "tokens.json")
	if c.shouldExclude(target) {
		c.incFilesSkipped()
		return nil
	}
	if err := c.writeReportFile(target, buffer); err != nil {
		return err
	}
	c.logger.Debug("Aggregated PBS token export completed (%d users)", len(aggregated))
	return nil
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
