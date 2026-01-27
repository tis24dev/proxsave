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

const (
	pveUserCfgPath    = "/etc/pve/user.cfg"
	pveDomainsCfgPath = "/etc/pve/domains.cfg"
	pveShadowCfgPath  = "/etc/pve/priv/shadow.cfg"
	pveTokenCfgPath   = "/etc/pve/priv/token.cfg"
	pveTFACfgPath     = "/etc/pve/priv/tfa.cfg"
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

	cfgPaths := []string{
		"etc/proxmox-backup/user.cfg",
		"etc/proxmox-backup/domains.cfg",
		"etc/proxmox-backup/acl.cfg",
		"etc/proxmox-backup/token.cfg",
	}
	for _, rel := range cfgPaths {
		if err := applyPBSConfigFileFromStage(ctx, logger, stageRoot, rel); err != nil {
			return err
		}
	}

	secretPaths := []struct {
		rel  string
		dest string
		mode os.FileMode
	}{
		{
			rel:  "etc/proxmox-backup/shadow.json",
			dest: "/etc/proxmox-backup/shadow.json",
			mode: 0o600,
		},
		{
			rel:  "etc/proxmox-backup/token.shadow",
			dest: "/etc/proxmox-backup/token.shadow",
			mode: 0o600,
		},
		{
			rel:  "etc/proxmox-backup/tfa.json",
			dest: "/etc/proxmox-backup/tfa.json",
			mode: 0o600,
		},
	}
	for _, item := range secretPaths {
		if err := applySensitiveFileFromStage(logger, stageRoot, item.rel, item.dest, item.mode); err != nil {
			return err
		}
	}

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
	mergedDomains := mergeRequiredRealms(backupDomainSections, currentDomainSections, []string{"pam"}, needsPVERealm)

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
	logger.Warning("PVE access control: TFA was restored 1:1; users with WebAuthn may require re-enrollment if origin/hostname changed (default behavior is warn, not disable)")
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

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := restoreFS.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmpPath := fmt.Sprintf("%s.proxsave.tmp.%d", path, nowRestore().UnixNano())
	f, err := restoreFS.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL|os.O_TRUNC, perm)
	if err != nil {
		return err
	}

	writeErr := func() error {
		if len(data) == 0 {
			return nil
		}
		_, err := f.Write(data)
		return err
	}()
	closeErr := f.Close()
	if writeErr != nil {
		_ = restoreFS.Remove(tmpPath)
		return writeErr
	}
	if closeErr != nil {
		_ = restoreFS.Remove(tmpPath)
		return closeErr
	}

	if err := restoreFS.Rename(tmpPath, path); err != nil {
		_ = restoreFS.Remove(tmpPath)
		return err
	}
	return nil
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

func mergeRequiredRealms(backup, current []proxmoxNotificationSection, always []string, includePVE bool) []proxmoxNotificationSection {
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

	required := append([]string(nil), always...)
	if includePVE {
		required = append(required, "pve")
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
