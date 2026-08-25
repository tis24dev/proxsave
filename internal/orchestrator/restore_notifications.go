package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

type proxmoxNotificationEntry struct {
	Key   string
	Value string
}

type proxmoxNotificationSection struct {
	Type        string
	Name        string
	Entries     []proxmoxNotificationEntry
	RedactFlags []string
}

var sectionHeaderTypePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// notificationsApplyGeteuid is a seam so the apply path can be exercised without root,
// matching haApplyGeteuid (restore_ha.go:21) and pbsAPIApplyGeteuid.
var notificationsApplyGeteuid = os.Geteuid

func maybeApplyNotificationsFromStage(ctx context.Context, logger *logging.Logger, plan *RestorePlan, stageRoot string, dryRun bool) (err error) {
	if plan == nil {
		return nil
	}
	if strings.TrimSpace(stageRoot) == "" {
		logging.DebugStep(logger, "notifications staged apply", "Skipped: staging directory not available")
		return nil
	}
	if !plan.HasCategoryID("pve_notifications") && !plan.HasCategoryID("pbs_notifications") {
		return nil
	}

	done := logging.DebugStart(logger, "notifications staged apply", "dryRun=%v stage=%s", dryRun, stageRoot)
	defer func() { done(err) }()

	if dryRun {
		logger.Info("Dry run enabled: skipping staged notifications apply")
		return nil
	}
	if !isRealRestoreFS(restoreFS) {
		logger.Debug("Skipping staged notifications apply: non-system filesystem in use")
		return nil
	}
	if notificationsApplyGeteuid() != 0 {
		logger.Warning("Skipping staged notifications apply: requires root privileges")
		return nil
	}

	if plan.SystemType.SupportsPBS() && plan.HasCategoryID("pbs_notifications") {
		behavior := plan.PBSRestoreBehavior
		strict := behavior == PBSRestoreBehaviorClean
		allowFileFallback := behavior == PBSRestoreBehaviorClean

		if err := ensurePBSServicesForAPI(ctx, logger); err != nil {
			if allowFileFallback {
				logger.Warning("PBS notifications API apply unavailable; falling back to file-based apply: %v", err)
				if err := applyPBSNotificationsFromStage(ctx, logger, stageRoot); err != nil {
					return err
				}
			} else {
				logger.Warning("PBS notifications API apply unavailable; skipping apply (merge mode): %v", err)
			}
		} else if rep, apiErr := applyPBSNotificationsViaAPI(ctx, logger, stageRoot, strict); apiErr != nil {
			// Say what was already written BEFORE the failure. The old code returned
			// applied=false here, and in merge mode the caller then logged "skipping apply
			// (merge mode)" -- false for a run that had already created half the endpoints.
			//
			// This is the ONLY sentence printed on the error path. err != nil here is
			// always a mid-flight abort, because remove failures are recorded in the report
			// rather than returned as an error; if that ever changes, this sentence becomes
			// false on a fully successful Clean and must be split.
			if rep.mutated() {
				logger.Warning("PBS notifications: the apply failed after %d object(s) had already been written (endpoints=%d matchers=%d removed=%d); the live configuration is partially updated",
					rep.endpointsUpserted+rep.matchersUpserted+len(rep.removed),
					rep.endpointsUpserted, rep.matchersUpserted, len(rep.removed))
			}
			if allowFileFallback {
				logger.Warning("PBS notifications API apply failed; falling back to file-based apply: %v", apiErr)
				if err := applyPBSNotificationsFromStage(ctx, logger, stageRoot); err != nil {
					return err
				}
			} else {
				logger.Warning("PBS notifications API apply failed; skipping apply (merge mode): %v", apiErr)
			}
		} else {
			logPBSNotificationApplyReport(logger, behavior, rep)
		}

	}

	if plan.SystemType.SupportsPVE() && plan.HasCategoryID("pve_notifications") {
		if plan.NeedsClusterRestore {
			logging.DebugStep(logger, "notifications staged apply", "Skip PVE notifications apply: cluster RECOVERY restores config.db")
			return nil
		}
		if _, err := restoreCmd.Run(ctx, "which", "pvesh"); err != nil {
			logger.Warning("pvesh not found; skipping PVE notifications apply")
			return nil
		}
		return applyPVENotificationsFromStage(ctx, logger, stageRoot)
	}

	return nil
}

// logPBSNotificationApplyReport phrases the operator sentence from what was COUNTED. There
// is exactly one place in this package that can print "applied via API", and it is gated on
// mutated(). Called only on the nil-error path: on an error the counters describe a partial
// apply, not an outcome, and the caller says so separately.
func logPBSNotificationApplyReport(logger *logging.Logger, behavior PBSRestoreBehavior, rep pbsNotificationApplyReport) {
	// A deletion is the one thing that must never be implied by a summary line. Successful
	// removals were previously logged NOWHERE at any level: a Clean restore could delete
	// every live notification object and produce an entirely empty log. No cause is
	// asserted -- "not present in the applied configuration" is what we observed;
	// "absent from the backup" would be false for anything dropped in translation.
	if len(rep.removed) > 0 {
		logger.Info("PBS notifications: removed %d live object(s) not present in the applied configuration: %s",
			len(rep.removed), strings.Join(rep.removed, ", "))
	}
	if len(rep.removeFailed) > 0 {
		logger.Warning("PBS notifications: %d live object(s) could not be removed and were left in place: %s",
			len(rep.removeFailed), strings.Join(rep.removeFailed, ", "))
	}
	if len(rep.removalsSkipped) > 0 {
		logger.Warning("PBS notifications: Clean 1:1 did not remove stale %s endpoint(s): at least one staged section of that kind could not be rebuilt, so a live endpoint missing from the desired set is not evidence that the backup lacked it",
			strings.Join(rep.removalsSkipped, ", "))
	}
	if rep.dropped() > 0 {
		logger.Warning("PBS notifications: %d staged section(s) were not applied (%d unknown type, %d missing a required field)",
			rep.dropped(), rep.droppedUnknownType, rep.droppedIncomplete)
	}

	if !rep.mutated() {
		switch {
		case !rep.staged:
			logger.Info("PBS notifications: this backup contains no notifications.cfg, so nothing was applied and the live configuration is unchanged")
		case rep.stagedEmpty:
			logger.Warning("PBS notifications: the staged notifications.cfg is empty, so nothing was applied and the live configuration is unchanged")
		case rep.sections == 0:
			logger.Warning("PBS notifications: no section header was recognised in the staged notifications.cfg, so nothing was applied and the live configuration is unchanged")
		case rep.planned == 0:
			logger.Warning("PBS notifications: all %d staged section(s) were skipped, so nothing was applied and the live configuration is unchanged", rep.sections)
		default:
			// Defensive: on the nil-error path every planned object issues at least one
			// command, so this is unreachable. It states what is known rather than assuming
			// the work happened.
			logger.Warning("PBS notifications: the staged notifications.cfg named %d object(s) but no command was acknowledged; the live configuration is unchanged", rep.planned)
		}
		return
	}

	logger.Info("PBS notifications applied via API (%s): endpoints=%d matchers=%d removed=%d",
		behavior.DisplayName(), rep.endpointsUpserted, rep.matchersUpserted, len(rep.removed))

	// A wipe whose only authority was a staged file naming nothing. This covers the EMPTY
	// file and the non-empty file whose every section was dropped: from the stage those are
	// the same evidence, and gating on stagedEmpty alone leaves the second one silent.
	if rep.planned == 0 && len(rep.removed) > 0 {
		logger.Warning("PBS notifications: the staged notifications.cfg named no endpoint and no matcher, so Clean 1:1 removed all %d live notification object(s) on that evidence alone; if that file should not have been empty, re-run from a verified backup",
			len(rep.removed))
	}
}

func applyPBSNotificationsFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) error {
	_ = ctx // reserved for future validation hooks

	paths := []struct {
		rel  string
		dest string
		mode os.FileMode
	}{
		{
			rel:  "etc/proxmox-backup/notifications.cfg",
			dest: "/etc/proxmox-backup/notifications.cfg",
			mode: 0o640,
		},
		{
			rel:  "etc/proxmox-backup/notifications-priv.cfg",
			dest: "/etc/proxmox-backup/notifications-priv.cfg",
			mode: 0o600,
		},
	}

	for _, item := range paths {
		if err := applyConfigFileFromStage(logger, stageRoot, item.rel, item.dest, item.mode); err != nil {
			return err
		}
	}
	return nil
}

func applyPVENotificationsFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) error {
	cfgPath := filepath.Join(stageRoot, "etc/pve/notifications.cfg")
	cfgData, err := restoreFS.ReadFile(cfgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logging.DebugStep(logger, "pve notifications apply", "Skipped: notifications.cfg not present in staging directory")
			return nil
		}
		return fmt.Errorf("read staged notifications.cfg: %w", err)
	}
	cfgRaw := strings.TrimSpace(string(cfgData))
	if cfgRaw == "" {
		logging.DebugStep(logger, "pve notifications apply", "Skipped: notifications.cfg is empty")
		return nil
	}

	privPath := filepath.Join(stageRoot, "etc/pve/priv/notifications.cfg")
	privRaw := ""
	if privData, err := restoreFS.ReadFile(privPath); err == nil {
		privRaw = strings.TrimSpace(string(privData))
	}

	cfgSections, err := parseProxmoxNotificationSections(cfgRaw)
	if err != nil {
		return fmt.Errorf("parse notifications.cfg: %w", err)
	}
	privSections, err := parseProxmoxNotificationSections(privRaw)
	if err != nil {
		return fmt.Errorf("parse priv notifications.cfg: %w", err)
	}

	privByKey := make(map[string][]proxmoxNotificationEntry)
	privRedactFlagsByKey := make(map[string][]string)
	for _, s := range privSections {
		if strings.TrimSpace(s.Type) == "" || strings.TrimSpace(s.Name) == "" {
			continue
		}
		key := fmt.Sprintf("%s:%s", s.Type, s.Name)
		privByKey[key] = append([]proxmoxNotificationEntry{}, s.Entries...)
		privRedactFlagsByKey[key] = append([]string(nil), notificationRedactFlagsFromEntries(s.Entries)...)
	}

	var endpoints []proxmoxNotificationSection
	var matchers []proxmoxNotificationSection
	for _, s := range cfgSections {
		switch strings.TrimSpace(s.Type) {
		case "smtp", "sendmail", "gotify", "webhook":
			key := fmt.Sprintf("%s:%s", s.Type, s.Name)
			if priv, ok := privByKey[key]; ok && len(priv) > 0 {
				s.Entries = append(s.Entries, priv...)
			}
			if redactFlags := privRedactFlagsByKey[key]; len(redactFlags) > 0 {
				s.RedactFlags = append(s.RedactFlags, redactFlags...)
			}
			endpoints = append(endpoints, s)
		case "matcher":
			matchers = append(matchers, s)
		default:
			logger.Warning("PVE notifications apply: unknown section %q (%s); skipping", s.Type, s.Name)
		}
	}

	failed := 0
	for _, s := range endpoints {
		if err := applyPVEEndpointSection(ctx, logger, s); err != nil {
			failed++
			logger.Warning("PVE notifications apply: endpoint %s:%s: %v", s.Type, s.Name, err)
		}
	}
	for _, s := range matchers {
		if err := applyPVEMatcherSection(ctx, logger, s); err != nil {
			failed++
			logger.Warning("PVE notifications apply: matcher %s: %v", s.Name, err)
		}
	}

	if failed > 0 {
		return fmt.Errorf("PVE notifications apply: %d item(s) failed", failed)
	}
	logger.Info("PVE notifications applied: endpoints=%d matchers=%d", len(endpoints), len(matchers))
	return nil
}

func applyPVEEndpointSection(ctx context.Context, logger *logging.Logger, section proxmoxNotificationSection) error {
	typ := strings.TrimSpace(section.Type)
	name := strings.TrimSpace(section.Name)
	if typ == "" || name == "" {
		return fmt.Errorf("invalid endpoint section")
	}
	if typ == "matcher" {
		return fmt.Errorf("endpoint section has matcher type")
	}

	setPath := fmt.Sprintf("/cluster/notifications/endpoints/%s/%s", typ, name)
	createPath := fmt.Sprintf("/cluster/notifications/endpoints/%s", typ)
	args := buildPveshArgs(section.Entries)
	return applyPveshObject(ctx, logger, setPath, createPath, name, args, notificationRedactFlags(section))
}

func applyPVEMatcherSection(ctx context.Context, logger *logging.Logger, section proxmoxNotificationSection) error {
	name := strings.TrimSpace(section.Name)
	if strings.TrimSpace(section.Type) != "matcher" || name == "" {
		return fmt.Errorf("invalid matcher section")
	}
	setPath := fmt.Sprintf("/cluster/notifications/matchers/%s", name)
	createPath := "/cluster/notifications/matchers"
	args := buildPveshArgs(section.Entries)
	return applyPveshObject(ctx, logger, setPath, createPath, name, args, nil)
}

func applyPveshObject(ctx context.Context, logger *logging.Logger, setPath, createPath, name string, args []string, redactFlags []string) error {
	setArgs := append([]string{"set", setPath}, args...)
	if len(redactFlags) > 0 {
		if _, err := runPveshSensitive(ctx, logger, setArgs, redactFlags...); err == nil {
			return nil
		}
	} else if err := runPvesh(ctx, logger, setArgs); err == nil {
		return nil
	}

	createArgs := []string{"create", createPath, "--name", name}
	createArgs = append(createArgs, args...)
	if len(redactFlags) > 0 {
		_, err := runPveshSensitive(ctx, logger, createArgs, redactFlags...)
		return err
	}
	return runPvesh(ctx, logger, createArgs)
}

func buildPveshArgs(entries []proxmoxNotificationEntry) []string {
	if len(entries) == 0 {
		return nil
	}
	args := make([]string, 0, len(entries)*2)
	for _, entry := range entries {
		key := strings.TrimSpace(entry.Key)
		if key == "" || key == "name" || key == "digest" {
			continue
		}
		args = append(args, "--"+key)
		args = append(args, entry.Value)
	}
	return args
}

func notificationRedactFlagsFromEntries(entries []proxmoxNotificationEntry) []string {
	if len(entries) == 0 {
		return nil
	}
	var out []string
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		key := strings.TrimSpace(entry.Key)
		if key == "" || key == "name" || key == "digest" {
			continue
		}
		flag := "--" + key
		if _, ok := seen[flag]; ok {
			continue
		}
		seen[flag] = struct{}{}
		out = append(out, flag)
	}
	return out
}

func notificationRedactFlags(section proxmoxNotificationSection) []string {
	out := make([]string, 0, len(section.RedactFlags)+8)
	seen := make(map[string]struct{}, len(section.RedactFlags)+8)
	add := func(flag string) {
		flag = strings.TrimSpace(flag)
		if flag == "" {
			return
		}
		if _, ok := seen[flag]; ok {
			return
		}
		seen[flag] = struct{}{}
		out = append(out, flag)
	}

	for _, flag := range section.RedactFlags {
		add(flag)
	}

	// Default set for notification endpoints; protects against secrets accidentally present in non-priv config.
	for _, flag := range []string{"--password", "--token", "--secret", "--apikey", "--api-key"} {
		add(flag)
	}

	// If the config uses alternative key names, still try to redact common secret-like fields.
	for _, entry := range section.Entries {
		key := strings.ToLower(strings.TrimSpace(entry.Key))
		switch key {
		case "password", "token", "secret", "apikey", "api-key":
			add("--" + strings.TrimSpace(entry.Key))
		}
	}

	return out
}

func applyConfigFileFromStage(logger *logging.Logger, stageRoot, relPath, destPath string, perm os.FileMode) error {
	stagePath := filepath.Join(stageRoot, relPath)
	data, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logging.DebugStep(logger, "notifications staged apply file", "Skip %s: not present in staging directory", relPath)
			return nil
		}
		return fmt.Errorf("read staged %s: %w", relPath, err)
	}

	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		logger.Warning("Notifications staged apply: %s is empty; removing %s to avoid Proxmox parse errors", relPath, destPath)
		return removeIfExists(destPath)
	}
	if !pbsConfigHasHeader(trimmed) {
		logger.Warning("Notifications staged apply: %s does not look like a valid Proxmox config file (missing section header); skipping apply", relPath)
		return nil
	}

	if err := writeFileAtomic(destPath, []byte(trimmed+"\n"), perm); err != nil {
		return fmt.Errorf("write %s: %w", destPath, err)
	}
	logging.DebugStep(logger, "notifications staged apply file", "Applied %s -> %s", relPath, destPath)
	return nil
}

func parseProxmoxNotificationSections(content string) ([]proxmoxNotificationSection, error) {
	raw := strings.TrimSpace(content)
	if raw == "" {
		return nil, nil
	}

	var out []proxmoxNotificationSection
	var current *proxmoxNotificationSection

	flush := func() {
		if current == nil {
			return
		}
		if strings.TrimSpace(current.Type) == "" || strings.TrimSpace(current.Name) == "" {
			current = nil
			return
		}
		out = append(out, *current)
		current = nil
	}

	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		typ, name, ok := parseProxmoxNotificationHeader(trimmed)
		if ok {
			flush()
			current = &proxmoxNotificationSection{Type: typ, Name: name}
			continue
		}

		if current == nil {
			continue
		}

		key, value := parseProxmoxNotificationKV(trimmed)
		if strings.TrimSpace(key) == "" {
			continue
		}
		current.Entries = append(current.Entries, proxmoxNotificationEntry{Key: key, Value: value})
	}
	flush()

	return out, nil
}

func parseSectionHeader(line string) (typ, name string, ok bool) {
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", "", false
	}
	typ = strings.TrimSpace(line[:idx])
	name = strings.TrimSpace(line[idx+1:])
	if typ == "" || name == "" {
		return "", "", false
	}
	if !sectionHeaderTypePattern.MatchString(typ) {
		return "", "", false
	}
	return typ, name, true
}

func parseProxmoxNotificationHeader(line string) (typ, name string, ok bool) {
	return parseSectionHeader(line)
}

func parseProxmoxNotificationKV(line string) (key, value string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", ""
	}
	key = strings.TrimSpace(fields[0])
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
	return key, value
}
