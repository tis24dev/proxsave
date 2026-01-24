package orchestrator

import (
	"context"
	"crypto/rand"
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

type pveAccessControlSecretsReport struct {
	GeneratedAt      string            `json:"generated_at"`
	System           string            `json:"system"`
	Notes            []string          `json:"notes,omitempty"`
	Users            []pveUserPassword `json:"users,omitempty"`
	APITokens        []pveAPIToken     `json:"api_tokens,omitempty"`
	TFAResetRequired []string          `json:"tfa_reset_required,omitempty"`
}

type pveUserPassword struct {
	UserID   string `json:"userid"`
	Password string `json:"password"`
}

type pveAPIToken struct {
	UserID      string `json:"userid"`
	TokenID     string `json:"tokenid"`
	FullTokenID string `json:"full_tokenid"`
	Value       string `json:"value"`
}

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
		if plan.NeedsClusterRestore {
			logging.DebugStep(logger, "access control staged apply", "Skip PVE access control apply: cluster RECOVERY restores config.db")
			return nil
		}
		if _, err := restoreCmd.Run(ctx, "which", "pvesh"); err != nil {
			logger.Warning("pvesh not found; skipping PVE access control apply")
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

	if err := restoreFS.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("ensure %s: %w", filepath.Dir(destPath), err)
	}
	if err := restoreFS.WriteFile(destPath, []byte(trimmed+"\n"), perm); err != nil {
		return fmt.Errorf("write %s: %w", destPath, err)
	}
	logging.DebugStep(logger, "access control staged apply file", "Applied %s -> %s", relPath, destPath)
	return nil
}

func applyPVEAccessControlFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) error {
	userCfgPath := filepath.Join(stageRoot, "etc/pve/user.cfg")
	userCfgData, err := restoreFS.ReadFile(userCfgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logging.DebugStep(logger, "pve access control apply", "Skipped: user.cfg not present in staging directory")
			return nil
		}
		return fmt.Errorf("read staged user.cfg: %w", err)
	}
	userCfgRaw := strings.TrimSpace(string(userCfgData))
	if userCfgRaw == "" {
		logging.DebugStep(logger, "pve access control apply", "Skipped: user.cfg is empty")
		return nil
	}

	userSections, err := parseProxmoxNotificationSections(userCfgRaw)
	if err != nil {
		return fmt.Errorf("parse user.cfg: %w", err)
	}

	domainCfgPath := filepath.Join(stageRoot, "etc/pve/domains.cfg")
	domainCfgRaw := ""
	if domainCfgData, err := restoreFS.ReadFile(domainCfgPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read staged domains.cfg: %w", err)
		}
	} else {
		domainCfgRaw = strings.TrimSpace(string(domainCfgData))
	}
	domainSections, err := parseProxmoxNotificationSections(domainCfgRaw)
	if err != nil {
		return fmt.Errorf("parse domains.cfg: %w", err)
	}

	tfaCfgRaw, err := readOptionalStageFile(stageRoot, "etc/pve/priv/tfa.cfg")
	if err != nil {
		return err
	}
	tfaUsers := extractUserIDsFromTFAConfig(tfaCfgRaw)

	tokenCfgRaw, err := readOptionalStageFile(stageRoot, "etc/pve/priv/token.cfg")
	if err != nil {
		return err
	}
	tokenSections, err := parseProxmoxNotificationSections(tokenCfgRaw)
	if err != nil {
		return fmt.Errorf("parse token.cfg: %w", err)
	}

	secretsReport := &pveAccessControlSecretsReport{
		GeneratedAt: nowRestore().UTC().Format(time.RFC3339),
		System:      "pve",
		Notes: []string{
			"SAFE mode applies PVE access control via API (pvesh).",
			"Passwords and API token secrets cannot be imported 1:1; ProxSave regenerates them (local users *@pve and API tokens).",
			"Users listed under tfa_reset_required must re-enroll TFA after restore.",
			"Review and store this file securely; delete it after use.",
		},
	}
	hadSecrets := false

	var domains []proxmoxNotificationSection
	for _, s := range domainSections {
		if strings.TrimSpace(s.Type) == "" || strings.TrimSpace(s.Name) == "" {
			continue
		}
		domains = append(domains, s)
	}

	var roles []proxmoxNotificationSection
	var groups []proxmoxNotificationSection
	var users []proxmoxNotificationSection
	var tokens []proxmoxNotificationSection
	var acls []proxmoxNotificationSection
	for _, s := range userSections {
		switch strings.TrimSpace(s.Type) {
		case "role":
			roles = append(roles, s)
		case "group":
			groups = append(groups, s)
		case "user":
			users = append(users, s)
		case "acl":
			acls = append(acls, s)
		default:
			logger.Warning("PVE access control apply: unknown section %q (%s); skipping", s.Type, s.Name)
		}
	}

	for _, s := range tokenSections {
		switch strings.TrimSpace(s.Type) {
		case "token":
			tokens = append(tokens, s)
		case "":
			continue
		default:
			logger.Warning("PVE access control apply: unknown token section %q (%s); skipping", s.Type, s.Name)
		}
	}

	sortSectionsByName(domains)
	sortSectionsByName(roles)
	sortSectionsByName(groups)
	sortSectionsByName(users)
	sortSectionsByName(tokens)
	sortSectionsByName(acls)

	failed := 0
	for _, s := range domains {
		if err := applyPVEDomainSection(ctx, logger, s); err != nil {
			failed++
			logger.Warning("PVE access control apply: domain %s:%s: %v", s.Type, s.Name, err)
		}
	}
	for _, s := range roles {
		if err := applyPVERoleSection(ctx, logger, s); err != nil {
			failed++
			logger.Warning("PVE access control apply: role %s: %v", s.Name, err)
		}
	}

	groupBaseSkip := map[string]struct{}{
		"users": {},
	}
	for _, s := range groups {
		base := s
		base.Entries = filterSectionEntries(s.Entries, groupBaseSkip)
		if err := applyPVEGroupSection(ctx, logger, base); err != nil {
			failed++
			logger.Warning("PVE access control apply: group %s: %v", s.Name, err)
		}
	}
	for _, s := range users {
		userID := strings.TrimSpace(s.Name)
		password := ""
		if isLocalPVEUser(userID) {
			pw, genErr := generateRandomPassword(24)
			if genErr != nil {
				failed++
				logger.Warning("PVE access control apply: user %s: password generation failed: %v", userID, genErr)
			} else {
				password = pw
			}
		}

		passwordApplied, err := applyPVEUserSection(ctx, logger, s, password)
		if err != nil {
			failed++
			logger.Warning("PVE access control apply: user %s: %v", userID, err)
			continue
		}
		if passwordApplied && strings.TrimSpace(password) != "" {
			hadSecrets = true
			secretsReport.Users = append(secretsReport.Users, pveUserPassword{UserID: userID, Password: password})
		}
	}
	for _, s := range groups {
		usersList := strings.TrimSpace(findSectionEntryValue(s.Entries, "users"))
		if usersList == "" {
			continue
		}
		if err := setPVEGroupUsers(ctx, logger, strings.TrimSpace(s.Name), usersList); err != nil {
			failed++
			logger.Warning("PVE access control apply: group membership %s: %v", s.Name, err)
		}
	}
	for _, s := range tokens {
		userID, tokenID, ok := splitPVETokenSectionName(strings.TrimSpace(s.Name))
		if !ok {
			failed++
			logger.Warning("PVE access control apply: token %s: invalid token section name", s.Name)
			continue
		}
		token, err := createPVEToken(ctx, logger, userID, tokenID, s.Entries)
		if err != nil {
			failed++
			logger.Warning("PVE access control apply: token %s!%s: %v", userID, tokenID, err)
			continue
		}
		hadSecrets = true
		secretsReport.APITokens = append(secretsReport.APITokens, token)
	}
	for _, s := range acls {
		if err := applyPVEACLSection(ctx, logger, s); err != nil {
			failed++
			logger.Warning("PVE access control apply: acl %s: %v", s.Name, err)
		}
	}

	if len(tfaUsers) > 0 {
		hadSecrets = true
		secretsReport.TFAResetRequired = append([]string(nil), tfaUsers...)
	}
	if hadSecrets {
		path := filepath.Join(stageRoot, "pve_access_control_secrets.json")
		if err := writeSecretsReport(path, secretsReport); err != nil {
			logger.Warning("PVE access control: failed to write regenerated secrets report: %v", err)
		} else {
			logger.Warning("PVE access control: regenerated secrets saved to %s (mode 0600)", path)
		}
	}

	if failed > 0 {
		return fmt.Errorf("PVE access control apply: %d item(s) failed", failed)
	}
	logger.Info("PVE access control applied: domains=%d roles=%d groups=%d users=%d tokens=%d acls=%d", len(domains), len(roles), len(groups), len(users), len(tokens), len(acls))
	return nil
}

func sortSectionsByName(sections []proxmoxNotificationSection) {
	sort.Slice(sections, func(i, j int) bool {
		return strings.TrimSpace(sections[i].Name) < strings.TrimSpace(sections[j].Name)
	})
}

func filterSectionEntries(entries []proxmoxNotificationEntry, skip map[string]struct{}) []proxmoxNotificationEntry {
	if len(entries) == 0 || len(skip) == 0 {
		return entries
	}
	out := make([]proxmoxNotificationEntry, 0, len(entries))
	for _, e := range entries {
		if _, ok := skip[strings.TrimSpace(e.Key)]; ok {
			continue
		}
		out = append(out, e)
	}
	return out
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

func applyPVEDomainSection(ctx context.Context, logger *logging.Logger, section proxmoxNotificationSection) error {
	typ := strings.TrimSpace(section.Type)
	realm := strings.TrimSpace(section.Name)
	if typ == "" || realm == "" {
		return fmt.Errorf("invalid domain section")
	}
	setPath := fmt.Sprintf("/access/domains/%s", realm)
	createPath := "/access/domains"
	args := buildPveshArgs(section.Entries)
	return applyPveshObjectWithIDFlag(ctx, logger, setPath, createPath, "--realm", realm, []string{"--type", typ}, args)
}

func applyPVERoleSection(ctx context.Context, logger *logging.Logger, section proxmoxNotificationSection) error {
	roleID := strings.TrimSpace(section.Name)
	if strings.TrimSpace(section.Type) != "role" || roleID == "" {
		return fmt.Errorf("invalid role section")
	}
	setPath := fmt.Sprintf("/access/roles/%s", roleID)
	createPath := "/access/roles"
	args := buildPveshArgs(section.Entries)
	return applyPveshObjectWithIDFlag(ctx, logger, setPath, createPath, "--roleid", roleID, nil, args)
}

func applyPVEGroupSection(ctx context.Context, logger *logging.Logger, section proxmoxNotificationSection) error {
	groupID := strings.TrimSpace(section.Name)
	if strings.TrimSpace(section.Type) != "group" || groupID == "" {
		return fmt.Errorf("invalid group section")
	}
	setPath := fmt.Sprintf("/access/groups/%s", groupID)
	createPath := "/access/groups"
	args := buildPveshArgs(section.Entries)
	return applyPveshObjectWithIDFlag(ctx, logger, setPath, createPath, "--groupid", groupID, nil, args)
}

func applyPVEUserSection(ctx context.Context, logger *logging.Logger, section proxmoxNotificationSection, password string) (bool, error) {
	userID := strings.TrimSpace(section.Name)
	if strings.TrimSpace(section.Type) != "user" || userID == "" {
		return false, fmt.Errorf("invalid user section")
	}
	setPath := fmt.Sprintf("/access/users/%s", userID)
	createPath := "/access/users"
	args := buildPveshArgs(section.Entries)

	// First try applying as update (does not include secrets).
	if err := runPvesh(ctx, logger, append([]string{"set", setPath}, args...)); err == nil {
		if strings.TrimSpace(password) == "" || !isLocalPVEUser(userID) {
			return false, nil
		}
		if err := setPVEUserPassword(ctx, logger, userID, password); err != nil {
			return false, err
		}
		return true, nil
	}

	// Create (include password only for local users).
	createArgs := []string{"create", createPath, "--userid", userID}
	if strings.TrimSpace(password) != "" && isLocalPVEUser(userID) {
		createArgs = append(createArgs, "--password", password)
	}
	createArgs = append(createArgs, args...)

	if strings.TrimSpace(password) != "" && isLocalPVEUser(userID) {
		if _, err := runPveshSensitive(ctx, logger, createArgs, "--password"); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := runPvesh(ctx, logger, createArgs); err != nil {
		return false, err
	}
	return false, nil
}

func setPVEGroupUsers(ctx context.Context, logger *logging.Logger, groupID, users string) error {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return fmt.Errorf("invalid group id")
	}
	setPath := fmt.Sprintf("/access/groups/%s", groupID)
	return runPvesh(ctx, logger, []string{"set", setPath, "--users", users})
}

func applyPVEACLSection(ctx context.Context, logger *logging.Logger, section proxmoxNotificationSection) error {
	if strings.TrimSpace(section.Type) != "acl" {
		return fmt.Errorf("invalid acl section")
	}

	path := strings.TrimSpace(findSectionEntryValue(section.Entries, "path"))
	if path == "" {
		path = aclPathFromSectionName(section.Name)
	}
	if path == "" {
		return fmt.Errorf("missing acl path")
	}

	args := buildPveshArgs(section.Entries)
	if !sliceHasFlag(args, "--path") {
		args = append([]string{"--path", path}, args...)
	}
	return runPvesh(ctx, logger, append([]string{"set", "/access/acl"}, args...))
}

func aclPathFromSectionName(name string) string {
	for _, field := range strings.Fields(strings.TrimSpace(name)) {
		if strings.HasPrefix(field, "/") {
			return field
		}
	}
	return ""
}

func sliceHasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func applyPveshObjectWithIDFlag(ctx context.Context, logger *logging.Logger, setPath, createPath, idFlag, id string, createExtra, args []string) error {
	if err := runPvesh(ctx, logger, append([]string{"set", setPath}, args...)); err == nil {
		return nil
	}

	createArgs := []string{"create", createPath, idFlag, id}
	createArgs = append(createArgs, createExtra...)
	createArgs = append(createArgs, args...)
	return runPvesh(ctx, logger, createArgs)
}

func readOptionalStageFile(stageRoot, relPath string) (string, error) {
	stagePath := filepath.Join(stageRoot, relPath)
	data, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read staged %s: %w", relPath, err)
	}
	return strings.TrimSpace(string(data)), nil
}

func writeSecretsReport(path string, report *pveAccessControlSecretsReport) error {
	if report == nil {
		return fmt.Errorf("invalid secrets report")
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return restoreFS.WriteFile(path, append(data, '\n'), 0o600)
}

func isLocalPVEUser(userID string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	idx := strings.LastIndex(userID, "@")
	if idx < 0 || idx+1 >= len(userID) {
		return false
	}
	return strings.TrimSpace(userID[idx+1:]) == "pve"
}

func generateRandomPassword(length int) (string, error) {
	if length <= 0 {
		length = 24
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}

func extractUserIDsFromTFAConfig(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	sections, err := parseProxmoxNotificationSections(trimmed)
	if err != nil || len(sections) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, s := range sections {
		name := strings.TrimSpace(s.Name)
		if strings.Contains(name, "@") {
			if _, ok := seen[name]; !ok {
				seen[name] = struct{}{}
				out = append(out, name)
			}
		}
		if user := strings.TrimSpace(findSectionEntryValue(s.Entries, "user")); strings.Contains(user, "@") {
			if _, ok := seen[user]; !ok {
				seen[user] = struct{}{}
				out = append(out, user)
			}
		}
	}
	sort.Strings(out)
	return out
}

func setPVEUserPassword(ctx context.Context, logger *logging.Logger, userID, password string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" || strings.TrimSpace(password) == "" {
		return fmt.Errorf("invalid user password request")
	}

	candidates := [][]string{
		{"set", fmt.Sprintf("/access/users/%s", userID), "--password", password},
		{"create", fmt.Sprintf("/access/users/%s/password", userID), "--password", password},
		{"set", "/access/password", "--userid", userID, "--password", password},
		{"create", "/access/password", "--userid", userID, "--password", password},
	}

	var lastErr error
	for _, args := range candidates {
		if _, err := runPveshSensitive(ctx, logger, args, "--password"); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("unable to set password for %s via pvesh: %w", userID, lastErr)
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

func createPVEToken(ctx context.Context, logger *logging.Logger, userID, tokenID string, entries []proxmoxNotificationEntry) (pveAPIToken, error) {
	userID = strings.TrimSpace(userID)
	tokenID = strings.TrimSpace(tokenID)
	if userID == "" || tokenID == "" {
		return pveAPIToken{}, fmt.Errorf("invalid token request")
	}

	args := buildPveshArgs(entries)

	token, err := tryCreatePVEToken(ctx, logger, userID, tokenID, args)
	if err == nil {
		return token, nil
	}
	_ = deletePVEToken(ctx, logger, userID, tokenID)
	token, retryErr := tryCreatePVEToken(ctx, logger, userID, tokenID, args)
	if retryErr == nil {
		return token, nil
	}
	return pveAPIToken{}, retryErr
}

func tryCreatePVEToken(ctx context.Context, logger *logging.Logger, userID, tokenID string, args []string) (pveAPIToken, error) {
	fullTokenID := fmt.Sprintf("%s!%s", userID, tokenID)
	tokenPath := fmt.Sprintf("/access/users/%s/token/%s", userID, tokenID)
	tokenBasePath := fmt.Sprintf("/access/users/%s/token", userID)

	candidates := [][]string{
		append([]string{"--output-format", "json", "create", tokenPath}, args...),
		append([]string{"create", tokenPath}, args...),
		append([]string{"--output-format", "json", "create", tokenBasePath, "--tokenid", tokenID}, args...),
		append([]string{"create", tokenBasePath, "--tokenid", tokenID}, args...),
	}

	var lastErr error
	for _, createArgs := range candidates {
		output, err := runPveshSensitive(ctx, logger, createArgs)
		if err != nil {
			lastErr = err
			continue
		}
		outFull, value, ok := parsePVETokenCreateOutput(output)
		if !ok {
			return pveAPIToken{}, fmt.Errorf("token %s created but secret could not be parsed from pvesh output", fullTokenID)
		}
		if strings.TrimSpace(outFull) == "" {
			outFull = fullTokenID
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return pveAPIToken{}, fmt.Errorf("token %s created but secret is empty", fullTokenID)
		}
		return pveAPIToken{
			UserID:      userID,
			TokenID:     tokenID,
			FullTokenID: outFull,
			Value:       value,
		}, nil
	}
	if lastErr != nil {
		return pveAPIToken{}, fmt.Errorf("unable to create token %s via pvesh: %w", fullTokenID, lastErr)
	}
	return pveAPIToken{}, fmt.Errorf("unable to create token %s via pvesh", fullTokenID)
}

func deletePVEToken(ctx context.Context, logger *logging.Logger, userID, tokenID string) error {
	userID = strings.TrimSpace(userID)
	tokenID = strings.TrimSpace(tokenID)
	if userID == "" || tokenID == "" {
		return fmt.Errorf("invalid token delete request")
	}

	tokenPath := fmt.Sprintf("/access/users/%s/token/%s", userID, tokenID)
	tokenBasePath := fmt.Sprintf("/access/users/%s/token", userID)
	candidates := [][]string{
		{"delete", tokenPath},
		{"delete", tokenBasePath, "--tokenid", tokenID},
	}

	var lastErr error
	for _, args := range candidates {
		if err := runPvesh(ctx, logger, args); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("token delete failed")
	}
	return lastErr
}

func parsePVETokenCreateOutput(output []byte) (fullTokenID, value string, ok bool) {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return "", "", false
	}

	var wrapper struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(trimmed), &wrapper); err == nil && len(wrapper.Data) > 0 {
		if full, val, ok := parsePVETokenCreateOutput(wrapper.Data); ok {
			return full, val, true
		}
	}

	var str string
	if err := json.Unmarshal([]byte(trimmed), &str); err == nil {
		str = strings.TrimSpace(str)
		if str != "" {
			return "", str, true
		}
	}

	var obj struct {
		Value       string `json:"value"`
		Token       string `json:"token"`
		FullTokenID string `json:"full-tokenid"`
	}
	if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
		secret := strings.TrimSpace(obj.Value)
		if secret == "" {
			secret = strings.TrimSpace(obj.Token)
		}
		if secret != "" {
			return strings.TrimSpace(obj.FullTokenID), secret, true
		}
	}

	var lineValue string
	var lineFull string
	for _, line := range strings.Split(trimmed, "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		fields := strings.Fields(l)
		if len(fields) >= 2 {
			key := strings.TrimSuffix(fields[0], ":")
			switch key {
			case "value":
				lineValue = fields[len(fields)-1]
			case "full-tokenid":
				lineFull = fields[len(fields)-1]
			}
		}
	}
	if strings.TrimSpace(lineValue) != "" {
		return strings.TrimSpace(lineFull), strings.TrimSpace(lineValue), true
	}
	if looksLikePVETokenSecret(trimmed) {
		return "", trimmed, true
	}
	return "", "", false
}

func looksLikePVETokenSecret(secret string) bool {
	secret = strings.TrimSpace(secret)
	if len(secret) < 16 {
		return false
	}
	for _, r := range secret {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == '+' || r == '/' || r == '=':
		default:
			return false
		}
	}
	return true
}
