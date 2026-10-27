package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

var pbsAPIApplyGeteuid = os.Geteuid

func normalizeProxmoxCfgKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "_", "-")
	return key
}

func buildProxmoxManagerFlags(entries []proxmoxNotificationEntry, skipKeys ...string) []string {
	if len(entries) == 0 {
		return nil
	}
	args := make([]string, 0, len(entries)*2)
	for _, kv := range proxmoxManagerFlagEntries(entries, skipKeys...) {
		args = append(args, proxmoxManagerFlag(kv.Key, kv.Value)...)
	}
	return args
}

// proxmoxManagerFlagEntries returns the entries that become flags, the key
// normalized and the value trimmed, without the skipped and no-op keys.
func proxmoxManagerFlagEntries(entries []proxmoxNotificationEntry, skipKeys ...string) []proxmoxNotificationEntry {
	skip := make(map[string]struct{}, len(skipKeys)+2)
	for _, k := range skipKeys {
		skip[normalizeProxmoxCfgKey(k)] = struct{}{}
	}
	// Common no-op keys
	skip["digest"] = struct{}{}
	skip["name"] = struct{}{}

	out := make([]proxmoxNotificationEntry, 0, len(entries))
	for _, kv := range entries {
		key := normalizeProxmoxCfgKey(kv.Key)
		if key == "" {
			continue
		}
		if _, ok := skip[key]; ok {
			continue
		}
		out = append(out, proxmoxNotificationEntry{Key: key, Value: strings.TrimSpace(kv.Value)})
	}
	return out
}

// proxmoxManagerFlag returns the arguments that pass value to the flag named key.
// A value that begins with '-' goes as one "--key=value" argument:
// proxmox-backup-manager reads a separate argument that begins with '-' as an
// option, refuses the command and names the value in its refusal ("'S3cret':
// missing parameter value.", measured on PBS 4.2.8).
func proxmoxManagerFlag(key, value string) []string {
	if strings.HasPrefix(value, "-") {
		return []string{"--" + key + "=" + value}
	}
	return []string{"--" + key, value}
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
			slices.Sort(available)
			return nil, fmt.Errorf("failed to parse PBS list row %d: none of %v present as non-empty string (available keys: %v)", idx, keys, available)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	slices.Sort(out)
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
// they are tried), the flags whose value a failed create/update must not show, the
// keys whose value the file stores base64-encoded while create/update take it
// plain, so it is decoded before it becomes a flag, the words its restore block
// shows, and the apply that writes the staged file as it is when the API cannot
// (nil: applyPBSConfigFileFromStage of file).
type pbsSectionCfg struct {
	file         string
	command      []string
	listKeys     []string
	secretFlags  []string
	base64Values []string
	labels       pbsSectionLabels
	fileFallback func(ctx context.Context, logger *logging.Logger, stageRoot string) error
	// keepSyncJobRemotes: the file fallback keeps an object of this PBS that a
	// sync job uses (remote.cfg), as PBS refuses to remove it.
	keepSyncJobRemotes bool
	// createVerb and updateVerb replace "create" and "update" ("add" and "set"
	// for ACME plugins); createWithType puts the section type in front of the id
	// on create.
	createVerb     string
	updateVerb     string
	createWithType bool
	// dataFileFlag is the flag whose value create takes as the path of a file
	// holding it decoded from the staged base64, while update takes it in
	// standard base64 (ACME plugin --data).
	dataFileFlag string
	// builtin are the objects PBS provides itself: never applied, removed,
	// counted or named.
	builtin []string
}

func (c pbsSectionCfg) isBuiltin(id string) bool {
	return slices.Contains(c.builtin, id)
}

func (c pbsSectionCfg) verb(verb, override string) string {
	if override != "" {
		return override
	}
	return verb
}

// writeFromStage writes this config's staged file, and only that file, to this PBS.
func (c pbsSectionCfg) writeFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) error {
	if c.fileFallback != nil {
		return c.fileFallback(ctx, logger, stageRoot)
	}
	return applyPBSConfigFileFromStage(ctx, logger, stageRoot, c.file)
}

// pbsSectionLabels are the words of the restore block of one section config, the
// only part of the block that differs from one config to another.
type pbsSectionLabels struct {
	// plural opens the block: "Restoring PBS <plural>...".
	plural string
	// countNoun counts the objects of the backup: "  <countNoun> to restore: N".
	countNoun string
	// noun is the subject of the outcome of one object, "PBS <noun> <id>", and
	// names the block in its DEBUG lines.
	noun string
	// eventNoun is the subject of the event of one object: "<eventNoun> <id> ...".
	eventNoun string
	// outcome is the subject of the outcome of the whole block.
	outcome string
}

var (
	pbsRemoteLabels = pbsSectionLabels{
		plural:    "remotes",
		countNoun: "Remotes",
		noun:      "remote",
		eventNoun: "Remote",
		outcome:   "PBS remote configuration",
	}
	pbsS3EndpointLabels = pbsSectionLabels{
		plural:    "S3 endpoints",
		countNoun: "S3 endpoints",
		noun:      "S3 endpoint",
		eventNoun: "S3 endpoint",
		outcome:   "PBS S3 endpoint configuration",
	}
	pbsSyncJobLabels = pbsSectionLabels{
		plural:    "sync jobs",
		countNoun: "Sync jobs",
		noun:      "sync job",
		eventNoun: "Sync job",
		outcome:   "PBS sync job configuration",
	}
	pbsVerifyJobLabels = pbsSectionLabels{
		plural:    "verify jobs",
		countNoun: "Verify jobs",
		noun:      "verify job",
		eventNoun: "Verify job",
		outcome:   "PBS verify job configuration",
	}
	pbsPruneJobLabels = pbsSectionLabels{
		plural:    "prune jobs",
		countNoun: "Prune jobs",
		noun:      "prune job",
		eventNoun: "Prune job",
		outcome:   "PBS prune job configuration",
	}
	pbsAcmePluginLabels = pbsSectionLabels{
		plural:    "ACME plugins",
		countNoun: "ACME plugins",
		noun:      "ACME plugin",
		eventNoun: "ACME plugin",
		outcome:   "PBS ACME plugin configuration",
	}
	pbsTrafficControlLabels = pbsSectionLabels{
		plural:    "traffic control rules",
		countNoun: "Traffic control rules",
		noun:      "traffic control rule",
		eventNoun: "Traffic control rule",
		outcome:   "PBS traffic control configuration",
	}
)

// pbsSectionFlags builds the create/update flags of one section with
// buildProxmoxManagerFlags, entry by entry, and decodes from standard base64 the value
// of every key named in base64Keys. The staged value is trimmed before it is decoded,
// the decoded value is passed as it is, so a password that begins or ends with a space
// keeps it. A value that is not valid base64 is passed as staged. One DEBUG line of
// the block, through debug, says whether each such value was decoded, naming the
// object as key=id and the setting, never the value.
func pbsSectionFlags(debug func(format string, args ...any), key, id string, entries []proxmoxNotificationEntry, base64Keys []string) []string {
	if len(base64Keys) == 0 {
		return buildProxmoxManagerFlags(entries)
	}
	decode := make(map[string]struct{}, len(base64Keys))
	for _, k := range base64Keys {
		decode[normalizeProxmoxCfgKey(k)] = struct{}{}
	}
	args := make([]string, 0, len(entries)*2)
	for _, kv := range proxmoxManagerFlagEntries(entries) {
		value := kv.Value
		if _, ok := decode[kv.Key]; ok {
			decoded, err := base64.StdEncoding.DecodeString(value)
			if err != nil {
				debug("%s=%s %s is not valid base64, passing the staged value unchanged", key, id, kv.Key)
			} else {
				debug("%s=%s %s decoded from base64", key, id, kv.Key)
				value = string(decoded)
			}
		}
		args = append(args, proxmoxManagerFlag(kv.Key, value)...)
	}
	return args
}

// pbsSectionRestoreIncompleteError is a section-config apply in which one or more
// objects were not applied, or in Clean (1:1) mode a stale object was not removed.
// applyPBSSectionCfgViaAPI has already logged the outcome of every object and of
// the whole block, so the caller does not log it again and does not run the
// file-based fallback over outcomes it has already reported (which would rewrite
// the .cfg and drop a stale object PBS refused to remove, e.g. one in use).
type pbsSectionRestoreIncompleteError struct {
	labels     pbsSectionLabels
	notApplied []string
	notRemoved []string
}

func (e *pbsSectionRestoreIncompleteError) Error() string {
	var parts []string
	if len(e.notApplied) > 0 {
		parts = append(parts, fmt.Sprintf("%d %s(s) not applied [%s]", len(e.notApplied), e.labels.noun, strings.Join(e.notApplied, ", ")))
	}
	if len(e.notRemoved) > 0 {
		parts = append(parts, fmt.Sprintf("%d stale %s(s) not removed [%s]", len(e.notRemoved), e.labels.noun, strings.Join(e.notRemoved, ", ")))
	}
	return e.labels.outcome + " incomplete: " + strings.Join(parts, "; ")
}

// pbsSectionRestoreStoppedError is a section-config apply that stopped before any
// object went through the API: the staged file could not be read, or in Clean
// (1:1) mode the objects configured on this PBS could not be read and the block
// wrote the staged file instead. The block has already logged its outcome, a ⚠
// one, so the caller neither logs it again nor runs another fallback, and the
// restore ends with warnings.
type pbsSectionRestoreStoppedError struct {
	err error
}

func (e *pbsSectionRestoreStoppedError) Error() string { return e.err.Error() }

func (e *pbsSectionRestoreStoppedError) Unwrap() error { return e.err }

// pbsSectionOutcomeLogged reports whether err is the result of a section block
// that has already logged its outcome.
func pbsSectionOutcomeLogged(err error) bool {
	var incomplete *pbsSectionRestoreIncompleteError
	var stopped *pbsSectionRestoreStoppedError
	return errors.As(err, &incomplete) || errors.As(err, &stopped)
}

// pbsSectionRestore carries the state of one section-config apply: what it
// attempted and what it could not do, for the per-object and block outcomes.
type pbsSectionRestore struct {
	ctx        context.Context
	logger     *logging.Logger
	cfg        pbsSectionCfg
	attempted  int
	notApplied []string
	notRemoved []string
}

func (r *pbsSectionRestore) debug(format string, args ...any) {
	r.logger.Debug("PBS "+r.cfg.labels.noun+" restore: "+format, args...)
}

// key names an object of the block in its DEBUG lines ("remote=r1"), keys counts
// them ("backup remotes=2").
func (r *pbsSectionRestore) key() string {
	return strings.ReplaceAll(strings.ToLower(r.cfg.labels.noun), " ", "-")
}

func (r *pbsSectionRestore) keys() string { return r.key() + "s" }

func (r *pbsSectionRestore) commandArgs(rest ...string) []string {
	return append(append([]string(nil), r.cfg.command...), rest...)
}

func (r *pbsSectionRestore) applied(id string) {
	r.logger.Info("%s PBS %s %s: applied", theme.SymbolSuccess, r.cfg.labels.noun, id)
}

// removeStale removes, in Clean (1:1) mode, an object configured on this PBS that
// the backup does not have. It returns only a cancellation.
func (r *pbsSectionRestore) removeStale(id string) error {
	r.attempted++
	r.debug("%s=%s not in backup action=remove", r.key(), id)
	if _, err := runPBSManager(r.ctx, r.commandArgs("remove", id)...); err != nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		r.debug("%s=%s remove failed error=%q", r.key(), id, pbsManagerErrorText(err))
		r.logger.Info("%s %s is configured on this PBS but not present in the backup", r.cfg.labels.eventNoun, id)
		r.logger.Warning("%s PBS %s %s: not removed", theme.SymbolWarning, r.cfg.labels.noun, id)
		r.notRemoved = append(r.notRemoved, id)
		return nil
	}
	r.debug("%s=%s removed", r.key(), id)
	r.logger.Info("%s %s is configured on this PBS but not present in the backup", r.cfg.labels.eventNoun, id)
	r.logger.Info("%s PBS %s %s: removed", theme.SymbolSuccess, r.cfg.labels.noun, id)
	return nil
}

// pbsDecodeStoredBase64 decodes a value PBS stores base64-encoded, in the standard
// or the URL-safe alphabet, with or without the padding: PBS 4.2.8 writes the ACME
// plugin data as base64url without it ("CF_Token=ab?\n" is stored as
// "Q0ZfVG9rZW49YWI_Cg").
func pbsDecodeStoredBase64(value string) ([]byte, error) {
	if data, err := base64.StdEncoding.DecodeString(value); err == nil {
		return data, nil
	}
	trimmed := strings.TrimRight(value, "=")
	if data, err := base64.RawURLEncoding.DecodeString(trimmed); err == nil {
		return data, nil
	}
	return base64.RawStdEncoding.DecodeString(trimmed)
}

// updateData returns the update flags of an object whose dataFileFlag value
// update takes as standard base64, which "acme plugin set" requires: the staged
// value is decoded and encoded again in the standard alphabet. A value that is
// not valid base64 is passed as staged (dataFile has said so at DEBUG).
func (r *pbsSectionRestore) updateData(flags []string) []string {
	if r.cfg.dataFileFlag == "" {
		return flags
	}
	flag := "--" + r.cfg.dataFileFlag
	out := append([]string(nil), flags...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] != flag {
			continue
		}
		if data, err := pbsDecodeStoredBase64(out[i+1]); err == nil {
			out[i+1] = base64.StdEncoding.EncodeToString(data)
		}
		return out
	}
	return out
}

// dataFile returns the create flags of an object whose dataFileFlag value create
// takes as a file: the staged base64 is decoded into a 0600 file in a 0700
// directory made for the call, and the flag carries its path. cleanup removes
// the directory; it is safe to call in every case. Neither the value nor the
// decoded data is ever logged.
func (r *pbsSectionRestore) dataFile(id string, flags []string) (out []string, cleanup func(), err error) {
	cleanup = func() {}
	if r.cfg.dataFileFlag == "" {
		return flags, cleanup, nil
	}
	flag := "--" + r.cfg.dataFileFlag
	out = append([]string(nil), flags...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] != flag {
			continue
		}
		data, decErr := pbsDecodeStoredBase64(out[i+1])
		if decErr != nil {
			r.debug("%s=%s %s is not valid base64, writing the staged value unchanged", r.key(), id, r.cfg.dataFileFlag)
			data = []byte(out[i+1])
		} else {
			r.debug("%s=%s %s decoded from base64", r.key(), id, r.cfg.dataFileFlag)
		}
		dir, mkErr := os.MkdirTemp("", "proxsave-pbs-data-")
		if mkErr != nil {
			return nil, cleanup, fmt.Errorf("create a directory for the %s data: %w", r.cfg.dataFileFlag, mkErr)
		}
		cleanup = func() { _ = os.RemoveAll(dir) }
		path := filepath.Join(dir, "data")
		if wErr := os.WriteFile(path, data, 0o600); wErr != nil {
			return nil, cleanup, fmt.Errorf("write the %s data: %w", r.cfg.dataFileFlag, wErr)
		}
		out[i+1] = path
		return out, cleanup, nil
	}
	return out, cleanup, nil
}

// apply restores one object of the backup: create, and when PBS refuses the
// create (the object may already exist) update. It returns only a cancellation.
func (r *pbsSectionRestore) apply(id string, s proxmoxNotificationSection) error {
	r.attempted++
	flags := pbsSectionFlags(r.debug, r.key(), id, s.Entries, r.cfg.base64Values)
	head := []string{r.cfg.verb("create", r.cfg.createVerb)}
	if r.cfg.createWithType {
		head = append(head, s.Type)
	}
	head = append(head, id)
	createFlags, cleanup, prepErr := r.dataFile(id, flags)
	var createErr error
	if prepErr != nil {
		createErr = prepErr
	} else {
		createArgs := r.commandArgs(append(head, createFlags...)...)
		_, createErr = runPBSManagerSensitive(r.ctx, createArgs, r.cfg.secretFlags...)
	}
	cleanup()
	if createErr != nil {
		return r.update(id, r.updateData(flags), createErr)
	}
	r.debug("%s=%s created", r.key(), id)
	r.applied(id)
	return nil
}

// pbsSectionRefusedAsExisting reports whether PBS refused a create because the
// object already exists ("... remote 'r1' already exists.").
func pbsSectionRefusedAsExisting(err error) bool {
	return strings.Contains(pbsManagerErrorLine(err), "already exists")
}

// update applies the backup's object over the one PBS refused to create. When
// PBS refuses the update too, the event carries the create's error, unless the
// create was refused only because the object already exists: then the update's
// error is the reason the object was not applied.
func (r *pbsSectionRestore) update(id string, flags []string, createErr error) error {
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	r.debug("%s=%s create failed error=%q action=update", r.key(), id, pbsManagerErrorText(createErr))
	updateArgs := r.commandArgs(append([]string{r.cfg.verb("update", r.cfg.updateVerb), id}, flags...)...)
	if _, err := runPBSManagerSensitive(r.ctx, updateArgs, r.cfg.secretFlags...); err != nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		r.debug("%s=%s update failed error=%q", r.key(), id, pbsManagerErrorText(err))
		rejected := createErr
		if pbsSectionRefusedAsExisting(createErr) {
			rejected = err
		}
		r.logger.Info("%s %s was rejected by PBS: \"%s\"", r.cfg.labels.eventNoun, id, pbsManagerErrorLine(rejected))
		r.logger.Warning("%s PBS %s %s: not applied", theme.SymbolWarning, r.cfg.labels.noun, id)
		r.notApplied = append(r.notApplied, id)
		return nil
	}
	r.debug("%s=%s updated", r.key(), id)
	r.applied(id)
	return nil
}

// configuration is the subject of the block's events about the whole
// configuration: "The remote configuration".
func (r *pbsSectionRestore) configuration() string {
	return "The " + strings.TrimPrefix(r.cfg.labels.outcome, "PBS ")
}

// configNotApplied logs the outcome of a block that applied nothing.
func (r *pbsSectionRestore) configNotApplied(cause error) error {
	r.logger.Warning("%s %s: not applied", theme.SymbolWarning, r.cfg.labels.outcome)
	return &pbsSectionRestoreStoppedError{err: cause}
}

// withoutBuiltin drops the objects PBS provides itself.
func (r *pbsSectionRestore) withoutBuiltin(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !r.cfg.isBuiltin(id) {
			out = append(out, id)
		}
	}
	return out
}

// readCurrent reads the ids of the objects configured on this PBS, or the error
// that kept them from being read.
func (r *pbsSectionRestore) readCurrent() ([]string, error) {
	kind := strings.Join(r.cfg.command, " ")
	listArgs := r.commandArgs("list", "--output-format=json")
	r.debug("reading current %s cmd=%q", r.keys(), "proxmox-backup-manager "+strings.Join(listArgs, " "))
	out, err := runPBSManager(r.ctx, listArgs...)
	if err != nil {
		r.debug("%s list failed error=%q", kind, pbsManagerErrorText(err))
		return nil, err
	}
	current, err := parsePBSListIDs(out, r.cfg.listKeys...)
	if err != nil {
		r.debug("%s list returned unparseable output error=%q", kind, err.Error())
		return nil, fmt.Errorf("parse %s list: %w", kind, err)
	}
	return r.withoutBuiltin(current), nil
}

// pbsSectionFileNames reads the objects of a section-config file on this PBS, by
// name, sorted; a missing file has none.
func pbsSectionFileNames(path string) ([]string, error) {
	sections, err := readProxmoxConfigSectionsOptional(path)
	if err != nil {
		return nil, err
	}
	names := make(map[string]struct{}, len(sections))
	for _, s := range sections {
		if name := strings.TrimSpace(s.Name); name != "" {
			names[name] = struct{}{}
		}
	}
	return pbsSortedNames(names), nil
}

// pbsSyncCfgPath is the sync job configuration of this PBS.
const pbsSyncCfgPath = "/etc/proxmox-backup/sync.cfg"

// pbsSyncJobsByRemote reads the sync jobs of this PBS's sync.cfg and returns, for
// each remote a job names (pull and push jobs both name one), the jobs that use
// it, sorted; a missing file has no jobs.
func pbsSyncJobsByRemote(path string) (byRemote map[string][]string, jobs int, err error) {
	sections, err := readProxmoxConfigSectionsOptional(path)
	if err != nil {
		return nil, 0, err
	}
	byRemote = make(map[string][]string)
	for _, s := range sections {
		jobs++
		for _, kv := range s.Entries {
			if normalizeProxmoxCfgKey(kv.Key) != "remote" {
				continue
			}
			if remote := strings.TrimSpace(kv.Value); remote != "" {
				byRemote[remote] = append(byRemote[remote], strings.TrimSpace(s.Name))
			}
		}
	}
	for remote := range byRemote {
		slices.Sort(byRemote[remote])
	}
	return byRemote, jobs, nil
}

// syncJobsByRemote is, for remote.cfg, the sync jobs of this PBS by the remote
// they use: a remote a job uses is kept, as PBS refuses to remove it. A sync.cfg
// that cannot be read is DEBUG only and keeps no remote. For any other config it
// is nil.
func (r *pbsSectionRestore) syncJobsByRemote() map[string][]string {
	if !r.cfg.keepSyncJobRemotes {
		return nil
	}
	byRemote, jobs, err := pbsSyncJobsByRemote(pbsSyncCfgPath)
	if err != nil {
		r.debug("fallback current sync jobs file=%s read failed error=%q", pbsSyncCfgPath, err.Error())
		return nil
	}
	remotes := pbsSortedNames(byRemote)
	r.debug("fallback current sync jobs=%d remotes_used=%s", jobs, strings.Join(remotes, ","))
	return byRemote
}

// pbsSectionFileBlocks splits a section-config file into its sections, each with
// its header and the lines under it, by the header rule of
// parseProxmoxNotificationSections.
func pbsSectionFileBlocks(content string) map[string][]string {
	blocks := make(map[string][]string)
	var name string
	var lines []string
	flush := func() {
		if name != "" {
			blocks[name] = append(blocks[name], strings.TrimRight(strings.Join(lines, "\n"), "\n \t"))
		}
		name, lines = "", nil
	}
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			if _, header, ok := parseProxmoxNotificationHeader(trimmed); ok {
				flush()
				name, lines = header, []string{line}
				continue
			}
		}
		if name != "" {
			lines = append(lines, line)
		}
	}
	flush()
	return blocks
}

// writeWithKept writes the target from the staged file (none when emptied) and
// the current sections of the kept objects, in that order.
func (r *pbsSectionRestore) writeWithKept(target, raw string, emptied bool, kept []string) error {
	data, err := restoreFS.ReadFile(target)
	if err != nil {
		return fmt.Errorf("read %s: %w", target, err)
	}
	blocks := pbsSectionFileBlocks(string(data))
	var parts []string
	if !emptied {
		parts = append(parts, strings.TrimSpace(raw))
	}
	for _, name := range kept {
		parts = append(parts, blocks[name]...)
	}
	if err := writeFileAtomic(target, []byte(strings.Join(parts, "\n\n")+"\n"), 0o640); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}

// fileFallback writes, in Clean (1:1) mode, the staged file of this config as it
// is when the API cannot apply it, and logs the outcome. event says why, raw is
// the staged file, ids the objects it names, cause the error that kept the API
// from applying it. A staged file without objects
// (empty, or only comments) removes the target file, as the API path removes
// every object. In remote.cfg a remote of this PBS that the backup does not have
// and a sync job uses keeps its current section, as PBS refuses to remove it.
// Once the file is written or removed, every object of the current file that
// the backup does not have is named as removed, or as not removed when kept.
func (r *pbsSectionRestore) fileFallback(stageRoot, raw string, ids []string, event string, cause error) error {
	r.logger.Info("%s", event)
	target := filepath.Join(string(os.PathSeparator), filepath.FromSlash(r.cfg.file))
	r.debug("fallback=file target=%s source=%s", target, filepath.Join(stageRoot, r.cfg.file))
	current, currentErr := pbsSectionFileNames(target)
	current = r.withoutBuiltin(current)
	if currentErr != nil {
		r.debug("fallback current file read failed error=%q", currentErr.Error())
	} else {
		r.debug("fallback current file %s=%d names=%s", r.keys(), len(current), strings.Join(current, ","))
	}

	// The objects of this PBS that the backup does not have; in remote.cfg those a
	// sync job of this PBS uses stay, as PBS refuses to remove them.
	inBackup := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		inBackup[id] = struct{}{}
	}
	usedBy := r.syncJobsByRemote()
	var stale, kept []string
	for _, name := range current {
		if _, ok := inBackup[name]; ok {
			continue
		}
		stale = append(stale, name)
		if len(usedBy[name]) > 0 {
			kept = append(kept, name)
		}
	}

	emptied := len(ids) == 0
	var err error
	switch {
	case !emptied && !pbsConfigHasHeader(raw):
		// The write would skip a file without a header the parser still read
		// objects from: say it was not written instead of applied.
		err = fmt.Errorf("%s does not look like a valid PBS config file (missing section header)", r.cfg.file)
	case len(kept) > 0:
		err = r.writeWithKept(target, raw, emptied, kept)
	case emptied:
		err = removeIfExists(target)
	default:
		err = r.cfg.writeFromStage(r.ctx, r.logger, stageRoot)
	}
	if err != nil {
		r.debug("fallback write failed error=%q", err.Error())
		r.logger.Info("%s could not be written to this PBS: \"%s\"", r.configuration(), err.Error())
		return r.configNotApplied(fmt.Errorf("%w; file fallback: %w", cause, err))
	}
	switch {
	case emptied && len(kept) > 0:
		r.debug("fallback staged file has no %s comments_only=%v, target written kept=%s", r.keys(), strings.TrimSpace(raw) != "", strings.Join(kept, ","))
	case emptied:
		r.debug("fallback staged file has no %s comments_only=%v, target removed", r.keys(), strings.TrimSpace(raw) != "")
	case len(kept) > 0:
		r.debug("fallback written kept=%s", strings.Join(kept, ","))
	default:
		r.debug("fallback written")
	}

	for _, name := range stale {
		if jobs := usedBy[name]; len(jobs) > 0 {
			r.debug("%s=%s not in backup used by sync jobs=%s action=keep", r.key(), name, strings.Join(jobs, ","))
			r.logger.Info("%s %s is configured on this PBS but not present in the backup", r.cfg.labels.eventNoun, name)
			r.logger.Warning("%s PBS %s %s: not removed", theme.SymbolWarning, r.cfg.labels.noun, name)
			continue
		}
		r.debug("%s=%s not in backup action=drop from file", r.key(), name)
		r.logger.Info("%s %s is configured on this PBS but not present in the backup", r.cfg.labels.eventNoun, name)
		r.logger.Info("%s PBS %s %s: removed", theme.SymbolSuccess, r.cfg.labels.noun, name)
	}
	r.logger.Warning("%s %s: applied from the backup file", theme.SymbolWarning, r.cfg.labels.outcome)
	return &pbsSectionRestoreStoppedError{err: cause}
}

// outcome logs the outcome of the whole block and returns the error the caller
// records the restore warning from.
func (r *pbsSectionRestore) outcome() error {
	if r.attempted == 0 {
		r.logger.Skip("%s: nothing to restore", r.cfg.labels.outcome)
		return nil
	}
	failures := len(r.notApplied) + len(r.notRemoved)
	switch failures {
	case 0:
		r.logger.Info("%s %s: applied", theme.SymbolSuccess, r.cfg.labels.outcome)
		return nil
	case r.attempted:
		r.logger.Warning("%s %s: not applied", theme.SymbolWarning, r.cfg.labels.outcome)
	default:
		r.logger.Warning("%s %s: partially applied", theme.SymbolWarning, r.cfg.labels.outcome)
	}
	return &pbsSectionRestoreIncompleteError{labels: r.cfg.labels, notApplied: r.notApplied, notRemoved: r.notRemoved}
}

// readStaged reads the staged file of this config and the objects it names, in
// sorted order. done is true when the block has already ended, with err its
// result: nothing is staged (DEBUG only), or the staged file could not be read
// or parsed (the block is logged as not applied).
func (r *pbsSectionRestore) readStaged(stageRoot, mode string) (raw string, desired map[string]proxmoxNotificationSection, ids []string, done bool, err error) {
	stagedPath := filepath.Join(stageRoot, r.cfg.file)
	raw, present, err := readStageFileOptional(stageRoot, r.cfg.file)
	if err == nil && !present {
		r.debug("staged=%s not in the backup action=skip", stagedPath)
		return "", nil, nil, true, nil
	}
	var sections []proxmoxNotificationSection
	if err == nil {
		if sections, err = parseProxmoxNotificationSections(raw); err != nil {
			err = fmt.Errorf("parse staged %s: %w", filepath.Base(r.cfg.file), err)
		}
	}
	if err != nil {
		r.logger.Info("Restoring PBS %s...", r.cfg.labels.plural)
		r.debug("mode=%s staged=%s", mode, stagedPath)
		r.debug("staged=%s read failed error=%q", stagedPath, err.Error())
		r.logger.Info("%s in the backup could not be read: \"%s\"", r.configuration(), err.Error())
		return "", nil, nil, true, r.configNotApplied(err)
	}
	desired = make(map[string]proxmoxNotificationSection, len(sections))
	for _, s := range sections {
		id := strings.TrimSpace(s.Name)
		if id == "" {
			continue
		}
		if r.cfg.isBuiltin(id) {
			continue
		}
		desired[id] = s
	}
	return raw, desired, pbsSortedNames(desired), false, nil
}

// open logs the opening of the block: the objects of the backup to restore.
func (r *pbsSectionRestore) open(stageRoot, mode string, ids []string) {
	r.logger.Info("Restoring PBS %s...", r.cfg.labels.plural)
	r.debug("mode=%s staged=%s", mode, filepath.Join(stageRoot, r.cfg.file))
	r.debug("backup %s=%d names=%s", r.keys(), len(ids), strings.Join(ids, ","))
	r.logger.Info("  %s to restore: %d", r.cfg.labels.countNoun, len(ids))
	if len(ids) > 0 {
		r.logger.Info("  Names: %s", strings.Join(ids, ", "))
	}
}

// applyPBSSectionCfgFileBlock is the block of a section config written from the
// backup file when the PBS API cannot apply it (Clean (1:1) mode, and Merge mode
// for the ACME plugins, which were always written as a file): the objects
// to restore, the event, then the file fallback of the API path, with the same
// lines. Its outcome is always already logged: the result is nil when nothing is
// staged, otherwise a pbsSectionRestoreStoppedError.
func applyPBSSectionCfgFileBlock(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool, cfg pbsSectionCfg, event string, cause error) error {
	r := &pbsSectionRestore{ctx: ctx, logger: logger, cfg: cfg}
	mode := "merge"
	if strict {
		mode = "clean"
	}
	raw, _, ids, done, err := r.readStaged(stageRoot, mode)
	if done {
		return err
	}
	r.open(stageRoot, mode, ids)
	return r.fileFallback(stageRoot, raw, ids, event, cause)
}

// applyPBSSectionCfgAfterAPIError is the block of a section config whose PBS API
// apply returned an error the block does not return today: the objects to
// restore and the event carrying that error, then in strict (Clean 1:1) mode the
// file written from the backup, as the file fallback, and in Merge mode the
// configuration not applied. Its outcome is always already logged: the result is
// nil when nothing is staged, otherwise a pbsSectionRestoreStoppedError.
func applyPBSSectionCfgAfterAPIError(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool, cfg pbsSectionCfg, apiErr error) error {
	r := &pbsSectionRestore{ctx: ctx, logger: logger, cfg: cfg}
	mode := "merge"
	if strict {
		mode = "clean"
	}
	raw, _, ids, done, err := r.readStaged(stageRoot, mode)
	if done {
		if err == nil {
			r.debug("api apply failed error=%q, nothing staged action=skip", apiErr.Error())
		}
		return err
	}
	r.open(stageRoot, mode, ids)
	r.debug("api apply failed error=%q", apiErr.Error())
	event := fmt.Sprintf("%s could not be applied through the PBS API: \"%s\"", r.configuration(), apiErr.Error())
	if strict {
		return r.fileFallback(stageRoot, raw, ids, event, apiErr)
	}
	r.logger.Info("%s", event)
	return r.configNotApplied(apiErr)
}

// applyPBSSectionCfgViaAPI restores the staged cfg.file through
// proxmox-backup-manager as one block: the objects of the backup, then in strict
// (Clean 1:1) mode the removal of every object of this PBS that the backup does
// not have, then every object of the backup in sorted order, created or, when PBS
// refuses the create, updated. Each object gets its outcome line and the block
// ends with its own. When an object was not applied or not removed, the result
// is a pbsSectionRestoreIncompleteError, whose outcome is already logged.
//
// A staged file that cannot be read ends the block as not applied, in both modes.
// In Clean mode, when the objects configured on this PBS cannot be read, the block
// writes the staged file as it is instead. Both return a
// pbsSectionRestoreStoppedError, whose outcome is already logged. A cancellation
// stops the apply at any point with the cancellation itself.
func applyPBSSectionCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool, cfg pbsSectionCfg) error {
	r := &pbsSectionRestore{ctx: ctx, logger: logger, cfg: cfg}
	mode := "merge"
	if strict {
		mode = "clean"
	}
	raw, desired, ids, done, err := r.readStaged(stageRoot, mode)
	if done {
		return err
	}

	// Clean mode reads this PBS's objects before the block opens.
	var current []string
	var listErr error
	if strict {
		if current, listErr = r.readCurrent(); listErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
		}
	}

	r.open(stageRoot, mode, ids)
	if listErr != nil {
		return r.fileFallback(stageRoot, raw, ids, fmt.Sprintf("The %s configured on this PBS could not be read", cfg.labels.plural), listErr)
	}

	if strict {
		r.debug("current %s=%d names=%s", r.keys(), len(current), strings.Join(current, ","))
		for _, id := range current {
			if _, ok := desired[id]; ok {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := r.removeStale(id); err != nil {
				return err
			}
		}
	}

	for _, id := range ids {
		// A cancelled restore stops here with the cancellation, not with one
		// "not applied" object per remaining id that hides it from the caller.
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.apply(id, desired[id]); err != nil {
			return err
		}
	}
	return r.outcome()
}

// pbsRemoteSectionCfg is the section config of remote.cfg.
func pbsRemoteSectionCfg() pbsSectionCfg {
	return pbsSectionCfg{
		file:        "etc/proxmox-backup/remote.cfg",
		command:     []string{"remote"},
		listKeys:    []string{"id", "name"},
		secretFlags: []string{"--password"},
		// PBS writes the password as standard base64 ("dummyprobe" is stored as
		// "ZHVtbXlwcm9iZQ==") and base64-encodes whatever --password receives.
		base64Values:       []string{"password"},
		labels:             pbsRemoteLabels,
		fileFallback:       applyPBSRemoteCfgFromStage,
		keepSyncJobRemotes: true,
	}
}

func applyPBSRemoteCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	return applyPBSSectionCfgViaAPI(ctx, logger, stageRoot, strict, pbsRemoteSectionCfg())
}

// pbsS3SectionCfg is the section config of s3.cfg.
func pbsS3SectionCfg() pbsSectionCfg {
	return pbsSectionCfg{
		file:         "etc/proxmox-backup/s3.cfg",
		command:      []string{"s3", "endpoint"},
		listKeys:     []string{"id", "name"},
		secretFlags:  []string{"--access-key", "--secret-key"},
		labels:       pbsS3EndpointLabels,
		fileFallback: applyPBSS3CfgFromStage,
	}
}

func applyPBSS3CfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	return applyPBSSectionCfgViaAPI(ctx, logger, stageRoot, strict, pbsS3SectionCfg())
}

// pbsAcmePluginsSectionCfg is the section config of acme/plugins.cfg. PBS 4.2.8
// creates a plugin with "acme plugin add <type> <id> --data <file>", the file
// holding the plugin data decoded, and updates it with "acme plugin set <id>
// --data <base64>", in standard base64 only, while plugins.cfg stores it as
// base64url. The standalone plugin is built in: it cannot be added, set or
// removed.
func pbsAcmePluginsSectionCfg() pbsSectionCfg {
	return pbsSectionCfg{
		file:           "etc/proxmox-backup/acme/plugins.cfg",
		command:        []string{"acme", "plugin"},
		listKeys:       []string{"plugin", "id"},
		secretFlags:    []string{"--data"},
		labels:         pbsAcmePluginLabels,
		createVerb:     "add",
		updateVerb:     "set",
		createWithType: true,
		dataFileFlag:   "data",
		builtin:        []string{"standalone"},
	}
}

func applyPBSAcmePluginsCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	return applyPBSSectionCfgViaAPI(ctx, logger, stageRoot, strict, pbsAcmePluginsSectionCfg())
}

// pbsDatastoreRestoreIncompleteError is a datastore.cfg apply in which one or more
// datastores were not applied, or in Clean (1:1) mode a stale datastore was not
// removed. applyPBSDatastoreCfgViaAPI has already logged the outcome of every
// datastore and of the whole block, so the caller does not log it again and does
// not run the file-based fallback over outcomes it has already reported.
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
// writes on a failure, with the "- " lines of a refusal of several parameters
// joined after it, and without the usage text it may print after them. A failure
// without such a line (the command could not run at all) gives the first line of
// its text instead.
func pbsManagerErrorLine(err error) string {
	text := pbsManagerErrorText(err)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		_, after, ok := strings.Cut(line, "Error: ")
		if !ok {
			continue
		}
		head := strings.TrimSpace(after)
		// Several refusals at once come as "Error: ...:" and one "- ..." line
		// each (measured on PBS 4.2.8): they are all part of the refusal.
		var items []string
		for _, next := range lines[i+1:] {
			item := strings.TrimSpace(next)
			if !strings.HasPrefix(item, "- ") {
				break
			}
			items = append(items, strings.TrimSpace(strings.TrimPrefix(item, "- ")))
		}
		if len(items) == 0 {
			return head
		}
		return head + " " + strings.Join(items, "; ")
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
// the backup does not have. PBS removes the jobs of the datastore with it. It
// returns only a cancellation.
func (r *pbsDatastoreRestore) removeStale(name string) error {
	r.attempted++
	r.debug("datastore=%s not in backup action=remove", name)
	if _, err := runPBSManager(r.ctx, "datastore", "remove", name); err != nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		r.debug("datastore=%s remove failed error=%q", name, pbsManagerErrorText(err))
		r.logger.Info("Datastore %s is configured on this PBS but not present in the backup", name)
		r.logger.Warning("%s PBS datastore %s: not removed", theme.SymbolWarning, name)
		r.notRemoved = append(r.notRemoved, name)
		return nil
	}
	r.debug("datastore=%s removed", name)
	r.logger.Info("Datastore %s is configured on this PBS but not present in the backup", name)
	r.logger.Info("%s PBS datastore %s: removed with its jobs", theme.SymbolSuccess, name)
	return nil
}

// update applies the backup's options to a datastore that already exists on this
// PBS, keeping its path. createErr is the refused create that led here, if any:
// when the update is refused too, the event carries the create's error. It
// returns only a cancellation.
// pbsDatastoreCreateOnlyFlags are the datastore settings "datastore create" takes and
// "datastore update" refuses (PBS 4.2.8 usage: the backend of an S3 datastore, the
// device of a removable one, are fixed once the datastore exists).
var pbsDatastoreCreateOnlyFlags = []string{"--backend", "--backing-device"}

// withoutPBSDatastoreCreateOnlyFlags drops those settings, in both the "--key value"
// and the "--key=value" form, from the flags of an update.
func withoutPBSDatastoreCreateOnlyFlags(flags []string) []string {
	out := make([]string, 0, len(flags))
	for i := 0; i < len(flags); i++ {
		name, _, joined := strings.Cut(flags[i], "=")
		if slices.Contains(pbsDatastoreCreateOnlyFlags, name) {
			if !joined {
				i++
			}
			continue
		}
		out = append(out, flags[i])
	}
	return out
}

func (r *pbsDatastoreRestore) update(name string, flags []string, keptPathEvent func(), createErr error) error {
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	updateArgs := append([]string{"datastore", "update", name}, withoutPBSDatastoreCreateOnlyFlags(flags)...)
	if _, err := runPBSManager(r.ctx, updateArgs...); err != nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		r.debug("datastore=%s update failed error=%q", name, pbsManagerErrorText(err))
		rejected := err
		if createErr != nil {
			rejected = createErr
		}
		r.failed(name, "Datastore %s was rejected by PBS: \"%s\"", name, pbsManagerErrorLine(rejected))
		return nil
	}
	r.debug("datastore=%s updated", name)
	if keptPathEvent != nil {
		keptPathEvent()
	}
	r.applied(name)
	return nil
}

// pbsChunkStoreExistsError reports whether PBS refused a datastore create because
// the path already holds a chunk store. PBS 4.2.8 says so in two ways: for a chunk
// store without backups "unable to create chunk store 'ds1' subdir
// "/srv/ds1/.chunks" - ... - EEXIST: File exists" (measured on VM 201), and for one
// with backups, whose vm/, ct/, host/ or ns/ directories sit next to .chunks,
// "datastore path not empty" (measured on pve-test). The second also answers a
// path that holds other files; there the --reuse-datastore retry is refused as
// well, "unable to open existing chunk store path", and changes nothing.
func pbsChunkStoreExistsError(err error) bool {
	text := pbsManagerErrorText(err)
	return strings.Contains(text, "unable to create chunk store") && strings.Contains(text, "EEXIST") ||
		strings.Contains(text, "datastore path not empty")
}

// createDatastore creates the datastore name at path. When PBS refuses the create
// because the path already holds a chunk store, it creates it again with
// --reuse-datastore true, which PBS 4.2.8 needs to configure a datastore over the
// data it finds there. When that is refused too, the result is the first refusal,
// the one that says why the datastore could not be created; the second is at DEBUG.
func (r *pbsDatastoreRestore) createDatastore(ctx context.Context, name, path string, flags []string) error {
	args := append([]string{"datastore", "create", name, path}, flags...)
	_, err := runPBSManager(ctx, args...)
	if err == nil || ctx.Err() != nil || !pbsChunkStoreExistsError(err) {
		return err
	}
	r.debug("datastore=%s path=%s chunk store exists action=create --reuse-datastore true", name, path)
	reuseArgs := append(append([]string(nil), args...), "--reuse-datastore", "true")
	if _, reuseErr := runPBSManager(ctx, reuseArgs...); reuseErr != nil {
		r.debug("datastore=%s path=%s reuse failed error=%q", name, path, pbsManagerErrorText(reuseErr))
		return err
	}
	return nil
}

// create creates a datastore this PBS does not list; when PBS refuses the create
// (it may exist after all, e.g. when the list could not be read) the datastore is
// updated instead. It returns only a cancellation.
func (r *pbsDatastoreRestore) create(name, path string, flags []string) error {
	if pbsDatastoreMountMissingFn(path, r.logger) {
		// PBS would build an empty chunk store on the root filesystem, where the disk or
		// pool should be mounted, and call it applied.
		r.debug("datastore=%s path=%s on_rootfs=true mount_expected=true action=skip", name, path)
		r.failed(name, "Datastore %s is at %s, on the root filesystem: its disk is not mounted", name, path)
		return nil
	}
	if err := r.createDatastore(r.ctx, name, path, flags); err != nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		r.debug("datastore=%s create failed error=%q action=update", name, pbsManagerErrorText(err))
		return r.update(name, flags, nil, err)
	}
	r.debug("datastore=%s created", name)
	r.applied(name)
	return nil
}

// pbsDatastoreMountMissingFn is pbsDatastoreMountMissing; the package tests, whose
// datastore paths sit on the build host's root filesystem, replace it.
var pbsDatastoreMountMissingFn = pbsDatastoreMountMissing

// pbsDatastoreMountMissing reports whether path resolves to the root filesystem while
// it looks like the mount of a disk or ZFS pool (the guard the file fallback and the
// directory recreation use), with no datastore data there yet.
func pbsDatastoreMountMissing(path string, logger *logging.Logger) bool {
	onRootFS, _, err := isPathOnRootFilesystem(path)
	if err != nil {
		return false
	}
	suspicious := isSuspiciousDatastoreMountLocation(path) || isLikelyZFSMountPoint(path, logger)
	hasData, dataErr := pbsDatastoreHasData(path)
	return rootFilesystemMountShouldSkip(onRootFS, suspicious, hasData, dataErr != nil)
}

// move re-creates, in Clean (1:1) mode, a datastore whose path on this PBS differs
// from the backup's: remove (without --destroy-data, so the chunks stay on disk),
// then create at the backup's path. When that create fails the datastore is
// re-created at its original path so it is not left deconfigured, even when the
// restore was cancelled meanwhile: the rollback runs without the cancellation,
// and the move then returns it. It returns only a cancellation.
func (r *pbsDatastoreRestore) move(name, currentPath, path string, flags []string) error {
	pathEvent := "Datastore %s is at %s on this PBS and at %s in the backup"
	if _, err := runPBSManager(r.ctx, "datastore", "remove", name); err != nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		r.debug("datastore=%s remove failed error=%q", name, pbsManagerErrorText(err))
		r.failed(name, pathEvent, name, currentPath, path)
		return nil
	}
	r.debug("datastore=%s removed", name)
	if err := r.createDatastore(r.ctx, name, path, flags); err != nil {
		r.debug("datastore=%s create failed path=%s error=%q action=rollback rollback_path=%s", name, path, pbsManagerErrorText(err), currentPath)
		if rbErr := r.createDatastore(context.WithoutCancel(r.ctx), name, currentPath, flags); rbErr != nil {
			r.debug("datastore=%s rollback failed path=%s error=%q", name, currentPath, pbsManagerErrorText(rbErr))
			r.logger.Info(pathEvent, name, currentPath, path)
			r.logger.Warning("%s PBS datastore %s: not configured, the data in %s is kept", theme.SymbolWarning, name, currentPath)
			r.notApplied = append(r.notApplied, name)
			return r.ctx.Err()
		}
		r.debug("datastore=%s created path=%s (rollback)", name, currentPath)
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		r.failed(name, pathEvent, name, currentPath, path)
		return nil
	}
	r.debug("datastore=%s created path=%s", name, path)
	r.logger.Info("Datastore %s moved from %s to %s, the data in %s is not moved", name, currentPath, path, currentPath)
	r.applied(name)
	return nil
}

// apply restores one datastore of the backup. It returns only a cancellation.
func (r *pbsDatastoreRestore) apply(name string, s proxmoxNotificationSection, currentPaths map[string]string) error {
	r.attempted++
	path, entries, ok := popEntryValue(s.Entries, "path")
	path = strings.TrimSpace(path)
	if !ok || path == "" {
		r.debug("datastore=%s backup_path=none action=skip", name)
		r.failed(name, "Datastore %s has no path in the backup", name)
		return nil
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
		return r.create(name, path, flags)
	}
	if currentPath == "" || currentPath == path {
		r.debug("datastore=%s backup_path=%s current_path=%s action=update", name, path, pbsDatastorePathOrNone(currentPath))
		return r.update(name, flags, nil, nil)
	}
	if r.strict {
		r.debug("datastore=%s backup_path=%s current_path=%s path differs action=remove,create", name, path, currentPath)
		return r.move(name, currentPath, path, flags)
	}
	r.debug("datastore=%s backup_path=%s current_path=%s path differs action=update (merge keeps the current path)", name, path, currentPath)
	return r.update(name, flags, func() {
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
	slices.Sort(keys)
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

// applyPBSDatastoreCfgAfterAPIError is the block of datastore.cfg when the API
// block returned an error it does not return today: the datastores to restore and
// the event carrying that error, then in strict (Clean 1:1) mode datastore.cfg
// written from the backup file, and in Merge mode the configuration not applied.
// Like the file block, its outcome is always already logged.
func applyPBSDatastoreCfgAfterAPIError(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool, apiErr error) error {
	r, _, _, done, err := beginPBSDatastoreRestore(ctx, logger, stageRoot, strict)
	if done {
		if err == nil {
			logger.Debug(pbsDatastoreRestoreDebug+"api apply failed error=%q, nothing staged action=skip", apiErr.Error())
		}
		return err
	}
	r.debug("api apply failed error=%q", apiErr.Error())
	return r.stopped(fmt.Sprintf("The datastore configuration could not be applied through the PBS API: \"%s\"", apiErr.Error()), apiErr)
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

// applyPBSDatastoreCfgViaAPI restores the staged datastore.cfg through
// proxmox-backup-manager as one block. A cancellation stops it at any point with
// the cancellation itself: no line for the datastores left, no file fallback.
func applyPBSDatastoreCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	r, desired, names, done, err := beginPBSDatastoreRestore(ctx, logger, stageRoot, strict)
	if done {
		return err
	}

	currentPaths, listErr := r.readCurrent()
	if listErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// Cancelled: no line, no file fallback, the restore stops.
			return ctxErr
		}
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
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.removeStale(name); err != nil {
			return err
		}
	}

	for _, name := range names {
		// A cancelled restore stops here with the cancellation, not with one
		// "not applied" datastore per remaining name.
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.apply(name, desired[name], currentPaths); err != nil {
			return err
		}
	}
	return r.outcome()
}

// pbsSyncSectionCfg is the section config of sync.cfg.
func pbsSyncSectionCfg() pbsSectionCfg {
	return pbsSectionCfg{
		file:     "etc/proxmox-backup/sync.cfg",
		command:  []string{"sync-job"},
		listKeys: []string{"id", "name"},
		labels:   pbsSyncJobLabels,
	}
}

func applyPBSSyncCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	return applyPBSSectionCfgViaAPI(ctx, logger, stageRoot, strict, pbsSyncSectionCfg())
}

// pbsVerificationSectionCfg is the section config of verification.cfg.
func pbsVerificationSectionCfg() pbsSectionCfg {
	return pbsSectionCfg{
		file:     "etc/proxmox-backup/verification.cfg",
		command:  []string{"verify-job"},
		listKeys: []string{"id", "name"},
		labels:   pbsVerifyJobLabels,
	}
}

func applyPBSVerificationCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	return applyPBSSectionCfgViaAPI(ctx, logger, stageRoot, strict, pbsVerificationSectionCfg())
}

// pbsPruneSectionCfg is the section config of prune.cfg.
func pbsPruneSectionCfg() pbsSectionCfg {
	return pbsSectionCfg{
		file:     "etc/proxmox-backup/prune.cfg",
		command:  []string{"prune-job"},
		listKeys: []string{"id", "name"},
		labels:   pbsPruneJobLabels,
	}
}

func applyPBSPruneCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	return applyPBSSectionCfgViaAPI(ctx, logger, stageRoot, strict, pbsPruneSectionCfg())
}

// pbsTrafficControlSectionCfg is the section config of traffic-control.cfg.
func pbsTrafficControlSectionCfg() pbsSectionCfg {
	return pbsSectionCfg{
		file:     "etc/proxmox-backup/traffic-control.cfg",
		command:  []string{"traffic-control"},
		listKeys: []string{"name", "id"},
		labels:   pbsTrafficControlLabels,
	}
}

func applyPBSTrafficControlCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	return applyPBSSectionCfgViaAPI(ctx, logger, stageRoot, strict, pbsTrafficControlSectionCfg())
}

// pbsNodeSecretFlags are the node update flags whose value an error must not
// show: the HTTP proxy URL may carry credentials.
var pbsNodeSecretFlags = []string{"--http-proxy"}

// pbsNodeUpdateFlags builds the "node update" flags of a node.cfg in the format
// PBS 4.2.8 writes it (measured on VM 201): one "key: value" line per setting,
// and the description as "#" lines, one per line of it. keys names the settings,
// in the file's order, the description last.
func pbsNodeUpdateFlags(raw string) (flags, keys []string) {
	var description []string
	for line := range strings.SplitSeq(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if after, ok := strings.CutPrefix(trimmed, "#"); ok {
			description = append(description, after)
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		flags = append(flags, proxmoxManagerFlag(key, strings.TrimSpace(value))...)
		keys = append(keys, key)
	}
	if len(description) > 0 {
		flags = append(flags, proxmoxManagerFlag("description", strings.Join(description, "\n"))...)
		keys = append(keys, "description")
	}
	return flags, keys
}

// pbsNodeDeletableKeys are the settings "node update --delete" takes (PBS 4.2.8).
var pbsNodeDeletableKeys = map[string]bool{
	"acme": true, "acmedomain0": true, "acmedomain1": true, "acmedomain2": true, "acmedomain3": true, "acmedomain4": true,
	"http-proxy": true, "email-from": true, "ciphers-tls-1.3": true, "ciphers-tls-1.2": true, "default-lang": true,
	"description": true, "task-log-max-days": true, "consent-text": true, "location": true,
}

// applyPBSNodeCfgViaAPI sets every setting of the staged node.cfg with one
// "node update", which also deletes deleteKeys (settings of this PBS the backup
// does not have, in a Clean restore).
func applyPBSNodeCfgViaAPI(ctx context.Context, stageRoot string, deleteKeys []string) error {
	raw, present, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/node.cfg")
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	flags, _ := pbsNodeUpdateFlags(raw)
	if len(flags) == 0 {
		return nil
	}
	for _, key := range deleteKeys {
		flags = append(flags, "--delete", key)
	}
	// node update applies to the local node: it sets every setting of the file.
	args := append([]string{"node", "update"}, flags...)
	if _, err := runPBSManagerSensitive(ctx, args, pbsNodeSecretFlags...); err != nil {
		return err
	}
	return nil
}
