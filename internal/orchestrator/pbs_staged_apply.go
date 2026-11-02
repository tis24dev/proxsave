package orchestrator

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/theme"
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
	pbsStagedApplyAcmePluginsCfgViaAPIFn    = applyPBSAcmePluginsCfgViaAPI
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

	// outcomeLogged holds the blocks that left something not applied or not
	// removed, whose outcomes they already logged: the restore ends with
	// warnings (BH-003) and the step adds no line of its own.
	var outcomeLogged []error

	if plan.HasCategoryID("pbs_host") {
		// Always restore file-only configs (no stable API coverage yet).
		// ACME should be applied before node config (node.cfg references ACME accounts/plugins).
		// The accounts are a directory, not a .cfg, so they get their own apply.
		// Each block logs its own outcome: a failure is recorded as already logged.
		if err := applyPBSAcmeAccountsBlock(logger, stageRoot); err != nil {
			outcomeLogged = append(outcomeLogged, err)
		}
		// The ACME plugins go through the PBS API as a section config. Without the
		// API they are written from the backup file in Merge mode too, as they
		// always were.
		if apiAvailable {
			if err := pbsStagedApplyAcmePluginsCfgViaAPIFn(ctx, logger, stageRoot, strict); err != nil {
				if stop := pbsSectionApplyFailed(ctx, logger, stageRoot, strict, pbsAcmePluginsSectionCfg(), err, &outcomeLogged); stop != nil {
					return stop
				}
			}
		} else {
			pbsSectionFileBlock(ctx, logger, stageRoot, strict, pbsAcmePluginsSectionCfg(), &outcomeLogged)
		}
		for _, f := range []pbsConfigFileBlock{pbsMetricServerFileBlock, pbsProxyFileBlock} {
			if err := applyPBSConfigFileBlock(logger, stageRoot, f); err != nil {
				outcomeLogged = append(outcomeLogged, err)
			}
		}

		if apiAvailable {
			if err := pbsStagedApplyTrafficControlCfgViaAPIFn(ctx, logger, stageRoot, strict); err != nil {
				if stop := pbsSectionApplyFailed(ctx, logger, stageRoot, strict, pbsTrafficControlSectionCfg(), err, &outcomeLogged); stop != nil {
					return stop
				}
			}
			if err := applyPBSNodeCfgBlock(ctx, logger, stageRoot, strict, true); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				outcomeLogged = append(outcomeLogged, err)
			}
		} else if allowFileFallback {
			pbsSectionFileBlock(ctx, logger, stageRoot, true, pbsTrafficControlSectionCfg(), &outcomeLogged)
			if err := applyPBSNodeCfgBlock(ctx, logger, stageRoot, true, false); err != nil {
				outcomeLogged = append(outcomeLogged, err)
			}
		} else {
			logging.DebugStep(logger, "pbs staged apply", "Skipping node.cfg/traffic-control.cfg: merge mode requires PBS API apply")
		}
	}

	if plan.HasCategoryID("datastore_pbs") {
		if apiAvailable {
			if err := pbsStagedApplyS3CfgViaAPIFn(ctx, logger, stageRoot, strict); err != nil {
				if stop := pbsSectionApplyFailed(ctx, logger, stageRoot, strict, pbsS3SectionCfg(), err, &outcomeLogged); stop != nil {
					return stop
				}
			}
			if err := pbsStagedApplyDatastoreCfgViaAPIFn(ctx, logger, stageRoot, strict); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					// Cancelled: no line, no file fallback, the restore stops.
					return ctxErr
				}
				var incomplete *pbsDatastoreRestoreIncompleteError
				var stopped *pbsDatastoreRestoreNotAppliedError
				switch {
				case errors.As(err, &stopped):
					// The block stopped before any datastore through the API, ran the
					// file-based fallback itself in Clean mode, and logged its outcome.
					outcomeLogged = append(outcomeLogged, err)
				case errors.As(err, &incomplete):
					// The datastore block already logged the outcome of every datastore
					// and its own: no line here, no file fallback over those outcomes,
					// and the restore still ends with warnings.
					outcomeLogged = append(outcomeLogged, err)
				default:
					// An error the block does not return today: the block again, with
					// that error as its event, written from the backup file in Clean
					// mode and not applied in Merge mode.
					if ferr := applyPBSDatastoreCfgAfterAPIError(ctx, logger, stageRoot, strict, err); ferr != nil {
						outcomeLogged = append(outcomeLogged, ferr)
					}
				}
			}
		} else if allowFileFallback {
			pbsSectionFileBlock(ctx, logger, stageRoot, true, pbsS3SectionCfg(), &outcomeLogged)
			if err := applyPBSDatastoreCfgFileBlock(ctx, logger, stageRoot, pbsDatastoreEventAPIUnavailable, errors.New("PBS API unavailable")); err != nil {
				// The block logged the outcome of every datastore and its own.
				outcomeLogged = append(outcomeLogged, err)
			}
		} else {
			logging.DebugStep(logger, "pbs staged apply", "Skipping datastore.cfg/s3.cfg: merge mode requires PBS API apply")
		}
	}

	// The jobs before the remotes: a Clean restore removes a stale remote only when no
	// job uses it, so removing the stale jobs first lets it go. PBS accepts a sync job
	// whose remote does not exist yet (measured on PBS 4.2.8, pull and push).
	if plan.HasCategoryID("pbs_jobs") {
		if apiAvailable {
			if err := pbsStagedApplySyncCfgViaAPIFn(ctx, logger, stageRoot, strict); err != nil {
				if stop := pbsSectionApplyFailed(ctx, logger, stageRoot, strict, pbsSyncSectionCfg(), err, &outcomeLogged); stop != nil {
					return stop
				}
			}
			if err := pbsStagedApplyVerificationCfgViaAPIFn(ctx, logger, stageRoot, strict); err != nil {
				if stop := pbsSectionApplyFailed(ctx, logger, stageRoot, strict, pbsVerificationSectionCfg(), err, &outcomeLogged); stop != nil {
					return stop
				}
			}
			if err := pbsStagedApplyPruneCfgViaAPIFn(ctx, logger, stageRoot, strict); err != nil {
				if stop := pbsSectionApplyFailed(ctx, logger, stageRoot, strict, pbsPruneSectionCfg(), err, &outcomeLogged); stop != nil {
					return stop
				}
			}
		} else if allowFileFallback {
			for _, cfg := range []pbsSectionCfg{pbsSyncSectionCfg(), pbsVerificationSectionCfg(), pbsPruneSectionCfg()} {
				pbsSectionFileBlock(ctx, logger, stageRoot, true, cfg, &outcomeLogged)
			}
		} else {
			logging.DebugStep(logger, "pbs staged apply", "Skipping sync/verification/prune configs: merge mode requires PBS API apply")
		}
	}

	if plan.HasCategoryID("pbs_remotes") {
		if apiAvailable {
			if err := pbsStagedApplyRemoteCfgViaAPIFn(ctx, logger, stageRoot, strict); err != nil {
				if stop := pbsSectionApplyFailed(ctx, logger, stageRoot, strict, pbsRemoteSectionCfg(), err, &outcomeLogged); stop != nil {
					return stop
				}
			}
		} else if allowFileFallback {
			pbsSectionFileBlock(ctx, logger, stageRoot, true, pbsRemoteSectionCfg(), &outcomeLogged)
		} else {
			logging.DebugStep(logger, "pbs staged apply", "Skipping remote.cfg: merge mode requires PBS API apply")
		}
	}

	if plan.HasCategoryID("pbs_tape") {
		// Tape is file-only: drives and changers need their device, and the keys
		// are secret material. Each file is its own block.
		for _, f := range pbsTapeFileBlocks {
			if err := applyPBSConfigFileBlock(logger, stageRoot, f); err != nil {
				outcomeLogged = append(outcomeLogged, err)
			}
		}
	}

	if len(outcomeLogged) > 0 {
		return &stageApplyOutcomeLoggedError{err: joinPBSLoggedOutcomes(outcomeLogged)}
	}
	return nil
}

// joinPBSLoggedOutcomes is the error of the blocks whose outcomes are already
// logged, in the order they ran, each still matchable with errors.Is and errors.As.
func joinPBSLoggedOutcomes(errs []error) error {
	joined := errs[0]
	for _, err := range errs[1:] {
		joined = fmt.Errorf("%w; %w", joined, err)
	}
	return joined
}

// pbsSectionFileBlock writes, in Clean (1:1) mode without the PBS API, a section
// config from the backup file as its block, which names what it does and logs
// its outcome: a result other than nil is recorded in outcomeLogged.
func pbsSectionFileBlock(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool, cfg pbsSectionCfg, outcomeLogged *[]error) {
	cause := fmt.Errorf("%s: PBS API unavailable", filepath.Base(cfg.file))
	if err := applyPBSSectionCfgFileBlock(ctx, logger, stageRoot, strict, cfg, pbsDatastoreEventAPIUnavailable, cause); err != nil {
		*outcomeLogged = append(*outcomeLogged, err)
	}
}

// pbsSectionApplyFailed handles the failed PBS API apply of a section config, the
// same way for every config. A cancellation stops the staged apply: it is
// returned, with no line and no fallback. A block that has already logged its
// outcome is recorded in outcomeLogged, with no line and no fallback over it.
// Any other error, which the block does not return today, runs the block again
// with that error as its event: the file written from the backup in strict
// (Clean 1:1) mode, the configuration not applied in Merge mode.
func pbsSectionApplyFailed(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool, cfg pbsSectionCfg, err error, outcomeLogged *[]error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		// Cancelled: no line, no file fallback, the restore stops.
		return ctxErr
	}
	if pbsSectionOutcomeLogged(err) {
		// The section block already logged the outcome of every object and its
		// own: no line here, no file fallback over those outcomes, and the
		// restore still ends with warnings.
		*outcomeLogged = append(*outcomeLogged, err)
		return nil
	}
	if blockErr := applyPBSSectionCfgAfterAPIError(ctx, logger, stageRoot, strict, cfg, err); blockErr != nil {
		*outcomeLogged = append(*outcomeLogged, blockErr)
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

	lines := strings.SplitSeq(content, "\n")
	for line := range lines {
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
	// A removable datastore (backing-device) has a relative path that PBS resolves to
	// /mnt/datastore/<name> on its own device: there is nothing to inspect here.
	for _, line := range block.Lines {
		if strings.HasPrefix(strings.TrimSpace(line), "backing-device ") {
			return true, "", false, ""
		}
	}
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
	res, err := applyPBSAcmeAccounts(logger, stageRoot)
	if err != nil {
		return err
	}
	for _, rm := range res.removals {
		if rm.err != nil {
			return rm.err
		}
		logger.Info("PBS staged apply: removed ACME account not present in the backup: %s", rm.name)
	}
	return nil
}

// pbsAcmeAccountRemoval is one account of this PBS that the backup does not have:
// err is why it could not be removed, nil when it was.
type pbsAcmeAccountRemoval struct {
	name string
	err  error
}

// pbsAcmeAccountsResult is what applyPBSAcmeAccounts did: the accounts it wrote
// and, in the order the directory lists them, the removals of the others.
type pbsAcmeAccountsResult struct {
	applied  int
	removals []pbsAcmeAccountRemoval
}

// applyPBSAcmeAccounts is applyPBSAcmeAccountsFromStage without its lines: it
// returns what it did for its caller to log. A removal that fails does not stop
// the others.
func applyPBSAcmeAccounts(logger *logging.Logger, stageRoot string) (res pbsAcmeAccountsResult, err error) {
	stageDir := filepath.Join(stageRoot, pbsAcmeAccountsRelPath)

	info, err := restoreFS.Lstat(stageDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logging.DebugStep(logger, "pbs staged apply acme accounts", "Skip: %s not present in staging directory", pbsAcmeAccountsRelPath)
			return res, nil
		}
		return res, &pbsAcmeAccountsStagedError{fmt.Errorf("stat staged %s: %w", pbsAcmeAccountsRelPath, err)}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return res, &pbsAcmeAccountsStagedError{fmt.Errorf("staged %s must not be a symlink", pbsAcmeAccountsRelPath)}
	}
	if !info.IsDir() {
		return res, &pbsAcmeAccountsStagedError{fmt.Errorf("staged %s is not a directory", pbsAcmeAccountsRelPath)}
	}

	entries, err := restoreFS.ReadDir(stageDir)
	if err != nil {
		return res, &pbsAcmeAccountsStagedError{fmt.Errorf("read staged %s: %w", pbsAcmeAccountsRelPath, err)}
	}

	// Validate the whole staged set BEFORE the destination is touched, so a malformed
	// archive leaves the node exactly as it was: not even the account directory is created
	// for it.
	names, err := readPBSAcmeStagedAccounts(stageDir, entries)
	if err != nil {
		return res, &pbsAcmeAccountsStagedError{err}
	}

	destDir := filepath.Join(string(os.PathSeparator), filepath.FromSlash(pbsAcmeAccountsRelPath))
	if err := ensurePBSAcmeAccountsDir(destDir); err != nil {
		return res, err
	}

	applied := make(map[string]struct{}, len(names))
	for _, name := range names {
		rel := path.Join(pbsAcmeAccountsRelPath, name)

		data, err := restoreFS.ReadFile(filepath.Join(stageDir, name))
		if err != nil {
			return res, &pbsAcmeAccountsStagedError{fmt.Errorf("read staged %s: %w", rel, err)}
		}
		if err := writeFileAtomic(filepath.Join(destDir, name), data, 0o600); err != nil {
			return res, fmt.Errorf("write %s: %w", filepath.Join(destDir, name), err)
		}
		applied[name] = struct{}{}
	}

	res.applied = len(applied)
	if res.removals, err = removePBSAcmeAccountsNotIn(destDir, applied); err != nil {
		return res, err
	}
	removed := 0
	for _, rm := range res.removals {
		if rm.err == nil {
			removed++
		}
	}
	logging.DebugStep(logger, "pbs staged apply acme accounts",
		"Applied %d account(s) to %s, removed %d not present in the backup", len(applied), destDir, removed)
	return res, nil
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
// account in place and ends the accounts block with a ⚠ outcome, which the restore reports
// as completed with warnings.
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
// did not carry, each one even when another could not be removed, and returns every
// removal with its result so the caller can log the whole outcome.
func removePBSAcmeAccountsNotIn(destDir string, keep map[string]struct{}) ([]pbsAcmeAccountRemoval, error) {
	entries, err := restoreFS.ReadDir(destDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", destDir, err)
	}

	var removals []pbsAcmeAccountRemoval
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		name := entry.Name()
		if _, ok := keep[name]; ok {
			continue
		}
		target := filepath.Join(destDir, name)
		rm := pbsAcmeAccountRemoval{name: name}
		if err := restoreFS.RemoveAll(target); err != nil {
			rm.err = fmt.Errorf("remove %s: %w", target, err)
		}
		removals = append(removals, rm)
	}
	return removals, nil
}

// pbsAcmeAccountsStagedError is a failure of the ACME accounts apply on the side
// of the backup: the staged directory or an account in it could not be read, or
// is not what PBS writes. Any other failure is on the side of this PBS.
type pbsAcmeAccountsStagedError struct{ err error }

func (e *pbsAcmeAccountsStagedError) Error() string { return e.err.Error() }

func (e *pbsAcmeAccountsStagedError) Unwrap() error { return e.err }

const pbsAcmeAccountsRestoreDebug = "PBS ACME accounts restore: "

// applyPBSAcmeAccountsBlock restores the ACME accounts as one block: nothing
// visible when the backup has none, otherwise the opening line and the outcome,
// with the event of a failure in between. A failure returns its error, whose
// outcome is already logged.
func applyPBSAcmeAccountsBlock(logger *logging.Logger, stageRoot string) error {
	stageDir := filepath.Join(stageRoot, pbsAcmeAccountsRelPath)
	if _, err := restoreFS.Lstat(stageDir); errors.Is(err, os.ErrNotExist) {
		logger.Debug(pbsAcmeAccountsRestoreDebug+"staged=%s not in the backup action=skip", stageDir)
		return nil
	}
	logger.Info("Restoring PBS ACME accounts...")
	logger.Debug(pbsAcmeAccountsRestoreDebug+"staged=%s target=%s", stageDir, filepath.Join(string(os.PathSeparator), filepath.FromSlash(pbsAcmeAccountsRelPath)))
	res, err := applyPBSAcmeAccounts(logger, stageRoot)
	var staged *pbsAcmeAccountsStagedError
	switch {
	case err == nil:
		return pbsAcmeAccountsOutcome(logger, res)
	case errors.As(err, &staged):
		logger.Debug(pbsAcmeAccountsRestoreDebug+"staged read failed error=%q", err.Error())
		logger.Info("The ACME accounts in the backup could not be read: \"%s\"", err.Error())
	default:
		logger.Debug(pbsAcmeAccountsRestoreDebug+"write failed error=%q", err.Error())
		logger.Info("The ACME accounts could not be written to this PBS: \"%s\"", err.Error())
	}
	logger.Warning("%s PBS ACME accounts: not applied", theme.SymbolWarning)
	return err
}

// pbsAcmeAccountsOutcome names each account of this PBS that the backup does not
// have, removed or not, then logs the outcome of the block: applied, or when a
// removal failed partially applied, or not applied when nothing else was done.
func pbsAcmeAccountsOutcome(logger *logging.Logger, res pbsAcmeAccountsResult) error {
	var notRemoved []string
	for _, rm := range res.removals {
		logger.Debug(pbsAcmeAccountsRestoreDebug+"account=%s not in backup action=remove", rm.name)
		if rm.err != nil {
			logger.Debug(pbsAcmeAccountsRestoreDebug+"account=%s remove failed error=%q", rm.name, rm.err.Error())
			logger.Info("ACME account %s is configured on this PBS but not present in the backup", rm.name)
			logger.Warning("%s PBS ACME account %s: not removed", theme.SymbolWarning, rm.name)
			notRemoved = append(notRemoved, rm.name)
			continue
		}
		logger.Info("ACME account %s is configured on this PBS but not present in the backup", rm.name)
		logger.Info("%s PBS ACME account %s: removed", theme.SymbolSuccess, rm.name)
	}
	switch {
	case len(notRemoved) == 0:
		logger.Info("%s PBS ACME accounts: applied", theme.SymbolSuccess)
		return nil
	case len(notRemoved) == res.applied+len(res.removals):
		logger.Warning("%s PBS ACME accounts: not applied", theme.SymbolWarning)
	default:
		logger.Warning("%s PBS ACME accounts: partially applied", theme.SymbolWarning)
	}
	return fmt.Errorf("%d ACME account(s) not removed [%s]", len(notRemoved), strings.Join(notRemoved, ", "))
}

// pbsConfigFileBlock is a PBS configuration file restored as a file, with the
// words of its block.
type pbsConfigFileBlock struct {
	rel     string
	debug   string // DEBUG prefix
	opening string
	config  string // event subject: "The metric server configuration"
	outcome string
	// json: the file is JSON, not a section config: no section header or
	// comment rule, only an empty file removes the target. perm is its mode
	// (0 means 0640).
	json bool
	perm os.FileMode
}

var (
	pbsMetricServerFileBlock = pbsConfigFileBlock{
		rel:     "etc/proxmox-backup/metricserver.cfg",
		debug:   "PBS metric server restore: ",
		opening: "Restoring PBS metric server configuration...",
		config:  "The metric server configuration",
		outcome: "PBS metric server configuration",
	}
	// pbsTapeFileBlocks are the tape files, written as staged (the drives and
	// changers need their device). The modes are the ones PBS writes: the keys
	// 0600, the key configuration 0640 for the backup group, as
	// proxmox-backup-proxy (user backup) reads it to list the keys and to label
	// an encrypted media set.
	pbsTapeFileBlocks = []pbsConfigFileBlock{
		{rel: "etc/proxmox-backup/tape.cfg", debug: "PBS tape drives restore: ",
			opening: "Restoring PBS tape drives and changers...", config: "The tape drives and changers", outcome: "PBS tape drives and changers"},
		{rel: "etc/proxmox-backup/media-pool.cfg", debug: "PBS tape media pools restore: ",
			opening: "Restoring PBS tape media pools...", config: "The tape media pools", outcome: "PBS tape media pools"},
		{rel: "etc/proxmox-backup/tape-job.cfg", debug: "PBS tape backup jobs restore: ",
			opening: "Restoring PBS tape backup jobs...", config: "The tape backup jobs", outcome: "PBS tape backup jobs"},
		{rel: "etc/proxmox-backup/tape-encryption-keys.json", debug: "PBS tape encryption keys restore: ",
			opening: "Restoring PBS tape encryption keys...", config: "The tape encryption keys", outcome: "PBS tape encryption keys",
			json: true, perm: 0o600},
		{rel: "etc/proxmox-backup/tape-encryption-key-config.json", debug: "PBS tape encryption key configuration restore: ",
			opening: "Restoring PBS tape encryption key configuration...", config: "The tape encryption key configuration", outcome: "PBS tape encryption key configuration",
			json: true, perm: 0o640},
	}
	pbsProxyFileBlock = pbsConfigFileBlock{
		rel:     "etc/proxmox-backup/proxy.cfg",
		debug:   "PBS proxy restore: ",
		opening: "Restoring PBS proxy configuration...",
		config:  "The proxy configuration",
		outcome: "PBS proxy configuration",
	}
)

// pbsConfigCommentsOnly reports whether content holds only comments and blank
// lines.
func pbsConfigCommentsOnly(content string) bool {
	for line := range strings.SplitSeq(content, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			return false
		}
	}
	return true
}

// applyPBSConfigFileBlock writes one staged PBS configuration file to this PBS as
// one block: nothing visible when the backup does not have it; a file without
// configuration (empty, or only comments) removes the target; a file without a
// section header is not written. A failure returns its error, whose outcome is
// already logged.
func applyPBSConfigFileBlock(logger *logging.Logger, stageRoot string, f pbsConfigFileBlock) error {
	stagePath := filepath.Join(stageRoot, f.rel)
	destPath := filepath.Join(string(os.PathSeparator), filepath.FromSlash(f.rel))
	data, err := restoreFS.ReadFile(stagePath)
	if errors.Is(err, os.ErrNotExist) {
		logger.Debug(f.debug+"staged=%s not in the backup action=skip", stagePath)
		return nil
	}
	logger.Info("%s", f.opening)
	logger.Debug(f.debug+"staged=%s target=%s", stagePath, destPath)
	notApplied := func(err error) error {
		logger.Warning("%s %s: not applied", theme.SymbolWarning, f.outcome)
		return err
	}
	if err != nil {
		err = fmt.Errorf("read staged %s: %w", f.rel, err)
		logger.Debug(f.debug+"staged read failed error=%q", err.Error())
		logger.Info("%s in the backup could not be read: \"%s\"", f.config, err.Error())
		return notApplied(err)
	}
	trimmed := strings.TrimSpace(string(data))
	perm := f.perm
	perm = cmp.Or(perm, 0o640)
	var writeErr error
	switch {
	case f.json && trimmed == "":
		if writeErr = removeIfExists(destPath); writeErr == nil {
			logger.Debug("%sstaged file is empty, target removed", f.debug)
		}
	case f.json:
		if writeErr = writeFileAtomic(destPath, []byte(trimmed+"\n"), perm); writeErr != nil {
			writeErr = fmt.Errorf("write %s: %w", destPath, writeErr)
		} else {
			logger.Debug("%swritten", f.debug)
		}
	case pbsConfigCommentsOnly(trimmed):
		if writeErr = removeIfExists(destPath); writeErr == nil {
			logger.Debug(f.debug+"staged file has no configuration comments_only=%v, target removed", trimmed != "")
		}
	case !pbsConfigHasHeader(trimmed):
		writeErr = fmt.Errorf("%s does not look like a valid PBS config file (missing section header)", f.rel)
	default:
		if writeErr = writeFileAtomic(destPath, []byte(trimmed+"\n"), perm); writeErr != nil {
			writeErr = fmt.Errorf("write %s: %w", destPath, writeErr)
		} else {
			logger.Debug("%swritten", f.debug)
		}
	}
	if writeErr != nil {
		logger.Debug(f.debug+"write failed error=%q", writeErr.Error())
		logger.Info("%s could not be written to this PBS: \"%s\"", f.config, writeErr.Error())
		return notApplied(writeErr)
	}
	logger.Info("%s %s: applied", theme.SymbolSuccess, f.outcome)
	return nil
}

// pbsNodeKeysToDelete reads this PBS's node.cfg and returns, for a Clean
// restore, the settings it has that the backup (keys) does not, which
// "node update --delete" takes. Only key names are logged, never values; a
// node.cfg that cannot be read is DEBUG only and deletes nothing.
func pbsNodeKeysToDelete(debug func(string, ...any), keys []string) []string {
	target := filepath.Join(string(os.PathSeparator), filepath.FromSlash(pbsNodeCfgRel))
	data, err := restoreFS.ReadFile(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		debug("current file=%s read failed error=%q", target, err.Error())
		return nil
	}
	_, current := pbsNodeUpdateFlags(string(data))
	debug("current keys=%s", strings.Join(current, ","))
	inBackup := make(map[string]bool, len(keys))
	for _, k := range keys {
		inBackup[k] = true
	}
	var del []string
	for _, k := range current {
		if !inBackup[k] && pbsNodeDeletableKeys[k] {
			del = append(del, k)
		}
	}
	if len(del) > 0 {
		debug("action=delete keys=%s", strings.Join(del, ","))
	}
	return del
}

const (
	pbsNodeCfgRel       = "etc/proxmox-backup/node.cfg"
	pbsNodeRestoreDebug = "PBS node restore: "
)

// applyPBSNodeCfgBlock restores node.cfg as one block. Through the PBS API
// (apiAvailable) every setting of the staged file is applied with one
// "node update" (Merge and Clean alike: settings this PBS has and the backup
// does not are kept); when PBS refuses it, Clean (1:1) mode writes the staged file
// instead and Merge mode leaves the node configuration as it is. Without the API
// (Clean mode only) the staged file is written. Nothing visible happens when the
// backup has no node.cfg or no setting in it. A ⚠ outcome returns its error,
// already logged; a cancellation returns the cancellation, with no outcome.
func applyPBSNodeCfgBlock(ctx context.Context, logger *logging.Logger, stageRoot string, strict, apiAvailable bool) error {
	debug := func(format string, args ...any) { logger.Debug(pbsNodeRestoreDebug+format, args...) }
	stagePath := filepath.Join(stageRoot, pbsNodeCfgRel)
	raw, present, err := readStageFileOptional(stageRoot, pbsNodeCfgRel)
	if err == nil && !present {
		debug("staged=%s not in the backup action=skip", stagePath)
		return nil
	}
	mode := "merge"
	if strict {
		mode = "clean"
	}
	notApplied := func(err error) error {
		logger.Warning("%s PBS node configuration: not applied", theme.SymbolWarning)
		return err
	}
	if err != nil {
		logger.Info("Restoring PBS node configuration...")
		debug("mode=%s staged=%s", mode, stagePath)
		debug("staged=%s read failed error=%q", stagePath, err.Error())
		logger.Info("The node configuration in the backup could not be read: \"%s\"", err.Error())
		return notApplied(err)
	}
	_, keys := pbsNodeUpdateFlags(raw)
	if len(keys) == 0 {
		debug("staged=%s has no setting action=skip", stagePath)
		return nil
	}
	logger.Info("Restoring PBS node configuration...")
	debug("mode=%s staged=%s", mode, stagePath)

	var cause error
	if apiAvailable {
		debug("action=update keys=%s", strings.Join(keys, ","))
		var deleteKeys []string
		if strict {
			deleteKeys = pbsNodeKeysToDelete(debug, keys)
		}
		updateErr := pbsStagedApplyNodeCfgViaAPIFn(ctx, stageRoot, deleteKeys)
		if updateErr == nil {
			debug("updated")
			logger.Info("%s PBS node configuration: applied", theme.SymbolSuccess)
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		debug("update failed error=%q", pbsManagerErrorText(updateErr))
		logger.Info("The node configuration was rejected by PBS: \"%s\"", pbsManagerErrorLine(updateErr))
		// The command line runPBSManager puts in front of the refusal may carry a
		// flag value: the result keeps the refusal only.
		cause = fmt.Errorf("node update refused: %s", pbsManagerErrorLine(updateErr))
		if !strict {
			return notApplied(cause)
		}
	} else {
		logger.Info("%s", pbsDatastoreEventAPIUnavailable)
		cause = errors.New("node.cfg: PBS API unavailable")
	}

	target := filepath.Join(string(os.PathSeparator), filepath.FromSlash(pbsNodeCfgRel))
	debug("fallback=file target=%s source=%s", target, stagePath)
	// node.cfg has no section header (its description is a block of comments):
	// it is written as staged.
	var writeErr error
	if err := writeFileAtomic(target, []byte(strings.TrimSpace(raw)+"\n"), 0o640); err != nil {
		writeErr = fmt.Errorf("write %s: %w", target, err)
	}
	if writeErr != nil {
		debug("fallback write failed error=%q", writeErr.Error())
		logger.Info("The node configuration could not be written to this PBS: \"%s\"", writeErr.Error())
		return notApplied(fmt.Errorf("%w; file fallback: %w", cause, writeErr))
	}
	debug("fallback written")
	logger.Warning("%s PBS node configuration: applied from the backup file", theme.SymbolWarning)
	return cause
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
	for line := range strings.SplitSeq(content, "\n") {
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
