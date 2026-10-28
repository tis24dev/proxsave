package orchestrator

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// The PVE access control files are not section configs, apart from domains.cfg.
// Measured on PVE 9.1.9 (pveum on pve-test, 2026-10-26):
//
//	user.cfg         one colon-separated line per object, the type first:
//	                 user:pxsu@pve:1:0:::p@x.y:probe user:x:
//	                 token:root@pam!pxstok:0:1:root probe:
//	                 group:pxsg:pxsu@pve:probe grp:
//	                 pool:pxspool:probe pool:::
//	                 role:PxsRole:Sys.Audit,VM.Audit:
//	                 acl:1:/vms:pxsu@pve:PVEAuditor:
//	priv/token.cfg   "<userid>!<tokenid> <secret>" per line
//	priv/shadow.cfg  "<username>:<hash>:" per line, pve realm users only, without the realm
//	priv/tfa.cfg     JSON, {"users":{"<userid>":{...}}}
//
// PVE reads user.cfg in order and drops a token, group member or ACL entry whose
// user or token is not defined yet ("user config - ignore invalid acl token"), so
// the merged file is written the way PVE writes it: users each followed by their
// tokens, then groups, pools, roles and ACLs.

const pveRootUserID = "root@pam"

type pveUserCfgLine struct {
	typ    string
	fields []string
}

func (l pveUserCfgLine) render() string {
	return l.typ + ":" + strings.Join(l.fields, ":")
}

func (l pveUserCfgLine) field(i int) string {
	if i < len(l.fields) {
		return strings.TrimSpace(l.fields[i])
	}
	return ""
}

// parsePVEUserCfg returns the object lines of a user.cfg; blank lines and comments
// are not objects.
func parsePVEUserCfg(raw string) []pveUserCfgLine {
	var out []pveUserCfgLine
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		typ, rest, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(typ) == "" {
			continue
		}
		out = append(out, pveUserCfgLine{typ: strings.TrimSpace(typ), fields: strings.Split(rest, ":")})
	}
	return out
}

func isPVERootTokenID(id string) bool {
	return strings.HasPrefix(strings.TrimSpace(id), pveRootUserID+"!")
}

// splitPVEACLMembers returns the users, groups and tokens of an ACL line split into
// those that are root@pam tokens and the others.
func splitPVEACLMembers(members string) (rootTokens, others []string) {
	for m := range strings.SplitSeq(members, ",") {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if isPVERootTokenID(m) {
			rootTokens = append(rootTokens, m)
		} else {
			others = append(others, m)
		}
	}
	return rootTokens, others
}

func withPVEACLMembers(l pveUserCfgLine, members []string) pveUserCfgLine {
	fields := slices.Clone(l.fields)
	for len(fields) < 4 {
		fields = append(fields, "")
	}
	fields[2] = strings.Join(members, ",")
	return pveUserCfgLine{typ: l.typ, fields: fields}
}

// mergePVEUserCfg restores the backup's user.cfg 1:1 except root@pam: its user line,
// its tokens and the ACL entries of those tokens are this host's, and root@pam keeps
// Administrator on "/". It reports whether a user of the pve realm remains.
func mergePVEUserCfg(backupRaw, currentRaw string) (string, bool) {
	backup, current := parsePVEUserCfg(backupRaw), parsePVEUserCfg(currentRaw)

	var users, tokens, groups, pools, roles, acls, others []pveUserCfgLine
	for _, l := range backup {
		switch l.typ {
		case "user":
			if l.field(0) != pveRootUserID {
				users = append(users, l)
			}
		case "token":
			if !isPVERootTokenID(l.field(0)) {
				tokens = append(tokens, l)
			}
		case "group":
			groups = append(groups, l)
		case "pool":
			pools = append(pools, l)
		case "role":
			roles = append(roles, l)
		case "acl":
			if _, kept := splitPVEACLMembers(l.field(2)); len(kept) > 0 {
				acls = append(acls, withPVEACLMembers(l, kept))
			}
		default:
			others = append(others, l)
		}
	}

	rootUser := pveUserCfgLine{typ: "user", fields: []string{pveRootUserID, "1", "0", "", "", "", "", "", ""}}
	for _, l := range current {
		switch {
		case l.typ == "user" && l.field(0) == pveRootUserID:
			rootUser = l
		case l.typ == "token" && isPVERootTokenID(l.field(0)):
			tokens = append(tokens, l)
		case l.typ == "acl":
			if rootTokens, _ := splitPVEACLMembers(l.field(2)); len(rootTokens) > 0 {
				acls = append(acls, withPVEACLMembers(l, rootTokens))
			}
		}
	}
	users = append(users, rootUser)

	rootAdmin := false
	for _, l := range acls {
		if l.field(1) == "/" && listContains(l.field(2), pveRootUserID) && listContains(l.field(3), "Administrator") {
			rootAdmin = true
			break
		}
	}
	if !rootAdmin {
		acls = append(acls, pveUserCfgLine{typ: "acl", fields: []string{"1", "/", pveRootUserID, "Administrator", ""}})
	}

	pveRealm := false
	var blocks [][]pveUserCfgLine
	var userBlock []pveUserCfgLine
	for _, u := range users {
		if userRealm(u.field(0)) == "pve" {
			pveRealm = true
		}
		userBlock = append(userBlock, u)
		for _, t := range tokens {
			if user, _, ok := splitPVETokenSectionName(t.field(0)); ok && user == u.field(0) {
				userBlock = append(userBlock, t)
			}
		}
	}
	blocks = append(blocks, userBlock, groups, pools, roles, acls, others)

	var b strings.Builder
	for _, block := range blocks {
		if len(block) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		for _, l := range block {
			b.WriteString(l.render())
			b.WriteString("\n")
		}
	}
	return b.String(), pveRealm
}

// mergePVETokenCfg restores the backup's token secrets except root@pam's tokens,
// which are this host's: the same tokens mergePVEUserCfg keeps in user.cfg.
func mergePVETokenCfg(backupRaw, currentRaw string) string {
	var b strings.Builder
	add := func(raw string, root bool) {
		for line := range strings.SplitSeq(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			id, _, _ := strings.Cut(line, " ")
			if isPVERootTokenID(id) == root {
				b.WriteString(line)
				b.WriteString("\n")
			}
		}
	}
	add(backupRaw, false)
	add(currentRaw, true)
	return b.String()
}

// mergePVETFAJSON restores the backup's tfa.cfg except root@pam's entries, which are
// this host's, and returns the users it holds.
func mergePVETFAJSON(backupRaw, currentRaw string) ([]byte, map[string]json.RawMessage, error) {
	if strings.TrimSpace(backupRaw) == "" {
		backupRaw = "{}" // what PVE writes when no user has TFA
	}
	var backup map[string]json.RawMessage
	if err := json.Unmarshal([]byte(backupRaw), &backup); err != nil {
		return nil, nil, fmt.Errorf("parse staged priv/tfa.cfg: %w", err)
	}
	if backup == nil {
		backup = map[string]json.RawMessage{}
	}
	var current map[string]json.RawMessage
	if strings.TrimSpace(currentRaw) != "" {
		_ = json.Unmarshal([]byte(currentRaw), &current)
	}
	users := parseTFAUsersMap(backup)
	delete(users, pveRootUserID)
	if root, ok := parseTFAUsersMap(current)[pveRootUserID]; ok {
		users[pveRootUserID] = root
	}
	backup["users"] = mustMarshalRaw(users)
	out, err := json.Marshal(backup)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal priv/tfa.cfg: %w", err)
	}
	return append(out, '\n'), users, nil
}
