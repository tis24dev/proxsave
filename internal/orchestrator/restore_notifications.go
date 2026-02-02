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
	if os.Geteuid() != 0 {
		logger.Warning("Skipping staged notifications apply: requires root privileges")
		return nil
	}

	switch plan.SystemType {
	case SystemTypePBS:
		if !plan.HasCategoryID("pbs_notifications") {
			return nil
		}
		return applyPBSNotificationsFromStage(ctx, logger, stageRoot)
	case SystemTypePVE:
		if !plan.HasCategoryID("pve_notifications") {
			return nil
		}
		if plan.NeedsClusterRestore {
			logging.DebugStep(logger, "notifications staged apply", "Skip PVE notifications apply: cluster RECOVERY restores config.db")
			return nil
		}
		if _, err := restoreCmd.Run(ctx, "which", "pvesh"); err != nil {
			logger.Warning("pvesh not found; skipping PVE notifications apply")
			return nil
		}
		return applyPVENotificationsFromStage(ctx, logger, stageRoot)
	default:
		return nil
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

	if err := restoreFS.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("ensure %s: %w", filepath.Dir(destPath), err)
	}
	if err := restoreFS.WriteFile(destPath, []byte(trimmed+"\n"), perm); err != nil {
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

func parseProxmoxNotificationHeader(line string) (typ, name string, ok bool) {
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", "", false
	}
	typ = strings.TrimSpace(line[:idx])
	name = strings.TrimSpace(line[idx+1:])
	if typ == "" || name == "" {
		return "", "", false
	}
	for _, r := range typ {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return "", "", false
		}
	}
	return typ, name, true
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
