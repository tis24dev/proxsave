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

// collectDatastoreConfigs collects detailed datastore configurations
func (c *Collector) collectDatastoreConfigs(ctx context.Context, datastores []pbsDatastore) error {
	if len(datastores) == 0 {
		c.logger.Debug("No datastores found")
		return nil
	}
	c.logger.Debug("Collecting datastore details for %d datastores", len(datastores))

	datastoreDir := c.proxsaveInfoDir("pbs", "datastores")
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
	// Write location is deterministic; if excluded, skip the whole operation.
	outputPath := filepath.Join(datastoreDir, fmt.Sprintf("%s_namespaces.json", ds.Name))
	if c.shouldExclude(outputPath) {
		c.incFilesSkipped()
		return nil
	}

	namespaces, fromFallback, err := listNamespacesFunc(ds.Name, ds.Path)
	if err != nil {
		return err
	}

	// Write namespaces to JSON file
	data, err := json.MarshalIndent(namespaces, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal namespaces: %w", err)
	}

	if err := c.writeReportFile(outputPath, data); err != nil {
		return fmt.Errorf("failed to write namespaces file: %w", err)
	}

	if fromFallback {
		c.logger.Debug("Successfully collected %d namespaces for datastore %s via filesystem fallback", len(namespaces), ds.Name)
	} else {
		c.logger.Debug("Successfully collected %d namespaces for datastore %s via CLI", len(namespaces), ds.Name)
	}
	return nil
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

	pxarRoot := c.proxsaveInfoDir("pbs", "pxar")
	metaRoot := filepath.Join(pxarRoot, "metadata")
	if err := c.ensureDir(metaRoot); err != nil {
		return fmt.Errorf("failed to create PXAR metadata directory: %w", err)
	}

	selectedRoot := filepath.Join(pxarRoot, "selected")
	if err := c.ensureDir(selectedRoot); err != nil {
		return fmt.Errorf("failed to create selected_pxar directory: %w", err)
	}

	smallRoot := filepath.Join(pxarRoot, "small")
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
