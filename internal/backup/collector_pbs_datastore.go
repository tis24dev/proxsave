package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tis24dev/proxsave/internal/pbs"
	"github.com/tis24dev/proxsave/internal/safefs"
)

const (
	pbsDatastoreSourceCLI      = "cli"
	pbsDatastoreSourceOverride = "override"
	pbsDatastoreSourceConfig   = "config"
	pbsDatastoreOriginMerged   = "merged"
)

var (
	pbsDatastoreNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	listNamespacesFunc      = pbs.ListNamespaces
	discoverNamespacesFunc  = pbs.DiscoverNamespacesFromFilesystem
)

type pbsDatastore struct {
	Name           string
	Path           string
	Comment        string
	Source         string
	CLIName        string
	NormalizedPath string
	OutputKey      string
}

func normalizePBSDatastorePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	return filepath.Clean(trimmed)
}

func buildPBSOverrideDisplayName(path string, idx int) string {
	name := filepath.Base(normalizePBSDatastorePath(path))
	if name == "" || name == "." || name == string(os.PathSeparator) || !pbsDatastoreNamePattern.MatchString(name) {
		return fmt.Sprintf("datastore_%d", idx+1)
	}
	return name
}

func buildPBSOverrideOutputKey(path string) string {
	normalized := normalizePBSDatastorePath(path)
	if normalized == "" {
		return "entry"
	}

	label := filepath.Base(normalized)
	if label == "" || label == "." || label == string(os.PathSeparator) || !pbsDatastoreNamePattern.MatchString(label) {
		label = "datastore"
	}

	sum := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("%s_%s", sanitizeFilename(label), hex.EncodeToString(sum[:4]))
}

func pbsOutputKeyDigest(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:4])
}

func pbsDatastoreIdentityKey(ds pbsDatastore) string {
	if ds.isOverride() {
		if normalized := ds.normalizedPath(); normalized != "" {
			return "override-path:" + normalized
		}
		if path := strings.TrimSpace(ds.Path); path != "" {
			return "override-path:" + path
		}
		return ""
	}

	name := strings.TrimSpace(ds.Name)
	if name == "" {
		return ""
	}
	return "name:" + name
}

func pbsDatastoreDefinitionIdentityKey(def pbsDatastoreDefinition) string {
	if strings.TrimSpace(def.Origin) == pbsDatastoreSourceOverride {
		if normalized := normalizePBSDatastorePath(def.Path); normalized != "" {
			return "override-path:" + normalized
		}
		if path := strings.TrimSpace(def.Path); path != "" {
			return "override-path:" + path
		}
		return ""
	}

	name := strings.TrimSpace(def.Name)
	if name == "" {
		name = strings.TrimSpace(def.CLIName)
	}
	if name == "" {
		return ""
	}
	return "name:" + name
}

func pbsDatastoreCandidateOutputKey(ds pbsDatastore) string {
	if ds.isOverride() {
		if normalized := ds.normalizedPath(); normalized != "" {
			return buildPBSOverrideOutputKey(normalized)
		}
		return buildPBSOverrideOutputKey(ds.Path)
	}
	return collectorPathKey(ds.Name)
}

func pbsDatastoreDefinitionCandidateOutputKey(def pbsDatastoreDefinition) string {
	if strings.TrimSpace(def.Origin) == pbsDatastoreSourceOverride {
		if normalized := normalizePBSDatastorePath(def.Path); normalized != "" {
			return buildPBSOverrideOutputKey(normalized)
		}
		return buildPBSOverrideOutputKey(def.Path)
	}

	name := strings.TrimSpace(def.Name)
	if name == "" {
		name = strings.TrimSpace(def.CLIName)
	}
	return collectorPathKey(name)
}

func pbsOutputKeyPriority(origin string) int {
	if strings.TrimSpace(origin) == pbsDatastoreSourceOverride {
		return 1
	}
	return 0
}

type pbsOutputKeyAssignment struct {
	Index    int
	Identity string
	BaseKey  string
	Priority int
}

func assignUniquePBSOutputKeys[T any](items []T, identityFn func(T) string, baseKeyFn func(T) string, priorityFn func(T) int, assignFn func(*T, string)) {
	if len(items) == 0 {
		return
	}

	grouped := make(map[string][]pbsOutputKeyAssignment, len(items))
	baseKeys := make([]string, 0, len(items))
	for idx, item := range items {
		baseKey := strings.TrimSpace(baseKeyFn(item))
		if baseKey == "" {
			baseKey = "entry"
		}

		identity := strings.TrimSpace(identityFn(item))
		if identity == "" {
			identity = fmt.Sprintf("anonymous:%s:%d", baseKey, idx)
		}

		if _, ok := grouped[baseKey]; !ok {
			baseKeys = append(baseKeys, baseKey)
		}
		grouped[baseKey] = append(grouped[baseKey], pbsOutputKeyAssignment{
			Index:    idx,
			Identity: identity,
			BaseKey:  baseKey,
			Priority: priorityFn(item),
		})
	}

	sort.Strings(baseKeys)

	usedKeys := make(map[string]string, len(items))
	identityKeys := make(map[string]string, len(items))

	for _, baseKey := range baseKeys {
		assignments := grouped[baseKey]
		sort.SliceStable(assignments, func(i, j int) bool {
			if assignments[i].Priority != assignments[j].Priority {
				return assignments[i].Priority < assignments[j].Priority
			}
			if assignments[i].Identity != assignments[j].Identity {
				return assignments[i].Identity < assignments[j].Identity
			}
			return assignments[i].Index < assignments[j].Index
		})

		for pos, assignment := range assignments {
			if existing := strings.TrimSpace(identityKeys[assignment.Identity]); existing != "" {
				assignFn(&items[assignment.Index], existing)
				continue
			}

			preferBase := pos == 0
			for attempt := 0; ; attempt++ {
				candidate := assignment.BaseKey
				if !preferBase || attempt > 0 {
					seed := assignment.Identity
					if attempt > 0 {
						seed = fmt.Sprintf("%s#%d", assignment.Identity, attempt)
					}
					candidate = fmt.Sprintf("%s_%s", assignment.BaseKey, pbsOutputKeyDigest(seed))
				}

				if owner, ok := usedKeys[candidate]; ok && owner != assignment.Identity {
					continue
				}

				usedKeys[candidate] = assignment.Identity
				identityKeys[assignment.Identity] = candidate
				assignFn(&items[assignment.Index], candidate)
				break
			}
		}
	}
}

func assignUniquePBSDatastoreOutputKeys(datastores []pbsDatastore) {
	assignUniquePBSOutputKeys(datastores,
		pbsDatastoreIdentityKey,
		pbsDatastoreCandidateOutputKey,
		func(ds pbsDatastore) int {
			return pbsOutputKeyPriority(ds.Source)
		},
		func(ds *pbsDatastore, key string) {
			ds.OutputKey = key
		})
}

func assignUniquePBSDatastoreDefinitionOutputKeys(defs []pbsDatastoreDefinition) {
	assignUniquePBSOutputKeys(defs,
		pbsDatastoreDefinitionIdentityKey,
		pbsDatastoreDefinitionCandidateOutputKey,
		func(def pbsDatastoreDefinition) int {
			return pbsOutputKeyPriority(def.Origin)
		},
		func(def *pbsDatastoreDefinition, key string) {
			def.OutputKey = key
		})
}

func clonePBSDatastores(in []pbsDatastore) []pbsDatastore {
	if len(in) == 0 {
		return nil
	}

	out := make([]pbsDatastore, len(in))
	copy(out, in)
	return out
}

func (ds pbsDatastore) normalizedPath() string {
	if path := strings.TrimSpace(ds.NormalizedPath); path != "" {
		return path
	}
	return normalizePBSDatastorePath(ds.Path)
}

func (ds pbsDatastore) pathKey() string {
	if key := strings.TrimSpace(ds.OutputKey); key != "" {
		return key
	}
	return pbsDatastoreCandidateOutputKey(ds)
}

func (ds pbsDatastore) cliName() string {
	if name := strings.TrimSpace(ds.CLIName); name != "" {
		return name
	}
	return strings.TrimSpace(ds.Name)
}

func (ds pbsDatastore) isOverride() bool {
	return strings.TrimSpace(ds.Source) == pbsDatastoreSourceOverride
}

func (ds pbsDatastore) inventoryOrigin() string {
	if origin := strings.TrimSpace(ds.Source); origin != "" {
		return origin
	}
	return pbsDatastoreSourceCLI
}

// collectDatastoreConfigs collects detailed datastore configurations
func (c *Collector) collectDatastoreConfigs(ctx context.Context, datastores []pbsDatastore) error {
	if len(datastores) == 0 {
		c.logger.Debug("No datastores found")
		return nil
	}
	datastores = clonePBSDatastores(datastores)
	assignUniquePBSDatastoreOutputKeys(datastores)
	c.logger.Debug("Collecting datastore details for %d datastores", len(datastores))

	datastoreDir := c.proxsaveInfoDir("pbs", "datastores")
	if err := c.ensureDir(datastoreDir); err != nil {
		return fmt.Errorf("failed to create datastores directory: %w", err)
	}

	for _, ds := range datastores {
		dsKey := ds.pathKey()

		if cliName := ds.cliName(); cliName != "" && !ds.isOverride() {
			// Get datastore configuration details for CLI-backed datastores only.
			c.safeCmdOutput(ctx,
				fmt.Sprintf("proxmox-backup-manager datastore show %s --output-format=json", cliName),
				filepath.Join(datastoreDir, fmt.Sprintf("%s_config.json", dsKey)),
				fmt.Sprintf("Datastore %s configuration", ds.Name),
				false)
		} else {
			c.logger.Debug("Skipping datastore CLI config for %s (path=%s): no PBS datastore identity", ds.Name, ds.Path)
		}

		// Get namespace list using CLI/Filesystem fallback
		if err := c.collectDatastoreNamespaces(ctx, ds, datastoreDir); err != nil {
			c.logger.Debug("Failed to collect namespaces for datastore %s: %v", ds.Name, err)
		}
	}

	c.logger.Debug("Datastore configuration collection completed")
	return nil
}

// collectDatastoreNamespaces collects namespace information for a datastore
// using CLI first, then filesystem fallback.
func (c *Collector) collectDatastoreNamespaces(ctx context.Context, ds pbsDatastore, datastoreDir string) error {
	c.logger.Debug("Collecting namespaces for datastore %s (path: %s)", ds.Name, ds.Path)
	// Write location is deterministic; if excluded, skip the whole operation.
	outputPath := filepath.Join(datastoreDir, fmt.Sprintf("%s_namespaces.json", ds.pathKey()))
	if c.shouldExclude(outputPath) {
		c.incFilesSkipped()
		return nil
	}

	ioTimeout := time.Duration(0)
	if c.config != nil && c.config.FsIoTimeoutSeconds > 0 {
		ioTimeout = time.Duration(c.config.FsIoTimeoutSeconds) * time.Second
	}

	var (
		namespaces   []pbs.Namespace
		fromFallback bool
		err          error
	)
	if ds.isOverride() {
		namespaces, err = discoverNamespacesFunc(ctx, ds.normalizedPath(), ioTimeout)
		fromFallback = true
	} else {
		namespaces, fromFallback, err = listNamespacesFunc(ctx, ds.cliName(), ds.Path, ioTimeout)
	}
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
	datastores = clonePBSDatastores(datastores)
	assignUniquePBSDatastoreOutputKeys(datastores)
	c.logger.Debug("Collecting PXAR metadata for %d datastores", len(datastores))
	dsWorkers := c.config.PxarDatastoreConcurrency
	if dsWorkers <= 0 {
		dsWorkers = 1
	}
	mode := "sequential"
	if dsWorkers > 1 {
		mode = fmt.Sprintf("parallel (%d workers)", dsWorkers)
	}
	c.logger.Debug("PXAR metadata concurrency: datastores=%s", mode)

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

	ioTimeout := time.Duration(0)
	if c.config != nil && c.config.FsIoTimeoutSeconds > 0 {
		ioTimeout = time.Duration(c.config.FsIoTimeoutSeconds) * time.Second
	}

	stat, err := safefs.Stat(ctx, ds.Path, ioTimeout)
	if err != nil {
		if errors.Is(err, safefs.ErrTimeout) {
			c.logger.Warning("Skipping PXAR metadata for datastore %s (path=%s): filesystem probe timed out (%v)", ds.Name, ds.Path, err)
			return nil
		}
		c.logger.Debug("Skipping PXAR metadata for datastore %s (path not accessible: %s): %v", ds.Name, ds.Path, err)
		return nil
	}
	if !stat.IsDir() {
		c.logger.Debug("Skipping PXAR metadata for datastore %s (path not a directory: %s)", ds.Name, ds.Path)
		return nil
	}

	start := time.Now()
	c.logger.Debug("PXAR: scanning datastore %s at %s", ds.Name, ds.Path)

	dsKey := ds.pathKey()
	dsDir := filepath.Join(metaRoot, dsKey)
	if err := c.ensureDir(dsDir); err != nil {
		return fmt.Errorf("failed to create PXAR metadata directory for %s: %w", ds.Name, err)
	}

	for _, base := range []string{
		filepath.Join(selectedRoot, dsKey, "vm"),
		filepath.Join(selectedRoot, dsKey, "ct"),
		filepath.Join(smallRoot, dsKey, "vm"),
		filepath.Join(smallRoot, dsKey, "ct"),
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

	if dirs, err := c.sampleDirectoriesBounded(ctx, ds.Path, 2, 30, ioTimeout); errors.Is(err, safefs.ErrTimeout) {
		c.logger.Warning("Skipping PXAR metadata for datastore %s (path=%s): directory sampling timed out (%v)", ds.Name, ds.Path, err)
		return nil
	} else if err == nil && len(dirs) > 0 {
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
	if files, err := c.sampleFilesBounded(ctx, ds.Path, includePatterns, excludePatterns, 8, 200, ioTimeout); errors.Is(err, safefs.ErrTimeout) {
		c.logger.Warning("Skipping PXAR metadata for datastore %s (path=%s): file sampling timed out (%v)", ds.Name, ds.Path, err)
		return nil
	} else if err == nil && len(files) > 0 {
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

	if err := c.writePxarSubdirReport(ctx, filepath.Join(dsDir, fmt.Sprintf("%s_subdirs.txt", dsKey)), ds, ioTimeout); err != nil {
		if errors.Is(err, safefs.ErrTimeout) {
			c.logger.Warning("Skipping PXAR metadata for datastore %s (path=%s): subdir report timed out (%v)", ds.Name, ds.Path, err)
			return nil
		}
		return err
	}

	if err := c.writePxarListReport(ctx, filepath.Join(dsDir, fmt.Sprintf("%s_vm_pxar_list.txt", dsKey)), ds, "vm", ioTimeout); err != nil {
		if errors.Is(err, safefs.ErrTimeout) {
			c.logger.Warning("Skipping PXAR metadata for datastore %s (path=%s): VM list report timed out (%v)", ds.Name, ds.Path, err)
			return nil
		}
		return err
	}

	if err := c.writePxarListReport(ctx, filepath.Join(dsDir, fmt.Sprintf("%s_ct_pxar_list.txt", dsKey)), ds, "ct", ioTimeout); err != nil {
		if errors.Is(err, safefs.ErrTimeout) {
			c.logger.Warning("Skipping PXAR metadata for datastore %s (path=%s): CT list report timed out (%v)", ds.Name, ds.Path, err)
			return nil
		}
		return err
	}

	c.logger.Debug("PXAR: datastore %s completed in %s", ds.Name, time.Since(start).Truncate(time.Millisecond))
	return nil
}

func (c *Collector) writePxarSubdirReport(ctx context.Context, target string, ds pbsDatastore, ioTimeout time.Duration) error {
	c.logger.Debug("Writing PXAR subdirectory report for datastore %s", ds.Name)
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("# Datastore subdirectories in %s generated on %s\n", ds.Path, time.Now().Format(time.RFC1123)))
	builder.WriteString(fmt.Sprintf("# Datastore: %s\n", ds.Name))

	entries, err := safefs.ReadDir(ctx, ds.Path, ioTimeout)
	if err != nil {
		if errors.Is(err, safefs.ErrTimeout) {
			return err
		}
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

func (c *Collector) writePxarListReport(ctx context.Context, target string, ds pbsDatastore, subDir string, ioTimeout time.Duration) error {
	c.logger.Debug("Writing PXAR file list for datastore %s subdir %s", ds.Name, subDir)
	basePath := filepath.Join(ds.Path, subDir)

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("# List of .pxar files in %s generated on %s\n", basePath, time.Now().Format(time.RFC1123)))
	builder.WriteString(fmt.Sprintf("# Datastore: %s, Subdirectory: %s\n", ds.Name, subDir))
	builder.WriteString("# Format: permissions size date name\n")

	entries, err := safefs.ReadDir(ctx, basePath, ioTimeout)
	if err != nil {
		if errors.Is(err, safefs.ErrTimeout) {
			return err
		}
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

		fullPath := filepath.Join(basePath, entry.Name())
		info, err := safefs.Stat(ctx, fullPath, ioTimeout)
		if err != nil {
			if errors.Is(err, safefs.ErrTimeout) {
				return err
			}
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

	type datastoreEntry struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Comment string `json:"comment"`
	}

	datastores := make([]pbsDatastore, 0, len(c.config.PBSDatastorePaths))
	if _, err := c.depLookPath("proxmox-backup-manager"); err != nil {
		c.logger.Debug("Skipping PBS datastore CLI enumeration: proxmox-backup-manager not available: %v", err)
	} else {
		output, err := c.depRunCommand(ctx, "proxmox-backup-manager", "datastore", "list", "--output-format=json")
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			c.logger.Debug("PBS datastore CLI enumeration failed: %v", err)
		} else {
			var entries []datastoreEntry
			if err := json.Unmarshal(output, &entries); err != nil {
				c.logger.Debug("Failed to parse PBS datastore list JSON: %v", err)
			} else {
				datastores = make([]pbsDatastore, 0, len(entries)+len(c.config.PBSDatastorePaths))
				for _, entry := range entries {
					name := strings.TrimSpace(entry.Name)
					if name == "" {
						continue
					}
					path := strings.TrimSpace(entry.Path)
					datastores = append(datastores, pbsDatastore{
						Name:           name,
						Path:           path,
						Comment:        strings.TrimSpace(entry.Comment),
						Source:         pbsDatastoreSourceCLI,
						CLIName:        name,
						NormalizedPath: normalizePBSDatastorePath(path),
						OutputKey:      collectorPathKey(name),
					})
				}
			}
		}
	}

	if len(c.config.PBSDatastorePaths) > 0 {
		existing := make(map[string]struct{}, len(datastores))
		for _, ds := range datastores {
			if normalized := ds.normalizedPath(); normalized != "" {
				existing[normalized] = struct{}{}
			}
		}
		for idx, override := range c.config.PBSDatastorePaths {
			override = strings.TrimSpace(override)
			if override == "" {
				continue
			}
			normalized := normalizePBSDatastorePath(override)
			if normalized == "" {
				continue
			}
			if !filepath.IsAbs(normalized) {
				c.logger.Warning("Skipping PBS_DATASTORE_PATH override %q: path must be absolute", override)
				continue
			}
			if _, ok := existing[normalized]; ok {
				continue
			}
			existing[normalized] = struct{}{}
			name := buildPBSOverrideDisplayName(normalized, idx)
			datastores = append(datastores, pbsDatastore{
				Name:           name,
				Path:           override,
				Comment:        "configured via PBS_DATASTORE_PATH",
				Source:         pbsDatastoreSourceOverride,
				NormalizedPath: normalized,
				OutputKey:      buildPBSOverrideOutputKey(normalized),
			})
		}
	}

	assignUniquePBSDatastoreOutputKeys(datastores)

	c.logger.Debug("Detected %d configured datastores", len(datastores))
	return datastores, nil
}
