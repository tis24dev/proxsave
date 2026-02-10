package orchestrator

import (
	"context"
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

	if plan.HasCategoryID("datastore_pbs") {
		if err := applyPBSS3CfgFromStage(ctx, logger, stageRoot); err != nil {
			logger.Warning("PBS staged apply: s3.cfg: %v", err)
		}
		if err := applyPBSDatastoreCfgFromStage(ctx, logger, stageRoot); err != nil {
			logger.Warning("PBS staged apply: datastore.cfg: %v", err)
		}
	}
	if plan.HasCategoryID("pbs_jobs") {
		if err := applyPBSJobConfigsFromStage(ctx, logger, stageRoot); err != nil {
			logger.Warning("PBS staged apply: job configs: %v", err)
		}
	}
	if plan.HasCategoryID("pbs_remotes") {
		if err := applyPBSRemoteCfgFromStage(ctx, logger, stageRoot); err != nil {
			logger.Warning("PBS staged apply: remote.cfg: %v", err)
		}
	}
	if plan.HasCategoryID("pbs_host") {
		if err := applyPBSHostConfigsFromStage(ctx, logger, stageRoot); err != nil {
			logger.Warning("PBS staged apply: host configs: %v", err)
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

func applyPBSHostConfigsFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) (err error) {
	done := logging.DebugStart(logger, "pbs staged apply host configs", "stage=%s", stageRoot)
	defer func() { done(err) }()

	// ACME should be applied before node.cfg (node.cfg references ACME account/plugins).
	paths := []string{
		"etc/proxmox-backup/acme/accounts.cfg",
		"etc/proxmox-backup/acme/plugins.cfg",
		"etc/proxmox-backup/metricserver.cfg",
		"etc/proxmox-backup/traffic-control.cfg",
		"etc/proxmox-backup/proxy.cfg",
		"etc/proxmox-backup/node.cfg",
	}
	for _, rel := range paths {
		if err := applyPBSConfigFileFromStage(ctx, logger, stageRoot, rel); err != nil {
			logger.Warning("PBS staged apply: %s: %v", rel, err)
		}
	}
	return nil
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
		if warn := validatePBSDatastoreReadOnly(path, logger); warn != "" {
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
		if !strings.HasSuffix(head, ":") {
			return false
		}
		key := strings.TrimSuffix(head, ":")
		if key == "" {
			return false
		}
		for _, r := range key {
			switch {
			case r >= 'a' && r <= 'z':
			case r >= 'A' && r <= 'Z':
			case r >= '0' && r <= '9':
			case r == '-' || r == '_':
			default:
				return false
			}
		}
		return true
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
