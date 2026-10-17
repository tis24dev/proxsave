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
	"github.com/tis24dev/proxsave/internal/ui/theme"
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

// pbsDatastoreRestoreIncompleteError is a datastore.cfg apply in which one or more
// datastores were not applied, or in Clean (1:1) mode a stale datastore was not
// removed. applyPBSDatastoreCfgViaAPI has already logged the outcome of every
// datastore and of the whole block, so the caller does not log it again and does
// not run the file-based fallback over outcomes it has already reported. When a
// stale datastore was not removed it also matches errPBSCleanRemoveIncomplete.
type pbsDatastoreRestoreIncompleteError struct {
	notApplied []string
	notRemoved []string
}

func (e *pbsDatastoreRestoreIncompleteError) Error() string {
	var parts []string
	if len(e.notApplied) > 0 {
		parts = append(parts, fmt.Sprintf("%d datastore(s) not applied [%s]", len(e.notApplied), strings.Join(e.notApplied, ", ")))
	}
	if len(e.notRemoved) > 0 {
		parts = append(parts, fmt.Sprintf("%d stale datastore(s) not removed [%s]", len(e.notRemoved), strings.Join(e.notRemoved, ", ")))
	}
	return "PBS datastore configuration incomplete: " + strings.Join(parts, "; ")
}

func (e *pbsDatastoreRestoreIncompleteError) Unwrap() error {
	if len(e.notRemoved) > 0 {
		return errPBSCleanRemoveIncomplete
	}
	return nil
}

// pbsDatastoreRestore carries the state of one datastore.cfg apply: what it
// attempted and what it could not do, for the per-datastore and block outcomes.
type pbsDatastoreRestore struct {
	ctx        context.Context
	logger     *logging.Logger
	strict     bool
	stageRoot  string
	attempted  int
	notApplied []string
	notRemoved []string
	// currentUnknown is set when the datastores configured on this PBS could not
	// be read (Merge mode goes on with create/update).
	currentUnknown bool
}

const pbsDatastoreRestoreDebug = "PBS datastore restore: "

func (r *pbsDatastoreRestore) debug(format string, args ...any) {
	r.logger.Debug(pbsDatastoreRestoreDebug+format, args...)
}

// pbsManagerErrorText is the failure of a proxmox-backup-manager call without the
// command line runPBSManager puts in front of it, which the DEBUG line already names.
func pbsManagerErrorText(err error) string {
	if inner := errors.Unwrap(err); inner != nil {
		return inner.Error()
	}
	return err.Error()
}

// pbsManagerErrorLine is the content of the "Error: " line proxmox-backup-manager
// writes on a failure, without the usage text it may print after it. A failure
// without such a line (the command could not run at all) gives the first line of
// its text instead.
func pbsManagerErrorLine(err error) string {
	text := pbsManagerErrorText(err)
	for _, line := range strings.Split(text, "\n") {
		if idx := strings.Index(line, "Error: "); idx >= 0 {
			return strings.TrimSpace(line[idx+len("Error: "):])
		}
	}
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(first)
}

func pbsDatastorePathOrNone(path string) string {
	if strings.TrimSpace(path) == "" {
		return "none"
	}
	return path
}

func (r *pbsDatastoreRestore) applied(name string) {
	r.logger.Info("%s PBS datastore %s: applied", theme.SymbolSuccess, name)
}

func (r *pbsDatastoreRestore) failed(name, event string, args ...any) {
	r.logger.Info(event, args...)
	r.logger.Warning("%s PBS datastore %s: not applied", theme.SymbolWarning, name)
	r.notApplied = append(r.notApplied, name)
}

// removeStale removes, in Clean (1:1) mode, a datastore configured on this PBS that
// the backup does not have. PBS removes the jobs of the datastore with it.
func (r *pbsDatastoreRestore) removeStale(name string) {
	r.attempted++
	r.debug("datastore=%s not in backup action=remove", name)
	if _, err := runPBSManager(r.ctx, "datastore", "remove", name); err != nil {
		r.debug("datastore=%s remove failed error=%q", name, pbsManagerErrorText(err))
		r.logger.Info("Datastore %s is configured on this PBS but not present in the backup", name)
		r.logger.Warning("%s PBS datastore %s: not removed", theme.SymbolWarning, name)
		r.notRemoved = append(r.notRemoved, name)
		return
	}
	r.debug("datastore=%s removed", name)
	r.logger.Info("Datastore %s is configured on this PBS but not present in the backup", name)
	r.logger.Info("%s PBS datastore %s: removed with its jobs", theme.SymbolSuccess, name)
}

// update applies the backup's options to a datastore that already exists on this
// PBS, keeping its path. createErr is the refused create that led here, if any:
// when the update is refused too, the event carries the create's error.
func (r *pbsDatastoreRestore) update(name string, flags []string, keptPathEvent func(), createErr error) {
	updateArgs := append([]string{"datastore", "update", name}, flags...)
	if _, err := runPBSManager(r.ctx, updateArgs...); err != nil {
		r.debug("datastore=%s update failed error=%q", name, pbsManagerErrorText(err))
		rejected := err
		if createErr != nil {
			rejected = createErr
		}
		r.failed(name, "Datastore %s was rejected by PBS: \"%s\"", name, pbsManagerErrorLine(rejected))
		return
	}
	r.debug("datastore=%s updated", name)
	if keptPathEvent != nil {
		keptPathEvent()
	}
	r.applied(name)
}

// create creates a datastore this PBS does not list; when PBS refuses the create
// (it may exist after all, e.g. when the list could not be read) the datastore is
// updated instead.
func (r *pbsDatastoreRestore) create(name, path string, flags []string) {
	createArgs := append([]string{"datastore", "create", name, path}, flags...)
	if _, err := runPBSManager(r.ctx, createArgs...); err != nil {
		r.debug("datastore=%s create failed error=%q action=update", name, pbsManagerErrorText(err))
		r.update(name, flags, nil, err)
		return
	}
	r.debug("datastore=%s created", name)
	r.applied(name)
}

// move re-creates, in Clean (1:1) mode, a datastore whose path on this PBS differs
// from the backup's: remove (without --destroy-data, so the chunks stay on disk),
// then create at the backup's path. When that create fails the datastore is
// re-created at its original path so it is not left deconfigured.
func (r *pbsDatastoreRestore) move(name, currentPath, path string, flags []string) {
	pathEvent := "Datastore %s is at %s on this PBS and at %s in the backup"
	if _, err := runPBSManager(r.ctx, "datastore", "remove", name); err != nil {
		r.debug("datastore=%s remove failed error=%q", name, pbsManagerErrorText(err))
		r.failed(name, pathEvent, name, currentPath, path)
		return
	}
	r.debug("datastore=%s removed", name)
	createArgs := append([]string{"datastore", "create", name, path}, flags...)
	if _, err := runPBSManager(r.ctx, createArgs...); err != nil {
		r.debug("datastore=%s create failed path=%s error=%q action=rollback rollback_path=%s", name, path, pbsManagerErrorText(err), currentPath)
		rollbackArgs := append([]string{"datastore", "create", name, currentPath}, flags...)
		if _, rbErr := runPBSManager(r.ctx, rollbackArgs...); rbErr != nil {
			r.debug("datastore=%s rollback failed path=%s error=%q", name, currentPath, pbsManagerErrorText(rbErr))
			r.logger.Info(pathEvent, name, currentPath, path)
			r.logger.Warning("%s PBS datastore %s: not configured, the data in %s is kept", theme.SymbolWarning, name, currentPath)
			r.notApplied = append(r.notApplied, name)
			return
		}
		r.debug("datastore=%s created path=%s (rollback)", name, currentPath)
		r.failed(name, pathEvent, name, currentPath, path)
		return
	}
	r.debug("datastore=%s created path=%s", name, path)
	r.logger.Info("Datastore %s moved from %s to %s, the data in %s is not moved", name, currentPath, path, currentPath)
	r.applied(name)
}

// apply restores one datastore of the backup.
func (r *pbsDatastoreRestore) apply(name string, s proxmoxNotificationSection, currentPaths map[string]string) {
	r.attempted++
	path, entries, ok := popEntryValue(s.Entries, "path")
	path = strings.TrimSpace(path)
	if !ok || path == "" {
		r.debug("datastore=%s backup_path=none action=skip", name)
		r.failed(name, "Datastore %s has no path in the backup", name)
		return
	}
	flags := buildProxmoxManagerFlags(entries)
	currentPath, exists := currentPaths[name]
	currentPath = strings.TrimSpace(currentPath)
	if !exists {
		unknown := "none"
		if r.currentUnknown {
			unknown = "unknown"
		}
		r.debug("datastore=%s backup_path=%s current_path=%s action=create", name, path, unknown)
		r.create(name, path, flags)
		return
	}
	if currentPath == "" || currentPath == path {
		r.debug("datastore=%s backup_path=%s current_path=%s action=update", name, path, pbsDatastorePathOrNone(currentPath))
		r.update(name, flags, nil, nil)
		return
	}
	if r.strict {
		r.debug("datastore=%s backup_path=%s current_path=%s path differs action=remove,create", name, path, currentPath)
		r.move(name, currentPath, path, flags)
		return
	}
	r.debug("datastore=%s backup_path=%s current_path=%s path differs action=update (merge keeps the current path)", name, path, currentPath)
	r.update(name, flags, func() {
		r.logger.Info("Datastore %s stays at %s, the backup has it at %s", name, currentPath, path)
	}, nil)
}

// outcome logs the outcome of the whole block and returns the error the caller
// records the restore warning from.
func (r *pbsDatastoreRestore) outcome() error {
	if r.attempted == 0 {
		r.logger.Skip("PBS datastore configuration: nothing to restore")
		return nil
	}
	failures := len(r.notApplied) + len(r.notRemoved)
	switch failures {
	case 0:
		r.logger.Info("%s PBS datastore configuration: applied", theme.SymbolSuccess)
		return nil
	case r.attempted:
		r.logger.Warning("%s PBS datastore configuration: not applied", theme.SymbolWarning)
	default:
		r.logger.Warning("%s PBS datastore configuration: partially applied", theme.SymbolWarning)
	}
	return &pbsDatastoreRestoreIncompleteError{notApplied: r.notApplied, notRemoved: r.notRemoved}
}

// pbsDatastoreRestoreNotAppliedError is a datastore.cfg apply that stopped before
// any datastore through the API: the staged datastore.cfg could not be read, or in
// Clean (1:1) mode the datastores configured on this PBS could not be read. In
// Clean mode the block then wrote datastore.cfg from the backup file. The block
// has already logged its outcome, a ⚠ one, so the caller neither logs it again nor
// runs another fallback, and the restore ends with warnings.
type pbsDatastoreRestoreNotAppliedError struct {
	err error
}

func (e *pbsDatastoreRestoreNotAppliedError) Error() string { return e.err.Error() }

func (e *pbsDatastoreRestoreNotAppliedError) Unwrap() error { return e.err }

const (
	pbsDatastoreEventBackupUnreadable  = "The datastore configuration in the backup could not be read"
	pbsDatastoreEventCurrentUnreadable = "The datastores configured on this PBS could not be read"
	pbsDatastoreEventAPIUnavailable    = "The PBS API is not available"
)

// stopped logs a block that stopped before any datastore. In Clean (1:1) mode the
// datastore.cfg of the backup is then written as a file, as before the block
// existed; in Merge mode the configuration is not applied.
func (r *pbsDatastoreRestore) stopped(event string, err error) error {
	if r.strict {
		return r.fileFallback(event, err)
	}
	r.logger.Info("%s", event)
	return r.configNotApplied(err)
}

func (r *pbsDatastoreRestore) configNotApplied(err error) error {
	r.logger.Warning("%s PBS datastore configuration: not applied", theme.SymbolWarning)
	return &pbsDatastoreRestoreNotAppliedError{err: err}
}

// fileFallback writes /etc/proxmox-backup/datastore.cfg from the staged file and
// logs, in the form of the block, what it wrote and the block outcome. event is
// the block's own event; when the staged file cannot be read it carries the error.
func (r *pbsDatastoreRestore) fileFallback(event string, cause error) error {
	// The event of an unreadable backup file waits for the fallback's own read,
	// so that it can carry the error; any other event comes first.
	eventAfterRead := event == pbsDatastoreEventBackupUnreadable
	if !eventAfterRead {
		r.logger.Info("%s", event)
	}
	r.debug("fallback=file target=%s source=%s", "/etc/proxmox-backup/datastore.cfg", filepath.Join(r.stageRoot, "etc/proxmox-backup/datastore.cfg"))
	res, err := runPBSDatastoreCfgFile(r.stageRoot)
	if err != nil && res.readFailed {
		r.debug("fallback read failed error=%q", err.Error())
		r.logger.Info("%s: \"%s\"", pbsDatastoreEventBackupUnreadable, err.Error())
		return r.configNotApplied(cause)
	}
	if eventAfterRead {
		r.logger.Info("%s", event)
	}
	if res.currentErr != nil {
		r.debug("fallback current file=%s read failed error=%q", res.destPath, res.currentErr.Error())
	} else {
		r.debug("fallback current file=%s datastores=%d names=%s", res.destPath, len(res.current), pbsBlockNames(res.current))
	}
	if res.fixed > 0 {
		r.debug("fallback normalized malformed lines=%d (properties must be indented)", res.fixed)
	}
	if res.invalidReason != "" {
		r.debug("fallback staged file invalid reason=%q action=rebuild source=pbs_datastore_inventory.json", res.invalidReason)
		if res.recoveryFailed != "" {
			r.debug("fallback rebuild failed error=%q", res.recoveryFailed)
			r.logger.Info("The datastore configuration in the backup is damaged and could not be rebuilt")
			return r.configNotApplied(cause)
		}
		if res.recoveredFixed > 0 {
			r.debug("fallback rebuilt file normalized malformed lines=%d", res.recoveredFixed)
		}
		r.debug("fallback rebuilt from=%s", res.recoveredFrom)
		r.logger.Info("The datastore configuration in the backup is damaged, it is rebuilt from the datastore inventory")
	}
	switch {
	case err != nil:
		r.debug("fallback write failed error=%q", err.Error())
		r.logger.Info("The datastore configuration could not be written to this PBS: \"%s\"", err.Error())
		return r.configNotApplied(cause)
	case res.missing:
		r.debug("fallback nothing to write")
		r.logger.Skip("PBS datastore configuration: nothing to restore")
		return nil
	case res.emptied:
		r.debug("fallback staged file has no datastores comments_only=%v, target removed", res.commentsOnly)
		r.fileRemoved(res)
		r.logger.Warning("%s PBS datastore configuration: applied from the backup file", theme.SymbolWarning)
		return &pbsDatastoreRestoreNotAppliedError{err: cause}
	}

	r.fileRemoved(res)
	for _, it := range res.items {
		path := filepath.Clean(strings.TrimSpace(it.path))
		switch {
		case it.deferReason != "":
			r.debug("datastore=%s path=%s deferred reason=%q", it.name, path, it.deferReason)
			if it.keptCurrent {
				r.debug("datastore=%s kept current definition in file", it.name)
			}
			if res.deferredErr != nil {
				r.debug("datastore=%s deferred definition not saved error=%q", it.name, res.deferredErr.Error())
				r.logger.Info("Datastore %s is not safe to apply now, its definition could not be saved: \"%s\"", it.name, res.deferredErr.Error())
			} else {
				r.logger.Info("Datastore %s is not safe to apply now, its definition is saved to %s", it.name, res.deferredPath)
			}
			r.logger.Warning("%s PBS datastore %s: not applied", theme.SymbolWarning, it.name)
		case it.onRootFS:
			r.debug("datastore=%s path=%s on root filesystem", it.name, path)
			r.logger.Info("Datastore %s path %s is on the root filesystem", it.name, path)
			r.logger.Warning("%s PBS datastore %s: applied without its mount", theme.SymbolWarning, it.name)
		case it.preflight != "":
			r.debug("datastore=%s preflight path=%s result=%s", it.name, path, it.preflight)
			r.logger.Info("Datastore %s path %s: %s", it.name, path, it.preflight)
			r.logger.Warning("%s PBS datastore %s: applied with a problem on its path", theme.SymbolWarning, it.name)
		default:
			r.applied(it.name)
		}
	}
	r.debug("fallback written datastores=%d deferred=%d kept_current=%d", res.written, res.deferredCount(), res.keptCurrentCount())
	if res.noSafe {
		r.logger.Info("The backup has no datastore that is safe to apply, the current configuration is kept")
		return r.configNotApplied(cause)
	}
	r.logger.Warning("%s PBS datastore configuration: applied from the backup file", theme.SymbolWarning)
	return &pbsDatastoreRestoreNotAppliedError{err: cause}
}

// fileRemoved logs the datastores of this PBS that the file fallback took out of
// datastore.cfg because the backup does not have them, with the jobs and
// permissions removed with them.
func (r *pbsDatastoreRestore) fileRemoved(res pbsDatastoreFileResult) {
	for _, name := range res.removed {
		r.debug("datastore=%s not in backup action=drop from file", name)
		c := res.cleanup[name]
		if c == nil {
			c = &pbsDatastoreCleanup{}
		}
		for _, job := range c.jobs {
			r.debug("datastore=%s job=%s removed from %s", name, job.id, job.file)
		}
		if c.acl > 0 {
			r.debug("datastore=%s acl entries removed=%d from %s", name, c.acl, pbsACLCfgPath)
		}
		r.logger.Info("Datastore %s is configured on this PBS but not present in the backup", name)
		if c.err != nil {
			r.debug("datastore=%s jobs file=%s write failed error=%q", name, c.errFile, c.err.Error())
			r.logger.Info("The jobs of datastore %s could not all be removed: \"%s\"", name, c.err.Error())
			r.logger.Warning("%s PBS datastore %s: removed, some of its jobs are kept", theme.SymbolWarning, name)
			continue
		}
		r.logger.Info("%s PBS datastore %s: removed with its jobs", theme.SymbolSuccess, name)
	}
}

func pbsBlockNames(blocks []pbsDatastoreBlock) string {
	names := make([]string, 0, len(blocks))
	for _, b := range blocks {
		names = append(names, b.Name)
	}
	return strings.Join(names, ",")
}

// readCurrent reads the datastores configured on this PBS, by name, with their
// paths, or the error that kept them from being read.
func (r *pbsDatastoreRestore) readCurrent() (currentPaths map[string]string, err error) {
	type dsRow struct {
		Name  string `json:"name"`
		Store string `json:"store"`
		ID    string `json:"id"`
		Path  string `json:"path"`
	}
	r.debug("reading current datastores cmd=%q", "proxmox-backup-manager datastore list --output-format=json")
	out, err := runPBSManager(r.ctx, "datastore", "list", "--output-format=json")
	if err != nil {
		r.debug("datastore list failed error=%q", pbsManagerErrorText(err))
		return nil, fmt.Errorf("datastore list failed: %w", err)
	}
	var rows []dsRow
	if err := json.Unmarshal(unwrapPBSJSONData(out), &rows); err != nil {
		r.debug("datastore list returned unparseable JSON error=%q", err.Error())
		return nil, fmt.Errorf("datastore list returned unparseable JSON: %w", err)
	}
	currentPaths = make(map[string]string, len(rows))
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
	return currentPaths, nil
}

func pbsSortedNames[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// applyPBSDatastoreCfgFileBlock is the datastore block written from the backup
// file, in Clean (1:1) mode, when the PBS API cannot apply it: the datastores to
// restore, the event, then datastore.cfg written by the same fallback, with the
// same lines. Like the fallback it returns a pbsDatastoreRestoreNotAppliedError
// whenever its outcome is a ⚠ one, already logged.
func applyPBSDatastoreCfgFileBlock(ctx context.Context, logger *logging.Logger, stageRoot, event string, cause error) error {
	r, _, _, done, err := beginPBSDatastoreRestore(ctx, logger, stageRoot, true)
	if done {
		return err
	}
	return r.fileFallback(event, cause)
}

// beginPBSDatastoreRestore opens the datastore block: it reads the staged
// datastore.cfg and logs the datastores to restore. done is true when the block
// has already ended, with err its result: nothing is staged, or the staged file
// could not be read.
func beginPBSDatastoreRestore(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) (r *pbsDatastoreRestore, desired map[string]proxmoxNotificationSection, names []string, done bool, err error) {
	const stagedRel = "etc/proxmox-backup/datastore.cfg"
	stagedPath := filepath.Join(stageRoot, stagedRel)
	dsRaw, present, readErr := readStageFileOptional(stageRoot, stagedRel)
	if readErr == nil && !present {
		logger.Debug(pbsDatastoreRestoreDebug+"staged=%s not in the backup action=skip", stagedPath)
		return nil, nil, nil, true, nil
	}

	r = &pbsDatastoreRestore{ctx: ctx, logger: logger, strict: strict, stageRoot: stageRoot}
	mode := "merge"
	if strict {
		mode = "clean"
	}
	logger.Info("Restoring PBS datastore configuration...")
	r.debug("mode=%s staged=%s", mode, stagedPath)

	var sections []proxmoxNotificationSection
	if readErr == nil {
		var parseErr error
		if sections, parseErr = parseProxmoxNotificationSections(dsRaw); parseErr != nil {
			r.debug("staged=%s parse failed error=%q", stagedPath, parseErr.Error())
			return r, nil, nil, true, r.stopped(pbsDatastoreEventBackupUnreadable, fmt.Errorf("parse staged datastore.cfg: %w", parseErr))
		}
	} else {
		r.debug("staged=%s read failed error=%q", stagedPath, readErr.Error())
		return r, nil, nil, true, r.stopped(pbsDatastoreEventBackupUnreadable, readErr)
	}

	desired = make(map[string]proxmoxNotificationSection, len(sections))
	for _, s := range sections {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		desired[name] = s
	}
	names = pbsSortedNames(desired)
	r.debug("backup datastores=%d names=%s", len(names), strings.Join(names, ","))
	logger.Info("  Datastores to restore: %d", len(names))
	if len(names) > 0 {
		logger.Info("  Names: %s", strings.Join(names, ", "))
	}
	return r, desired, names, false, nil
}

func applyPBSDatastoreCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	r, desired, names, done, err := beginPBSDatastoreRestore(ctx, logger, stageRoot, strict)
	if done {
		return err
	}

	currentPaths, listErr := r.readCurrent()
	if listErr != nil {
		// The current PBS state is unknown. A strict Clean 1:1 restore cannot safely prune
		// stale datastores without it, so it stops rather than report an incomplete
		// clean (P-08). Merge never prunes, and create/update self-heals a datastore
		// that exists after all.
		if strict {
			return r.stopped(pbsDatastoreEventCurrentUnreadable, fmt.Errorf("%w, cannot enforce Clean 1:1", listErr))
		}
		logger.Info("%s", pbsDatastoreEventCurrentUnreadable)
		r.currentUnknown = true
		currentPaths = map[string]string{}
	}

	current := pbsSortedNames(currentPaths)
	if listErr == nil {
		pairs := make([]string, 0, len(current)+1)
		pairs = append(pairs, fmt.Sprintf("current datastores=%d", len(current)))
		for _, name := range current {
			pairs = append(pairs, name+"="+pbsDatastorePathOrNone(currentPaths[name]))
		}
		r.debug("%s", strings.Join(pairs, " "))
	}
	for _, name := range current {
		if _, ok := desired[name]; ok {
			continue
		}
		if !strict {
			r.debug("datastore=%s not in backup action=keep (merge)", name)
			continue
		}
		r.removeStale(name)
	}

	for _, name := range names {
		r.apply(name, desired[name], currentPaths)
	}
	return r.outcome()
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
