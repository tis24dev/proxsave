package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

func maybeVerifyAndRepairPBSNotificationsAfterRestore(ctx context.Context, logger *logging.Logger, plan *RestorePlan, stageRoot string, dryRun bool) error {
	if plan == nil || logger == nil {
		return nil
	}
	if dryRun {
		return nil
	}
	if plan.SystemType != SystemTypePBS || !plan.HasCategoryID("pbs_notifications") {
		return nil
	}
	if strings.TrimSpace(stageRoot) == "" && !isRealRestoreFS(restoreFS) {
		return nil
	}
	if os.Geteuid() != 0 {
		logger.Warning("Skipping PBS notifications verification/repair: requires root privileges")
		return nil
	}
	if _, err := restoreCmd.Run(ctx, "which", "proxmox-backup-manager"); err != nil {
		logger.Warning("proxmox-backup-manager not found; skipping PBS notifications verification/repair")
		return nil
	}

	cfgRaw, privRaw, source, err := loadPBSNotificationsConfig(stageRoot)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfgRaw) == "" && strings.TrimSpace(privRaw) == "" {
		logging.DebugStep(logger, "pbs notifications verify", "Skipped: notifications config not found (%s)", source)
		return nil
	}

	expectedTargets, expectedMatchers, err := expectedPBSNotificationNames(cfgRaw)
	if err != nil {
		logger.Warning("PBS notifications verify: failed to parse staged config (%s): %v", source, err)
		return nil
	}
	if len(expectedTargets) == 0 && len(expectedMatchers) == 0 {
		logging.DebugStep(logger, "pbs notifications verify", "Skipped: no notification sections detected (%s)", source)
		return nil
	}

	currentTargets, errTargets := pbsNotificationTargetNames(ctx)
	currentMatchers, errMatchers := pbsNotificationMatcherNames(ctx)
	if errTargets != nil || errMatchers != nil {
		return fmt.Errorf("PBS notifications verify: failed to list current config (targets=%v matchers=%v)", errTargets, errMatchers)
	}

	missingTargets := missingNameSet(expectedTargets, currentTargets)
	missingMatchers := missingNameSet(expectedMatchers, currentMatchers)
	if len(missingTargets) == 0 && len(missingMatchers) == 0 {
		logging.DebugStep(logger, "pbs notifications verify", "OK: targets=%d matchers=%d", len(currentTargets), len(currentMatchers))
		return nil
	}

	logger.Warning(
		"PBS notifications verify: missing targets=%s missing matchers=%s; attempting API repair via proxmox-backup-manager",
		strings.Join(missingTargets, ","),
		strings.Join(missingMatchers, ","),
	)

	if err := applyPBSNotificationsViaProxmoxBackupManager(ctx, logger, cfgRaw, privRaw); err != nil {
		return err
	}

	afterTargets, _ := pbsNotificationTargetNames(ctx)
	afterMatchers, _ := pbsNotificationMatcherNames(ctx)
	logger.Info("PBS notifications repaired: targets=%d matchers=%d", len(afterTargets), len(afterMatchers))
	return nil
}

func loadPBSNotificationsConfig(stageRoot string) (cfgRaw, privRaw, source string, err error) {
	type candidate struct {
		source string
		cfg    string
		priv   string
	}

	readMaybe := func(path string) (string, error) {
		data, rerr := restoreFS.ReadFile(path)
		if rerr != nil {
			if errors.Is(rerr, os.ErrNotExist) {
				return "", nil
			}
			return "", rerr
		}
		return strings.TrimSpace(string(data)), nil
	}

	var candidates []candidate
	if strings.TrimSpace(stageRoot) != "" {
		cfgPath := filepath.Join(stageRoot, "etc/proxmox-backup/notifications.cfg")
		privPath := filepath.Join(stageRoot, "etc/proxmox-backup/notifications-priv.cfg")
		cfg, rerr := readMaybe(cfgPath)
		if rerr != nil {
			return "", "", "", fmt.Errorf("read staged notifications.cfg: %w", rerr)
		}
		priv, rerr := readMaybe(privPath)
		if rerr != nil {
			return "", "", "", fmt.Errorf("read staged notifications-priv.cfg: %w", rerr)
		}
		candidates = append(candidates, candidate{source: "staging", cfg: cfg, priv: priv})
	}

	cfg, rerr := readMaybe("/etc/proxmox-backup/notifications.cfg")
	if rerr != nil {
		return "", "", "", fmt.Errorf("read /etc/proxmox-backup/notifications.cfg: %w", rerr)
	}
	priv, rerr := readMaybe("/etc/proxmox-backup/notifications-priv.cfg")
	if rerr != nil {
		return "", "", "", fmt.Errorf("read /etc/proxmox-backup/notifications-priv.cfg: %w", rerr)
	}
	candidates = append(candidates, candidate{source: "system", cfg: cfg, priv: priv})

	for _, c := range candidates {
		if strings.TrimSpace(c.cfg) != "" || strings.TrimSpace(c.priv) != "" {
			return c.cfg, c.priv, c.source, nil
		}
	}
	if len(candidates) > 0 {
		last := candidates[len(candidates)-1]
		return last.cfg, last.priv, last.source, nil
	}
	return "", "", "none", nil
}

func expectedPBSNotificationNames(cfgRaw string) (targets, matchers map[string]struct{}, err error) {
	sections, err := parseProxmoxNotificationSections(cfgRaw)
	if err != nil {
		return nil, nil, err
	}

	targets = make(map[string]struct{})
	matchers = make(map[string]struct{})
	for _, s := range sections {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		switch strings.TrimSpace(s.Type) {
		case "smtp", "sendmail", "gotify", "webhook":
			targets[name] = struct{}{}
		case "matcher":
			matchers[name] = struct{}{}
		}
	}
	return targets, matchers, nil
}

func pbsNotificationTargetNames(ctx context.Context) (map[string]struct{}, error) {
	out, err := restoreCmd.Run(ctx, "proxmox-backup-manager", "notification", "target", "list", "--output-format=json")
	if err != nil {
		return nil, fmt.Errorf("proxmox-backup-manager notification target list failed: %w", err)
	}
	return jsonListNameSet(out, "name", "id", "target")
}

func pbsNotificationMatcherNames(ctx context.Context) (map[string]struct{}, error) {
	out, err := restoreCmd.Run(ctx, "proxmox-backup-manager", "notification", "matcher", "list", "--output-format=json")
	if err != nil {
		return nil, fmt.Errorf("proxmox-backup-manager notification matcher list failed: %w", err)
	}
	return jsonListNameSet(out, "name", "id", "matcher")
}

func jsonListItems(raw []byte) ([]any, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, nil
	}

	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return nil, err
	}
	switch t := v.(type) {
	case []any:
		return t, nil
	case map[string]any:
		if data, ok := t["data"]; ok {
			if arr, ok := data.([]any); ok {
				return arr, nil
			}
		}
		return nil, fmt.Errorf("unexpected JSON object output")
	default:
		return nil, fmt.Errorf("unexpected JSON output")
	}
}

func jsonListNameSet(raw []byte, keys ...string) (map[string]struct{}, error) {
	items, err := jsonListItems(raw)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(items))
	if len(items) == 0 {
		return out, nil
	}
	if len(keys) == 0 {
		keys = []string{"name"}
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok || m == nil {
			continue
		}
		for _, key := range keys {
			v, ok := m[key]
			if !ok {
				continue
			}
			s, ok := v.(string)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			out[s] = struct{}{}
			break
		}
	}
	return out, nil
}

func missingNameSet(expected, actual map[string]struct{}) []string {
	if len(expected) == 0 {
		return nil
	}
	if len(actual) == 0 {
		out := make([]string, 0, len(expected))
		for name := range expected {
			out = append(out, name)
		}
		sort.Strings(out)
		return out
	}

	var out []string
	for name := range expected {
		if _, ok := actual[name]; ok {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func applyPBSNotificationsViaProxmoxBackupManager(ctx context.Context, logger *logging.Logger, cfgRaw, privRaw string) error {
	cfgSections, err := parseProxmoxNotificationSections(cfgRaw)
	if err != nil {
		return fmt.Errorf("parse notifications.cfg: %w", err)
	}
	privSections, err := parseProxmoxNotificationSections(privRaw)
	if err != nil {
		return fmt.Errorf("parse notifications-priv.cfg: %w", err)
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
			logger.Warning("PBS notifications API apply: unknown section %q (%s); skipping", s.Type, s.Name)
		}
	}

	failed := 0
	for _, s := range endpoints {
		if err := applyPBSEndpointSection(ctx, logger, s); err != nil {
			failed++
			logger.Warning("PBS notifications API apply: endpoint %s:%s: %v", s.Type, s.Name, err)
		}
	}
	for _, s := range matchers {
		if err := applyPBSMatcherSection(ctx, logger, s); err != nil {
			failed++
			logger.Warning("PBS notifications API apply: matcher %s: %v", s.Name, err)
		}
	}

	if failed > 0 {
		return fmt.Errorf("PBS notifications API apply: %d item(s) failed", failed)
	}
	logger.Info("PBS notifications applied via API: endpoints=%d matchers=%d", len(endpoints), len(matchers))
	return nil
}

func applyPBSEndpointSection(ctx context.Context, logger *logging.Logger, section proxmoxNotificationSection) error {
	typ := strings.TrimSpace(section.Type)
	name := strings.TrimSpace(section.Name)
	if typ == "" || name == "" {
		return fmt.Errorf("invalid endpoint section")
	}
	if typ == "matcher" {
		return fmt.Errorf("endpoint section has matcher type")
	}

	args := buildPveshArgs(section.Entries)
	updateArgs := append([]string{"notification", "endpoint", typ, "update", name}, args...)
	createArgs := append([]string{"notification", "endpoint", typ, "create", name}, args...)
	return applyProxmoxBackupManagerObject(ctx, logger, updateArgs, createArgs, notificationRedactFlags(section))
}

func applyPBSMatcherSection(ctx context.Context, logger *logging.Logger, section proxmoxNotificationSection) error {
	name := strings.TrimSpace(section.Name)
	if strings.TrimSpace(section.Type) != "matcher" || name == "" {
		return fmt.Errorf("invalid matcher section")
	}
	args := buildPveshArgs(section.Entries)
	updateArgs := append([]string{"notification", "matcher", "update", name}, args...)
	createArgs := append([]string{"notification", "matcher", "create", name}, args...)
	return applyProxmoxBackupManagerObject(ctx, logger, updateArgs, createArgs, nil)
}

func applyProxmoxBackupManagerObject(ctx context.Context, logger *logging.Logger, updateArgs, createArgs []string, redactFlags []string) error {
	if len(redactFlags) > 0 {
		if _, err := runProxmoxBackupManagerSensitive(ctx, logger, updateArgs, redactFlags...); err == nil {
			return nil
		}
	} else if err := runProxmoxBackupManager(ctx, logger, updateArgs); err == nil {
		return nil
	}

	if len(redactFlags) > 0 {
		_, err := runProxmoxBackupManagerSensitive(ctx, logger, createArgs, redactFlags...)
		return err
	}
	return runProxmoxBackupManager(ctx, logger, createArgs)
}

func runProxmoxBackupManager(ctx context.Context, logger *logging.Logger, args []string) error {
	output, err := restoreCmd.Run(ctx, "proxmox-backup-manager", args...)
	if len(output) > 0 {
		logger.Debug("proxmox-backup-manager %v output: %s", args, strings.TrimSpace(string(output)))
	}
	if err != nil {
		return fmt.Errorf("proxmox-backup-manager %v failed: %w", args, err)
	}
	return nil
}

func runProxmoxBackupManagerSensitive(ctx context.Context, _ *logging.Logger, args []string, redactFlags ...string) ([]byte, error) {
	output, err := restoreCmd.Run(ctx, "proxmox-backup-manager", args...)
	if err != nil {
		redacted := redactCLIArgs(args, redactFlags)
		return output, fmt.Errorf("proxmox-backup-manager %s failed: %w", strings.Join(redacted, " "), err)
	}
	return output, nil
}
