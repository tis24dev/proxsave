package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
)

// The PVE access control files in the formats PVE writes them (measured on PVE 9.1.9
// with pveum, 2026-10-26): user.cfg and its colon lines, token.cfg "<id> <secret>",
// shadow.cfg "<name>:<hash>:", tfa.cfg JSON. The backup is restored 1:1 except
// root@pam: its user line, its tokens with their secrets and ACL entries, and its TFA
// are this host's, and root@pam keeps Administrator on "/".
func TestApplyPVEAccessControlFromStage_Restores1To1ExceptRoot(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	files := map[string]string{
		// This host.
		pveDomainsCfgPath: "pam: pam\n\tcomment freshpam\n\npve: pve\n\tcomment freshpve\n",
		pveUserCfgPath: "user:carol@pve:1:0::::::\n" +
			"user:root@pam:1:0:::fresh@host.example:FreshRoot::\n" +
			"token:root@pam!fresh:0:1:fresh token:\n\n" +
			"acl:1:/:root@pam!fresh:PVEAuditor:\n" +
			"acl:1:/vms:carol@pve:PVEAuditor:\n",
		pveTokenCfgPath:  "carol@pve!c 22222222-2222-2222-2222-222222222222\nroot@pam!fresh 11111111-1111-1111-1111-111111111111\n",
		pveShadowCfgPath: "carol:freshhash:\n",
		pveTFACfgPath:    `{"users":{"carol@pve":{"totp":[{"id":"c"}]},"root@pam":{"totp":[{"id":"fresh"}]}}}`,
		// The backup.
		stageRoot + "/etc/pve/domains.cfg": "pam: pam\n\tcomment backup-pam\n\nldap: myldap\n\tbase_dn dc=example,dc=com\n",
		stageRoot + "/etc/pve/user.cfg": "user:alice@pve:1:0:::a@x.y:Alice:x:\n" +
			"token:alice@pve!mytoken:0:1:alice token:\n" +
			"user:root@pam:0:0:::old@x.y:OldRoot::\n" +
			"token:root@pam!old:0:1:old token:\n\n" +
			"group:admins:alice@pve:Admins:\n\n" +
			"pool:prod:production:100,101:local-lvm:\n\n" +
			"role:MyRole:VM.Audit:\n\n" +
			"acl:1:/:root@pam!old,alice@pve:MyRole:\n" +
			"acl:1:/storage:root@pam!old:PVEAuditor:\n",
		stageRoot + "/etc/pve/priv/token.cfg":  "alice@pve!mytoken 33333333-3333-3333-3333-333333333333\nroot@pam!old 44444444-4444-4444-4444-444444444444\n",
		stageRoot + "/etc/pve/priv/shadow.cfg": "alice:alicehash:\n",
		stageRoot + "/etc/pve/priv/tfa.cfg":    `{"users":{"alice@pve":{"totp":[{"id":"a"}]},"root@pam":{"totp":[{"id":"old"}]}},"webauthn":{"rp":"pve.example"}}`,
	}
	for path, content := range files {
		if err := fakeFS.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	if err := applyPVEAccessControlFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPVEAccessControlFromStage error: %v", err)
	}

	read := func(path string) string {
		t.Helper()
		got, err := fakeFS.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(got)
	}
	wantUser := "user:alice@pve:1:0:::a@x.y:Alice:x:\n" +
		"token:alice@pve!mytoken:0:1:alice token:\n" +
		"user:root@pam:1:0:::fresh@host.example:FreshRoot::\n" +
		"token:root@pam!fresh:0:1:fresh token:\n\n" +
		"group:admins:alice@pve:Admins:\n\n" +
		"pool:prod:production:100,101:local-lvm:\n\n" +
		"role:MyRole:VM.Audit:\n\n" +
		"acl:1:/:alice@pve:MyRole:\n" +
		"acl:1:/:root@pam!fresh:PVEAuditor:\n" +
		"acl:1:/:root@pam:Administrator:\n"
	if got := read(pveUserCfgPath); got != wantUser {
		t.Fatalf("user.cfg\n--- got ---\n%s--- want ---\n%s", got, wantUser)
	}
	if got, want := read(pveTokenCfgPath), "alice@pve!mytoken 33333333-3333-3333-3333-333333333333\nroot@pam!fresh 11111111-1111-1111-1111-111111111111\n"; got != want {
		t.Fatalf("token.cfg\n--- got ---\n%s--- want ---\n%s", got, want)
	}
	if got, want := read(pveShadowCfgPath), "alice:alicehash:\n"; got != want {
		t.Fatalf("shadow.cfg = %q, want %q", got, want)
	}
	if got, want := read(pveTFACfgPath), `{"users":{"alice@pve":{"totp":[{"id":"a"}]},"root@pam":{"totp":[{"id":"fresh"}]}},"webauthn":{"rp":"pve.example"}}`+"\n"; got != want {
		t.Fatalf("tfa.cfg\n--- got ---\n%s--- want ---\n%s", got, want)
	}
	domains := read(pveDomainsCfgPath)
	for _, want := range []string{"comment freshpam", "pve: pve", "ldap: myldap"} {
		if !strings.Contains(domains, want) {
			t.Fatalf("domains.cfg lacks %q:\n%s", want, domains)
		}
	}
}

func TestMaybeApplyAccessControlFromStage_SkipsClusterBackupInSafeMode(t *testing.T) {
	plan := &RestorePlan{
		SystemType:    SystemTypePVE,
		ClusterBackup: true,
	}
	plan.StagedCategories = []Category{{ID: "pve_access_control"}}

	if err := maybeApplyAccessControlFromStage(context.Background(), newTestLogger(), plan, "/stage", false); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestApplyPBSAccessControlFromStage_Restores1To1ExceptRoot(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"

	// Fresh-install baseline (must be preserved for root@pam safety rail).
	currentDomains := `
pam: pam
  comment freshpam

pbs: pbs
  comment freshpbs
`
	currentUser := `
user: root@pam
  comment FreshRoot
  enable 1

token: root@pam!fresh
  comment fresh-token

user: keepme@pbs
  enable 1
`
	currentTokenShadow := `{"root@pam!fresh":"fresh-secret"}`
	currentTFA := `{"users":{"root@pam":{"totp":[9]}},"webauthn":{"rp":"fresh"}}`

	if err := fakeFS.WriteFile(pbsDomainsCfgPath, []byte(currentDomains), 0o640); err != nil {
		t.Fatalf("write current domains.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(pbsUserCfgPath, []byte(currentUser), 0o640); err != nil {
		t.Fatalf("write current user.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(pbsTokenShadowPath, []byte(currentTokenShadow), 0o600); err != nil {
		t.Fatalf("write current token.shadow: %v", err)
	}
	if err := fakeFS.WriteFile(pbsTFAJSONPath, []byte(currentTFA), 0o600); err != nil {
		t.Fatalf("write current tfa.json: %v", err)
	}

	// Backup/stage includes a conflicting root@pam definition (must NOT be applied), plus a @pbs user.
	stagedDomains := `
pam: pam
  comment backup-pam-should-not-win

ldap: myldap
  base_dn dc=example,dc=com
`
	stagedUser := `
user: root@pam
  enable 0

token: root@pam!old
  comment old-token-should-not-win

user: alice@pbs
  enable 1
`
	stagedACL := `
acl:1:/:root@pam:Admin
acl:1:/:root@pam!old:Admin
acl:1:/:alice@pbs:Admin
`
	stagedShadow := `{"root@pbs":"old-root-hash","alice@pbs":"alice-hash"}`
	stagedTokenShadow := `{"root@pam!old":"old-secret","alice@pbs!tok":"alice-secret"}`
	stagedTFA := `{"users":{"root@pam":{"totp":[1]},"alice@pbs":{"totp":[2]}},"webauthn":{"rp":"backup"}}`

	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/domains.cfg", []byte(stagedDomains), 0o640); err != nil {
		t.Fatalf("write staged domains.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/user.cfg", []byte(stagedUser), 0o640); err != nil {
		t.Fatalf("write staged user.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/acl.cfg", []byte(stagedACL), 0o640); err != nil {
		t.Fatalf("write staged acl.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/shadow.json", []byte(stagedShadow), 0o600); err != nil {
		t.Fatalf("write staged shadow.json: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/token.shadow", []byte(stagedTokenShadow), 0o600); err != nil {
		t.Fatalf("write staged token.shadow: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/tfa.json", []byte(stagedTFA), 0o600); err != nil {
		t.Fatalf("write staged tfa.json: %v", err)
	}

	if err := applyPBSAccessControlFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSAccessControlFromStage error: %v", err)
	}

	// Root realm safety: pam realm must be preserved from fresh install.
	gotDomains, err := fakeFS.ReadFile(pbsDomainsCfgPath)
	if err != nil {
		t.Fatalf("read %s: %v", pbsDomainsCfgPath, err)
	}
	if !strings.Contains(string(gotDomains), "comment freshpam") {
		t.Fatalf("expected fresh pam realm to be preserved, got:\n%s", string(gotDomains))
	}
	if !strings.Contains(string(gotDomains), "ldap: myldap") {
		t.Fatalf("expected ldap realm restored, got:\n%s", string(gotDomains))
	}
	// pbs realm should be present because alice@pbs exists.
	if !strings.Contains(string(gotDomains), "pbs: pbs") {
		t.Fatalf("expected pbs realm to be present, got:\n%s", string(gotDomains))
	}

	// Root user safety: root@pam must be preserved from currentUser and not disabled.
	gotUser, err := fakeFS.ReadFile(pbsUserCfgPath)
	if err != nil {
		t.Fatalf("read %s: %v", pbsUserCfgPath, err)
	}
	if !strings.Contains(string(gotUser), "comment FreshRoot") {
		t.Fatalf("expected root@pam section preserved from fresh install, got:\n%s", string(gotUser))
	}
	if strings.Contains(string(gotUser), "user: root@pam\n  enable 0") {
		t.Fatalf("expected staged root@pam not to be applied, got:\n%s", string(gotUser))
	}
	// Root token safety: keep fresh root token, do not import staged root token.
	if !strings.Contains(string(gotUser), "token: root@pam!fresh") {
		t.Fatalf("expected fresh root token to be preserved, got:\n%s", string(gotUser))
	}
	if strings.Contains(string(gotUser), "token: root@pam!old") {
		t.Fatalf("expected staged root token to be excluded, got:\n%s", string(gotUser))
	}
	if !strings.Contains(string(gotUser), "user: alice@pbs") {
		t.Fatalf("expected alice user to be restored, got:\n%s", string(gotUser))
	}

	// ACL safety rail: ensure root has Admin on / and root token ACLs from backup are excluded.
	gotACL, err := fakeFS.ReadFile(pbsACLCfgPath)
	if err != nil {
		t.Fatalf("read %s: %v", pbsACLCfgPath, err)
	}
	if !strings.Contains(string(gotACL), "acl:1:/:root@pam:Admin") {
		t.Fatalf("expected root admin ACL to be present, got:\n%s", string(gotACL))
	}
	if strings.Contains(string(gotACL), "root@pam!old") {
		t.Fatalf("expected staged root token ACL to be excluded, got:\n%s", string(gotACL))
	}
	if !strings.Contains(string(gotACL), "alice@pbs") {
		t.Fatalf("expected alice ACL to be restored, got:\n%s", string(gotACL))
	}

	// token.shadow safety rail: keep fresh root token secret, restore alice token secret, exclude staged root token secret.
	gotTokenShadow, err := fakeFS.ReadFile(pbsTokenShadowPath)
	if err != nil {
		t.Fatalf("read %s: %v", pbsTokenShadowPath, err)
	}
	if strings.Contains(string(gotTokenShadow), "root@pam!old") {
		t.Fatalf("expected staged root token secret to be excluded, got:\n%s", string(gotTokenShadow))
	}
	if !strings.Contains(string(gotTokenShadow), "root@pam!fresh") {
		t.Fatalf("expected fresh root token secret to be preserved, got:\n%s", string(gotTokenShadow))
	}
	if !strings.Contains(string(gotTokenShadow), "alice@pbs!tok") {
		t.Fatalf("expected alice token secret to be restored, got:\n%s", string(gotTokenShadow))
	}

	// tfa.json safety rail: keep fresh root TFA, restore alice TFA, preserve backup webauthn config.
	gotTFA, err := fakeFS.ReadFile(pbsTFAJSONPath)
	if err != nil {
		t.Fatalf("read %s: %v", pbsTFAJSONPath, err)
	}
	if !strings.Contains(string(gotTFA), "\"root@pam\"") || strings.Contains(string(gotTFA), "\"totp\":[1]") {
		t.Fatalf("expected staged root TFA to be excluded and fresh root TFA preserved, got:\n%s", string(gotTFA))
	}
	if !strings.Contains(string(gotTFA), "\"alice@pbs\"") {
		t.Fatalf("expected alice TFA to be restored, got:\n%s", string(gotTFA))
	}
	if !strings.Contains(string(gotTFA), "\"rp\":\"backup\"") {
		t.Fatalf("expected backup webauthn config to be preserved, got:\n%s", string(gotTFA))
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
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/token.shadow", []byte("{}"), 0o600); err != nil {
		t.Fatalf("write staged token.shadow: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/tfa.json", []byte("{}"), 0o600); err != nil {
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
