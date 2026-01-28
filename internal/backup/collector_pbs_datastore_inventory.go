package backup

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type inventoryFileSnapshot struct {
	LogicalPath string `json:"logical_path"`
	SourcePath  string `json:"source_path,omitempty"`
	Exists      bool   `json:"exists"`
	Skipped     bool   `json:"skipped,omitempty"`
	Reason      string `json:"reason,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Content     string `json:"content,omitempty"`
	Error       string `json:"error,omitempty"`
}

type inventoryDirSnapshot struct {
	LogicalPath string              `json:"logical_path"`
	SourcePath  string              `json:"source_path,omitempty"`
	Exists      bool                `json:"exists"`
	Skipped     bool                `json:"skipped,omitempty"`
	Reason      string              `json:"reason,omitempty"`
	Error       string              `json:"error,omitempty"`
	Files       []inventoryDirEntry `json:"files,omitempty"`
}

type inventoryDirEntry struct {
	RelativePath  string `json:"relative_path"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	IsSymlink     bool   `json:"is_symlink,omitempty"`
	SymlinkTarget string `json:"symlink_target,omitempty"`
	Error         string `json:"error,omitempty"`
}

type inventoryCommandSnapshot struct {
	Command string `json:"command"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type pbsDatastorePathMarkers struct {
	HasChunks   bool `json:"has_chunks,omitempty"`
	HasLock     bool `json:"has_lock,omitempty"`
	HasGCStatus bool `json:"has_gc_status,omitempty"`
	HasVMDir    bool `json:"has_vm_dir,omitempty"`
	HasCTDir    bool `json:"has_ct_dir,omitempty"`
}

type pbsDatastoreInventoryEntry struct {
	Name      string                  `json:"name"`
	Path      string                  `json:"path,omitempty"`
	Comment   string                  `json:"comment,omitempty"`
	Sources   []string                `json:"sources,omitempty"`
	StatPath  string                  `json:"stat_path,omitempty"`
	PathOK    bool                    `json:"path_ok,omitempty"`
	PathIsDir bool                    `json:"path_is_dir,omitempty"`
	Markers   pbsDatastorePathMarkers `json:"markers,omitempty"`

	Findmnt inventoryCommandSnapshot `json:"findmnt,omitempty"`
	DF      inventoryCommandSnapshot `json:"df,omitempty"`
}

type pbsDatastoreInventoryReport struct {
	GeneratedAt       string `json:"generated_at"`
	Hostname          string `json:"hostname,omitempty"`
	SystemRootPrefix  string `json:"system_root_prefix,omitempty"`
	PBSConfigPath     string `json:"pbs_config_path,omitempty"`
	HostCommands      bool   `json:"host_commands,omitempty"`
	DatastoreCfgParse bool   `json:"datastore_cfg_parse,omitempty"`

	Files      map[string]inventoryFileSnapshot    `json:"files,omitempty"`
	Dirs       map[string]inventoryDirSnapshot     `json:"dirs,omitempty"`
	Commands   map[string]inventoryCommandSnapshot `json:"commands,omitempty"`
	Datastores []pbsDatastoreInventoryEntry        `json:"datastores,omitempty"`
}

func (c *Collector) collectPBSDatastoreInventory(ctx context.Context, cliDatastores []pbsDatastore) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	commandsDir := c.proxsaveCommandsDir("pbs")
	if err := c.ensureDir(commandsDir); err != nil {
		return fmt.Errorf("ensure commands dir: %w", err)
	}

	outputPath := filepath.Join(commandsDir, "pbs_datastore_inventory.json")
	if c.shouldExclude(outputPath) {
		c.incFilesSkipped()
		return nil
	}

	ensureSystemPath()

	// Copy storage stack configuration that may be needed to re-mount datastore paths (best effort).
	for _, dir := range []struct {
		src  string
		dest string
		desc string
	}{
		{src: "/etc/iscsi", dest: filepath.Join(c.tempDir, "etc/iscsi"), desc: "iSCSI configuration"},
		{src: "/var/lib/iscsi", dest: filepath.Join(c.tempDir, "var/lib/iscsi"), desc: "iSCSI runtime state"},
		{src: "/etc/multipath", dest: filepath.Join(c.tempDir, "etc/multipath"), desc: "multipath configuration"},
		{src: "/etc/mdadm", dest: filepath.Join(c.tempDir, "etc/mdadm"), desc: "mdadm configuration"},
		{src: "/etc/lvm/backup", dest: filepath.Join(c.tempDir, "etc/lvm/backup"), desc: "LVM metadata backups"},
		{src: "/etc/lvm/archive", dest: filepath.Join(c.tempDir, "etc/lvm/archive"), desc: "LVM metadata archives"},
		{src: "/etc/zfs", dest: filepath.Join(c.tempDir, "etc/zfs"), desc: "ZFS configuration/cache"},
	} {
		if err := c.safeCopyDir(ctx, c.systemPath(dir.src), dir.dest, dir.desc); err != nil {
			c.logger.Warning("Failed to collect %s (%s): %v", dir.desc, dir.src, err)
		}
	}
	if err := c.safeCopyFile(ctx, c.systemPath("/etc/multipath.conf"), filepath.Join(c.tempDir, "etc/multipath.conf"), "multipath.conf"); err != nil {
		c.logger.Warning("Failed to collect /etc/multipath.conf: %v", err)
	}

	report := pbsDatastoreInventoryReport{
		GeneratedAt:      time.Now().Format(time.RFC3339),
		SystemRootPrefix: strings.TrimSpace(c.config.SystemRootPrefix),
		PBSConfigPath:    c.pbsConfigPath(),
		HostCommands:     c.shouldRunHostCommands(),
		Files:            make(map[string]inventoryFileSnapshot),
		Dirs:             make(map[string]inventoryDirSnapshot),
		Commands:         make(map[string]inventoryCommandSnapshot),
	}
	if host, err := os.Hostname(); err == nil {
		report.Hostname = host
	}

	report.Files["pbs_datastore_cfg"] = c.captureInventoryFile(filepath.Join(c.pbsConfigPath(), "datastore.cfg"), "pbsConfig/datastore.cfg")
	report.Files["fstab"] = c.captureInventoryFile(c.systemPath("/etc/fstab"), "/etc/fstab")
	report.Files["crypttab"] = c.captureInventoryFile(c.systemPath("/etc/crypttab"), "/etc/crypttab")
	report.Files["mdstat"] = c.captureInventoryFile(c.systemPath("/proc/mdstat"), "/proc/mdstat")
	report.Files["os_release"] = c.captureInventoryFile(c.systemPath("/etc/os-release"), "/etc/os-release")
	report.Files["proc_mounts"] = c.captureInventoryFile(c.systemPath("/proc/mounts"), "/proc/mounts")
	report.Files["lvm_conf"] = c.captureInventoryFile(c.systemPath("/etc/lvm/lvm.conf"), "/etc/lvm/lvm.conf")
	report.Files["multipath_conf"] = c.captureInventoryFile(c.systemPath("/etc/multipath.conf"), "/etc/multipath.conf")
	report.Files["multipath_bindings"] = c.captureInventoryFile(c.systemPath("/etc/multipath/bindings"), "/etc/multipath/bindings")
	report.Files["multipath_wwids"] = c.captureInventoryFile(c.systemPath("/etc/multipath/wwids"), "/etc/multipath/wwids")
	report.Files["mdadm_conf"] = c.captureInventoryFile(c.systemPath("/etc/mdadm/mdadm.conf"), "/etc/mdadm/mdadm.conf")
	report.Files["iscsi_initiatorname"] = c.captureInventoryFile(c.systemPath("/etc/iscsi/initiatorname.iscsi"), "/etc/iscsi/initiatorname.iscsi")
	report.Files["iscsi_iscsid_conf"] = c.captureInventoryFile(c.systemPath("/etc/iscsi/iscsid.conf"), "/etc/iscsi/iscsid.conf")
	report.Files["autofs_master"] = c.captureInventoryFile(c.systemPath("/etc/auto.master"), "/etc/auto.master")
	report.Files["autofs_conf"] = c.captureInventoryFile(c.systemPath("/etc/autofs.conf"), "/etc/autofs.conf")
	report.Files["zfs_zpool_cache"] = c.captureInventoryFile(c.systemPath("/etc/zfs/zpool.cache"), "/etc/zfs/zpool.cache")

	report.Dirs["iscsi_etc"] = c.captureInventoryDir(ctx, c.systemPath("/etc/iscsi"), "/etc/iscsi")
	report.Dirs["iscsi_var_lib"] = c.captureInventoryDir(ctx, c.systemPath("/var/lib/iscsi"), "/var/lib/iscsi")
	report.Dirs["multipath_etc"] = c.captureInventoryDir(ctx, c.systemPath("/etc/multipath"), "/etc/multipath")
	report.Dirs["mdadm_etc"] = c.captureInventoryDir(ctx, c.systemPath("/etc/mdadm"), "/etc/mdadm")
	report.Dirs["lvm_backup"] = c.captureInventoryDir(ctx, c.systemPath("/etc/lvm/backup"), "/etc/lvm/backup")
	report.Dirs["lvm_archive"] = c.captureInventoryDir(ctx, c.systemPath("/etc/lvm/archive"), "/etc/lvm/archive")
	report.Dirs["zfs_etc"] = c.captureInventoryDir(ctx, c.systemPath("/etc/zfs"), "/etc/zfs")
	report.Dirs["autofs_master_d"] = c.captureInventoryDir(ctx, c.systemPath("/etc/auto.master.d"), "/etc/auto.master.d")

	// Capture systemd mount units (common for remote storage mounts outside /etc/fstab).
	report.Dirs["systemd_mount_units"] = c.captureInventoryDirFiltered(
		ctx,
		c.systemPath("/etc/systemd/system"),
		"/etc/systemd/system",
		func(rel string, info os.FileInfo) bool {
			name := strings.ToLower(filepath.Base(rel))
			return strings.HasSuffix(name, ".mount") || strings.HasSuffix(name, ".automount")
		},
	)
	if err := c.safeCopySystemdMountUnitFiles(ctx); err != nil {
		c.logger.Warning("Failed to collect systemd mount units: %v", err)
	}

	// Capture common autofs map files and copy them into the backup tree (best effort).
	if err := c.safeCopyAutofsMapFiles(ctx); err != nil {
		c.logger.Warning("Failed to collect autofs map files: %v", err)
	}

	// Best-effort: capture and copy referenced key/credential files from crypttab/fstab.
	for _, ref := range uniqueSortedStrings(append(
		extractCrypttabKeyFiles(report.Files["crypttab"].Content),
		extractFstabReferencedFiles(report.Files["fstab"].Content)...,
	)) {
		ref := ref
		key := referencedFileKey(ref)
		snap := c.captureInventoryFile(c.systemPath(ref), ref)
		if !snap.Skipped && snap.Reason == "" {
			snap.Reason = "referenced by fstab/crypttab"
		}
		report.Files[key] = snap

		dest := filepath.Join(c.tempDir, strings.TrimPrefix(ref, "/"))
		if err := c.safeCopyFile(ctx, c.systemPath(ref), dest, "Referenced file"); err != nil {
			c.logger.Warning("Failed to collect referenced file %s: %v", ref, err)
		}
	}

	configDatastores := parsePBSDatastoreCfg(report.Files["pbs_datastore_cfg"].Content)
	if len(configDatastores) > 0 {
		report.DatastoreCfgParse = true
	}

	merged := mergePBSDatastoreDefinitions(cliDatastores, configDatastores)
	report.Datastores = make([]pbsDatastoreInventoryEntry, 0, len(merged))

	for _, def := range merged {
		entry := pbsDatastoreInventoryEntry{
			Name:    def.Name,
			Path:    def.Path,
			Comment: def.Comment,
			Sources: append([]string(nil), def.Sources...),
		}

		statPath := def.Path
		if filepath.IsAbs(statPath) {
			statPath = c.systemPath(statPath)
		}
		entry.StatPath = statPath

		if statPath != "" {
			if info, err := os.Stat(statPath); err == nil {
				entry.PathOK = true
				entry.PathIsDir = info.IsDir()
				entry.Markers = c.inspectPBSDatastorePathMarkers(statPath)
			}
		}

		if report.HostCommands && def.Path != "" && filepath.IsAbs(def.Path) {
			entry.Findmnt = c.captureInventoryCommand(ctx, fmt.Sprintf("findmnt -J -T %s", def.Path), "findmnt", "-J", "-T", def.Path)
			entry.DF = c.captureInventoryCommand(ctx, fmt.Sprintf("df -T %s", def.Path), "df", "-T", def.Path)
		}

		report.Datastores = append(report.Datastores, entry)
	}

	if report.HostCommands {
		report.Commands["uname"] = c.captureInventoryCommand(ctx, "uname -a", "uname", "-a")
		report.Commands["blkid"] = c.captureInventoryCommand(ctx, "blkid", "blkid")
		report.Commands["lsblk_json"] = c.captureInventoryCommand(ctx, "lsblk -J -O", "lsblk", "-J", "-O")
		report.Commands["findmnt_json"] = c.captureInventoryCommand(ctx, "findmnt -J", "findmnt", "-J")
		report.Commands["nfsstat_mounts"] = c.captureInventoryCommand(ctx, "nfsstat -m", "nfsstat", "-m")

		report.Commands["dmsetup_tree"] = c.captureInventoryCommand(ctx, "dmsetup ls --tree", "dmsetup", "ls", "--tree")
		report.Commands["pvs_json"] = c.captureInventoryCommand(ctx, "pvs --reportformat json --units b", "pvs", "--reportformat", "json", "--units", "b")
		report.Commands["vgs_json"] = c.captureInventoryCommand(ctx, "vgs --reportformat json --units b", "vgs", "--reportformat", "json", "--units", "b")
		report.Commands["lvs_json"] = c.captureInventoryCommand(ctx, "lvs --reportformat json --units b -a", "lvs", "--reportformat", "json", "--units", "b", "-a")

		report.Commands["proc_mdstat"] = c.captureInventoryCommand(ctx, "cat /proc/mdstat", "cat", "/proc/mdstat")
		report.Commands["mdadm_scan"] = c.captureInventoryCommand(ctx, "mdadm --detail --scan", "mdadm", "--detail", "--scan")

		report.Commands["multipath_ll"] = c.captureInventoryCommand(ctx, "multipath -ll", "multipath", "-ll")

		report.Commands["iscsi_sessions"] = c.captureInventoryCommand(ctx, "iscsiadm -m session", "iscsiadm", "-m", "session")
		report.Commands["iscsi_nodes"] = c.captureInventoryCommand(ctx, "iscsiadm -m node", "iscsiadm", "-m", "node")
		report.Commands["iscsi_ifaces"] = c.captureInventoryCommand(ctx, "iscsiadm -m iface", "iscsiadm", "-m", "iface")

		report.Commands["zpool_status"] = c.captureInventoryCommand(ctx, "zpool status -P", "zpool", "status", "-P")
		report.Commands["zpool_list"] = c.captureInventoryCommand(ctx, "zpool list", "zpool", "list")
		report.Commands["zfs_list"] = c.captureInventoryCommand(ctx, "zfs list", "zfs", "list")
	} else {
		report.Commands["host_commands_skipped"] = inventoryCommandSnapshot{
			Command: "host_commands",
			Skipped: true,
			Reason:  "system_root_prefix is not host root; skipping host-only commands",
		}
	}

	// Include already collected PBS command outputs if available (best-effort).
	report.Commands["pbs_version_file"] = c.captureInventoryCommandFromFile(filepath.Join(commandsDir, "pbs_version.txt"), "var/lib/proxsave-info/commands/pbs/pbs_version.txt")
	report.Commands["datastore_list_file"] = c.captureInventoryCommandFromFile(filepath.Join(commandsDir, "datastore_list.json"), "var/lib/proxsave-info/commands/pbs/datastore_list.json")

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal datastore inventory report: %w", err)
	}

	if err := c.writeReportFile(outputPath, data); err != nil {
		return err
	}
	return nil
}

func (c *Collector) captureInventoryFile(sourcePath, logicalPath string) inventoryFileSnapshot {
	snap := inventoryFileSnapshot{
		LogicalPath: logicalPath,
		SourcePath:  sourcePath,
	}

	if c.shouldExclude(sourcePath) {
		snap.Skipped = true
		snap.Reason = "excluded by pattern"
		return snap
	}

	data, err := os.ReadFile(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return snap
		}
		snap.Error = err.Error()
		return snap
	}

	snap.Exists = true
	snap.SizeBytes = int64(len(data))
	snap.SHA256 = sha256Hex(data)
	snap.Content = string(data)
	return snap
}

func (c *Collector) captureInventoryDir(ctx context.Context, sourcePath, logicalPath string) inventoryDirSnapshot {
	snap := inventoryDirSnapshot{
		LogicalPath: logicalPath,
		SourcePath:  sourcePath,
	}

	if c.shouldExclude(sourcePath) {
		snap.Skipped = true
		snap.Reason = "excluded by pattern"
		return snap
	}

	info, err := os.Stat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return snap
		}
		snap.Error = err.Error()
		return snap
	}

	if !info.IsDir() {
		snap.Exists = true
		snap.Error = "not a directory"
		return snap
	}

	snap.Exists = true

	var files []inventoryDirEntry
	walkErr := filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
		if err != nil {
			return err
		}
		if info == nil || info.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(sourcePath, path)
		if err != nil {
			return err
		}

		entry := inventoryDirEntry{
			RelativePath: rel,
			SizeBytes:    info.Size(),
		}

		if info.Mode()&os.ModeSymlink != 0 {
			entry.IsSymlink = true
			if target, err := os.Readlink(path); err == nil {
				entry.SymlinkTarget = target
			} else {
				entry.Error = err.Error()
			}
			files = append(files, entry)
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			entry.Error = err.Error()
		} else {
			entry.SHA256 = sha256Hex(data)
		}

		files = append(files, entry)
		return nil
	})
	if walkErr != nil {
		snap.Error = walkErr.Error()
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].RelativePath < files[j].RelativePath
	})
	snap.Files = files
	return snap
}

func (c *Collector) captureInventoryCommandFromFile(path, logical string) inventoryCommandSnapshot {
	out := inventoryCommandSnapshot{
		Command: logical,
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			out.Skipped = true
			out.Reason = "file not present"
			return out
		}
		out.Error = err.Error()
		return out
	}
	out.Output = string(data)
	return out
}

func (c *Collector) captureInventoryCommand(ctx context.Context, pretty string, name string, args ...string) inventoryCommandSnapshot {
	result := inventoryCommandSnapshot{
		Command: pretty,
	}

	if err := ctx.Err(); err != nil {
		result.Error = err.Error()
		return result
	}

	if _, err := c.depLookPath(name); err != nil {
		result.Skipped = true
		result.Reason = "command not found"
		return result
	}

	output, err := c.depRunCommand(ctx, name, args...)
	if err != nil {
		result.Error = err.Error()
	}
	if len(output) > 0 {
		result.Output = string(output)
	}
	return result
}

type pbsDatastoreDefinition struct {
	Name    string
	Path    string
	Comment string
	Sources []string
}

func mergePBSDatastoreDefinitions(cli, config []pbsDatastore) []pbsDatastoreDefinition {
	merged := make(map[string]*pbsDatastoreDefinition)

	add := func(ds pbsDatastore, source string) {
		name := strings.TrimSpace(ds.Name)
		if name == "" {
			return
		}

		entry := merged[name]
		if entry == nil {
			entry = &pbsDatastoreDefinition{Name: name}
			merged[name] = entry
		}

		entry.Sources = append(entry.Sources, source)

		if entry.Path == "" && strings.TrimSpace(ds.Path) != "" {
			entry.Path = strings.TrimSpace(ds.Path)
		}
		if entry.Comment == "" && strings.TrimSpace(ds.Comment) != "" {
			entry.Comment = strings.TrimSpace(ds.Comment)
		}
	}

	for _, ds := range config {
		add(ds, "datastore.cfg")
	}
	for _, ds := range cli {
		add(ds, "cli")
	}

	out := make([]pbsDatastoreDefinition, 0, len(merged))
	for _, v := range merged {
		if v == nil {
			continue
		}
		v.Sources = uniqueSortedStrings(v.Sources)
		out = append(out, *v)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func parsePBSDatastoreCfg(contents string) []pbsDatastore {
	contents = strings.TrimSpace(contents)
	if contents == "" {
		return nil
	}

	var (
		out     []pbsDatastore
		current *pbsDatastore
	)

	flush := func() {
		if current == nil {
			return
		}
		if strings.TrimSpace(current.Name) == "" {
			current = nil
			return
		}
		out = append(out, *current)
		current = nil
	}

	scanner := bufio.NewScanner(strings.NewReader(contents))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "datastore:") {
			flush()
			name := strings.TrimSpace(strings.TrimPrefix(line, "datastore:"))
			if name == "" {
				continue
			}
			current = &pbsDatastore{Name: name}
			continue
		}

		if current == nil {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		key := fields[0]
		rest := strings.TrimSpace(line[len(key):])

		switch key {
		case "path":
			current.Path = strings.TrimSpace(rest)
		case "comment":
			current.Comment = strings.TrimSpace(rest)
		}
	}
	flush()

	return out
}

func (c *Collector) inspectPBSDatastorePathMarkers(path string) pbsDatastorePathMarkers {
	markers := pbsDatastorePathMarkers{}
	if path == "" {
		return markers
	}

	statAny := func(rel string) bool {
		_, err := os.Stat(filepath.Join(path, rel))
		return err == nil
	}

	markers.HasChunks = statAny(".chunks")
	markers.HasLock = statAny(".lock")
	markers.HasGCStatus = statAny(".gc-status")
	markers.HasVMDir = statAny("vm")
	markers.HasCTDir = statAny("ct")

	return markers
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func uniqueSortedStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		out = append(out, raw)
	}
	sort.Strings(out)
	return out
}

func referencedFileKey(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "ref_empty"
	}
	sum := sha256.Sum256([]byte(path))
	return fmt.Sprintf("ref_%s_%s", sanitizeFilename(path), hex.EncodeToString(sum[:4]))
}

func extractCrypttabKeyFiles(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	var out []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		keyFile := strings.TrimSpace(fields[2])
		if keyFile == "" || keyFile == "none" || keyFile == "-" {
			continue
		}
		if strings.HasPrefix(keyFile, "/") {
			out = append(out, keyFile)
		}
	}
	return uniqueSortedStrings(out)
}

func extractFstabReferencedFiles(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	keys := map[string]struct{}{
		"credentials": {},
		"cred":        {},
		"passwd":      {},
		"passfile":    {},
		"keyfile":     {},
		"identityfile": {},
	}

	var out []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		opts := fields[3]
		for _, opt := range strings.Split(opts, ",") {
			opt = strings.TrimSpace(opt)
			if opt == "" || !strings.Contains(opt, "=") {
				continue
			}
			parts := strings.SplitN(opt, "=", 2)
			key := strings.ToLower(strings.TrimSpace(parts[0]))
			val := strings.TrimSpace(parts[1])
			if key == "" || val == "" {
				continue
			}
			if _, ok := keys[key]; !ok {
				continue
			}
			if strings.HasPrefix(val, "/") {
				out = append(out, val)
			}
		}
	}
	return uniqueSortedStrings(out)
}

func (c *Collector) safeCopySystemdMountUnitFiles(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	base := c.systemPath("/etc/systemd/system")
	info, err := os.Stat(base)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		return nil
	}

	destBase := filepath.Join(c.tempDir, "etc/systemd/system")
	if c.shouldExclude(base) || c.shouldExclude(destBase) {
		c.incFilesSkipped()
		return nil
	}

	if c.dryRun {
		return nil
	}
	if err := c.ensureDir(destBase); err != nil {
		return err
	}

	return filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
		if err != nil {
			return err
		}
		if info == nil || info.IsDir() {
			return nil
		}
		name := strings.ToLower(info.Name())
		if !strings.HasSuffix(name, ".mount") && !strings.HasSuffix(name, ".automount") {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(destBase, rel)
		if c.shouldExclude(path) || c.shouldExclude(dest) {
			return nil
		}
		return c.safeCopyFile(ctx, path, dest, "systemd mount unit")
	})
}

func (c *Collector) safeCopyAutofsMapFiles(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	for _, path := range []string{
		"/etc/auto.master",
		"/etc/autofs.conf",
	} {
		src := c.systemPath(path)
		dest := filepath.Join(c.tempDir, strings.TrimPrefix(path, "/"))
		if err := c.safeCopyFile(ctx, src, dest, "autofs config"); err != nil {
			// Non-critical; safeCopyFile already counts failures when appropriate.
			continue
		}
	}

	// /etc/auto.* maps (e.g. /etc/auto.nfs, /etc/auto.cifs)
	glob := c.systemPath("/etc/auto.*")
	matches, _ := filepath.Glob(glob)
	for _, src := range matches {
		base := filepath.Base(src)
		if base == "auto.master" {
			continue
		}
		rel := filepath.Join("etc", base)
		dest := filepath.Join(c.tempDir, rel)
		_ = c.safeCopyFile(ctx, src, dest, "autofs map")
	}

	// /etc/auto.master.d (drop-in directory)
	_ = c.safeCopyDir(ctx, c.systemPath("/etc/auto.master.d"), filepath.Join(c.tempDir, "etc/auto.master.d"), "autofs drop-in configs")
	return nil
}

func (c *Collector) captureInventoryDirFiltered(ctx context.Context, sourcePath, logicalPath string, include func(rel string, info os.FileInfo) bool) inventoryDirSnapshot {
	snap := inventoryDirSnapshot{
		LogicalPath: logicalPath,
		SourcePath:  sourcePath,
	}

	if c.shouldExclude(sourcePath) {
		snap.Skipped = true
		snap.Reason = "excluded by pattern"
		return snap
	}

	info, err := os.Stat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return snap
		}
		snap.Error = err.Error()
		return snap
	}
	if !info.IsDir() {
		snap.Exists = true
		snap.Error = "not a directory"
		return snap
	}
	snap.Exists = true

	var files []inventoryDirEntry
	walkErr := filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
		if err != nil {
			return err
		}
		if info == nil || info.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(sourcePath, path)
		if err != nil {
			return err
		}
		if include != nil && !include(rel, info) {
			return nil
		}

		entry := inventoryDirEntry{
			RelativePath: rel,
			SizeBytes:    info.Size(),
		}

		if info.Mode()&os.ModeSymlink != 0 {
			entry.IsSymlink = true
			if target, err := os.Readlink(path); err == nil {
				entry.SymlinkTarget = target
			} else {
				entry.Error = err.Error()
			}
			files = append(files, entry)
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			entry.Error = err.Error()
		} else {
			entry.SHA256 = sha256Hex(data)
		}

		files = append(files, entry)
		return nil
	})
	if walkErr != nil {
		snap.Error = walkErr.Error()
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].RelativePath < files[j].RelativePath
	})
	snap.Files = files
	return snap
}
