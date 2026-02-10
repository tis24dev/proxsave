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

const (
	pveUserCfgPath    = "/etc/pve/user.cfg"
	pveDomainsCfgPath = "/etc/pve/domains.cfg"
	pveShadowCfgPath  = "/etc/pve/priv/shadow.cfg"
	pveTokenCfgPath   = "/etc/pve/priv/token.cfg"
	pveTFACfgPath     = "/etc/pve/priv/tfa.cfg"

	pbsUserCfgPath     = "/etc/proxmox-backup/user.cfg"
	pbsDomainsCfgPath  = "/etc/proxmox-backup/domains.cfg"
	pbsACLCfgPath      = "/etc/proxmox-backup/acl.cfg"
	pbsTokenCfgPath    = "/etc/proxmox-backup/token.cfg"
	pbsShadowJSONPath  = "/etc/proxmox-backup/shadow.json"
	pbsTokenShadowPath = "/etc/proxmox-backup/token.shadow"
	pbsTFAJSONPath     = "/etc/proxmox-backup/tfa.json"
)

func maybeApplyAccessControlFromStage(ctx context.Context, logger *logging.Logger, plan *RestorePlan, stageRoot string, dryRun bool) (err error) {
	if plan == nil {
		return nil
	}
	if strings.TrimSpace(stageRoot) == "" {
		logging.DebugStep(logger, "access control staged apply", "Skipped: staging directory not available")
		return nil
	}
	if !plan.HasCategoryID("pve_access_control") && !plan.HasCategoryID("pbs_access_control") {
		return nil
	}

	// Cluster backups: avoid applying PVE access control in SAFE mode.
	// Full-fidelity access control + secrets restore requires cluster RECOVERY (config.db) on an isolated/offline cluster.
	if plan.SystemType == SystemTypePVE &&
		plan.HasCategoryID("pve_access_control") &&
		plan.ClusterBackup &&
		!plan.NeedsClusterRestore {
		logger.Warning("PVE access control: cluster backup detected; skipping 1:1 access control apply in SAFE mode (use cluster RECOVERY for full fidelity)")
		return nil
	}

	done := logging.DebugStart(logger, "access control staged apply", "dryRun=%v stage=%s", dryRun, stageRoot)
	defer func() { done(err) }()

	if dryRun {
		logger.Info("Dry run enabled: skipping staged access control apply")
		return nil
	}
	if !isRealRestoreFS(restoreFS) {
		logger.Debug("Skipping staged access control apply: non-system filesystem in use")
		return nil
	}
	if os.Geteuid() != 0 {
		logger.Warning("Skipping staged access control apply: requires root privileges")
		return nil
	}

	switch plan.SystemType {
	case SystemTypePBS:
		if !plan.HasCategoryID("pbs_access_control") {
			return nil
		}
		return applyPBSAccessControlFromStage(ctx, logger, stageRoot)
	case SystemTypePVE:
		if !plan.HasCategoryID("pve_access_control") {
			return nil
		}

		// In cluster RECOVERY mode, config.db restoration owns access control state (including secrets).
		if plan.NeedsClusterRestore {
			logging.DebugStep(logger, "access control staged apply", "Skip PVE access control apply: cluster RECOVERY restores config.db")
			return nil
		}

		return applyPVEAccessControlFromStage(ctx, logger, stageRoot)
	default:
		return nil
	}
}

func applyPBSAccessControlFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) (err error) {
	done := logging.DebugStart(logger, "pbs access control apply", "stage=%s", stageRoot)
	defer func() { done(err) }()

	stagedUserRaw, userPresent, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/user.cfg")
	if err != nil {
		return err
	}
	stagedDomainsRaw, domainsPresent, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/domains.cfg")
	if err != nil {
		return err
	}
	stagedACLRaw, aclPresent, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/acl.cfg")
	if err != nil {
		return err
	}
	stagedTokenCfgRaw, tokenCfgPresent, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/token.cfg")
	if err != nil {
		return err
	}

	if userPresent || domainsPresent || aclPresent || tokenCfgPresent {
		currentUserSections, _ := readProxmoxConfigSectionsOptional(pbsUserCfgPath)
		currentDomainSections, _ := readProxmoxConfigSectionsOptional(pbsDomainsCfgPath)
		currentTokenCfgSections, _ := readProxmoxConfigSectionsOptional(pbsTokenCfgPath)

		backupUserSections, err := parseProxmoxNotificationSections(stagedUserRaw)
		if err != nil {
			return fmt.Errorf("parse staged user.cfg: %w", err)
		}
		backupDomainSections, err := parseProxmoxNotificationSections(stagedDomainsRaw)
		if err != nil {
			return fmt.Errorf("parse staged domains.cfg: %w", err)
		}
		backupTokenCfgSections, err := parseProxmoxNotificationSections(stagedTokenCfgRaw)
		if err != nil {
			return fmt.Errorf("parse staged token.cfg: %w", err)
		}

		rootUser := findPBSRootUserSection(currentUserSections)
		if rootUser == nil {
			rootUser = &proxmoxNotificationSection{
				Type: "user",
				Name: "root@pam",
				Entries: []proxmoxNotificationEntry{
					{Key: "enable", Value: "1"},
				},
			}
		}
		rootTokens := findPBSRootTokenSections(currentUserSections)

		// Merge user.cfg: restore 1:1 except root users/tokens (preserve from fresh install).
		mergedUser := make([]proxmoxNotificationSection, 0, len(backupUserSections)+1+len(rootTokens))
		for _, s := range backupUserSections {
			if strings.EqualFold(strings.TrimSpace(s.Type), "user") && isRootPBSUserID(strings.TrimSpace(s.Name)) {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(s.Type), "token") && isRootPBSAuthID(strings.TrimSpace(s.Name)) {
				continue
			}
			mergedUser = append(mergedUser, s)
		}
		mergedUser = append(mergedUser, *rootUser)
		mergedUser = append(mergedUser, rootTokens...)

		needsPBSRealm := anyUserInRealm(mergedUser, "pbs")
		requiredRealms := []string{"pam"}
		if needsPBSRealm {
			requiredRealms = append(requiredRealms, "pbs")
		}
		mergedDomains := mergeRequiredRealms(backupDomainSections, currentDomainSections, requiredRealms)

		// Merge token.cfg (if present): restore 1:1 except root-bound entries (preserve from fresh install).
		mergedTokenCfg := mergeUserBoundSectionsExcludeRoot(backupTokenCfgSections, currentTokenCfgSections, tokenSectionUserID, "root@pam")

		if domainsPresent {
			if err := writeFileAtomic(pbsDomainsCfgPath, []byte(renderProxmoxConfig(mergedDomains)), 0o640); err != nil {
				return fmt.Errorf("write %s: %w", pbsDomainsCfgPath, err)
			}
		}
		if userPresent {
			if err := writeFileAtomic(pbsUserCfgPath, []byte(renderProxmoxConfig(mergedUser)), 0o640); err != nil {
				return fmt.Errorf("write %s: %w", pbsUserCfgPath, err)
			}
		}
		if tokenCfgPresent {
			if err := writeFileAtomic(pbsTokenCfgPath, []byte(renderProxmoxConfig(mergedTokenCfg)), 0o640); err != nil {
				return fmt.Errorf("write %s: %w", pbsTokenCfgPath, err)
			}
		}

		if aclPresent {
			if err := applyPBSACLFromStage(logger, stagedACLRaw); err != nil {
				return err
			}
		}
	}

	// Restore secrets 1:1 except root@pam (preserve from fresh install).
	if err := applyPBSShadowJSONFromStage(logger, stageRoot); err != nil {
		return err
	}
	if err := applyPBSTokenShadowFromStage(logger, stageRoot); err != nil {
		return err
	}
	if err := applyPBSTFAJSONFromStage(logger, stageRoot); err != nil {
		return err
	}

	logger.Warning("PBS access control: restored 1:1 from backup; root@pam preserved from fresh install and kept Admin on /")
	logger.Warning("PBS access control: TFA was restored 1:1; users with WebAuthn may require re-enrollment if origin/hostname changed (for best compatibility, keep the same FQDN/origin and restore network+ssl; default behavior is warn, not disable)")

	return nil
}

func applySensitiveFileFromStage(logger *logging.Logger, stageRoot, relPath, destPath string, perm os.FileMode) error {
	stagePath := filepath.Join(stageRoot, relPath)
	data, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logging.DebugStep(logger, "access control staged apply file", "Skip %s: not present in staging directory", relPath)
			return nil
		}
		return fmt.Errorf("read staged %s: %w", relPath, err)
	}

	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		logger.Warning("Access control staged apply: %s is empty; removing %s", relPath, destPath)
		return removeIfExists(destPath)
	}

	return writeFileAtomic(destPath, []byte(trimmed+"\n"), perm)
}

func isRootPBSUserID(userID string) bool {
	trimmed := strings.TrimSpace(userID)
	if trimmed == "" {
		return false
	}
	if idx := strings.Index(trimmed, "@"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return strings.TrimSpace(trimmed) == "root"
}

func isRootPBSAuthID(authID string) bool {
	trimmed := strings.TrimSpace(authID)
	if trimmed == "" {
		return false
	}
	if idx := strings.Index(trimmed, "!"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return isRootPBSUserID(trimmed)
}

func findPBSRootUserSection(sections []proxmoxNotificationSection) *proxmoxNotificationSection {
	for i := range sections {
		if !strings.EqualFold(strings.TrimSpace(sections[i].Type), "user") {
			continue
		}
		if isRootPBSUserID(strings.TrimSpace(sections[i].Name)) {
			return &sections[i]
		}
	}
	return nil
}

func findPBSRootTokenSections(sections []proxmoxNotificationSection) []proxmoxNotificationSection {
	var out []proxmoxNotificationSection
	for _, s := range sections {
		if !strings.EqualFold(strings.TrimSpace(s.Type), "token") {
			continue
		}
		if isRootPBSAuthID(strings.TrimSpace(s.Name)) {
			out = append(out, s)
		}
	}
	return out
}

func applyPBSACLFromStage(logger *logging.Logger, stagedACL string) error {
	raw := strings.TrimSpace(stagedACL)
	if raw == "" {
		logger.Warning("PBS access control: staged acl.cfg is empty; removing %s", pbsACLCfgPath)
		return removeIfExists(pbsACLCfgPath)
	}

	// PBS supports two ACL formats across versions:
	// - header-style (section + indented keys)
	// - colon-delimited line format (acl:<propagate>:<path>:<userlist>:<rolelist>)
	if pbsConfigHasHeader(raw) {
		return applyPBSACLSectionFormat(logger, raw)
	}
	if isPBSACLLineFormat(raw) {
		return applyPBSACLLineFormat(logger, raw)
	}

	logger.Warning("PBS access control: staged acl.cfg has unknown format; skipping apply")
	return nil
}

func applyPBSACLSectionFormat(logger *logging.Logger, raw string) error {
	backupSections, err := parseProxmoxNotificationSections(raw)
	if err != nil {
		return fmt.Errorf("parse staged acl.cfg: %w", err)
	}

	var merged []proxmoxNotificationSection
	for _, s := range backupSections {
		if !strings.EqualFold(strings.TrimSpace(s.Type), "acl") {
			merged = append(merged, s)
			continue
		}
		users := findSectionEntryValue(s.Entries, "users")
		filtered, ok := filterPBSACLUsers(users)
		if !ok {
			continue
		}
		s.Entries = setSectionEntryValue(s.Entries, "users", filtered)
		merged = append(merged, s)
	}

	if !hasPBSRootAdminOnRootSectionFormat(merged) {
		merged = append(merged, proxsavePBSRootAdminACLSection())
	}

	if err := writeFileAtomic(pbsACLCfgPath, []byte(renderProxmoxConfig(merged)), 0o640); err != nil {
		return fmt.Errorf("write %s: %w", pbsACLCfgPath, err)
	}
	return nil
}

type pbsACLLine struct {
	Propagate string
	Path      string
	UserList  string
	Roles     string
	Raw       string
}

func isPBSACLLineFormat(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(trimmed, "acl:") {
			return false
		}
		parts := strings.SplitN(trimmed, ":", 5)
		return len(parts) == 5
	}
	return false
}

func parsePBSACLLine(line string) (pbsACLLine, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return pbsACLLine{}, false
	}
	if !strings.HasPrefix(trimmed, "acl:") {
		return pbsACLLine{Raw: trimmed}, true
	}

	parts := strings.SplitN(trimmed, ":", 5)
	if len(parts) != 5 || strings.TrimSpace(parts[0]) != "acl" {
		return pbsACLLine{Raw: trimmed}, true
	}
	return pbsACLLine{
		Propagate: strings.TrimSpace(parts[1]),
		Path:      strings.TrimSpace(parts[2]),
		UserList:  strings.TrimSpace(parts[3]),
		Roles:     strings.TrimSpace(parts[4]),
		Raw:       trimmed,
	}, true
}

func applyPBSACLLineFormat(logger *logging.Logger, raw string) error {
	var outLines []string
	var hasRootAdmin bool

	for _, line := range strings.Split(raw, "\n") {
		entry, ok := parsePBSACLLine(line)
		if !ok {
			continue
		}
		if strings.TrimSpace(entry.Raw) != "" && !strings.HasPrefix(entry.Raw, "acl:") {
			// Preserve unknown non-empty lines verbatim.
			outLines = append(outLines, entry.Raw)
			continue
		}

		filteredUsers, ok := filterPBSACLUsers(entry.UserList)
		if !ok {
			continue
		}
		entry.UserList = filteredUsers

		if entry.Path == "/" && entry.Propagate == "1" && listContains(entry.UserList, "root@pam") && listContains(entry.Roles, "Admin") {
			hasRootAdmin = true
		}

		outLines = append(outLines, fmt.Sprintf("acl:%s:%s:%s:%s", entry.Propagate, entry.Path, entry.UserList, entry.Roles))
	}

	if !hasRootAdmin {
		outLines = append(outLines, "acl:1:/:root@pam:Admin")
	}

	content := strings.TrimSpace(strings.Join(outLines, "\n"))
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := writeFileAtomic(pbsACLCfgPath, []byte(content), 0o640); err != nil {
		return fmt.Errorf("write %s: %w", pbsACLCfgPath, err)
	}
	return nil
}

func filterPBSACLUsers(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}

	trimmed = strings.ReplaceAll(trimmed, ",", " ")
	var kept []string
	for _, field := range strings.Fields(trimmed) {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if isRootPBSAuthID(field) {
			continue
		}
		kept = append(kept, field)
	}
	if len(kept) == 0 {
		return "", false
	}
	return strings.Join(kept, ","), true
}

func setSectionEntryValue(entries []proxmoxNotificationEntry, key, value string) []proxmoxNotificationEntry {
	match := strings.TrimSpace(key)
	if match == "" {
		return entries
	}
	out := make([]proxmoxNotificationEntry, 0, len(entries)+1)
	found := false
	for _, e := range entries {
		if strings.TrimSpace(e.Key) == match {
			if !found {
				out = append(out, proxmoxNotificationEntry{Key: match, Value: strings.TrimSpace(value)})
				found = true
			}
			continue
		}
		out = append(out, e)
	}
	if !found {
		out = append(out, proxmoxNotificationEntry{Key: match, Value: strings.TrimSpace(value)})
	}
	return out
}

func hasPBSRootAdminOnRootSectionFormat(sections []proxmoxNotificationSection) bool {
	for _, s := range sections {
		if strings.TrimSpace(s.Type) != "acl" {
			continue
		}
		path := strings.TrimSpace(findSectionEntryValue(s.Entries, "path"))
		if path == "" {
			path = aclPathFromSectionName(s.Name)
		}
		if path != "/" {
			continue
		}

		users := strings.TrimSpace(findSectionEntryValue(s.Entries, "users"))
		roles := strings.TrimSpace(findSectionEntryValue(s.Entries, "roles"))
		if listContains(users, "root@pam") && listContains(roles, "Admin") {
			return true
		}
	}
	return false
}

func proxsavePBSRootAdminACLSection() proxmoxNotificationSection {
	return proxmoxNotificationSection{
		Type: "acl",
		Name: "proxsave-root-admin",
		Entries: []proxmoxNotificationEntry{
			{Key: "path", Value: "/"},
			{Key: "users", Value: "root@pam"},
			{Key: "roles", Value: "Admin"},
			{Key: "propagate", Value: "1"},
		},
	}
}

func applyPBSShadowJSONFromStage(logger *logging.Logger, stageRoot string) error {
	stagePath := filepath.Join(stageRoot, "etc/proxmox-backup/shadow.json")
	backupBytes, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read staged shadow.json: %w", err)
	}
	trimmed := strings.TrimSpace(string(backupBytes))
	if trimmed == "" {
		logger.Warning("PBS access control: staged shadow.json is empty; removing %s", pbsShadowJSONPath)
		return removeIfExists(pbsShadowJSONPath)
	}

	var backup map[string]string
	if err := json.Unmarshal([]byte(trimmed), &backup); err != nil {
		return fmt.Errorf("parse staged shadow.json: %w", err)
	}

	currentBytes, err := restoreFS.ReadFile(pbsShadowJSONPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read current shadow.json: %w", err)
	}
	var current map[string]string
	if len(currentBytes) > 0 {
		_ = json.Unmarshal(currentBytes, &current)
	}

	for userID := range backup {
		if isRootPBSUserID(userID) {
			delete(backup, userID)
		}
	}
	for userID, hash := range current {
		if isRootPBSUserID(userID) {
			backup[userID] = hash
		}
	}

	out, err := json.Marshal(backup)
	if err != nil {
		return fmt.Errorf("marshal shadow.json: %w", err)
	}
	return writeFileAtomic(pbsShadowJSONPath, append(out, '\n'), 0o600)
}

func applyPBSTokenShadowFromStage(logger *logging.Logger, stageRoot string) error {
	stagePath := filepath.Join(stageRoot, "etc/proxmox-backup/token.shadow")
	backupBytes, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read staged token.shadow: %w", err)
	}
	trimmed := strings.TrimSpace(string(backupBytes))
	if trimmed == "" {
		logger.Warning("PBS access control: staged token.shadow is empty; removing %s", pbsTokenShadowPath)
		return removeIfExists(pbsTokenShadowPath)
	}

	var backup map[string]string
	if err := json.Unmarshal([]byte(trimmed), &backup); err != nil {
		return fmt.Errorf("parse staged token.shadow: %w", err)
	}

	currentBytes, err := restoreFS.ReadFile(pbsTokenShadowPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read current token.shadow: %w", err)
	}
	var current map[string]string
	if len(currentBytes) > 0 {
		_ = json.Unmarshal(currentBytes, &current)
	}

	for tokenID := range backup {
		if isRootPBSAuthID(tokenID) {
			delete(backup, tokenID)
		}
	}
	for tokenID, secret := range current {
		if isRootPBSAuthID(tokenID) {
			backup[tokenID] = secret
		}
	}

	out, err := json.Marshal(backup)
	if err != nil {
		return fmt.Errorf("marshal token.shadow: %w", err)
	}
	return writeFileAtomic(pbsTokenShadowPath, append(out, '\n'), 0o600)
}

func applyPBSTFAJSONFromStage(logger *logging.Logger, stageRoot string) error {
	stagePath := filepath.Join(stageRoot, "etc/proxmox-backup/tfa.json")
	backupBytes, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read staged tfa.json: %w", err)
	}
	trimmed := strings.TrimSpace(string(backupBytes))
	if trimmed == "" {
		logger.Warning("PBS access control: staged tfa.json is empty; removing %s", pbsTFAJSONPath)
		return removeIfExists(pbsTFAJSONPath)
	}

	var backup map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &backup); err != nil {
		return fmt.Errorf("parse staged tfa.json: %w", err)
	}

	currentBytes, err := restoreFS.ReadFile(pbsTFAJSONPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read current tfa.json: %w", err)
	}
	var current map[string]json.RawMessage
	if len(currentBytes) > 0 {
		_ = json.Unmarshal(currentBytes, &current)
	}

	backupUsers := parseTFAUsersMap(backup)
	currentUsers := parseTFAUsersMap(current)

	for userID := range backupUsers {
		if isRootPBSUserID(userID) {
			delete(backupUsers, userID)
		}
	}
	for userID, payload := range currentUsers {
		if isRootPBSUserID(userID) {
			backupUsers[userID] = payload
		}
	}

	webauthnUsers := extractWebAuthnUsersFromPBSTFAUsers(backupUsers)
	if len(webauthnUsers) > 0 {
		logger.Warning("PBS TFA/WebAuthn: detected %d enrolled user(s): %s", len(webauthnUsers), summarizeUserIDs(webauthnUsers, 8))
	}
	backup["users"] = mustMarshalRaw(backupUsers)

	out, err := json.Marshal(backup)
	if err != nil {
		return fmt.Errorf("marshal tfa.json: %w", err)
	}
	return writeFileAtomic(pbsTFAJSONPath, append(out, '\n'), 0o600)
}

func parseTFAUsersMap(obj map[string]json.RawMessage) map[string]json.RawMessage {
	if obj == nil {
		return map[string]json.RawMessage{}
	}
	raw, ok := obj["users"]
	if !ok || len(raw) == 0 {
		return map[string]json.RawMessage{}
	}
	var users map[string]json.RawMessage
	if err := json.Unmarshal(raw, &users); err != nil || users == nil {
		return map[string]json.RawMessage{}
	}
	return users
}

func mustMarshalRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage([]byte("{}"))
	}
	return json.RawMessage(b)
}

func applyPVEAccessControlFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) (err error) {
	done := logging.DebugStart(logger, "pve access control apply", "stage=%s", stageRoot)
	defer func() { done(err) }()

	// Safety: only apply to the real pmxcfs mount. If /etc/pve is not mounted, writing here would
	// shadow files on the root filesystem and can break a fresh install.
	if isRealRestoreFS(restoreFS) {
		mounted, mountErr := isMounted("/etc/pve")
		if mountErr != nil {
			logger.Warning("PVE access control: unable to check pmxcfs mount status for /etc/pve: %v", mountErr)
		} else if !mounted {
			return fmt.Errorf("refusing PVE access control apply: /etc/pve is not mounted (pmxcfs not available)")
		}
	}

	userCfgRaw, userCfgPresent, err := readStageFileOptional(stageRoot, "etc/pve/user.cfg")
	if err != nil {
		return err
	}
	domainsCfgRaw, domainsCfgPresent, err := readStageFileOptional(stageRoot, "etc/pve/domains.cfg")
	if err != nil {
		return err
	}
	shadowRaw, shadowPresent, err := readStageFileOptional(stageRoot, "etc/pve/priv/shadow.cfg")
	if err != nil {
		return err
	}
	tokenRaw, tokenPresent, err := readStageFileOptional(stageRoot, "etc/pve/priv/token.cfg")
	if err != nil {
		return err
	}
	tfaRaw, tfaPresent, err := readStageFileOptional(stageRoot, "etc/pve/priv/tfa.cfg")
	if err != nil {
		return err
	}

	if !userCfgPresent && !domainsCfgPresent && !shadowPresent && !tokenPresent && !tfaPresent {
		logging.DebugStep(logger, "pve access control apply", "No PVE access control files found in staging; skipping")
		return nil
	}

	backupUserSections, err := parseProxmoxNotificationSections(userCfgRaw)
	if err != nil {
		return fmt.Errorf("parse staged user.cfg: %w", err)
	}
	backupDomainSections, err := parseProxmoxNotificationSections(domainsCfgRaw)
	if err != nil {
		return fmt.Errorf("parse staged domains.cfg: %w", err)
	}
	backupShadowSections, err := parseProxmoxNotificationSections(shadowRaw)
	if err != nil {
		return fmt.Errorf("parse staged priv/shadow.cfg: %w", err)
	}
	backupTokenSections, err := parseProxmoxNotificationSections(tokenRaw)
	if err != nil {
		return fmt.Errorf("parse staged priv/token.cfg: %w", err)
	}
	backupTFASections, err := parseProxmoxNotificationSections(tfaRaw)
	if err != nil {
		return fmt.Errorf("parse staged priv/tfa.cfg: %w", err)
	}

	currentUserSections, _ := readProxmoxConfigSectionsOptional(pveUserCfgPath)
	currentDomainSections, _ := readProxmoxConfigSectionsOptional(pveDomainsCfgPath)
	currentShadowSections, _ := readProxmoxConfigSectionsOptional(pveShadowCfgPath)
	currentTokenSections, _ := readProxmoxConfigSectionsOptional(pveTokenCfgPath)
	currentTFASections, _ := readProxmoxConfigSectionsOptional(pveTFACfgPath)

	rootUser := findSection(currentUserSections, "user", "root@pam")
	if rootUser == nil {
		rootUser = &proxmoxNotificationSection{
			Type: "user",
			Name: "root@pam",
			Entries: []proxmoxNotificationEntry{
				{Key: "enable", Value: "1"},
			},
		}
	}

	// Merge user.cfg: restore 1:1 except root@pam (preserve from fresh install), and ensure root has Administrator on "/".
	mergedUser := make([]proxmoxNotificationSection, 0, len(backupUserSections)+2)
	for _, s := range backupUserSections {
		if strings.EqualFold(strings.TrimSpace(s.Type), "user") && strings.TrimSpace(s.Name) == "root@pam" {
			continue
		}
		mergedUser = append(mergedUser, s)
	}
	mergedUser = append(mergedUser, *rootUser)
	if !hasRootAdminOnRoot(mergedUser) {
		mergedUser = append(mergedUser, proxsaveRootAdminACLSection())
	}

	needsPVERealm := anyUserInRealm(mergedUser, "pve")
	requiredRealms := []string{"pam"}
	if needsPVERealm {
		requiredRealms = append(requiredRealms, "pve")
	}
	mergedDomains := mergeRequiredRealms(backupDomainSections, currentDomainSections, requiredRealms)

	// Merge secrets: restore 1:1 except root@pam token/TFA entries (preserve from fresh install).
	mergedTokens := mergeUserBoundSectionsExcludeRoot(backupTokenSections, currentTokenSections, tokenSectionUserID, "root@pam")
	mergedTFA := mergeUserBoundSectionsExcludeRoot(backupTFASections, currentTFASections, tfaSectionUserID, "root@pam")
	mergedShadow := mergeUserBoundSectionsExcludeRoot(backupShadowSections, currentShadowSections, shadowSectionUserID, "root@pam")

	if domainsCfgPresent {
		if err := writeFileAtomic(pveDomainsCfgPath, []byte(renderProxmoxConfig(mergedDomains)), 0o640); err != nil {
			return fmt.Errorf("write %s: %w", pveDomainsCfgPath, err)
		}
	}
	if userCfgPresent {
		if err := writeFileAtomic(pveUserCfgPath, []byte(renderProxmoxConfig(mergedUser)), 0o640); err != nil {
			return fmt.Errorf("write %s: %w", pveUserCfgPath, err)
		}
	}
	if shadowPresent {
		if err := writeFileAtomic(pveShadowCfgPath, []byte(renderProxmoxConfig(mergedShadow)), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", pveShadowCfgPath, err)
		}
	}
	if tokenPresent {
		if err := writeFileAtomic(pveTokenCfgPath, []byte(renderProxmoxConfig(mergedTokens)), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", pveTokenCfgPath, err)
		}
	}
	if tfaPresent {
		if err := writeFileAtomic(pveTFACfgPath, []byte(renderProxmoxConfig(mergedTFA)), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", pveTFACfgPath, err)
		}
	}

	logger.Warning("PVE access control: restored 1:1 from backup via pmxcfs; root@pam preserved from fresh install and kept Administrator on /")
	logger.Warning("PVE access control: TFA was restored 1:1; users with WebAuthn may require re-enrollment if origin/hostname changed (for best compatibility, keep the same FQDN/origin and restore network+ssl; default behavior is warn, not disable)")
	if tfaPresent {
		webauthnUsers := extractWebAuthnUsersFromPVETFA(mergedTFA)
		if len(webauthnUsers) > 0 {
			logger.Warning("PVE TFA/WebAuthn: detected %d enrolled user(s): %s", len(webauthnUsers), summarizeUserIDs(webauthnUsers, 8))
		}
	}
	return nil
}

func readStageFileOptional(stageRoot, relPath string) (content string, present bool, err error) {
	stagePath := filepath.Join(stageRoot, relPath)
	data, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read staged %s: %w", relPath, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return "", true, nil
	}
	return trimmed, true, nil
}

func readProxmoxConfigSectionsOptional(path string) ([]proxmoxNotificationSection, error) {
	data, err := restoreFS.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return nil, nil
	}
	return parseProxmoxNotificationSections(raw)
}

func renderProxmoxConfig(sections []proxmoxNotificationSection) string {
	if len(sections) == 0 {
		return ""
	}
	var b strings.Builder
	for i, s := range sections {
		typ := strings.TrimSpace(s.Type)
		name := strings.TrimSpace(s.Name)
		if typ == "" || name == "" {
			continue
		}
		if i > 0 && b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s: %s\n", typ, name)
		for _, kv := range s.Entries {
			key := strings.TrimSpace(kv.Key)
			if key == "" {
				continue
			}
			val := strings.TrimSpace(kv.Value)
			if val == "" {
				fmt.Fprintf(&b, "  %s\n", key)
				continue
			}
			fmt.Fprintf(&b, "  %s %s\n", key, val)
		}
	}
	out := b.String()
	if strings.TrimSpace(out) == "" {
		return ""
	}
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

func findSection(sections []proxmoxNotificationSection, typ, name string) *proxmoxNotificationSection {
	typ = strings.TrimSpace(typ)
	name = strings.TrimSpace(name)
	for i := range sections {
		if strings.EqualFold(strings.TrimSpace(sections[i].Type), typ) && strings.TrimSpace(sections[i].Name) == name {
			return &sections[i]
		}
	}
	return nil
}

func anyUserInRealm(sections []proxmoxNotificationSection, realm string) bool {
	realm = strings.TrimSpace(realm)
	if realm == "" {
		return false
	}
	for _, s := range sections {
		if !strings.EqualFold(strings.TrimSpace(s.Type), "user") {
			continue
		}
		if strings.EqualFold(userRealm(strings.TrimSpace(s.Name)), realm) {
			return true
		}
	}
	return false
}

func userRealm(userID string) string {
	userID = strings.TrimSpace(userID)
	idx := strings.LastIndex(userID, "@")
	if idx < 0 || idx+1 >= len(userID) {
		return ""
	}
	return strings.TrimSpace(userID[idx+1:])
}

func mergeRequiredRealms(backup, current []proxmoxNotificationSection, required []string) []proxmoxNotificationSection {
	type key struct {
		typ  string
		name string
	}
	seen := make(map[key]struct{})
	out := make([]proxmoxNotificationSection, 0, len(backup)+2)

	appendUnique := func(s proxmoxNotificationSection) {
		k := key{typ: strings.ToLower(strings.TrimSpace(s.Type)), name: strings.TrimSpace(s.Name)}
		if k.typ == "" || k.name == "" {
			return
		}
		if _, ok := seen[k]; ok {
			return
		}
		seen[k] = struct{}{}
		out = append(out, s)
	}

	// Start from backup (1:1).
	for _, s := range backup {
		appendUnique(s)
	}

	for _, realm := range required {
		realm = strings.TrimSpace(realm)
		if realm == "" {
			continue
		}

		// Domain section keys are `<type>: <realm>` (e.g. `pam: pam`, `pve: pve`, `ldap: myldap`).
		cur := findSection(current, realm, realm)
		if cur == nil {
			appendUnique(proxmoxNotificationSection{Type: realm, Name: realm})
			continue
		}

		// Override backup with the live realm definition for safety rail.
		k := key{typ: strings.ToLower(strings.TrimSpace(cur.Type)), name: strings.TrimSpace(cur.Name)}
		if k.typ == "" || k.name == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			for i := range out {
				if strings.EqualFold(strings.TrimSpace(out[i].Type), realm) && strings.TrimSpace(out[i].Name) == realm {
					out[i] = *cur
					break
				}
			}
			continue
		}
		seen[k] = struct{}{}
		out = append(out, *cur)
	}

	return out
}

func mergeUserBoundSectionsExcludeRoot(backup, current []proxmoxNotificationSection, userIDFn func(proxmoxNotificationSection) string, rootUserID string) []proxmoxNotificationSection {
	rootUserID = strings.TrimSpace(rootUserID)
	out := make([]proxmoxNotificationSection, 0, len(backup))
	for _, s := range backup {
		if strings.TrimSpace(rootUserID) != "" && strings.TrimSpace(userIDFn(s)) == rootUserID {
			continue
		}
		out = append(out, s)
	}
	for _, s := range current {
		if strings.TrimSpace(rootUserID) != "" && strings.TrimSpace(userIDFn(s)) == rootUserID {
			out = append(out, s)
		}
	}
	return out
}

func tokenSectionUserID(s proxmoxNotificationSection) string {
	if strings.TrimSpace(s.Type) != "token" {
		return ""
	}
	userID, _, ok := splitPVETokenSectionName(strings.TrimSpace(s.Name))
	if !ok {
		return ""
	}
	return userID
}

func tfaSectionUserID(s proxmoxNotificationSection) string {
	name := strings.TrimSpace(s.Name)
	if strings.Contains(name, "@") {
		return name
	}
	for _, kv := range s.Entries {
		if strings.EqualFold(strings.TrimSpace(kv.Key), "user") && strings.Contains(strings.TrimSpace(kv.Value), "@") {
			return strings.TrimSpace(kv.Value)
		}
	}
	return ""
}

func shadowSectionUserID(s proxmoxNotificationSection) string {
	if strings.EqualFold(strings.TrimSpace(s.Type), "user") && strings.Contains(strings.TrimSpace(s.Name), "@") {
		return strings.TrimSpace(s.Name)
	}
	if strings.Contains(strings.TrimSpace(s.Name), "@") {
		return strings.TrimSpace(s.Name)
	}
	for _, kv := range s.Entries {
		if strings.EqualFold(strings.TrimSpace(kv.Key), "userid") && strings.Contains(strings.TrimSpace(kv.Value), "@") {
			return strings.TrimSpace(kv.Value)
		}
	}
	return ""
}

func splitPVETokenSectionName(name string) (userID, tokenID string, ok bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", "", false
	}
	idx := strings.LastIndex(trimmed, "!")
	if idx <= 0 || idx+1 >= len(trimmed) {
		return "", "", false
	}
	userID = strings.TrimSpace(trimmed[:idx])
	tokenID = strings.TrimSpace(trimmed[idx+1:])
	if userID == "" || tokenID == "" {
		return "", "", false
	}
	return userID, tokenID, true
}

func hasRootAdminOnRoot(sections []proxmoxNotificationSection) bool {
	for _, s := range sections {
		if strings.TrimSpace(s.Type) != "acl" {
			continue
		}
		path := strings.TrimSpace(findSectionEntryValue(s.Entries, "path"))
		if path == "" {
			path = aclPathFromSectionName(s.Name)
		}
		if path != "/" {
			continue
		}

		users := strings.TrimSpace(findSectionEntryValue(s.Entries, "users"))
		roles := strings.TrimSpace(findSectionEntryValue(s.Entries, "roles"))
		if listContains(users, "root@pam") && listContains(roles, "Administrator") {
			return true
		}
	}
	return false
}

func proxsaveRootAdminACLSection() proxmoxNotificationSection {
	return proxmoxNotificationSection{
		Type: "acl",
		Name: "proxsave-root-admin",
		Entries: []proxmoxNotificationEntry{
			{Key: "path", Value: "/"},
			{Key: "users", Value: "root@pam"},
			{Key: "roles", Value: "Administrator"},
			{Key: "propagate", Value: "1"},
		},
	}
}

func findSectionEntryValue(entries []proxmoxNotificationEntry, key string) string {
	match := strings.TrimSpace(key)
	for _, e := range entries {
		if strings.TrimSpace(e.Key) == match {
			return strings.TrimSpace(e.Value)
		}
	}
	return ""
}

func listContains(raw, item string) bool {
	item = strings.TrimSpace(item)
	if item == "" {
		return false
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	trimmed = strings.ReplaceAll(trimmed, ",", " ")
	for _, field := range strings.Fields(trimmed) {
		if strings.TrimSpace(field) == item {
			return true
		}
	}
	return false
}

func aclPathFromSectionName(name string) string {
	for _, field := range strings.Fields(strings.TrimSpace(name)) {
		if strings.HasPrefix(field, "/") {
			return field
		}
	}
	return ""
}

func extractWebAuthnUsersFromPVETFA(sections []proxmoxNotificationSection) []string {
	if len(sections) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, 8)
	for _, s := range sections {
		typ := strings.ToLower(strings.TrimSpace(s.Type))
		if typ != "webauthn" && typ != "u2f" {
			continue
		}
		userID := strings.TrimSpace(tfaSectionUserID(s))
		if userID == "" || userID == "root@pam" {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		out = append(out, userID)
	}
	sort.Strings(out)
	return out
}

func extractWebAuthnUsersFromPBSTFAUsers(users map[string]json.RawMessage) []string {
	if len(users) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, 8)
	for userID, payload := range users {
		if isRootPBSUserID(userID) {
			continue
		}
		var methods map[string]json.RawMessage
		if err := json.Unmarshal(payload, &methods); err != nil {
			continue
		}
		if jsonRawNonNull(methods["webauthn"]) || jsonRawNonNull(methods["u2f"]) {
			if _, ok := seen[userID]; ok {
				continue
			}
			seen[userID] = struct{}{}
			out = append(out, userID)
		}
	}
	sort.Strings(out)
	return out
}

func jsonRawNonNull(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" || trimmed == "[]" {
		return false
	}
	return true
}

func summarizeUserIDs(userIDs []string, max int) string {
	if len(userIDs) == 0 {
		return ""
	}
	if max <= 0 {
		max = 10
	}
	if len(userIDs) <= max {
		return strings.Join(userIDs, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(userIDs[:max], ", "), len(userIDs)-max)
}
