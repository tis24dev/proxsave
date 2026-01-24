package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

type accessControlRunner struct {
	calls []commandCall
	known map[string]struct{}
}

func newAccessControlRunner() *accessControlRunner {
	return &accessControlRunner{known: make(map[string]struct{})}
}

func (r *accessControlRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	_ = ctx
	r.calls = append(r.calls, commandCall{name: name, args: append([]string(nil), args...)})

	cmd, rest := pveshCommand(args)
	if name != "pvesh" || cmd == "" || len(rest) < 1 {
		return []byte("ok"), nil
	}

	switch cmd {
	case "set":
		path := rest[0]
		if path == "/access/acl" {
			return []byte("ok"), nil
		}
		if _, ok := r.known[path]; ok {
			return []byte("ok"), nil
		}
		return nil, fmt.Errorf("not found")
	case "create":
		if created := createdPathFromPveshCreate(args); created != "" {
			r.known[created] = struct{}{}
		}
		path := rest[0]
		if tokenOutput, ok := tokenOutputForPveshCreate(args, path); ok {
			return tokenOutput, nil
		}
		return []byte("ok"), nil
	case "delete":
		path := rest[0]
		delete(r.known, path)
		return []byte("ok"), nil
	default:
		return []byte("ok"), nil
	}
}

func pveshCommand(args []string) (cmd string, rest []string) {
	i := 0
	for i < len(args) {
		if args[i] == "--output-format" && i+1 < len(args) {
			i += 2
			continue
		}
		if strings.HasPrefix(args[i], "--") {
			i++
			continue
		}
		break
	}
	if i >= len(args) {
		return "", nil
	}
	return args[i], args[i+1:]
}

func createdPathFromPveshCreate(args []string) string {
	cmd, rest := pveshCommand(args)
	if cmd != "create" || len(rest) < 1 {
		return ""
	}
	createPath := rest[0]
	switch createPath {
	case "/access/domains":
		realm := pveshArgValue(args, "--realm")
		if realm == "" {
			return ""
		}
		return "/access/domains/" + realm
	case "/access/roles":
		role := pveshArgValue(args, "--roleid")
		if role == "" {
			return ""
		}
		return "/access/roles/" + role
	case "/access/groups":
		group := pveshArgValue(args, "--groupid")
		if group == "" {
			return ""
		}
		return "/access/groups/" + group
	case "/access/users":
		user := pveshArgValue(args, "--userid")
		if user == "" {
			return ""
		}
		return "/access/users/" + user
	default:
		if strings.HasPrefix(createPath, "/access/users/") && strings.Contains(createPath, "/token") {
			userID, tokenID, ok := tokenFromPveshCreate(args, createPath)
			if !ok || userID == "" || tokenID == "" {
				return ""
			}
			return fmt.Sprintf("/access/users/%s/token/%s", userID, tokenID)
		}
		return ""
	}
}

func pveshArgValue(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func tokenOutputForPveshCreate(args []string, createPath string) ([]byte, bool) {
	userID, tokenID, ok := tokenFromPveshCreate(args, createPath)
	if !ok || userID == "" || tokenID == "" {
		return nil, false
	}
	payload := map[string]string{
		"full-tokenid": fmt.Sprintf("%s!%s", userID, tokenID),
		"value":        fmt.Sprintf("secret-%s!%s", userID, tokenID),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, false
	}
	return data, true
}

func tokenFromPveshCreate(args []string, createPath string) (userID, tokenID string, ok bool) {
	if !strings.HasPrefix(createPath, "/access/users/") {
		return "", "", false
	}
	remainder := strings.TrimPrefix(createPath, "/access/users/")
	parts := strings.Split(remainder, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] != "token" {
		return "", "", false
	}
	userID = parts[0]
	if len(parts) >= 3 && parts[2] != "" {
		tokenID = parts[2]
	} else {
		tokenID = pveshArgValue(args, "--tokenid")
	}
	if tokenID == "" {
		return "", "", false
	}
	return userID, tokenID, true
}

func TestApplyPVEAccessControlFromStage_AppliesDomainsRolesGroupsUsersACL(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	runner := newAccessControlRunner()
	restoreCmd = runner

	stageRoot := "/stage"
	domains := `
ldap: myldap
  comment testrealm
  base_dn dc=example,dc=com
`
	userCfg := `
role: MyRole
  privs VM.Audit

group: MyGroup
  comment testgroup
  users alice@pam

user: alice@pam
  comment Alice
  enable 1
  expire 0
  groups MyGroup

acl: 1
  path /vms/100
  roles MyRole
  users alice@pam
  propagate 1
`

	if err := fakeFS.WriteFile(stageRoot+"/etc/pve/domains.cfg", []byte(domains), 0o640); err != nil {
		t.Fatalf("write staged domains.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/pve/user.cfg", []byte(userCfg), 0o640); err != nil {
		t.Fatalf("write staged user.cfg: %v", err)
	}

	if err := applyPVEAccessControlFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPVEAccessControlFromStage error: %v", err)
	}

	want := []commandCall{
		{
			name: "pvesh",
			args: []string{
				"set", "/access/domains/myldap",
				"--comment", "testrealm",
				"--base_dn", "dc=example,dc=com",
			},
		},
		{
			name: "pvesh",
			args: []string{
				"create", "/access/domains",
				"--realm", "myldap",
				"--type", "ldap",
				"--comment", "testrealm",
				"--base_dn", "dc=example,dc=com",
			},
		},
		{
			name: "pvesh",
			args: []string{
				"set", "/access/roles/MyRole",
				"--privs", "VM.Audit",
			},
		},
		{
			name: "pvesh",
			args: []string{
				"create", "/access/roles",
				"--roleid", "MyRole",
				"--privs", "VM.Audit",
			},
		},
		{
			name: "pvesh",
			args: []string{
				"set", "/access/groups/MyGroup",
				"--comment", "testgroup",
			},
		},
		{
			name: "pvesh",
			args: []string{
				"create", "/access/groups",
				"--groupid", "MyGroup",
				"--comment", "testgroup",
			},
		},
		{
			name: "pvesh",
			args: []string{
				"set", "/access/users/alice@pam",
				"--comment", "Alice",
				"--enable", "1",
				"--expire", "0",
				"--groups", "MyGroup",
			},
		},
		{
			name: "pvesh",
			args: []string{
				"create", "/access/users",
				"--userid", "alice@pam",
				"--comment", "Alice",
				"--enable", "1",
				"--expire", "0",
				"--groups", "MyGroup",
			},
		},
		{
			name: "pvesh",
			args: []string{
				"set", "/access/groups/MyGroup",
				"--users", "alice@pam",
			},
		},
		{
			name: "pvesh",
			args: []string{
				"set", "/access/acl",
				"--path", "/vms/100",
				"--roles", "MyRole",
				"--users", "alice@pam",
				"--propagate", "1",
			},
		},
	}

	if len(runner.calls) != len(want) {
		t.Fatalf("calls=%d want %d: %#v", len(runner.calls), len(want), runner.calls)
	}
	for i := range want {
		if runner.calls[i].name != want[i].name {
			t.Fatalf("call[%d].name=%q want %q", i, runner.calls[i].name, want[i].name)
		}
		if fmt.Sprintf("%#v", runner.calls[i].args) != fmt.Sprintf("%#v", want[i].args) {
			t.Fatalf("call[%d].args=%#v want %#v", i, runner.calls[i].args, want[i].args)
		}
	}
}

func TestApplyPBSAccessControlFromStage_WritesFilesWithPermissions(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	userCfg := "user: root@pam\n  enable 1\n"
	domainsCfg := "pam: pam\n  comment builtin\n"
	aclCfg := "acl: 1\n  path /\n  users root@pam\n  roles Admin\n"

	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/user.cfg", []byte(userCfg), 0o640); err != nil {
		t.Fatalf("write staged user.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/domains.cfg", []byte(domainsCfg), 0o640); err != nil {
		t.Fatalf("write staged domains.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/acl.cfg", []byte(aclCfg), 0o640); err != nil {
		t.Fatalf("write staged acl.cfg: %v", err)
	}

	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/shadow.json", []byte("{}"), 0o600); err != nil {
		t.Fatalf("write staged shadow.json: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/token.shadow", []byte("token"), 0o600); err != nil {
		t.Fatalf("write staged token.shadow: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/tfa.json", []byte("[]"), 0o600); err != nil {
		t.Fatalf("write staged tfa.json: %v", err)
	}

	if err := applyPBSAccessControlFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSAccessControlFromStage error: %v", err)
	}

	expectPerm := func(path string, perm os.FileMode) {
		t.Helper()
		info, err := fakeFS.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != perm {
			t.Fatalf("%s mode=%#o want %#o", path, info.Mode().Perm(), perm)
		}
	}

	expectPerm("/etc/proxmox-backup/user.cfg", 0o640)
	expectPerm("/etc/proxmox-backup/domains.cfg", 0o640)
	expectPerm("/etc/proxmox-backup/acl.cfg", 0o640)
	expectPerm("/etc/proxmox-backup/shadow.json", 0o600)
	expectPerm("/etc/proxmox-backup/token.shadow", 0o600)
	expectPerm("/etc/proxmox-backup/tfa.json", 0o600)
}

func TestApplyPVEAccessControlFromStage_GeneratesLocalPasswordsAndWritesReport(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	runner := newAccessControlRunner()
	restoreCmd = runner

	stageRoot := "/stage"
	userCfg := `
user: bob@pve
  comment Bob
  enable 1
`
	if err := fakeFS.WriteFile(stageRoot+"/etc/pve/user.cfg", []byte(userCfg), 0o640); err != nil {
		t.Fatalf("write staged user.cfg: %v", err)
	}

	if err := applyPVEAccessControlFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPVEAccessControlFromStage error: %v", err)
	}

	password := ""
	for _, call := range runner.calls {
		if call.name != "pvesh" {
			continue
		}
		if len(call.args) < 2 {
			continue
		}
		found := false
		for i := 0; i < len(call.args)-1; i++ {
			if call.args[i] == "--userid" && call.args[i+1] == "bob@pve" {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		for i := 0; i < len(call.args)-1; i++ {
			if call.args[i] == "--password" {
				password = call.args[i+1]
				break
			}
		}
	}
	if password == "" {
		t.Fatalf("expected generated password passed to pvesh create user")
	}

	reportPath := stageRoot + "/pve_access_control_secrets.json"
	data, err := fakeFS.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read %s: %v", reportPath, err)
	}
	var report pveAccessControlSecretsReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if len(report.Users) != 1 {
		t.Fatalf("report.Users=%d want 1", len(report.Users))
	}
	if report.Users[0].UserID != "bob@pve" {
		t.Fatalf("report.Users[0].UserID=%q want %q", report.Users[0].UserID, "bob@pve")
	}
	if report.Users[0].Password != password {
		t.Fatalf("report.Users[0].Password mismatch")
	}
	info, err := fakeFS.Stat(reportPath)
	if err != nil {
		t.Fatalf("stat %s: %v", reportPath, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode=%#o want %#o", reportPath, info.Mode().Perm(), 0o600)
	}
}

func TestApplyPVEAccessControlFromStage_RegeneratesTokensBeforeApplyingACL(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	runner := newAccessControlRunner()
	restoreCmd = runner

	stageRoot := "/stage"
	userCfg := `
role: MyRole
  privs VM.Audit

user: bob@pve
  enable 1

acl: 1
  path /
  roles MyRole
  tokens bob@pve!mytoken
  propagate 1
`
	tokenCfg := `
token: bob@pve!mytoken
  comment test
  expire 0
  privsep 0
`

	if err := fakeFS.WriteFile(stageRoot+"/etc/pve/user.cfg", []byte(userCfg), 0o640); err != nil {
		t.Fatalf("write staged user.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/pve/priv/token.cfg", []byte(tokenCfg), 0o600); err != nil {
		t.Fatalf("write staged token.cfg: %v", err)
	}

	if err := applyPVEAccessControlFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPVEAccessControlFromStage error: %v", err)
	}

	idxToken := -1
	idxACL := -1
	for i, call := range runner.calls {
		if call.name != "pvesh" {
			continue
		}
		for j := 0; j < len(call.args)-1; j++ {
			if call.args[j] == "create" && strings.Contains(call.args[j+1], "/token") {
				idxToken = i
				break
			}
		}
		if len(call.args) >= 2 && call.args[0] == "set" && call.args[1] == "/access/acl" {
			idxACL = i
		}
	}
	if idxToken == -1 || idxACL == -1 {
		t.Fatalf("expected both token creation and ACL apply calls, got token=%d acl=%d calls=%#v", idxToken, idxACL, runner.calls)
	}
	if idxToken > idxACL {
		t.Fatalf("expected token create before ACL apply, got token=%d acl=%d", idxToken, idxACL)
	}

	reportPath := stageRoot + "/pve_access_control_secrets.json"
	data, err := fakeFS.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read %s: %v", reportPath, err)
	}
	var report pveAccessControlSecretsReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if len(report.APITokens) != 1 {
		t.Fatalf("report.APITokens=%d want 1", len(report.APITokens))
	}
	token := report.APITokens[0]
	if token.UserID != "bob@pve" || token.TokenID != "mytoken" || token.FullTokenID != "bob@pve!mytoken" {
		t.Fatalf("unexpected token entry: %#v", token)
	}
	if token.Value != "secret-bob@pve!mytoken" {
		t.Fatalf("unexpected token secret value: %q", token.Value)
	}
}
