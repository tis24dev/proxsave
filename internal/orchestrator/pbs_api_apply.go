package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

var pbsAPIApplyGeteuid = os.Geteuid

// errPBSCleanRemoveIncomplete marks a Clean (1:1) restore where one or more stale
// objects absent from the backup could NOT be removed from PBS. The objects are
// left in place (the conservative outcome, nothing destroyed), but the caller
// must report the restore "with warnings" instead of a clean success, and must
// NOT paper over it with the file-based fallback (which would force-rewrite the
// .cfg and drop the object, bypassing PBS's refusal, e.g. for an in-use object).
var errPBSCleanRemoveIncomplete = errors.New("PBS Clean 1:1 incomplete: stale object(s) could not be removed")

// pbsCleanRemoveResult returns nil when no clean-mode remove failed, otherwise an
// error wrapping errPBSCleanRemoveIncomplete listing the objects left behind.
func pbsCleanRemoveResult(kind string, failures []string) error {
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("%s remove failed for %d object(s) [%s]: %w", kind, len(failures), strings.Join(failures, ", "), errPBSCleanRemoveIncomplete)
}

// pbsCleanIncompleteApplyError is an apply failure of a Clean (1:1) apply in which
// one or more stale objects could not be removed. Its text is the apply
// failure alone; it also matches errPBSCleanRemoveIncomplete, so the caller does
// not take the file-based fallback.
type pbsCleanIncompleteApplyError struct {
	err    error
	remove error
}

func (e *pbsCleanIncompleteApplyError) Error() string { return e.err.Error() }

func (e *pbsCleanIncompleteApplyError) Unwrap() []error { return []error{e.err, e.remove} }

func normalizeProxmoxCfgKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "_", "-")
	return key
}

func buildProxmoxManagerFlags(entries []proxmoxNotificationEntry, skipKeys ...string) []string {
	if len(entries) == 0 {
		return nil
	}
	skip := make(map[string]struct{}, len(skipKeys)+2)
	for _, k := range skipKeys {
		skip[normalizeProxmoxCfgKey(k)] = struct{}{}
	}
	// Common no-op keys
	skip["digest"] = struct{}{}
	skip["name"] = struct{}{}

	args := make([]string, 0, len(entries)*2)
	for _, kv := range entries {
		key := normalizeProxmoxCfgKey(kv.Key)
		if key == "" {
			continue
		}
		if _, ok := skip[key]; ok {
			continue
		}
		value := strings.TrimSpace(kv.Value)
		args = append(args, "--"+key)
		args = append(args, value)
	}
	return args
}

func popEntryValue(entries []proxmoxNotificationEntry, keys ...string) (value string, remaining []proxmoxNotificationEntry, ok bool) {
	if len(entries) == 0 || len(keys) == 0 {
		return "", entries, false
	}
	want := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		want[normalizeProxmoxCfgKey(k)] = struct{}{}
	}

	remaining = make([]proxmoxNotificationEntry, 0, len(entries))
	for _, kv := range entries {
		key := normalizeProxmoxCfgKey(kv.Key)
		if _, match := want[key]; match && !ok {
			value = strings.TrimSpace(kv.Value)
			ok = true
			continue
		}
		remaining = append(remaining, kv)
	}
	return value, remaining, ok
}

// runPBSManagerRedacted runs proxmox-backup-manager, redacting the named flags and
// argument positions before the command reaches an error message.
//
// It captures STDOUT ONLY, because eight callers parse the output as JSON (every
// `... list --output-format=json` below, via parsePBSListIDs and the datastore
// list in applyPBSDatastores) and anything the tool writes to stderr makes that
// parse fail on a byte it never meant as data. internal/backup already reached the
// same conclusion for this binary and documents it at collector_deps.go:15
// (smartctl failures on `disk list`).
//
// Unlike pvesh this is NOT the locale bug: proxmox-backup-manager is Rust, and on
// a live PVE 9.1.9 node (2026-09-05) `datastore list`, `disk list` and `user list`
// all wrote 0 bytes to stderr even with LC_ALL set to a locale the host lacks. The
// mechanism is identical, the reachability is not demonstrated; stdout-only is the
// correct capture for parsed output either way.
//
// Every mutating caller writes `_, err :=` and drops the output, so the switch
// costs no diagnostics: on failure runCommandStdout folds stderr into the error,
// where the redacted message below carries it.
func runPBSManagerRedacted(ctx context.Context, args []string, redactFlags []string, redactIndexes []int) ([]byte, error) {
	out, err := runCommandStdout(ctx, "proxmox-backup-manager", args...)
	if err == nil {
		return out, nil
	}
	redacted := redactCLIArgs(args, redactFlags)
	for _, idx := range redactIndexes {
		if idx >= 0 && idx < len(redacted) {
			redacted[idx] = "<redacted>"
		}
	}
	return out, fmt.Errorf("proxmox-backup-manager %s failed: %w", strings.Join(redacted, " "), err)
}

func runPBSManager(ctx context.Context, args ...string) ([]byte, error) {
	return runPBSManagerRedacted(ctx, args, nil, nil)
}

func runPBSManagerSensitive(ctx context.Context, args []string, redactFlags ...string) ([]byte, error) {
	return runPBSManagerRedacted(ctx, args, redactFlags, nil)
}

func unwrapPBSJSONData(raw []byte) []byte {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &wrapper); err != nil {
		return []byte(trimmed)
	}
	if data, ok := wrapper["data"]; ok && len(bytesTrimSpace(data)) > 0 {
		return data
	}
	return []byte(trimmed)
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func parsePBSListIDs(raw []byte, candidateKeys ...string) ([]string, error) {
	data := unwrapPBSJSONData(raw)
	if len(data) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(candidateKeys))
	for _, k := range candidateKeys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no candidate keys provided for PBS list ID parsing")
	}

	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}

	out := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for idx, row := range rows {
		id := ""
		for _, k := range keys {
			v, ok := row[k]
			if !ok || v == nil {
				continue
			}
			s, ok := v.(string)
			if !ok {
				continue
			}
			id = strings.TrimSpace(s)
			if id != "" {
				break
			}
		}
		if id == "" {
			available := make([]string, 0, len(row))
			for k := range row {
				available = append(available, k)
			}
			sort.Strings(available)
			return nil, fmt.Errorf("failed to parse PBS list row %d: none of %v present as non-empty string (available keys: %v)", idx, keys, available)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func ensurePBSServicesForAPI(ctx context.Context, logger *logging.Logger) error {
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}

	if !isRealRestoreFS(restoreFS) {
		return fmt.Errorf("non-system filesystem in use")
	}
	if pbsAPIApplyGeteuid() != 0 {
		return fmt.Errorf("requires root privileges")
	}

	if _, err := restoreCmd.Run(ctx, "proxmox-backup-manager", "version"); err != nil {
		return fmt.Errorf("proxmox-backup-manager not available: %w", err)
	}

	// Best-effort: ensure services are started before API apply.
	startCtx, cancel := context.WithTimeout(ctx, 2*serviceStartTimeout+serviceVerifyTimeout+5*time.Second)
	defer cancel()
	if err := startPBSServices(startCtx, logger); err != nil {
		return err
	}
	return nil
}

// pbsSectionCfg describes one PBS section-config file that is restored through
// proxmox-backup-manager: the staged file, the command words in front of the verb
// ("remote", "s3 endpoint", ...), the keys a list row is identified by (in the order
// they are tried), the flags whose value a failed create/update must not show, and
// the keys whose value the file stores base64-encoded while create/update take it
// plain, so it is decoded before it becomes a flag.
type pbsSectionCfg struct {
	file         string
	command      []string
	listKeys     []string
	secretFlags  []string
	base64Values []string
}

// pbsSectionFlags builds the create/update flags of one section with
// buildProxmoxManagerFlags, entry by entry, and decodes from standard base64 the value
// of every key named in base64Keys. The staged value is trimmed before it is decoded,
// the decoded value is passed as it is, so a password that begins or ends with a space
// keeps it. A value that is not valid base64 is passed as staged, and one DEBUG line
// names the section and the key, never the value.
func pbsSectionFlags(logger *logging.Logger, kind, id string, entries []proxmoxNotificationEntry, base64Keys []string) []string {
	if len(base64Keys) == 0 {
		return buildProxmoxManagerFlags(entries)
	}
	decode := make(map[string]struct{}, len(base64Keys))
	for _, k := range base64Keys {
		decode[normalizeProxmoxCfgKey(k)] = struct{}{}
	}
	args := make([]string, 0, len(entries)*2)
	for _, kv := range entries {
		// One entry gives one "--key value" pair, or nothing when the key is skipped.
		flag := buildProxmoxManagerFlags([]proxmoxNotificationEntry{kv})
		if len(flag) != 2 {
			continue
		}
		key := strings.TrimPrefix(flag[0], "--")
		if _, ok := decode[key]; ok {
			decoded, err := base64.StdEncoding.DecodeString(flag[1])
			if err != nil {
				logger.Debug("PBS API apply: %s %s: %s is not valid base64, passing the staged value unchanged", kind, id, key)
			} else {
				flag[1] = string(decoded)
			}
		}
		args = append(args, flag...)
	}
	return args
}

// applyPBSSectionCfgViaAPI creates or updates every section of the staged cfg.file
// through proxmox-backup-manager, in sorted order, falling back from create to
// update. An object whose create and update both fail is logged and the apply
// continues with the next one; the result then names every object not applied. In
// strict (Clean 1:1) mode it first removes every listed object that the staged file
// does not name; a remove that fails is logged, the apply continues, and the result
// wraps errPBSCleanRemoveIncomplete, also when some objects were not applied.
func applyPBSSectionCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool, cfg pbsSectionCfg) error {
	kind := strings.Join(cfg.command, " ")
	commandArgs := func(rest ...string) []string {
		return append(append([]string(nil), cfg.command...), rest...)
	}

	raw, present, err := readStageFileOptional(stageRoot, cfg.file)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	sections, err := parseProxmoxNotificationSections(raw)
	if err != nil {
		return fmt.Errorf("parse staged %s: %w", filepath.Base(cfg.file), err)
	}

	desired := make(map[string]proxmoxNotificationSection, len(sections))
	for _, s := range sections {
		id := strings.TrimSpace(s.Name)
		if id == "" {
			continue
		}
		desired[id] = s
	}

	var removeFailures []string
	if strict {
		out, err := runPBSManager(ctx, commandArgs("list", "--output-format=json")...)
		if err != nil {
			return err
		}
		current, err := parsePBSListIDs(out, cfg.listKeys...)
		if err != nil {
			return fmt.Errorf("parse %s list: %w", kind, err)
		}
		for _, id := range current {
			if _, ok := desired[id]; ok {
				continue
			}
			if _, err := runPBSManager(ctx, commandArgs("remove", id)...); err != nil {
				logger.Warning("PBS API apply: %s remove %s failed (continuing): %v", kind, id, err)
				removeFailures = append(removeFailures, id)
			}
		}
	}

	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var applyFailures []string
	for _, id := range ids {
		s := desired[id]
		flags := pbsSectionFlags(logger, kind, id, s.Entries, cfg.base64Values)
		createArgs := commandArgs(append([]string{"create", id}, flags...)...)
		if _, err := runPBSManagerSensitive(ctx, createArgs, cfg.secretFlags...); err != nil {
			updateArgs := commandArgs(append([]string{"update", id}, flags...)...)
			if _, upErr := runPBSManagerSensitive(ctx, updateArgs, cfg.secretFlags...); upErr != nil {
				logger.Warning("PBS API apply: %s %s failed (continuing): %v (create) / %v (update)", kind, id, err, upErr)
				applyFailures = append(applyFailures, id)
			}
		}
	}

	if len(applyFailures) > 0 {
		applyErr := fmt.Errorf("%d %s object(s) not applied: [%s]", len(applyFailures), kind, strings.Join(applyFailures, ", "))
		if removeErr := pbsCleanRemoveResult(kind, removeFailures); removeErr != nil {
			return &pbsCleanIncompleteApplyError{err: applyErr, remove: removeErr}
		}
		return applyErr
	}
	return pbsCleanRemoveResult(kind, removeFailures)
}

func applyPBSRemoteCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	return applyPBSSectionCfgViaAPI(ctx, logger, stageRoot, strict, pbsSectionCfg{
		file:        "etc/proxmox-backup/remote.cfg",
		command:     []string{"remote"},
		listKeys:    []string{"id", "name"},
		secretFlags: []string{"--password"},
		// PBS writes the password as standard base64 ("dummyprobe" is stored as
		// "ZHVtbXlwcm9iZQ==") and base64-encodes whatever --password receives.
		base64Values: []string{"password"},
	})
}

func applyPBSS3CfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	return applyPBSSectionCfgViaAPI(ctx, logger, stageRoot, strict, pbsSectionCfg{
		file:        "etc/proxmox-backup/s3.cfg",
		command:     []string{"s3", "endpoint"},
		listKeys:    []string{"id", "name"},
		secretFlags: []string{"--access-key", "--secret-key"},
	})
}

func applyPBSDatastoreCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	dsRaw, present, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/datastore.cfg")
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	sections, err := parseProxmoxNotificationSections(dsRaw)
	if err != nil {
		return fmt.Errorf("parse staged datastore.cfg: %w", err)
	}

	desired := make(map[string]proxmoxNotificationSection, len(sections))
	for _, s := range sections {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		desired[name] = s
	}

	type dsRow struct {
		Name  string `json:"name"`
		Store string `json:"store"`
		ID    string `json:"id"`
		Path  string `json:"path"`
	}
	currentPaths := make(map[string]string)
	out, listErr := runPBSManager(ctx, "datastore", "list", "--output-format=json")
	if listErr != nil {
		// The current PBS state is unknown. A strict Clean 1:1 restore cannot safely prune
		// stale datastores without it, so fail closed rather than silently report an
		// incomplete clean (P-08). Non-strict logs and continues: it never prunes anyway,
		// and the create/update loop below self-heals an already-existing datastore.
		if strict {
			return fmt.Errorf("datastore list failed, cannot enforce Clean 1:1: %w", listErr)
		}
		logger.Warning("PBS API apply: datastore list failed (%v); create/update will proceed, stale datastores are not pruned", listErr)
	} else {
		var rows []dsRow
		if err := json.Unmarshal(unwrapPBSJSONData(out), &rows); err != nil {
			if strict {
				return fmt.Errorf("datastore list returned unparseable JSON, cannot enforce Clean 1:1: %w", err)
			}
			logger.Warning("PBS API apply: datastore list JSON parse failed (%v); stale datastores are not pruned", err)
		} else {
			for _, row := range rows {
				name := strings.TrimSpace(row.Name)
				if name == "" {
					name = strings.TrimSpace(row.Store)
				}
				if name == "" {
					name = strings.TrimSpace(row.ID)
				}
				if name == "" {
					continue
				}
				currentPaths[name] = strings.TrimSpace(row.Path)
			}
		}
	}

	var removeFailures []string
	if strict {
		current := make([]string, 0, len(currentPaths))
		for name := range currentPaths {
			current = append(current, name)
		}
		sort.Strings(current)
		for _, name := range current {
			if _, ok := desired[name]; ok {
				continue
			}
			if _, err := runPBSManager(ctx, "datastore", "remove", name); err != nil {
				logger.Warning("PBS API apply: datastore remove %s failed (continuing): %v", name, err)
				removeFailures = append(removeFailures, name)
			}
		}
	}
	// keepCleanIncomplete keeps a refused remove reachable through errors.Is on an
	// error that ends the apply early, without changing its text.
	keepCleanIncomplete := func(err error) error {
		if removeErr := pbsCleanRemoveResult("datastore", removeFailures); removeErr != nil {
			return &pbsCleanIncompleteApplyError{err: err, remove: removeErr}
		}
		return err
	}

	names := make([]string, 0, len(desired))
	for name := range desired {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := desired[name]
		path, entries, ok := popEntryValue(s.Entries, "path")
		if !ok || strings.TrimSpace(path) == "" {
			logger.Warning("PBS API apply: datastore %s missing path; skipping", name)
			continue
		}
		flags := buildProxmoxManagerFlags(entries)
		if currentPath, exists := currentPaths[name]; exists {
			if currentPath != "" && strings.TrimSpace(currentPath) != strings.TrimSpace(path) {
				if strict {
					if _, err := runPBSManager(ctx, "datastore", "remove", name); err != nil {
						return keepCleanIncomplete(fmt.Errorf("datastore %s: path mismatch (%s != %s) and remove failed: %w", name, currentPath, path, err))
					}
					createArgs := append([]string{"datastore", "create", name, path}, flags...)
					if _, err := runPBSManager(ctx, createArgs...); err != nil {
						// The datastore was removed but the create at the new path failed.
						// Re-create it at its original path so it is not left deconfigured
						// (remove has no --destroy-data, so the chunks are still on disk).
						rollbackArgs := append([]string{"datastore", "create", name, currentPath}, flags...)
						if _, rbErr := runPBSManager(ctx, rollbackArgs...); rbErr != nil {
							return keepCleanIncomplete(fmt.Errorf("datastore %s: path change to %s failed (%v) and rollback re-create at original path %s also failed (%v); the datastore is left removed, its data is still on disk and it must be re-created manually", name, path, err, currentPath, rbErr))
						}
						return keepCleanIncomplete(fmt.Errorf("datastore %s: path change to %s failed (%w); the datastore was restored to its original path %s", name, path, err, currentPath))
					}
					continue
				}
				logger.Warning("PBS API apply: datastore %s path mismatch (%s != %s); leaving path unchanged (use Clean 1:1 restore to enforce 1:1)", name, currentPath, path)
			}

			updateArgs := append([]string{"datastore", "update", name}, flags...)
			if _, err := runPBSManager(ctx, updateArgs...); err != nil {
				return keepCleanIncomplete(fmt.Errorf("datastore %s: update failed: %w", name, err))
			}
			continue
		}

		createArgs := append([]string{"datastore", "create", name, path}, flags...)
		if _, err := runPBSManager(ctx, createArgs...); err != nil {
			updateArgs := append([]string{"datastore", "update", name}, flags...)
			if _, upErr := runPBSManager(ctx, updateArgs...); upErr != nil {
				return keepCleanIncomplete(fmt.Errorf("datastore %s: %v (create) / %v (update)", name, err, upErr))
			}
		}
	}

	return pbsCleanRemoveResult("datastore", removeFailures)
}

func applyPBSSyncCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	return applyPBSSectionCfgViaAPI(ctx, logger, stageRoot, strict, pbsSectionCfg{
		file:     "etc/proxmox-backup/sync.cfg",
		command:  []string{"sync-job"},
		listKeys: []string{"id", "name"},
	})
}

func applyPBSVerificationCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	return applyPBSSectionCfgViaAPI(ctx, logger, stageRoot, strict, pbsSectionCfg{
		file:     "etc/proxmox-backup/verification.cfg",
		command:  []string{"verify-job"},
		listKeys: []string{"id", "name"},
	})
}

func applyPBSPruneCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	return applyPBSSectionCfgViaAPI(ctx, logger, stageRoot, strict, pbsSectionCfg{
		file:     "etc/proxmox-backup/prune.cfg",
		command:  []string{"prune-job"},
		listKeys: []string{"id", "name"},
	})
}

func applyPBSTrafficControlCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	return applyPBSSectionCfgViaAPI(ctx, logger, stageRoot, strict, pbsSectionCfg{
		file:     "etc/proxmox-backup/traffic-control.cfg",
		command:  []string{"traffic-control"},
		listKeys: []string{"name", "id"},
	})
}

func applyPBSNodeCfgViaAPI(ctx context.Context, stageRoot string) error {
	raw, present, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/node.cfg")
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	sections, err := parseProxmoxNotificationSections(raw)
	if err != nil {
		return fmt.Errorf("parse staged node.cfg: %w", err)
	}
	if len(sections) == 0 {
		return nil
	}
	// node update applies to the local node; use the first section.
	flags := buildProxmoxManagerFlags(sections[0].Entries)
	args := append([]string{"node", "update"}, flags...)
	if _, err := runPBSManager(ctx, args...); err != nil {
		return err
	}
	return nil
}
