package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

// Hookable functions for testing staged PBS apply logic without touching the real system/API.
var (
	pbsStagedApplyIsRealRestoreFSFn         = isRealRestoreFS
	pbsStagedApplyGeteuidFn                 = os.Geteuid
	pbsStagedApplyEnsurePBSServicesForAPIFn = ensurePBSServicesForAPI
	pbsStagedApplyTrafficControlCfgViaAPIFn = applyPBSTrafficControlCfgViaAPI
	pbsStagedApplyNodeCfgViaAPIFn           = applyPBSNodeCfgViaAPI
	pbsStagedApplyS3CfgViaAPIFn             = applyPBSS3CfgViaAPI
	pbsStagedApplyDatastoreCfgViaAPIFn      = applyPBSDatastoreCfgViaAPI
	pbsStagedApplyRemoteCfgViaAPIFn         = applyPBSRemoteCfgViaAPI
	pbsStagedApplySyncCfgViaAPIFn           = applyPBSSyncCfgViaAPI
	pbsStagedApplyVerificationCfgViaAPIFn   = applyPBSVerificationCfgViaAPI
	pbsStagedApplyPruneCfgViaAPIFn          = applyPBSPruneCfgViaAPI
)

func maybeApplyPBSConfigsFromStage(ctx context.Context, logger *logging.Logger, plan *RestorePlan, stageRoot string, dryRun bool) (err error) {
	if plan == nil || !plan.SystemType.SupportsPBS() {
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
	if !pbsStagedApplyIsRealRestoreFSFn(restoreFS) {
		logger.Debug("Skipping staged PBS config apply: non-system filesystem in use")
		return nil
	}
	if pbsStagedApplyGeteuidFn() != 0 {
		logger.Warning("Skipping staged PBS config apply: requires root privileges")
		return nil
	}

	behavior := plan.PBSRestoreBehavior
	strict := behavior == PBSRestoreBehaviorClean
	allowFileFallback := behavior == PBSRestoreBehaviorClean

	needsAPI := plan.HasCategoryID("pbs_host") || plan.HasCategoryID("datastore_pbs") || plan.HasCategoryID("pbs_remotes") || plan.HasCategoryID("pbs_jobs")
	apiAvailable := false
	if needsAPI {
		if err := pbsStagedApplyEnsurePBSServicesForAPIFn(ctx, logger); err != nil {
			if allowFileFallback {
				logger.Warning("PBS API apply unavailable; falling back to file-based staged apply where possible: %v", err)
			} else {
				logger.Warning("PBS API apply unavailable; skipping API-applied PBS categories (merge mode): %v", err)
			}
		} else {
			apiAvailable = true
		}
	}

	// failedItems accumulates config items that ended up NOT applied (the API
	// apply failed and either there was no fallback or the fallback also failed),
	// so the caller reports the restore "with warnings" instead of a clean
	// success rather than silently swallowing failed applies (BH-003).
	var failedItems []string
	// datastoreOutcomeLogged is a datastore.cfg API apply that left datastores not
	// applied or not removed, whose outcomes its block already logged. It is not
	// in failedItems, so the step summary does not repeat it.
	var datastoreOutcomeLogged error

	if plan.HasCategoryID("pbs_host") {
		// Always restore file-only configs (no stable API coverage yet).
		// ACME should be applied before node config (node.cfg references ACME accounts/plugins).
		// The accounts are a directory, not a .cfg, so they get their own apply.
		if err := applyPBSAcmeAccountsFromStage(logger, stageRoot); err != nil {
			logger.Warning("PBS staged apply: %s: %v", pbsAcmeAccountsRelPath, err)
			failedItems = append(failedItems, pbsAcmeAccountsRelPath)
		}
		for _, rel := range []string{
			"etc/proxmox-backup/acme/plugins.cfg",
			"etc/proxmox-backup/metricserver.cfg",
			"etc/proxmox-backup/proxy.cfg",
		} {
			if err := applyPBSConfigFileFromStage(ctx, logger, stageRoot, rel); err != nil {
				logger.Warning("PBS staged apply: %s: %v", rel, err)
				failedItems = append(failedItems, rel)
			}
		}

		if apiAvailable {
			if err := pbsStagedApplyTrafficControlCfgViaAPIFn(ctx, logger, stageRoot, strict); err != nil {
				if stop := pbsSectionApplyFailed(ctx, logger, err, pbsSectionFailure{label: "traffic-control", item: "traffic-control.cfg", fallbackLabel: "traffic-control.cfg"}, allowFileFallback, &failedItems, func() error {
					return applyPBSConfigFileFromStage(ctx, logger, stageRoot, "etc/proxmox-backup/traffic-control.cfg")
				}); stop != nil {
					return stop
				}
			}
			if err := pbsStagedApplyNodeCfgViaAPIFn(ctx, stageRoot); err != nil {
				logger.Warning("PBS API apply: node config failed: %v", err)
				if !pbsFallbackApplied(logger, "node.cfg", allowFileFallback, func() error {
					return applyPBSConfigFileFromStage(ctx, logger, stageRoot, "etc/proxmox-backup/node.cfg")
				}) {
					failedItems = append(failedItems, "node.cfg")
				}
			}
		} else if allowFileFallback {
			for _, rel := range []string{
				"etc/proxmox-backup/traffic-control.cfg",
				"etc/proxmox-backup/node.cfg",
			} {
				if err := applyPBSConfigFileFromStage(ctx, logger, stageRoot, rel); err != nil {
					logger.Warning("PBS staged apply: %s: %v", rel, err)
					failedItems = append(failedItems, rel)
				}
			}
		} else {
			logging.DebugStep(logger, "pbs staged apply", "Skipping node.cfg/traffic-control.cfg: merge mode requires PBS API apply")
		}
	}

	if plan.HasCategoryID("datastore_pbs") {
		if apiAvailable {
			if err := pbsStagedApplyS3CfgViaAPIFn(ctx, logger, stageRoot, strict); err != nil {
				if stop := pbsSectionApplyFailed(ctx, logger, err, pbsSectionFailure{label: "s3.cfg", item: "s3.cfg", fallbackLabel: "s3.cfg"}, allowFileFallback, &failedItems, func() error {
					return applyPBSS3CfgFromStage(ctx, logger, stageRoot)
				}); stop != nil {
					return stop
				}
			}
			if err := pbsStagedApplyDatastoreCfgViaAPIFn(ctx, logger, stageRoot, strict); err != nil {
				var incomplete *pbsDatastoreRestoreIncompleteError
				var stopped *pbsDatastoreRestoreNotAppliedError
				switch {
				case errors.As(err, &stopped):
					// The block stopped before any datastore through the API, ran the
					// file-based fallback itself in Clean mode, and logged its outcome.
					datastoreOutcomeLogged = err
				case errors.As(err, &incomplete):
					// The datastore block already logged the outcome of every datastore
					// and its own: no line here, no file fallback over those outcomes,
					// and the restore still ends with warnings.
					datastoreOutcomeLogged = err
				case allowFileFallback:
					// An error the block does not return today: write datastore.cfg
					// from the backup file as the same block.
					event := fmt.Sprintf("The datastore configuration could not be applied through the PBS API: \"%s\"", err.Error())
					if ferr := applyPBSDatastoreCfgFileBlock(ctx, logger, stageRoot, event, err); ferr != nil {
						datastoreOutcomeLogged = ferr
					}
				default:
					logger.Warning("PBS API apply: datastore.cfg failed: %v", err)
					failedItems = append(failedItems, "datastore.cfg")
				}
			}
		} else if allowFileFallback {
			if err := applyPBSS3CfgFromStage(ctx, logger, stageRoot); err != nil {
				logger.Warning("PBS staged apply: s3.cfg: %v", err)
				failedItems = append(failedItems, "s3.cfg")
			}
			if err := applyPBSDatastoreCfgFileBlock(ctx, logger, stageRoot, pbsDatastoreEventAPIUnavailable, errors.New("PBS API unavailable")); err != nil {
				// The block logged the outcome of every datastore and its own.
				datastoreOutcomeLogged = err
			}
		} else {
			logging.DebugStep(logger, "pbs staged apply", "Skipping datastore.cfg/s3.cfg: merge mode requires PBS API apply")
		}
	}

	if plan.HasCategoryID("pbs_remotes") {
		if apiAvailable {
			if err := pbsStagedApplyRemoteCfgViaAPIFn(ctx, logger, stageRoot, strict); err != nil {
				if stop := pbsSectionApplyFailed(ctx, logger, err, pbsSectionFailure{label: "remote.cfg", item: "remote.cfg", fallbackLabel: "remote.cfg"}, allowFileFallback, &failedItems, func() error {
					return applyPBSRemoteCfgFromStage(ctx, logger, stageRoot)
				}); stop != nil {
					return stop
				}
			}
		} else if allowFileFallback {
			if err := applyPBSRemoteCfgFromStage(ctx, logger, stageRoot); err != nil {
				logger.Warning("PBS staged apply: remote.cfg: %v", err)
				failedItems = append(failedItems, "remote.cfg")
			}
		} else {
			logging.DebugStep(logger, "pbs staged apply", "Skipping remote.cfg: merge mode requires PBS API apply")
		}
	}

	if plan.HasCategoryID("pbs_jobs") {
		if apiAvailable {
			if err := pbsStagedApplySyncCfgViaAPIFn(ctx, logger, stageRoot, strict); err != nil {
				if stop := pbsSectionApplyFailed(ctx, logger, err, pbsSectionFailure{label: "sync jobs", item: "sync.cfg", fallbackLabel: "job configs"}, allowFileFallback, &failedItems, func() error {
					return applyPBSJobConfigsFromStage(ctx, logger, stageRoot)
				}); stop != nil {
					return stop
				}
			}
			if err := pbsStagedApplyVerificationCfgViaAPIFn(ctx, logger, stageRoot, strict); err != nil {
				if stop := pbsSectionApplyFailed(ctx, logger, err, pbsSectionFailure{label: "verification jobs", item: "verification.cfg", fallbackLabel: "job configs"}, allowFileFallback, &failedItems, func() error {
					return applyPBSJobConfigsFromStage(ctx, logger, stageRoot)
				}); stop != nil {
					return stop
				}
			}
			if err := pbsStagedApplyPruneCfgViaAPIFn(ctx, logger, stageRoot, strict); err != nil {
				if stop := pbsSectionApplyFailed(ctx, logger, err, pbsSectionFailure{label: "prune jobs", item: "prune.cfg", fallbackLabel: "job configs"}, allowFileFallback, &failedItems, func() error {
					return applyPBSJobConfigsFromStage(ctx, logger, stageRoot)
				}); stop != nil {
					return stop
				}
			}
		} else if allowFileFallback {
			if err := applyPBSJobConfigsFromStage(ctx, logger, stageRoot); err != nil {
				logger.Warning("PBS staged apply: job configs: %v", err)
				failedItems = append(failedItems, "job configs")
			}
		} else {
			logging.DebugStep(logger, "pbs staged apply", "Skipping sync/verification/prune configs: merge mode requires PBS API apply")
		}
	}

	if plan.HasCategoryID("pbs_tape") {
		if err := applyPBSTapeConfigsFromStage(ctx, logger, stageRoot); err != nil {
			logger.Warning("PBS staged apply: tape configs: %v", err)
			failedItems = append(failedItems, "tape configs")
		}
	}

	if len(failedItems) > 0 {
		return fmt.Errorf("%d PBS config item(s) failed to apply: %s", len(failedItems), strings.Join(failedItems, ", "))
	}
	if datastoreOutcomeLogged != nil {
		return &stageApplyOutcomeLoggedError{err: datastoreOutcomeLogged}
	}
	return nil
}

// pbsSectionFailure names a section config in the handling of its failed API
// apply: label in the warning, item in the failed items, fallbackLabel in the
// fallback lines.
type pbsSectionFailure struct {
	label         string
	item          string
	fallbackLabel string
}

// pbsSectionApplyFailed handles the failed PBS API apply of a section config, the
// same way for every config. A cancellation stops the staged apply: it is
// returned, with no warning and no fallback. Otherwise the warning, then a Clean
// 1:1 incomplete apply is recorded as such with no fallback over it, and any
// other failure runs the file-based fallback; the item is recorded as failed
// when nothing applied it.
func pbsSectionApplyFailed(ctx context.Context, logger *logging.Logger, err error, f pbsSectionFailure, allowFileFallback bool, failedItems *[]string, fallback func() error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		// Cancelled: no warning, no file fallback, the restore stops.
		return ctxErr
	}
	logger.Warning("PBS API apply: %s failed: %v", f.label, err)
	if errors.Is(err, errPBSCleanRemoveIncomplete) {
		*failedItems = append(*failedItems, f.item+" (clean 1:1 incomplete)")
	} else if !pbsFallbackApplied(logger, f.fallbackLabel, allowFileFallback, fallback) {
		*failedItems = append(*failedItems, f.item)
	}
	return nil
}

// pbsFallbackApplied runs the file-based fallback for a PBS API apply that
// failed. It returns true when the item was applied (clean mode and the fallback
// succeeded) and false when the item remains unapplied (merge mode has no
// fallback, or the fallback itself failed) so the caller can record a failure.
func pbsFallbackApplied(logger *logging.Logger, label string, allowFileFallback bool, fallback func() error) bool {
	if !allowFileFallback {
		return false
	}
	logger.Warning("PBS staged apply: falling back to file-based %s", label)
	if err := fallback(); err != nil {
		logger.Warning("PBS staged apply: %s fallback: %v", label, err)
		return false
	}
	return true
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

// pbsDatastoreFileItem is one datastore of the staged datastore.cfg as the
// file-based apply judged it.
type pbsDatastoreFileItem struct {
	name string
	path string
	// deferReason is set when the datastore is not safe to apply now: its
	// definition is saved aside instead of written.
	deferReason string
	// onRootFS: the path resolves to the root filesystem (its mount is missing?);
	// the definition is written anyway.
	onRootFS bool
	// preflight is a problem found on the path of a datastore that has data; the
	// definition is written anyway.
	preflight string
	// keptCurrent: the datastore is deferred and this PBS already has it, so its
	// current definition is written back unchanged.
	keptCurrent bool
}

// pbsDatastoreFileResult is what the file-based datastore.cfg apply found and did,
// for the caller to log in its own form.
type pbsDatastoreFileResult struct {
	stagePath string
	destPath  string
	// missing: no datastore.cfg in the staging directory, nothing done.
	missing bool
	// emptied: the staged datastore.cfg has no datastore definitions (it is empty
	// or holds only comments), the target file was removed.
	emptied bool
	// commentsOnly: the staged file that emptied the target holds only comments.
	commentsOnly bool
	// current holds the datastores of the target file before it was written,
	// currentErr why they could not be read. removed are the datastores of this
	// PBS that the write took out, set only once the write succeeded.
	current    []pbsDatastoreBlock
	currentErr error
	removed    []string
	// cleanup is what was removed with each removed datastore: its jobs and its
	// permissions, as PBS removes them with the datastore.
	cleanup map[string]*pbsDatastoreCleanup
	// fixed is the number of malformed lines normalized in the staged file.
	fixed int
	// invalidReason is set when the staged file looked invalid and the
	// configuration was rebuilt from pbs_datastore_inventory.json.
	invalidReason  string
	recoveredFrom  string
	recoveredFixed int
	// recoveryFailed is set when that rebuild failed; the current configuration
	// is kept and nothing is written.
	recoveryFailed string
	items          []pbsDatastoreFileItem
	// deferredPath is where the deferred definitions were saved, deferredErr why
	// they could not be.
	deferredPath string
	deferredErr  error
	// readFailed: the staged file could not be read or parsed.
	readFailed bool
	// noSafe: every definition was deferred, the current configuration is kept.
	noSafe  bool
	written int
}

func (r pbsDatastoreFileResult) deferredCount() int {
	n := 0
	for _, it := range r.items {
		if it.deferReason != "" {
			n++
		}
	}
	return n
}

func (r pbsDatastoreFileResult) keptCurrentCount() int {
	n := 0
	for _, it := range r.items {
		if it.keptCurrent {
			n++
		}
	}
	return n
}

// runPBSDatastoreCfgFile writes /etc/proxmox-backup/datastore.cfg from the staged
// datastore.cfg, without logging: the datastores whose path is not safe to apply
// now are saved aside, a staged file that looks invalid is rebuilt from the
// datastore inventory.
func runPBSDatastoreCfgFile(stageRoot string) (res pbsDatastoreFileResult, err error) {
	res.stagePath = filepath.Join(stageRoot, "etc/proxmox-backup/datastore.cfg")
	res.destPath = "/etc/proxmox-backup/datastore.cfg"
	data, err := restoreFS.ReadFile(res.stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			res.missing = true
			return res, nil
		}
		res.readFailed = true
		return res, fmt.Errorf("read staged datastore.cfg: %w", err)
	}

	res.current, res.currentErr = readCurrentPBSDatastoreBlocks(res.destPath)

	raw := string(data)
	if strings.TrimSpace(raw) == "" {
		res.emptied = true
		return res, res.removeTarget()
	}

	normalized, fixed := normalizePBSDatastoreCfgContent(raw)
	res.fixed = fixed
	blocks, err := parsePBSDatastoreCfgBlocks(normalized)
	if err != nil {
		res.readFailed = true
		return res, err
	}
	if len(blocks) == 0 {
		// Only comments: a backup without datastores, like an empty file.
		res.emptied = true
		res.commentsOnly = true
		return res, res.removeTarget()
	}

	if reason := detectPBSDatastoreCfgDuplicateKeys(blocks); reason != "" {
		res.invalidReason = reason
		recovered, src, recErr := loadPBSDatastoreCfgFromInventory(stageRoot)
		switch {
		case recErr != nil:
			res.recoveryFailed = fmt.Sprintf("unable to recover datastore.cfg from inventory (%v)", recErr)
			return res, nil
		case strings.TrimSpace(recovered) == "":
			res.recoveryFailed = fmt.Sprintf("recovered datastore.cfg from %s is empty", src)
			return res, nil
		}
		normalized, res.recoveredFixed = normalizePBSDatastoreCfgContent(recovered)
		blocks, err = parsePBSDatastoreCfgBlocks(normalized)
		if err != nil {
			res.recoveryFailed = fmt.Sprintf("recovered datastore.cfg from %s is still invalid (%v)", src, err)
			return res, nil
		}
		if reason := detectPBSDatastoreCfgDuplicateKeys(blocks); reason != "" {
			res.recoveryFailed = fmt.Sprintf("recovered datastore.cfg from %s still looks invalid (%s)", src, reason)
			return res, nil
		}
		res.recoveredFrom = src
	}

	currentByName := make(map[string]pbsDatastoreBlock, len(res.current))
	for _, b := range res.current {
		currentByName[b.Name] = b
	}
	inBackup := make(map[string]bool, len(blocks))
	var applyBlocks, deferred, keptCurrent []pbsDatastoreBlock
	for _, b := range blocks {
		inBackup[b.Name] = true
		item := pbsDatastoreFileItem{name: b.Name, path: b.Path}
		ok, reason, onRootFS, preflight := inspectPBSDatastoreBlock(b)
		item.onRootFS, item.preflight = onRootFS, preflight
		if ok {
			applyBlocks = append(applyBlocks, b)
		} else {
			item.deferReason = reason
			deferred = append(deferred, b)
			if cur, found := currentByName[b.Name]; found {
				item.keptCurrent = true
				keptCurrent = append(keptCurrent, cur)
			}
		}
		res.items = append(res.items, item)
	}

	if len(deferred) > 0 {
		res.deferredPath, res.deferredErr = writeDeferredPBSDatastoreCfg(deferred)
	}
	if len(applyBlocks) == 0 {
		res.noSafe = true
		return res, nil
	}

	var out strings.Builder
	for i, b := range append(applyBlocks, keptCurrent...) {
		if i > 0 {
			out.WriteString("\n")
		}
		out.WriteString(strings.TrimRight(strings.Join(b.Lines, "\n"), "\n"))
		out.WriteString("\n")
	}
	if err := writeFileAtomic(res.destPath, []byte(out.String()), 0o640); err != nil {
		return res, fmt.Errorf("write %s: %w", res.destPath, err)
	}
	res.written = len(applyBlocks)
	for _, b := range res.current {
		if !inBackup[b.Name] {
			res.removed = append(res.removed, b.Name)
		}
	}
	res.cleanup = removePBSDatastoreJobsAndACL(res.removed)
	return res, nil
}

// removeTarget removes the target datastore.cfg for a staged file without
// datastores; once it is gone every datastore of this PBS is removed.
func (r *pbsDatastoreFileResult) removeTarget() error {
	if err := removeIfExists(r.destPath); err != nil {
		return err
	}
	for _, b := range r.current {
		r.removed = append(r.removed, b.Name)
	}
	r.cleanup = removePBSDatastoreJobsAndACL(r.removed)
	return nil
}

// readCurrentPBSDatastoreBlocks reads the datastores of the datastore.cfg on this
// PBS; a missing file has none.
func readCurrentPBSDatastoreBlocks(path string) ([]pbsDatastoreBlock, error) {
	data, err := restoreFS.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	normalized, _ := normalizePBSDatastoreCfgContent(string(data))
	return parsePBSDatastoreCfgBlocks(normalized)
}

type pbsDatastoreInventoryRestoreLite struct {
	Files map[string]struct {
		Content string `json:"content"`
	} `json:"files"`
	Datastores []struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Comment string `json:"comment"`
		Origin  string `json:"origin"`
		CLIName string `json:"cli_name"`
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
		if strings.TrimSpace(ds.Origin) == "override" {
			continue
		}

		name := strings.TrimSpace(ds.Name)
		if name == "" {
			name = strings.TrimSpace(ds.CLIName)
		}
		path := strings.TrimSpace(ds.Path)
		if name == "" || path == "" {
			continue
		}
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		fmt.Fprintf(&out, "datastore: %s\n", name)
		if comment := strings.TrimSpace(ds.Comment); comment != "" {
			fmt.Fprintf(&out, "    comment %s\n", comment)
		}
		fmt.Fprintf(&out, "    path %s\n", path)
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
	ok, reason, onRootFS, preflight := inspectPBSDatastoreBlock(block)
	if logger != nil {
		if onRootFS {
			logger.Warning("PBS staged apply: datastore %s path %s resolves to root filesystem (mount missing?), applying definition anyway", block.Name, filepath.Clean(strings.TrimSpace(block.Path)))
		}
		if preflight != "" {
			logger.Warning("PBS datastore preflight: %s", preflight)
		}
	}
	return ok, reason
}

// inspectPBSDatastoreBlock decides whether a staged datastore definition is safe to
// write now. onRootFS reports a path that resolves to the root filesystem (its mount
// may be offline), preflight a problem found on the path of a datastore with data;
// both are written anyway.
func inspectPBSDatastoreBlock(block pbsDatastoreBlock) (ok bool, reason string, onRootFS bool, preflight string) {
	path := filepath.Clean(strings.TrimSpace(block.Path))
	if path == "" || path == "." || path == string(os.PathSeparator) {
		return false, "invalid or missing datastore path", false, ""
	}

	hasData, dataErr := pbsDatastoreHasData(path)
	if dataErr != nil {
		return false, fmt.Sprintf("datastore path inspection failed: %v", dataErr), false, ""
	}

	rootFS, _, devErr := isPathOnRootFilesystem(path)
	if devErr != nil {
		return false, fmt.Sprintf("filesystem identity check failed: %v", devErr), false, ""
	}
	// On fresh restores the mount backing this path may be offline/not mounted yet.
	// The datastore definition is still applied 1:1 so PBS shows the datastore as
	// unavailable rather than silently dropping it from datastore.cfg.
	onRootFS = rootFS && isSuspiciousDatastoreMountLocation(path) && !hasData

	if hasData {
		return true, "", onRootFS, validatePBSDatastoreReadOnly(path)
	}

	unexpected, err := pbsDatastoreHasUnexpectedEntries(path)
	if err != nil {
		return false, fmt.Sprintf("failed to inspect datastore directory: %v", err), onRootFS, ""
	}
	if unexpected {
		return false, "datastore directory is not empty (unexpected entries present)", onRootFS, ""
	}

	return true, "", onRootFS, ""
}

func writeDeferredPBSDatastoreCfg(blocks []pbsDatastoreBlock) (string, error) {
	if len(blocks) == 0 {
		return "", nil
	}
	base := RestoreRunDir()
	if err := restoreFS.MkdirAll(base, 0o700); err != nil {
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

// pbsAcmeAccountsRelPath is the archive-relative directory holding the PBS ACME account
// registrations: one JSON document per account. PBS has no acme/accounts.cfg (#313).
const pbsAcmeAccountsRelPath = "etc/proxmox-backup/acme/accounts"

// applyPBSAcmeAccountsFromStage mirrors the staged ACME account directory onto the system.
// These cannot go through applyPBSConfigFileFromStage: that reads a single file and refuses
// anything without a PBS section header, which the per-account JSON does not have.
//
// The directory is MIRRORED, not merged: an account on the system that the backup does not
// carry is removed. That is what every other pbs_host apply does to the contents of the file
// it rewrites (writeFileAtomic replaces the whole .cfg, dropping any section the backup lacks),
// and a half-merged account set would leave the node holding registrations the restored
// node.cfg never references.
//
// A stage WITHOUT the directory carries no information about accounts (an archive taken before
// this was collected, or BACKUP_PBS_ACME_ACCOUNTS=false at backup time) and leaves the system
// untouched, so a restore can never wipe the accounts of a working node just because the backup
// predates the fix. A staged directory that exists and is empty does say "no accounts" and is
// mirrored as such.
//
// Modes and ownership are set explicitly rather than inherited: /etc/proxmox-backup is
// 0700 backup:backup on a real PBS, so inheriting from it would hand the ACME private keys to
// the unprivileged backup service account. PBS itself creates this directory 0700 root:root.
func applyPBSAcmeAccountsFromStage(logger *logging.Logger, stageRoot string) error {
	stageDir := filepath.Join(stageRoot, pbsAcmeAccountsRelPath)

	info, err := restoreFS.Lstat(stageDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logging.DebugStep(logger, "pbs staged apply acme accounts", "Skip: %s not present in staging directory", pbsAcmeAccountsRelPath)
			return nil
		}
		return fmt.Errorf("stat staged %s: %w", pbsAcmeAccountsRelPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("staged %s must not be a symlink", pbsAcmeAccountsRelPath)
	}
	if !info.IsDir() {
		return fmt.Errorf("staged %s is not a directory", pbsAcmeAccountsRelPath)
	}

	entries, err := restoreFS.ReadDir(stageDir)
	if err != nil {
		return fmt.Errorf("read staged %s: %w", pbsAcmeAccountsRelPath, err)
	}

	// Validate the whole staged set BEFORE the destination is touched, so a malformed
	// archive leaves the node exactly as it was: not even the account directory is created
	// for it.
	names, err := readPBSAcmeStagedAccounts(stageDir, entries)
	if err != nil {
		return err
	}

	destDir := filepath.Join(string(os.PathSeparator), filepath.FromSlash(pbsAcmeAccountsRelPath))
	if err := ensurePBSAcmeAccountsDir(destDir); err != nil {
		return err
	}

	applied := make(map[string]struct{}, len(names))
	for _, name := range names {
		rel := path.Join(pbsAcmeAccountsRelPath, name)

		data, err := restoreFS.ReadFile(filepath.Join(stageDir, name))
		if err != nil {
			return fmt.Errorf("read staged %s: %w", rel, err)
		}
		if err := writeFileAtomic(filepath.Join(destDir, name), data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", filepath.Join(destDir, name), err)
		}
		applied[name] = struct{}{}
	}

	removed, err := removePBSAcmeAccountsNotIn(logger, destDir, applied)
	if err != nil {
		return err
	}

	logging.DebugStep(logger, "pbs staged apply acme accounts",
		"Applied %d account(s) to %s, removed %d not present in the backup", len(applied), destDir, removed)
	return nil
}

// readPBSAcmeStagedAccounts returns the account names the staged directory carries, and
// refuses the whole set when any entry is not a regular file. PBS writes one plain file per
// account, so a symlink, a socket or a nested directory there means the archive is malformed:
// the same class of fault as a staged accounts path that is a file instead of a directory,
// and it gets the same answer.
//
// It refuses instead of skipping the entry because the caller MIRRORS. A skipped name never
// reaches the keep-set, so removePBSAcmeAccountsNotIn would delete the live account of that
// name: the node would lose a working registration and its private key, while the staged
// replacement it was supposed to get was never written either. Refusing leaves every live
// account in place and surfaces the archive as a failed item, which the restore reports as
// completed with warnings.
func readPBSAcmeStagedAccounts(stageDir string, entries []os.DirEntry) ([]string, error) {
	names := make([]string, 0, len(entries))
	var rejected []string

	for _, entry := range entries {
		if entry == nil {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || name == "." || name == ".." {
			continue
		}

		info, err := restoreFS.Lstat(filepath.Join(stageDir, name))
		if err != nil {
			return nil, fmt.Errorf("stat staged %s: %w", path.Join(pbsAcmeAccountsRelPath, name), err)
		}
		if !info.Mode().IsRegular() {
			rejected = append(rejected, name)
			continue
		}
		names = append(names, name)
	}

	if len(rejected) > 0 {
		// ReadDir is os.ReadDir on every FS in use here, which sorts by name, so the
		// rejected list reads the same way on every run.
		return nil, fmt.Errorf("not a regular account file: %s; no account was applied and none was removed",
			strings.Join(rejected, ", "))
	}
	return names, nil
}

// ensurePBSAcmeAccountsDir creates the account directory with the mode and owner PBS uses,
// instead of the nearest-parent metadata ensureDirExistsWithInheritedMeta would copy.
func ensurePBSAcmeAccountsDir(destDir string) error {
	if err := restoreFS.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", destDir, err)
	}

	dir, err := restoreFS.Open(destDir)
	if err != nil {
		return fmt.Errorf("open %s: %w", destDir, err)
	}
	defer func() { _ = dir.Close() }()

	if err := atomicFileChmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod %s: %w", destDir, err)
	}
	if atomicGeteuid() == 0 {
		if err := atomicFileChown(dir, 0, 0); err != nil {
			return fmt.Errorf("chown %s: %w", destDir, err)
		}
	}
	return nil
}

// removePBSAcmeAccountsNotIn is the mirroring half: it drops the account entries the backup
// did not carry. It reports how many it removed so the caller can log the whole outcome.
func removePBSAcmeAccountsNotIn(logger *logging.Logger, destDir string, keep map[string]struct{}) (int, error) {
	entries, err := restoreFS.ReadDir(destDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read %s: %w", destDir, err)
	}

	removed := 0
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		name := entry.Name()
		if _, ok := keep[name]; ok {
			continue
		}
		target := filepath.Join(destDir, name)
		if err := restoreFS.RemoveAll(target); err != nil {
			return removed, fmt.Errorf("remove %s: %w", target, err)
		}
		logger.Info("PBS staged apply: removed ACME account not present in the backup: %s", name)
		removed++
	}
	return removed, nil
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
