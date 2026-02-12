package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

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

func runPBSManagerRedacted(ctx context.Context, args []string, redactFlags []string, redactIndexes []int) ([]byte, error) {
	out, err := restoreCmd.Run(ctx, "proxmox-backup-manager", args...)
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

	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}

	out := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		id := ""
		for _, k := range candidateKeys {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if v, ok := row[k]; ok {
				if s, ok := v.(string); ok {
					id = strings.TrimSpace(s)
					break
				}
			}
		}
		if id == "" {
			for _, v := range row {
				if s, ok := v.(string); ok {
					id = strings.TrimSpace(s)
					break
				}
			}
		}
		if id == "" {
			continue
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
	if os.Geteuid() != 0 {
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

func applyPBSRemoteCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	remoteRaw, present, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/remote.cfg")
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	sections, err := parseProxmoxNotificationSections(remoteRaw)
	if err != nil {
		return fmt.Errorf("parse staged remote.cfg: %w", err)
	}

	desired := make(map[string]proxmoxNotificationSection, len(sections))
	for _, s := range sections {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		desired[name] = s
	}

	if strict {
		out, err := runPBSManager(ctx, "remote", "list", "--output-format=json")
		if err != nil {
			return err
		}
		current, err := parsePBSListIDs(out, "id", "name")
		if err != nil {
			return fmt.Errorf("parse remote list: %w", err)
		}
		for _, id := range current {
			if _, ok := desired[id]; ok {
				continue
			}
			if _, err := runPBSManager(ctx, "remote", "remove", id); err != nil {
				logger.Warning("PBS API apply: remote remove %s failed (continuing): %v", id, err)
			}
		}
	}

	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := desired[id]
		flags := buildProxmoxManagerFlags(s.Entries)
		createArgs := append([]string{"remote", "create", id}, flags...)
		if _, err := runPBSManagerSensitive(ctx, createArgs, "--password"); err != nil {
			updateArgs := append([]string{"remote", "update", id}, flags...)
			if _, upErr := runPBSManagerSensitive(ctx, updateArgs, "--password"); upErr != nil {
				return fmt.Errorf("remote %s: %v (create) / %v (update)", id, err, upErr)
			}
		}
	}

	return nil
}

func applyPBSS3CfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	s3Raw, present, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/s3.cfg")
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	sections, err := parseProxmoxNotificationSections(s3Raw)
	if err != nil {
		return fmt.Errorf("parse staged s3.cfg: %w", err)
	}

	desired := make(map[string]proxmoxNotificationSection, len(sections))
	for _, s := range sections {
		id := strings.TrimSpace(s.Name)
		if id == "" {
			continue
		}
		desired[id] = s
	}

	if strict {
		out, err := runPBSManager(ctx, "s3", "endpoint", "list", "--output-format=json")
		if err != nil {
			return err
		}
		current, err := parsePBSListIDs(out, "id", "name")
		if err != nil {
			return fmt.Errorf("parse s3 endpoint list: %w", err)
		}
		for _, id := range current {
			if _, ok := desired[id]; ok {
				continue
			}
			if _, err := runPBSManager(ctx, "s3", "endpoint", "remove", id); err != nil {
				logger.Warning("PBS API apply: s3 endpoint remove %s failed (continuing): %v", id, err)
			}
		}
	}

	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := desired[id]
		flags := buildProxmoxManagerFlags(s.Entries)
		createArgs := append([]string{"s3", "endpoint", "create", id}, flags...)
		if _, err := runPBSManagerSensitive(ctx, createArgs, "--access-key", "--secret-key"); err != nil {
			updateArgs := append([]string{"s3", "endpoint", "update", id}, flags...)
			if _, upErr := runPBSManagerSensitive(ctx, updateArgs, "--access-key", "--secret-key"); upErr != nil {
				return fmt.Errorf("s3 endpoint %s: %v (create) / %v (update)", id, err, upErr)
			}
		}
	}

	return nil
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
	if out, err := runPBSManager(ctx, "datastore", "list", "--output-format=json"); err == nil {
		var rows []dsRow
		if err := json.Unmarshal(unwrapPBSJSONData(out), &rows); err == nil {
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
			}
		}
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
						return fmt.Errorf("datastore %s: path mismatch (%s != %s) and remove failed: %w", name, currentPath, path, err)
					}
					createArgs := append([]string{"datastore", "create", name, path}, flags...)
					if _, err := runPBSManager(ctx, createArgs...); err != nil {
						return fmt.Errorf("datastore %s: recreate after path mismatch failed: %w", name, err)
					}
					continue
				}
				logger.Warning("PBS API apply: datastore %s path mismatch (%s != %s); leaving path unchanged (use Clean 1:1 restore to enforce 1:1)", name, currentPath, path)
			}

			updateArgs := append([]string{"datastore", "update", name}, flags...)
			if _, err := runPBSManager(ctx, updateArgs...); err != nil {
				return fmt.Errorf("datastore %s: update failed: %w", name, err)
			}
			continue
		}

		createArgs := append([]string{"datastore", "create", name, path}, flags...)
		if _, err := runPBSManager(ctx, createArgs...); err != nil {
			updateArgs := append([]string{"datastore", "update", name}, flags...)
			if _, upErr := runPBSManager(ctx, updateArgs...); upErr != nil {
				return fmt.Errorf("datastore %s: %v (create) / %v (update)", name, err, upErr)
			}
		}
	}

	return nil
}

func applyPBSSyncCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	raw, present, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/sync.cfg")
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	sections, err := parseProxmoxNotificationSections(raw)
	if err != nil {
		return fmt.Errorf("parse staged sync.cfg: %w", err)
	}

	desired := make(map[string]proxmoxNotificationSection, len(sections))
	for _, s := range sections {
		id := strings.TrimSpace(s.Name)
		if id == "" {
			continue
		}
		desired[id] = s
	}

	if strict {
		out, err := runPBSManager(ctx, "sync-job", "list", "--output-format=json")
		if err != nil {
			return err
		}
		current, err := parsePBSListIDs(out, "id", "name")
		if err != nil {
			return fmt.Errorf("parse sync-job list: %w", err)
		}
		for _, id := range current {
			if _, ok := desired[id]; ok {
				continue
			}
			if _, err := runPBSManager(ctx, "sync-job", "remove", id); err != nil {
				logger.Warning("PBS API apply: sync-job remove %s failed (continuing): %v", id, err)
			}
		}
	}

	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := desired[id]
		flags := buildProxmoxManagerFlags(s.Entries)
		createArgs := append([]string{"sync-job", "create", id}, flags...)
		if _, err := runPBSManager(ctx, createArgs...); err != nil {
			updateArgs := append([]string{"sync-job", "update", id}, flags...)
			if _, upErr := runPBSManager(ctx, updateArgs...); upErr != nil {
				return fmt.Errorf("sync-job %s: %v (create) / %v (update)", id, err, upErr)
			}
		}
	}

	return nil
}

func applyPBSVerificationCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	raw, present, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/verification.cfg")
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	sections, err := parseProxmoxNotificationSections(raw)
	if err != nil {
		return fmt.Errorf("parse staged verification.cfg: %w", err)
	}

	desired := make(map[string]proxmoxNotificationSection, len(sections))
	for _, s := range sections {
		id := strings.TrimSpace(s.Name)
		if id == "" {
			continue
		}
		desired[id] = s
	}

	if strict {
		out, err := runPBSManager(ctx, "verify-job", "list", "--output-format=json")
		if err != nil {
			return err
		}
		current, err := parsePBSListIDs(out, "id", "name")
		if err != nil {
			return fmt.Errorf("parse verify-job list: %w", err)
		}
		for _, id := range current {
			if _, ok := desired[id]; ok {
				continue
			}
			if _, err := runPBSManager(ctx, "verify-job", "remove", id); err != nil {
				logger.Warning("PBS API apply: verify-job remove %s failed (continuing): %v", id, err)
			}
		}
	}

	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := desired[id]
		flags := buildProxmoxManagerFlags(s.Entries)
		createArgs := append([]string{"verify-job", "create", id}, flags...)
		if _, err := runPBSManager(ctx, createArgs...); err != nil {
			updateArgs := append([]string{"verify-job", "update", id}, flags...)
			if _, upErr := runPBSManager(ctx, updateArgs...); upErr != nil {
				return fmt.Errorf("verify-job %s: %v (create) / %v (update)", id, err, upErr)
			}
		}
	}

	return nil
}

func applyPBSPruneCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	raw, present, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/prune.cfg")
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	sections, err := parseProxmoxNotificationSections(raw)
	if err != nil {
		return fmt.Errorf("parse staged prune.cfg: %w", err)
	}

	desired := make(map[string]proxmoxNotificationSection, len(sections))
	for _, s := range sections {
		id := strings.TrimSpace(s.Name)
		if id == "" {
			continue
		}
		desired[id] = s
	}

	if strict {
		out, err := runPBSManager(ctx, "prune-job", "list", "--output-format=json")
		if err != nil {
			return err
		}
		current, err := parsePBSListIDs(out, "id", "name")
		if err != nil {
			return fmt.Errorf("parse prune-job list: %w", err)
		}
		for _, id := range current {
			if _, ok := desired[id]; ok {
				continue
			}
			if _, err := runPBSManager(ctx, "prune-job", "remove", id); err != nil {
				logger.Warning("PBS API apply: prune-job remove %s failed (continuing): %v", id, err)
			}
		}
	}

	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := desired[id]
		flags := buildProxmoxManagerFlags(s.Entries)
		createArgs := append([]string{"prune-job", "create", id}, flags...)
		if _, err := runPBSManager(ctx, createArgs...); err != nil {
			updateArgs := append([]string{"prune-job", "update", id}, flags...)
			if _, upErr := runPBSManager(ctx, updateArgs...); upErr != nil {
				return fmt.Errorf("prune-job %s: %v (create) / %v (update)", id, err, upErr)
			}
		}
	}

	return nil
}

func applyPBSTrafficControlCfgViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	raw, present, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/traffic-control.cfg")
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	sections, err := parseProxmoxNotificationSections(raw)
	if err != nil {
		return fmt.Errorf("parse staged traffic-control.cfg: %w", err)
	}

	desired := make(map[string]proxmoxNotificationSection, len(sections))
	for _, s := range sections {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		desired[name] = s
	}

	if strict {
		out, err := runPBSManager(ctx, "traffic-control", "list", "--output-format=json")
		if err != nil {
			return err
		}
		current, err := parsePBSListIDs(out, "name", "id")
		if err != nil {
			return fmt.Errorf("parse traffic-control list: %w", err)
		}
		for _, name := range current {
			if _, ok := desired[name]; ok {
				continue
			}
			if _, err := runPBSManager(ctx, "traffic-control", "remove", name); err != nil {
				logger.Warning("PBS API apply: traffic-control remove %s failed (continuing): %v", name, err)
			}
		}
	}

	names := make([]string, 0, len(desired))
	for name := range desired {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := desired[name]
		flags := buildProxmoxManagerFlags(s.Entries)
		createArgs := append([]string{"traffic-control", "create", name}, flags...)
		if _, err := runPBSManager(ctx, createArgs...); err != nil {
			updateArgs := append([]string{"traffic-control", "update", name}, flags...)
			if _, upErr := runPBSManager(ctx, updateArgs...); upErr != nil {
				return fmt.Errorf("traffic-control %s: %v (create) / %v (update)", name, err, upErr)
			}
		}
	}

	return nil
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
