package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

const (
	etcPasswdPath  = "/etc/passwd"
	etcGroupPath   = "/etc/group"
	etcShadowPath  = "/etc/shadow"
	etcGshadowPath = "/etc/gshadow"
	etcSudoersPath = "/etc/sudoers"

	// Accounts/groups whose numeric id is below this threshold belong to the
	// host/distro (root@uid0, daemon users, sudo/docker groups, ...). They are
	// preserved from the CURRENT host, so restoring accounts never locks out the
	// running machine. Only regular accounts (id >= threshold) are imported.
	systemAccountIDThreshold = 1000

	passwdMinFields = 7
	groupMinFields  = 4
	shadowMinFields = 2

	// lockedShadowSuffix, appended to a username, yields a well-formed but locked
	// (password-less) shadow entry. Used when an imported passwd user has no shadow
	// line in the backup, so passwd and shadow never desync.
	lockedShadowSuffix = ":*:::::::"
)

// maybeApplyAccountsFromStage is the wired, gated entry point for restoring OS
// account files from the staging tree (#67). The merge lives in
// applyAccountsFromStage so it can be unit-tested with an in-memory FS.
func maybeApplyAccountsFromStage(ctx context.Context, logger *logging.Logger, plan *RestorePlan, stageRoot string, dryRun bool) (err error) {
	if plan == nil || !plan.HasCategoryID("accounts") {
		return nil
	}
	if strings.TrimSpace(stageRoot) == "" {
		logging.DebugStep(logger, "accounts staged apply", "Skipped: staging directory not available")
		return nil
	}
	done := logging.DebugStart(logger, "accounts staged apply", "dryRun=%v stage=%s", dryRun, stageRoot)
	defer func() { done(err) }()

	if dryRun {
		logger.Info("Dry run enabled: skipping staged system accounts apply")
		return nil
	}
	if !isRealRestoreFS(restoreFS) {
		logger.Debug("Skipping staged system accounts apply: non-system filesystem in use")
		return nil
	}
	if accessControlApplyGeteuid() != 0 {
		logger.Warning("Skipping staged system accounts apply: requires root privileges")
		return nil
	}
	return applyAccountsFromStage(ctx, logger, stageRoot)
}

func applyAccountsFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	stagedPasswd, hasPasswd, err := readStageFileOptional(stageRoot, "etc/passwd")
	if err != nil {
		return err
	}
	if !hasPasswd {
		logging.DebugStep(logger, "accounts staged apply", "Skipped: etc/passwd not present in stage")
		return nil
	}
	currentPasswd, err := readCurrentAccountFile(etcPasswdPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(currentPasswd) == "" {
		// Never rewrite the account database without the host baseline (would drop
		// root and system accounts). Skip rather than risk a lockout.
		logger.Warning("Skipping system accounts restore: current /etc/passwd is empty/unreadable")
		return nil
	}
	currentGroup, err := readCurrentAccountFile(etcGroupPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(currentGroup) == "" {
		// Same anti-lockout rationale as the empty /etc/passwd guard above: never
		// rewrite the group DB without the host baseline (it would drop root and all
		// host system groups). Skip rather than risk dropping host group memberships.
		logger.Warning("Skipping system accounts restore: current /etc/group is empty/unreadable")
		return nil
	}

	stagedShadow, _, err := readStageFileOptional(stageRoot, "etc/shadow")
	if err != nil {
		return err
	}
	currentShadow, err := readCurrentAccountFile(etcShadowPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(currentShadow) == "" {
		// As above: without the host shadow baseline the rewrite would drop root and
		// every host account's credentials. Skip to avoid a lockout.
		logger.Warning("Skipping system accounts restore: current /etc/shadow is empty/unreadable")
		return nil
	}
	stagedGroup, _, err := readStageFileOptional(stageRoot, "etc/group")
	if err != nil {
		return err
	}
	stagedGshadow, _, err := readStageFileOptional(stageRoot, "etc/gshadow")
	if err != nil {
		return err
	}
	currentGshadow, err := readCurrentAccountFile(etcGshadowPath)
	if err != nil {
		return err
	}

	// Host identity maps. hostSystemUsers = names of host accounts with uid < threshold
	// (incl. the uid 0 entry whatever its name), so a renamed root or a name-clash never
	// clobbers a host account. hostGroupGID maps every host group name->gid and hostGIDs
	// is the set of ALL host group gids: together they ensure the merge never overwrites
	// an existing host group, never reuses a host gid for a new group, and never enrolls
	// an imported user into an existing host group (system OR privileged) via primary gid.
	hostSystemUsers := lowIDNames(currentPasswd, 2)
	hostGroupGID := groupGIDsByName(currentGroup)
	hostGIDs := gidValueSet(hostGroupGID)

	importedUsers, mergedPasswd := mergePasswd(currentPasswd, stagedPasswd, hostSystemUsers, hostGIDs)
	mergedShadow := mergeShadow(currentShadow, stagedShadow, importedUsers)
	importedGroups, mergedGroup := mergeGroup(currentGroup, stagedGroup, hostGroupGID, hostGIDs, importedUsers)
	mergedGshadow := mergeGshadow(currentGshadow, stagedGshadow, importedGroups)

	if err := writeFileAtomic(etcPasswdPath, []byte(mergedPasswd), 0o644); err != nil {
		return err
	}
	if err := writeFileAtomic(etcShadowPath, []byte(mergedShadow), 0o640); err != nil {
		return err
	}
	if err := writeFileAtomic(etcGroupPath, []byte(mergedGroup), 0o644); err != nil {
		return err
	}
	if err := writeFileAtomic(etcGshadowPath, []byte(mergedGshadow), 0o640); err != nil {
		return err
	}
	logger.Info("Restored system accounts: imported %d user(s) and %d group(s); host root, system accounts and group memberships preserved", len(importedUsers), len(importedGroups))

	return applySudoersFromStage(ctx, logger, stageRoot)
}

// applySudoersFromStage replaces /etc/sudoers with the EXACT staged bytes only if
// they pass `visudo -c`, otherwise the current file is kept untouched.
func applySudoersFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) error {
	stagedPath := filepath.Join(stageRoot, "etc/sudoers")
	data, err := restoreFS.ReadFile(stagedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read staged sudoers: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	if _, err := restoreCmd.Run(ctx, "visudo", "-c", "-f", stagedPath); err != nil {
		logger.Warning("Skipping /etc/sudoers restore: staged sudoers failed validation (visudo -c): %v", err)
		return nil
	}
	if err := writeFileAtomic(etcSudoersPath, data, 0o440); err != nil {
		return err
	}
	logger.Info("Restored /etc/sudoers (validated with visudo -c)")
	return nil
}

func readCurrentAccountFile(path string) (string, error) {
	data, err := restoreFS.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// mergePasswd keeps every current entry and imports each backup user that is a
// regular account (uid >= threshold, >= passwdMinFields, not NIS, not "root", and
// whose name does not collide with a host system account). Returns the imported
// names and the merged file.
func mergePasswd(current, backup string, hostSystemUsers map[string]bool, hostGIDs map[uint64]bool) (map[string]bool, string) {
	lines := splitNonEmptyLines(current)
	index := indexByName(lines)
	imported := map[string]bool{}
	for _, line := range splitNonEmptyLines(backup) {
		if isNISLine(line) {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) < passwdMinFields {
			continue
		}
		name := parts[0]
		if !isValidAccountName(name) || name == "root" || hostSystemUsers[name] {
			continue
		}
		uid, ok := parseAccountID(parts[2])
		if !ok || uid < systemAccountIDThreshold {
			continue
		}
		// Never import a regular user whose PRIMARY group is root (gid 0), an existing
		// host group of ANY gid (system like sudo/shadow/disk OR a privileged regular
		// group like docker at gid >= 1000), or an overflowed/garbage gid: a passwd
		// primary gid grants that group's privileges on its own, bypassing the
		// /etc/group member-merge protections.
		pgid, ok := parseAccountID(parts[3])
		if !ok || pgid == 0 || hostGIDs[pgid] {
			continue
		}
		imported[name] = true
		upsert(&lines, index, name, line)
	}
	return imported, joinLines(lines)
}

// mergeShadow keeps every current line and, for each imported user, sets the
// backup shadow line; if the backup lacks a (valid) line, a locked placeholder is
// written so an imported passwd user is never left without a shadow entry.
func mergeShadow(current, backup string, imported map[string]bool) string {
	lines := splitNonEmptyLines(current)
	index := indexByName(lines)
	backupByName := byName(backup)
	for _, name := range sortedKeys(imported) {
		line, ok := backupByName[name]
		if !ok || len(strings.Split(line, ":")) < shadowMinFields {
			line = name + lockedShadowSuffix
		}
		upsert(&lines, index, name, line)
	}
	return joinLines(lines)
}

// mergeGroup keeps every current group and NEVER overwrites an existing host group.
//   - A backup group whose name is NOT on the host is imported whole, but only when
//     it is a regular group (gid >= threshold) and its gid does not collide with any
//     host group's gid (a collision would create two groups sharing a gid).
//   - A backup group whose name IS on the host is never replaced: its host gid and
//     existing members are preserved, and only the imported users are added as
//     supplementary members, and only when the backup line is genuinely the same
//     group (gid matches the host) and it is not the root group. This stops a backup
//     from changing a host gid, dropping host members, or injecting members into a
//     host privileged group (sudo/docker/...) via a spoofed or name-only line.
func mergeGroup(current, backup string, hostGroupGID map[string]uint64, hostGIDs map[uint64]bool, importedUsers map[string]bool) (map[string]bool, string) {
	lines := splitNonEmptyLines(current)
	index := indexByName(lines)
	imported := map[string]bool{}
	for _, line := range splitNonEmptyLines(backup) {
		if isNISLine(line) {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) < groupMinFields {
			continue
		}
		name := parts[0]
		if !isValidAccountName(name) {
			continue
		}
		gid, ok := parseAccountID(parts[2])
		if !ok {
			continue
		}
		hostGID, existsOnHost := hostGroupGID[name]
		if !existsOnHost {
			// Brand-new backup group: import only a regular group whose gid does not
			// collide with an existing host group gid (and never root/system range).
			if name == "root" || gid < systemAccountIDThreshold || hostGIDs[gid] {
				continue
			}
			// Restrict members to users we are also importing, so a new backup group
			// never silently enrolls an existing host account (mirrors the member
			// filtering on the existing-host-group path below).
			parts[3] = strings.Join(filterSet(groupMembers(parts), importedUsers), ",")
			line = strings.Join(parts, ":")
			imported[name] = true
			upsert(&lines, index, name, line)
			continue
		}
		// Existing host group: never overwrite. Merge imported members only, and only
		// when the backup line is the same group (gid matches) and not the root group.
		if gid == 0 || hostGID != gid {
			continue
		}
		if i, ok := index[name]; ok {
			add := filterSet(groupMembers(parts), importedUsers)
			lines[i] = addGroupMembers(lines[i], add)
		}
	}
	return imported, joinLines(lines)
}

// mergeGshadow substitutes the backup gshadow line for each imported regular group
// (gshadow is optional/advisory; system-group member merges are reflected in
// /etc/group, which is authoritative).
func mergeGshadow(current, backup string, importedGroups map[string]bool) string {
	lines := splitNonEmptyLines(current)
	index := indexByName(lines)
	backupByName := byName(backup)
	for _, name := range sortedKeys(importedGroups) {
		if line, ok := backupByName[name]; ok {
			upsert(&lines, index, name, line)
		}
	}
	return joinLines(lines)
}

func lowIDNames(content string, idField int) map[string]bool {
	out := map[string]bool{}
	for _, line := range splitNonEmptyLines(content) {
		if isNISLine(line) {
			continue
		}
		parts := strings.Split(line, ":")
		if idField >= len(parts) {
			continue
		}
		id, ok := parseAccountID(parts[idField])
		if ok && id < systemAccountIDThreshold {
			out[parts[0]] = true
		}
	}
	return out
}

func isNISLine(line string) bool {
	return strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")
}

// parseAccountID parses a uid/gid as a 32-bit unsigned value. There is no
// TrimSpace: a field with surrounding whitespace is non-canonical and we write
// the backup line verbatim, so it is rejected (ParseUint base-10 also rejects
// signs and whitespace). bitSize 32 closes the overflow vector (4294967296 ->0),
// and 0xFFFFFFFF (=(uid_t)-1, the nobody/error sentinel) is rejected explicitly.
func parseAccountID(s string) (uint64, bool) {
	id, err := strconv.ParseUint(s, 10, 32)
	if err != nil || id == 0xFFFFFFFF {
		return 0, false
	}
	return id, true
}

// isValidAccountName rejects empty/over-long names and any control byte (incl.
// NUL), DEL, whitespace, field separators (':' ',') or '/', so forged/malformed
// names are never written into the account database and cannot bypass the
// exact-match host-collision check (e.g. "daemon " with a trailing space).
func isValidAccountName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	return strings.IndexFunc(name, func(r rune) bool {
		return r < 0x21 || r == 0x7f || r == ':' || r == ',' || r == '/'
	}) < 0
}

// groupGIDsByName maps each host group name to its numeric gid (used to verify a
// backup group is genuinely the same group before merging members into it).
func groupGIDsByName(content string) map[string]uint64 {
	m := map[string]uint64{}
	for _, line := range splitNonEmptyLines(content) {
		if isNISLine(line) {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) < 3 {
			continue
		}
		if gid, ok := parseAccountID(parts[2]); ok {
			m[parts[0]] = gid
		}
	}
	return m
}

// gidValueSet returns the set of gid VALUES from a name->gid map (every host group
// gid), used to block gid collisions and primary-gid enrolment into host groups.
func gidValueSet(m map[string]uint64) map[uint64]bool {
	out := make(map[uint64]bool, len(m))
	for _, gid := range m {
		out[gid] = true
	}
	return out
}

func colonName(line string) string {
	if i := strings.IndexByte(line, ':'); i >= 0 {
		return line[:i]
	}
	return line
}

func indexByName(lines []string) map[string]int {
	m := make(map[string]int, len(lines))
	for i, l := range lines {
		m[colonName(l)] = i
	}
	return m
}

func byName(content string) map[string]string {
	m := map[string]string{}
	for _, l := range splitNonEmptyLines(content) {
		m[colonName(l)] = l
	}
	return m
}

func upsert(lines *[]string, index map[string]int, name, line string) {
	if i, ok := index[name]; ok {
		(*lines)[i] = line
		return
	}
	index[name] = len(*lines)
	*lines = append(*lines, line)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func groupMembers(parts []string) []string {
	if len(parts) < 4 {
		return nil
	}
	field := strings.TrimSpace(parts[3])
	if field == "" {
		return nil
	}
	return strings.Split(field, ",")
}

func filterSet(names []string, allow map[string]bool) []string {
	var out []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" && allow[n] {
			out = append(out, n)
		}
	}
	return out
}

// addGroupMembers adds names (not already present) to the members field (index 3)
// of a colon-separated group line, preserving order and the rest of the line.
func addGroupMembers(line string, add []string) string {
	if len(add) == 0 {
		return line
	}
	parts := strings.Split(line, ":")
	for len(parts) < 4 {
		parts = append(parts, "")
	}
	seen := map[string]bool{}
	var members []string
	for _, m := range strings.Split(parts[3], ",") {
		m = strings.TrimSpace(m)
		if m != "" && !seen[m] {
			seen[m] = true
			members = append(members, m)
		}
	}
	for _, n := range add {
		if !seen[n] {
			seen[n] = true
			members = append(members, n)
		}
	}
	parts[3] = strings.Join(members, ",")
	return strings.Join(parts, ":")
}

func splitNonEmptyLines(content string) []string {
	var out []string
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}
