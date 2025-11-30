package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tis24dev/proxsave/internal/pbs"
)

type pbsDatastore struct {
	Name    string
	Path    string
	Comment string
}

var listNamespacesFunc = pbs.ListNamespaces

func (c *Collector) pbsConfigPath() string {
	if c.config != nil && c.config.PBSConfigPath != "" {
		return c.systemPath(c.config.PBSConfigPath)
	}
	return c.systemPath("/etc/proxmox-backup")
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

	c.logger.Info("PBS configuration collection completed")
	return nil
}

// collectPBSDirectories collects PBS-specific directories
func (c *Collector) collectPBSDirectories(ctx context.Context, root string) error {
	c.logger.Debug("Collecting PBS directories (%s, configs, schedules)", root)
	// PBS main configuration directory
	if err := c.safeCopyDir(ctx,
		root,
		filepath.Join(c.tempDir, "etc/proxmox-backup"),
		"PBS configuration"); err != nil {
		return err
	}

	// Datastore configuration
	if c.config.BackupDatastoreConfigs {
		if err := c.safeCopyFile(ctx,
			filepath.Join(root, "datastore.cfg"),
			filepath.Join(c.tempDir, "etc/proxmox-backup/datastore.cfg"),
			"Datastore configuration"); err != nil {
			c.logger.Debug("No datastore.cfg found")
		}
	}

	// User configuration
	if c.config.BackupUserConfigs {
		if err := c.safeCopyFile(ctx,
			filepath.Join(root, "user.cfg"),
			filepath.Join(c.tempDir, "etc/proxmox-backup/user.cfg"),
			"User configuration"); err != nil {
			c.logger.Debug("No user.cfg found")
		}

		// ACL configuration
		if err := c.safeCopyFile(ctx,
			filepath.Join(root, "acl.cfg"),
			filepath.Join(c.tempDir, "etc/proxmox-backup/acl.cfg"),
			"ACL configuration"); err != nil {
			c.logger.Debug("No acl.cfg found")
		}
	}

	// Remote configuration (for sync jobs)
	if c.config.BackupRemoteConfigs {
		if err := c.safeCopyFile(ctx,
			filepath.Join(root, "remote.cfg"),
			filepath.Join(c.tempDir, "etc/proxmox-backup/remote.cfg"),
			"Remote configuration"); err != nil {
			c.logger.Debug("No remote.cfg found")
		}
	}

	// Sync jobs configuration
	if c.config.BackupSyncJobs {
		if err := c.safeCopyFile(ctx,
			filepath.Join(root, "sync.cfg"),
			filepath.Join(c.tempDir, "etc/proxmox-backup/sync.cfg"),
			"Sync configuration"); err != nil {
			c.logger.Debug("No sync.cfg found")
		}
	}

	// Verification jobs configuration
	if c.config.BackupVerificationJobs {
		if err := c.safeCopyFile(ctx,
			filepath.Join(root, "verification.cfg"),
			filepath.Join(c.tempDir, "etc/proxmox-backup/verification.cfg"),
			"Verification configuration"); err != nil {
			c.logger.Debug("No verification.cfg found")
		}
	}

	// Tape backup configuration (if applicable)
	if c.config.BackupTapeConfigs {
		if err := c.safeCopyFile(ctx,
			filepath.Join(root, "tape.cfg"),
			filepath.Join(c.tempDir, "etc/proxmox-backup/tape.cfg"),
			"Tape configuration"); err != nil {
			c.logger.Debug("No tape.cfg found")
		}

		// Media pool configuration
		if err := c.safeCopyFile(ctx,
			filepath.Join(root, "media-pool.cfg"),
			filepath.Join(c.tempDir, "etc/proxmox-backup/media-pool.cfg"),
			"Media pool configuration"); err != nil {
			c.logger.Debug("No media-pool.cfg found")
		}
	}

	// Network configuration
	if c.config.BackupNetworkConfigs {
		if err := c.safeCopyFile(ctx,
			filepath.Join(root, "network.cfg"),
			filepath.Join(c.tempDir, "etc/proxmox-backup/network.cfg"),
			"Network configuration"); err != nil {
			c.logger.Debug("No network.cfg found")
		}
	}

	// Prune/GC schedules
	if c.config.BackupPruneSchedules {
		if err := c.safeCopyFile(ctx,
			filepath.Join(root, "prune.cfg"),
			filepath.Join(c.tempDir, "etc/proxmox-backup/prune.cfg"),
			"Prune configuration"); err != nil {
			c.logger.Debug("No prune.cfg found")
		}
	}

	c.logger.Debug("PBS directory collection finished")
	return nil
}

// collectPBSCommands collects output from PBS commands
func (c *Collector) collectPBSCommands(ctx context.Context, datastores []pbsDatastore) error {
	commandsDir := filepath.Join(c.tempDir, "commands")
	if err := c.ensureDir(commandsDir); err != nil {
		return fmt.Errorf("failed to create commands directory: %w", err)
	}
	c.logger.Debug("Collecting PBS command outputs into %s", commandsDir)

	stateDir := filepath.Join(c.tempDir, "var/lib/proxmox-backup")
	if err := c.ensureDir(stateDir); err != nil {
		return fmt.Errorf("failed to create PBS state directory: %w", err)
	}
	c.logger.Debug("PBS state snapshots will be stored in %s", stateDir)

	// PBS version (CRITICAL)
	if err := c.collectCommandMulti(ctx,
		"proxmox-backup-manager version",
		filepath.Join(commandsDir, "pbs_version.txt"),
		"PBS version",
		true,
		filepath.Join(stateDir, "version.txt")); err != nil {
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
		false,
		filepath.Join(stateDir, "datastore_list.json")); err != nil {
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

	// User list
	if c.config.BackupUserConfigs {
		if err := c.collectCommandMulti(ctx,
			"proxmox-backup-manager user list --output-format=json",
			filepath.Join(commandsDir, "user_list.json"),
			"User list",
			false,
			filepath.Join(stateDir, "user_list.json")); err != nil {
			return err
		}

		// ACL list
		if err := c.collectCommandMulti(ctx,
			"proxmox-backup-manager acl list --output-format=json",
			filepath.Join(commandsDir, "acl_list.json"),
			"ACL list",
			false,
			filepath.Join(stateDir, "acl_list.json")); err != nil {
			return err
		}
	}

	// Remote list (sync sources)
	if c.config.BackupRemoteConfigs {
		if err := c.collectCommandMulti(ctx,
			"proxmox-backup-manager remote list --output-format=json",
			filepath.Join(commandsDir, "remote_list.json"),
			"Remote list",
			false,
			filepath.Join(stateDir, "remote_list.json")); err != nil {
			return err
		}
	}

	// Sync jobs status
	if c.config.BackupSyncJobs {
		if err := c.collectCommandMulti(ctx,
			"proxmox-backup-manager sync-job list --output-format=json",
			filepath.Join(commandsDir, "sync_jobs.json"),
			"Sync jobs",
			false,
			filepath.Join(stateDir, "sync_jobs.json")); err != nil {
			return err
		}
	}

	// Verification jobs status
	if c.config.BackupVerificationJobs {
		if err := c.collectCommandMulti(ctx,
			"proxmox-backup-manager verify-job list --output-format=json",
			filepath.Join(commandsDir, "verification_jobs.json"),
			"Verification jobs",
			false,
			filepath.Join(stateDir, "verify_jobs.json")); err != nil {
			return err
		}
	}

	// Prune jobs
	if c.config.BackupPruneSchedules {
		if err := c.collectCommandMulti(ctx,
			"proxmox-backup-manager prune-job list --output-format=json",
			filepath.Join(commandsDir, "prune_jobs.json"),
			"Prune jobs",
			false,
			filepath.Join(stateDir, "prune_jobs.json")); err != nil {
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
		false,
		filepath.Join(stateDir, "cert_info.txt")); err != nil {
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

	return nil
}

// collectDatastoreConfigs collects detailed datastore configurations
func (c *Collector) collectDatastoreConfigs(ctx context.Context, datastores []pbsDatastore) error {
	if len(datastores) == 0 {
		c.logger.Debug("No datastores found")
		return nil
	}
	c.logger.Debug("Collecting datastore details for %d datastores", len(datastores))

	datastoreDir := filepath.Join(c.tempDir, "datastores")
	if err := c.ensureDir(datastoreDir); err != nil {
		return fmt.Errorf("failed to create datastores directory: %w", err)
	}

	for _, ds := range datastores {
		// Get datastore configuration details
		c.safeCmdOutput(ctx,
			fmt.Sprintf("proxmox-backup-manager datastore show %s --output-format=json", ds.Name),
			filepath.Join(datastoreDir, fmt.Sprintf("%s_config.json", ds.Name)),
			fmt.Sprintf("Datastore %s configuration", ds.Name),
			false)

		// Get namespace list using CLI/Filesystem fallback
		if err := c.collectDatastoreNamespaces(ds, datastoreDir); err != nil {
			c.logger.Debug("Failed to collect namespaces for datastore %s: %v", ds.Name, err)
		}
	}

	c.logger.Debug("Datastore configuration collection completed")
	return nil
}

// collectDatastoreNamespaces collects namespace information for a datastore
// using CLI first, then filesystem fallback.
func (c *Collector) collectDatastoreNamespaces(ds pbsDatastore, datastoreDir string) error {
	c.logger.Debug("Collecting namespaces for datastore %s (path: %s)", ds.Name, ds.Path)
	namespaces, fromFallback, err := listNamespacesFunc(ds.Name, ds.Path)
	if err != nil {
		return err
	}

	// Write namespaces to JSON file
	outputPath := filepath.Join(datastoreDir, fmt.Sprintf("%s_namespaces.json", ds.Name))
	data, err := json.MarshalIndent(namespaces, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal namespaces: %w", err)
	}

	if err := os.WriteFile(outputPath, data, 0640); err != nil {
		c.incFilesFailed()
		return fmt.Errorf("failed to write namespaces file: %w", err)
	}

	c.incFilesProcessed()
	if fromFallback {
		c.logger.Debug("Successfully collected %d namespaces for datastore %s via filesystem fallback", len(namespaces), ds.Name)
	} else {
		c.logger.Debug("Successfully collected %d namespaces for datastore %s via CLI", len(namespaces), ds.Name)
	}
	return nil
}

// collectUserConfigs collects user and ACL configurations
func (c *Collector) collectUserConfigs(ctx context.Context) error {
	c.logger.Debug("Collecting PBS user and ACL information")
	usersDir := filepath.Join(c.tempDir, "users")
	if err := c.ensureDir(usersDir); err != nil {
		return fmt.Errorf("failed to create users directory: %w", err)
	}

	c.collectUserTokens(ctx, usersDir)

	c.logger.Debug("PBS user information collection completed")
	return nil
}

func (c *Collector) collectUserTokens(ctx context.Context, usersDir string) {
	c.logger.Debug("Collecting PBS API tokens for configured users")
	userListPath := filepath.Join(c.tempDir, "commands", "user_list.json")
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

	if err := os.WriteFile(filepath.Join(usersDir, "tokens.json"), buffer, 0640); err != nil {
		c.logger.Debug("Failed to write aggregated tokens.json: %v", err)
	}
	c.logger.Debug("Aggregated PBS token export completed (%d users)", len(aggregated))
}

func (c *Collector) collectPBSPxarMetadata(ctx context.Context, datastores []pbsDatastore) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if len(datastores) == 0 {
		return nil
	}
	c.logger.Debug("Collecting PXAR metadata for %d datastores", len(datastores))
	dsWorkers := c.config.PxarDatastoreConcurrency
	if dsWorkers <= 0 {
		dsWorkers = 1
	}
	intraWorkers := c.config.PxarIntraConcurrency
	if intraWorkers <= 0 {
		intraWorkers = 1
	}
	mode := "sequential"
	if dsWorkers > 1 {
		mode = fmt.Sprintf("parallel (%d workers)", dsWorkers)
	}
	c.logger.Debug("PXAR metadata concurrency: datastores=%s, per-datastore workers=%d", mode, intraWorkers)

	metaRoot := filepath.Join(c.tempDir, "var/lib/proxmox-backup/pxar_metadata")
	if err := c.ensureDir(metaRoot); err != nil {
		return fmt.Errorf("failed to create PXAR metadata directory: %w", err)
	}

	selectedRoot := filepath.Join(c.tempDir, "var/lib/proxmox-backup/selected_pxar")
	if err := c.ensureDir(selectedRoot); err != nil {
		return fmt.Errorf("failed to create selected_pxar directory: %w", err)
	}

	smallRoot := filepath.Join(c.tempDir, "var/lib/proxmox-backup/small_pxar")
	if err := c.ensureDir(smallRoot); err != nil {
		return fmt.Errorf("failed to create small_pxar directory: %w", err)
	}

	workerLimit := dsWorkers

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		sem      = make(chan struct{}, workerLimit)
		errMu    sync.Mutex
		firstErr error
	)

	for _, ds := range datastores {
		ds := ds
		if ds.Path == "" {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			if err := c.processPxarDatastore(ctx, ds, metaRoot, selectedRoot, smallRoot); err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				errMu.Unlock()
			}
		}()
	}

	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	c.logger.Debug("PXAR metadata collection completed")
	return nil
}

func (c *Collector) processPxarDatastore(ctx context.Context, ds pbsDatastore, metaRoot, selectedRoot, smallRoot string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ds.Path == "" {
		return nil
	}

	stat, err := os.Stat(ds.Path)
	if err != nil || !stat.IsDir() {
		c.logger.Debug("Skipping PXAR metadata for datastore %s (path not accessible: %s)", ds.Name, ds.Path)
		return nil
	}

	start := time.Now()
	c.logger.Debug("PXAR: scanning datastore %s at %s", ds.Name, ds.Path)

	dsDir := filepath.Join(metaRoot, ds.Name)
	if err := c.ensureDir(dsDir); err != nil {
		return fmt.Errorf("failed to create PXAR metadata directory for %s: %w", ds.Name, err)
	}

	for _, base := range []string{
		filepath.Join(selectedRoot, ds.Name, "vm"),
		filepath.Join(selectedRoot, ds.Name, "ct"),
		filepath.Join(smallRoot, ds.Name, "vm"),
		filepath.Join(smallRoot, ds.Name, "ct"),
	} {
		if err := c.ensureDir(base); err != nil {
			c.logger.Debug("Failed to prepare PXAR directory %s: %v", base, err)
		}
	}

	meta := struct {
		Name              string        `json:"name"`
		Path              string        `json:"path"`
		Comment           string        `json:"comment,omitempty"`
		ScannedAt         time.Time     `json:"scanned_at"`
		SampleDirectories []string      `json:"sample_directories,omitempty"`
		SamplePxarFiles   []FileSummary `json:"sample_pxar_files,omitempty"`
	}{
		Name:      ds.Name,
		Path:      ds.Path,
		Comment:   ds.Comment,
		ScannedAt: time.Now(),
	}

	if dirs, err := c.sampleDirectories(ctx, ds.Path, 2, 30); err == nil && len(dirs) > 0 {
		meta.SampleDirectories = dirs
		c.logger.Debug("PXAR: datastore %s -> selected %d sample directories", ds.Name, len(dirs))
	} else if err != nil {
		c.logger.Debug("PXAR: datastore %s -> sampleDirectories error: %v", ds.Name, err)
	}

	includePatterns := c.config.PxarFileIncludePatterns
	if len(includePatterns) == 0 {
		includePatterns = []string{"*.pxar", "*.pxar.*", "catalog.pxar", "catalog.pxar.*"}
	}
	excludePatterns := c.config.PxarFileExcludePatterns
	if files, err := c.sampleFiles(ctx, ds.Path, includePatterns, excludePatterns, 8, 200); err == nil && len(files) > 0 {
		meta.SamplePxarFiles = files
		c.logger.Debug("PXAR: datastore %s -> selected %d sample pxar files", ds.Name, len(files))
	} else if err != nil {
		c.logger.Debug("PXAR: datastore %s -> sampleFiles error: %v", ds.Name, err)
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal PXAR metadata for %s: %w", ds.Name, err)
	}

	if err := c.writeReportFile(filepath.Join(dsDir, "metadata.json"), data); err != nil {
		return err
	}

	if err := c.writePxarSubdirReport(filepath.Join(dsDir, fmt.Sprintf("%s_subdirs.txt", ds.Name)), ds); err != nil {
		return err
	}

	if err := c.writePxarListReport(filepath.Join(dsDir, fmt.Sprintf("%s_vm_pxar_list.txt", ds.Name)), ds, "vm"); err != nil {
		return err
	}

	if err := c.writePxarListReport(filepath.Join(dsDir, fmt.Sprintf("%s_ct_pxar_list.txt", ds.Name)), ds, "ct"); err != nil {
		return err
	}

	c.logger.Debug("PXAR: datastore %s completed in %s", ds.Name, time.Since(start).Truncate(time.Millisecond))
	return nil
}

func (c *Collector) writePxarSubdirReport(target string, ds pbsDatastore) error {
	c.logger.Debug("Writing PXAR subdirectory report for datastore %s", ds.Name)
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("# Datastore subdirectories in %s generated on %s\n", ds.Path, time.Now().Format(time.RFC1123)))
	builder.WriteString(fmt.Sprintf("# Datastore: %s\n", ds.Name))

	entries, err := os.ReadDir(ds.Path)
	if err != nil {
		builder.WriteString(fmt.Sprintf("# Unable to read datastore path: %v\n", err))
		return c.writeReportFile(target, []byte(builder.String()))
	}

	hasSubdirs := false
	for _, entry := range entries {
		if entry.IsDir() {
			builder.WriteString(entry.Name())
			builder.WriteByte('\n')
			hasSubdirs = true
		}
	}

	if !hasSubdirs {
		builder.WriteString("# No subdirectories found\n")
	}

	if err := c.writeReportFile(target, []byte(builder.String())); err != nil {
		return err
	}
	c.logger.Debug("PXAR subdirectory report written: %s", target)
	return nil
}

func (c *Collector) writePxarListReport(target string, ds pbsDatastore, subDir string) error {
	c.logger.Debug("Writing PXAR file list for datastore %s subdir %s", ds.Name, subDir)
	basePath := filepath.Join(ds.Path, subDir)

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("# List of .pxar files in %s generated on %s\n", basePath, time.Now().Format(time.RFC1123)))
	builder.WriteString(fmt.Sprintf("# Datastore: %s, Subdirectory: %s\n", ds.Name, subDir))
	builder.WriteString("# Format: permissions size date name\n")

	entries, err := os.ReadDir(basePath)
	if err != nil {
		builder.WriteString(fmt.Sprintf("# Unable to read directory: %v\n", err))
		if writeErr := c.writeReportFile(target, []byte(builder.String())); writeErr != nil {
			return writeErr
		}
		c.logger.Info("PXAR: datastore %s/%s -> path %s not accessible (%v)", ds.Name, subDir, basePath, err)
		return nil
	}

	type infoEntry struct {
		mode os.FileMode
		size int64
		time time.Time
		name string
	}

	var files []infoEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".pxar") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, infoEntry{
			mode: info.Mode(),
			size: info.Size(),
			time: info.ModTime(),
			name: entry.Name(),
		})
	}

	count := len(files)
	if count == 0 {
		builder.WriteString("# No .pxar files found\n")
	} else {
		for _, file := range files {
			builder.WriteString(fmt.Sprintf("%s %d %s %s\n",
				file.mode.String(),
				file.size,
				file.time.Format("2006-01-02 15:04:05"),
				file.name))
		}
	}

	if err := c.writeReportFile(target, []byte(builder.String())); err != nil {
		return err
	}
	c.logger.Debug("PXAR file list report written: %s", target)
	if count == 0 {
		c.logger.Info("PXAR: datastore %s/%s -> 0 .pxar files", ds.Name, subDir)
	} else {
		c.logger.Info("PXAR: datastore %s/%s -> %d .pxar file(s)", ds.Name, subDir, count)
	}
	return nil
}

// getDatastoreList retrieves the list of configured datastores
func (c *Collector) getDatastoreList(ctx context.Context) ([]pbsDatastore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.logger.Debug("Enumerating PBS datastores via proxmox-backup-manager")

	if _, err := c.depLookPath("proxmox-backup-manager"); err != nil {
		return nil, nil
	}

	output, err := c.depRunCommand(ctx, "proxmox-backup-manager", "datastore", "list", "--output-format=json")
	if err != nil {
		return nil, fmt.Errorf("proxmox-backup-manager datastore list failed: %w", err)
	}

	type datastoreEntry struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Comment string `json:"comment"`
	}

	var entries []datastoreEntry
	if err := json.Unmarshal(output, &entries); err != nil {
		return nil, fmt.Errorf("failed to parse datastore list JSON: %w", err)
	}

	datastores := make([]pbsDatastore, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name != "" {
			datastores = append(datastores, pbsDatastore{
				Name:    name,
				Path:    strings.TrimSpace(entry.Path),
				Comment: strings.TrimSpace(entry.Comment),
			})
		}
	}

	if len(c.config.PBSDatastorePaths) > 0 {
		existing := make(map[string]struct{}, len(datastores))
		for _, ds := range datastores {
			if ds.Path != "" {
				existing[ds.Path] = struct{}{}
			}
		}
		validName := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
		for idx, override := range c.config.PBSDatastorePaths {
			override = strings.TrimSpace(override)
			if override == "" {
				continue
			}
			if _, ok := existing[override]; ok {
				continue
			}
			name := filepath.Base(filepath.Clean(override))
			if name == "" || name == "." || name == string(os.PathSeparator) || !validName.MatchString(name) {
				name = fmt.Sprintf("datastore_%d", idx+1)
			}
			datastores = append(datastores, pbsDatastore{
				Name:    name,
				Path:    override,
				Comment: "configured via PBS_DATASTORE_PATH",
			})
		}
	}

	c.logger.Debug("Detected %d configured datastores", len(datastores))
	return datastores, nil
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
