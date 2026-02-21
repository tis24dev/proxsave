package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

func maybeApplyPBSConfigsFromStage(ctx context.Context, logger *logging.Logger, plan *RestorePlan, stageRoot string, dryRun bool) (err error) {
	if plan == nil || plan.SystemType != SystemTypePBS {
		return nil
	}
	if !plan.HasCategoryID("datastore_pbs") && !plan.HasCategoryID("pbs_jobs") && !plan.HasCategoryID("pbs_remotes") && !plan.HasCategoryID("pbs_host") && !plan.HasCategoryID("pbs_tape") {
		return nil
	}
	if strings.TrimSpace(stageRoot) == "" {
		logging.DebugStep(logger, "pbs staged apply", "Skipped: staging directory not available")
		return nil
	}

	done := logging.DebugStart(logger, "pbs staged apply", "dryRun=%v stage=%s", dryRun, stageRoot)
	defer func() { done(err) }()

	if dryRun {
		logger.Info("Dry run enabled: skipping staged PBS config apply")
		return nil
	}
	if !isRealRestoreFS(restoreFS) {
		logger.Debug("Skipping staged PBS config apply: non-system filesystem in use")
		return nil
	}
	if os.Geteuid() != 0 {
		logger.Warning("Skipping staged PBS config apply: requires root privileges")
		return nil
	}

	behavior := plan.PBSRestoreBehavior
	strict := behavior == PBSRestoreBehaviorClean
	allowFileFallback := behavior == PBSRestoreBehaviorClean

	needsAPI := plan.HasCategoryID("pbs_host") || plan.HasCategoryID("datastore_pbs") || plan.HasCategoryID("pbs_remotes") || plan.HasCategoryID("pbs_jobs")
	apiAvailable := false
	if needsAPI {
		if err := ensurePBSServicesForAPI(ctx, logger); err != nil {
			if allowFileFallback {
				logger.Warning("PBS API apply unavailable; falling back to file-based staged apply where possible: %v", err)
			} else {
				logger.Warning("PBS API apply unavailable; skipping API-applied PBS categories (merge mode): %v", err)
			}
		} else {
			apiAvailable = true
		}
	}

	if plan.HasCategoryID("pbs_host") {
		// Always restore file-only configs (no stable API coverage yet).
		// ACME should be applied before node config (node.cfg references ACME accounts/plugins).
		for _, rel := range []string{
			"etc/proxmox-backup/acme/accounts.cfg",
			"etc/proxmox-backup/acme/plugins.cfg",
			"etc/proxmox-backup/metricserver.cfg",
			"etc/proxmox-backup/proxy.cfg",
		} {
			if err := applyPBSConfigFileFromStage(ctx, logger, stageRoot, rel); err != nil {
				logger.Warning("PBS staged apply: %s: %v", rel, err)
			}
		}

		if apiAvailable {
			if err := applyPBSTrafficControlCfgViaAPI(ctx, logger, stageRoot, strict); err != nil {
				logger.Warning("PBS API apply: traffic-control failed: %v", err)
				if allowFileFallback {
					logger.Warning("PBS staged apply: falling back to file-based traffic-control.cfg")
					_ = applyPBSConfigFileFromStage(ctx, logger, stageRoot, "etc/proxmox-backup/traffic-control.cfg")
				}
			}
			if err := applyPBSNodeCfgViaAPI(ctx, stageRoot); err != nil {
				logger.Warning("PBS API apply: node config failed: %v", err)
				if allowFileFallback {
					logger.Warning("PBS staged apply: falling back to file-based node.cfg")
					_ = applyPBSConfigFileFromStage(ctx, logger, stageRoot, "etc/proxmox-backup/node.cfg")
				}
			}
		} else if allowFileFallback {
			for _, rel := range []string{
				"etc/proxmox-backup/traffic-control.cfg",
				"etc/proxmox-backup/node.cfg",
			} {
				if err := applyPBSConfigFileFromStage(ctx, logger, stageRoot, rel); err != nil {
					logger.Warning("PBS staged apply: %s: %v", rel, err)
				}
			}
		} else {
			logging.DebugStep(logger, "pbs staged apply", "Skipping node.cfg/traffic-control.cfg: merge mode requires PBS API apply")
		}
	}

	if plan.HasCategoryID("datastore_pbs") {
		if apiAvailable {
			if err := applyPBSS3CfgViaAPI(ctx, logger, stageRoot, strict); err != nil {
				logger.Warning("PBS API apply: s3.cfg failed: %v", err)
				if allowFileFallback {
					logger.Warning("PBS staged apply: falling back to file-based s3.cfg")
					_ = applyPBSS3CfgFromStage(ctx, logger, stageRoot)
				}
			}
			if err := applyPBSDatastoreCfgViaAPI(ctx, logger, stageRoot, strict); err != nil {
				logger.Warning("PBS API apply: datastore.cfg failed: %v", err)
				if allowFileFallback {
					logger.Warning("PBS staged apply: falling back to file-based datastore.cfg")
					_ = applyPBSDatastoreCfgFromStage(ctx, logger, stageRoot)
				}
			}
		} else if allowFileFallback {
			if err := applyPBSS3CfgFromStage(ctx, logger, stageRoot); err != nil {
				logger.Warning("PBS staged apply: s3.cfg: %v", err)
			}
			if err := applyPBSDatastoreCfgFromStage(ctx, logger, stageRoot); err != nil {
				logger.Warning("PBS staged apply: datastore.cfg: %v", err)
			}
		} else {
			logging.DebugStep(logger, "pbs staged apply", "Skipping datastore.cfg/s3.cfg: merge mode requires PBS API apply")
		}
	}

	if plan.HasCategoryID("pbs_remotes") {
		if apiAvailable {
			if err := applyPBSRemoteCfgViaAPI(ctx, logger, stageRoot, strict); err != nil {
				logger.Warning("PBS API apply: remote.cfg failed: %v", err)
				if allowFileFallback {
					logger.Warning("PBS staged apply: falling back to file-based remote.cfg")
					_ = applyPBSRemoteCfgFromStage(ctx, logger, stageRoot)
				}
			}
		} else if allowFileFallback {
			if err := applyPBSRemoteCfgFromStage(ctx, logger, stageRoot); err != nil {
				logger.Warning("PBS staged apply: remote.cfg: %v", err)
			}
		} else {
			logging.DebugStep(logger, "pbs staged apply", "Skipping remote.cfg: merge mode requires PBS API apply")
		}
	}

	if plan.HasCategoryID("pbs_jobs") {
		if apiAvailable {
			if err := applyPBSSyncCfgViaAPI(ctx, logger, stageRoot, strict); err != nil {
				logger.Warning("PBS API apply: sync jobs failed: %v", err)
				if allowFileFallback {
					logger.Warning("PBS staged apply: falling back to file-based job configs")
					_ = applyPBSJobConfigsFromStage(ctx, logger, stageRoot)
				}
			}
			if err := applyPBSVerificationCfgViaAPI(ctx, logger, stageRoot, strict); err != nil {
				logger.Warning("PBS API apply: verification jobs failed: %v", err)
				if allowFileFallback {
					logger.Warning("PBS staged apply: falling back to file-based job configs")
					_ = applyPBSJobConfigsFromStage(ctx, logger, stageRoot)
				}
			}
			if err := applyPBSPruneCfgViaAPI(ctx, logger, stageRoot, strict); err != nil {
				logger.Warning("PBS API apply: prune jobs failed: %v", err)
				if allowFileFallback {
					logger.Warning("PBS staged apply: falling back to file-based job configs")
					_ = applyPBSJobConfigsFromStage(ctx, logger, stageRoot)
				}
			}
		} else if allowFileFallback {
			if err := applyPBSJobConfigsFromStage(ctx, logger, stageRoot); err != nil {
				logger.Warning("PBS staged apply: job configs: %v", err)
			}
		} else {
			logging.DebugStep(logger, "pbs staged apply", "Skipping sync/verification/prune configs: merge mode requires PBS API apply")
		}
	}

	if plan.HasCategoryID("pbs_tape") {
		if err := applyPBSTapeConfigsFromStage(ctx, logger, stageRoot); err != nil {
			logger.Warning("PBS staged apply: tape configs: %v", err)
		}
	}

	return nil
}

func applyPBSRemoteCfgFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) (err error) {
	done := logging.DebugStart(logger, "pbs staged apply remote.cfg", "stage=%s", stageRoot)
	defer func() { done(err) }()

	return applyPBSConfigFileFromStage(ctx, logger, stageRoot, "etc/proxmox-backup/remote.cfg")
}

func applyPBSS3CfgFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) (err error) {
	done := logging.DebugStart(logger, "pbs staged apply s3.cfg", "stage=%s", stageRoot)
	defer func() { done(err) }()

	return applyPBSConfigFileFromStage(ctx, logger, stageRoot, "etc/proxmox-backup/s3.cfg")
}

func applyPBSTapeConfigsFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) (err error) {
	done := logging.DebugStart(logger, "pbs staged apply tape configs", "stage=%s", stageRoot)
	defer func() { done(err) }()

	paths := []string{
		"etc/proxmox-backup/tape.cfg",
		"etc/proxmox-backup/tape-job.cfg",
		"etc/proxmox-backup/media-pool.cfg",
	}
	for _, rel := range paths {
		if err := applyPBSConfigFileFromStage(ctx, logger, stageRoot, rel); err != nil {
			logger.Warning("PBS staged apply: %s: %v", rel, err)
		}
	}

	// Tape encryption keys are JSON (no section headers) and should be applied as a sensitive file.
	if err := applySensitiveFileFromStage(logger, stageRoot, "etc/proxmox-backup/tape-encryption-keys.json", "/etc/proxmox-backup/tape-encryption-keys.json", 0o600); err != nil {
		logger.Warning("PBS staged apply: tape-encryption-keys.json: %v", err)
	}

	return nil
}

type pbsDatastoreBlock struct {
	Name  string
	Path  string
	Lines []string
}

func applyPBSDatastoreCfgFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) (err error) {
	_ = ctx // reserved for future validation hooks

	done := logging.DebugStart(logger, "pbs staged apply datastore.cfg", "stage=%s", stageRoot)
	defer func() { done(err) }()

	stagePath := filepath.Join(stageRoot, "etc/proxmox-backup/datastore.cfg")
	data, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logging.DebugStep(logger, "pbs staged apply datastore.cfg", "Skipped: datastore.cfg not present in staging directory")
			return nil
		}
		return fmt.Errorf("read staged datastore.cfg: %w", err)
	}

	raw := string(data)
	if strings.TrimSpace(raw) == "" {
		logging.DebugStep(logger, "pbs staged apply datastore.cfg", "Staged datastore.cfg is empty; removing target file to avoid PBS parse errors")
		return removeIfExists("/etc/proxmox-backup/datastore.cfg")
	}

	normalized, fixed := normalizePBSDatastoreCfgContent(raw)
	if fixed > 0 {
		logger.Warning("PBS staged apply: datastore.cfg normalization fixed %d malformed line(s) (properties must be indented)", fixed)
	}

	blocks, err := parsePBSDatastoreCfgBlocks(normalized)
	if err != nil {
		return err
	}
	if len(blocks) == 0 {
		logging.DebugStep(logger, "pbs staged apply datastore.cfg", "No datastore blocks detected; skipping apply")
		return nil
	}

	if reason := detectPBSDatastoreCfgDuplicateKeys(blocks); reason != "" {
		logger.Warning("PBS staged apply: staged datastore.cfg looks invalid (%s); attempting recovery from pbs_datastore_inventory.json", reason)
		if recovered, src, recErr := loadPBSDatastoreCfgFromInventory(stageRoot); recErr != nil {
			logger.Warning("PBS staged apply: unable to recover datastore.cfg from inventory (%v); leaving current configuration unchanged", recErr)
			return nil
		} else if strings.TrimSpace(recovered) == "" {
			logger.Warning("PBS staged apply: recovered datastore.cfg from %s is empty; leaving current configuration unchanged", src)
			return nil
		} else {
			normalized, fixed = normalizePBSDatastoreCfgContent(recovered)
			if fixed > 0 {
				logger.Warning("PBS staged apply: recovered datastore.cfg normalization fixed %d malformed line(s) (properties must be indented)", fixed)
			}
			blocks, err = parsePBSDatastoreCfgBlocks(normalized)
			if err != nil {
				logger.Warning("PBS staged apply: recovered datastore.cfg from %s is still invalid (%v); leaving current configuration unchanged", src, err)
				return nil
			}
			if reason := detectPBSDatastoreCfgDuplicateKeys(blocks); reason != "" {
				logger.Warning("PBS staged apply: recovered datastore.cfg from %s still looks invalid (%s); leaving current configuration unchanged", src, reason)
				return nil
			}
			logger.Info("PBS staged apply: datastore.cfg recovered from %s", src)
		}
	}

	var applyBlocks []pbsDatastoreBlock
	var deferred []pbsDatastoreBlock
	for _, b := range blocks {
		ok, reason := shouldApplyPBSDatastoreBlock(b, logger)
		if ok {
			applyBlocks = append(applyBlocks, b)
		} else {
			logging.DebugStep(logger, "pbs staged apply datastore.cfg", "Deferring datastore %s (path=%s): %s", b.Name, b.Path, reason)
			deferred = append(deferred, b)
		}
	}

	if len(deferred) > 0 {
		if path, err := writeDeferredPBSDatastoreCfg(deferred); err != nil {
			logger.Debug("Failed to write deferred datastore.cfg: %v", err)
		} else {
			logger.Warning("PBS staged apply: deferred %d datastore definition(s); saved to %s", len(deferred), path)
		}
	}

	if len(applyBlocks) == 0 {
		logger.Warning("PBS staged apply: datastore.cfg contains no safe datastore definitions to apply; leaving current configuration unchanged")
		return nil
	}

	var out strings.Builder
	for i, b := range applyBlocks {
		if i > 0 {
			out.WriteString("\n")
		}
		out.WriteString(strings.TrimRight(strings.Join(b.Lines, "\n"), "\n"))
		out.WriteString("\n")
	}

	destPath := "/etc/proxmox-backup/datastore.cfg"
	if err := writeFileAtomic(destPath, []byte(out.String()), 0o640); err != nil {
		return fmt.Errorf("write %s: %w", destPath, err)
	}

	logger.Info("PBS staged apply: datastore.cfg applied (%d datastore(s)); deferred=%d", len(applyBlocks), len(deferred))
	return nil
}

type pbsDatastoreInventoryRestoreLite struct {
	Files map[string]struct {
		Content string `json:"content"`
	} `json:"files"`
	Datastores []struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Comment string `json:"comment"`
	} `json:"datastores"`
}

func loadPBSDatastoreCfgFromInventory(stageRoot string) (string, string, error) {
	inventoryPath := filepath.Join(stageRoot, "var/lib/proxsave-info/commands/pbs/pbs_datastore_inventory.json")
	raw, err := restoreFS.ReadFile(inventoryPath)
	if err != nil {
		return "", "", fmt.Errorf("read inventory %s: %w", inventoryPath, err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "", "", fmt.Errorf("inventory %s is empty", inventoryPath)
	}

	var report pbsDatastoreInventoryRestoreLite
	if err := json.Unmarshal([]byte(trimmed), &report); err != nil {
		return "", "", fmt.Errorf("parse inventory %s: %w", inventoryPath, err)
	}

	if report.Files != nil {
		if snap := strings.TrimSpace(report.Files["pbs_datastore_cfg"].Content); snap != "" {
			return report.Files["pbs_datastore_cfg"].Content, "pbs_datastore_inventory.json.files[pbs_datastore_cfg].content", nil
		}
	}

	// Fallback: generate a minimal datastore.cfg from the inventory's datastore list.
	var out strings.Builder
	for _, ds := range report.Datastores {
		name := strings.TrimSpace(ds.Name)
		path := strings.TrimSpace(ds.Path)
		if name == "" || path == "" {
			continue
		}
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(fmt.Sprintf("datastore: %s\n", name))
		if comment := strings.TrimSpace(ds.Comment); comment != "" {
			out.WriteString(fmt.Sprintf("    comment %s\n", comment))
		}
		out.WriteString(fmt.Sprintf("    path %s\n", path))
	}

	generated := strings.TrimSpace(out.String())
	if generated == "" {
		return "", "", fmt.Errorf("inventory %s contains no usable datastore definitions", inventoryPath)
	}
	return out.String(), "pbs_datastore_inventory.json.datastores", nil
}

func detectPBSDatastoreCfgDuplicateKeys(blocks []pbsDatastoreBlock) string {
	for _, block := range blocks {
		seen := map[string]int{}
		for _, line := range block.Lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "datastore:") {
				continue
			}

			fields := strings.Fields(trimmed)
			if len(fields) == 0 {
				continue
			}
			key := strings.TrimSpace(fields[0])
			if key == "" {
				continue
			}
			seen[key]++
			if seen[key] > 1 {
				return fmt.Sprintf("datastore %s has duplicate key %q", strings.TrimSpace(block.Name), key)
			}
		}
	}
	return ""
}

func parsePBSDatastoreCfgBlocks(content string) ([]pbsDatastoreBlock, error) {
	var blocks []pbsDatastoreBlock
	var current *pbsDatastoreBlock

	flush := func() {
		if current == nil {
			return
		}
		if strings.TrimSpace(current.Name) == "" {
			current = nil
			return
		}
		blocks = append(blocks, *current)
		current = nil
	}

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if current != nil {
				current.Lines = append(current.Lines, line)
			}
			continue
		}

		if strings.HasPrefix(trimmed, "datastore:") {
			flush()
			parts := strings.Fields(trimmed)
			if len(parts) < 2 {
				continue
			}
			current = &pbsDatastoreBlock{
				Name:  strings.TrimSuffix(strings.TrimSpace(parts[1]), ":"),
				Lines: []string{line},
			}
			continue
		}

		if current == nil {
			continue
		}
		current.Lines = append(current.Lines, line)
		if strings.HasPrefix(trimmed, "path ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				current.Path = strings.TrimSpace(parts[1])
			}
		}
	}
	flush()

	return blocks, nil
}

func shouldApplyPBSDatastoreBlock(block pbsDatastoreBlock, logger *logging.Logger) (bool, string) {
	path := filepath.Clean(strings.TrimSpace(block.Path))
	if path == "" || path == "." || path == string(os.PathSeparator) {
		return false, "invalid or missing datastore path"
	}

	hasData, dataErr := pbsDatastoreHasData(path)
	if dataErr != nil {
		return false, fmt.Sprintf("datastore path inspection failed: %v", dataErr)
	}

	onRootFS, _, devErr := isPathOnRootFilesystem(path)
	if devErr != nil {
		return false, fmt.Sprintf("filesystem identity check failed: %v", devErr)
	}
	if onRootFS && isSuspiciousDatastoreMountLocation(path) && !hasData {
		// On fresh restores the mount backing this path may be offline/not mounted yet.
		// We still apply the datastore definition 1:1 so PBS shows the datastore as unavailable
		// rather than silently dropping it from datastore.cfg.
		if logger != nil {
			logger.Warning("PBS staged apply: datastore %s path %s resolves to root filesystem (mount missing?) — applying definition anyway", block.Name, path)
		}
	}

	if hasData {
		if warn := validatePBSDatastoreReadOnly(path); warn != "" && logger != nil {
			logger.Warning("PBS datastore preflight: %s", warn)
		}
		return true, ""
	}

	unexpected, err := pbsDatastoreHasUnexpectedEntries(path)
	if err != nil {
		return false, fmt.Sprintf("failed to inspect datastore directory: %v", err)
	}
	if unexpected {
		return false, "datastore directory is not empty (unexpected entries present)"
	}

	return true, ""
}

func writeDeferredPBSDatastoreCfg(blocks []pbsDatastoreBlock) (string, error) {
	if len(blocks) == 0 {
		return "", nil
	}
	base := "/tmp/proxsave"
	if err := restoreFS.MkdirAll(base, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(base, fmt.Sprintf("datastore.cfg.deferred.%s", nowRestore().Format("20060102-150405")))
	var b strings.Builder
	for i, block := range blocks {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimRight(strings.Join(block.Lines, "\n"), "\n"))
		b.WriteString("\n")
	}
	if err := restoreFS.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func applyPBSJobConfigsFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) (err error) {
	done := logging.DebugStart(logger, "pbs staged apply jobs", "stage=%s", stageRoot)
	defer func() { done(err) }()

	paths := []string{
		"etc/proxmox-backup/sync.cfg",
		"etc/proxmox-backup/verification.cfg",
		"etc/proxmox-backup/prune.cfg",
	}

	for _, rel := range paths {
		if err := applyPBSConfigFileFromStage(ctx, logger, stageRoot, rel); err != nil {
			logger.Warning("PBS staged apply: %s: %v", rel, err)
		}
	}
	return nil
}

func applyPBSConfigFileFromStage(ctx context.Context, logger *logging.Logger, stageRoot, relPath string) error {
	_ = ctx // reserved for future validation hooks

	stagePath := filepath.Join(stageRoot, relPath)
	data, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logging.DebugStep(logger, "pbs staged apply file", "Skip %s: not present in staging directory", relPath)
			return nil
		}
		return fmt.Errorf("read staged %s: %w", relPath, err)
	}

	trimmed := strings.TrimSpace(string(data))
	destPath := filepath.Join(string(os.PathSeparator), filepath.FromSlash(relPath))

	if trimmed == "" {
		logger.Warning("PBS staged apply: %s is empty; removing %s to avoid PBS parse errors", relPath, destPath)
		return removeIfExists(destPath)
	}
	if !pbsConfigHasHeader(trimmed) {
		logger.Warning("PBS staged apply: %s does not look like a valid PBS config file (missing section header); skipping apply", relPath)
		return nil
	}

	if err := writeFileAtomic(destPath, []byte(trimmed+"\n"), 0o640); err != nil {
		return fmt.Errorf("write %s: %w", destPath, err)
	}

	logging.DebugStep(logger, "pbs staged apply file", "Applied %s -> %s", relPath, destPath)
	return nil
}

func pbsConfigHasHeader(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		head := strings.TrimSpace(fields[0])
		key := ""

		switch {
		case strings.HasSuffix(head, ":"):
			if len(fields) < 2 {
				continue
			}
			key = strings.TrimSuffix(head, ":")
		case strings.Count(head, ":") == 1 && !strings.ContainsAny(head, " \t"):
			parts := strings.SplitN(head, ":", 2)
			if len(parts) != 2 {
				continue
			}
			if strings.TrimSpace(parts[1]) == "" {
				continue
			}
			key = strings.TrimSpace(parts[0])
		default:
			continue
		}

		if key == "" {
			continue
		}
		valid := true
		for _, r := range key {
			switch {
			case r >= 'a' && r <= 'z':
			case r >= 'A' && r <= 'Z':
			case r >= '0' && r <= '9':
			case r == '-' || r == '_':
			default:
				valid = false
			}
			if !valid {
				break
			}
		}
		if valid {
			return true
		}
	}
	return false
}

func removeIfExists(path string) error {
	if err := restoreFS.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}
