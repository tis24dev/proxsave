package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestApplyPVEAccessControlFromStage_Restores1To1ExceptRoot(t *testing.T) {
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

pve: pve
  comment freshpve
`
	currentUser := `
user: root@pam
  comment FreshRoot
  enable 1
`
	currentToken := `
token: root@pam!fresh
  comment fresh-token
`
	currentTFA := `
totp: root@pam
  user root@pam
  comment fresh-tfa
`
	currentShadow := `
user: root@pam
  hash fresh-hash
`

	if err := fakeFS.WriteFile(pveDomainsCfgPath, []byte(currentDomains), 0o640); err != nil {
		t.Fatalf("write current domains.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(pveUserCfgPath, []byte(currentUser), 0o640); err != nil {
		t.Fatalf("write current user.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(pveTokenCfgPath, []byte(currentToken), 0o600); err != nil {
		t.Fatalf("write current token.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(pveTFACfgPath, []byte(currentTFA), 0o600); err != nil {
		t.Fatalf("write current tfa.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(pveShadowCfgPath, []byte(currentShadow), 0o600); err != nil {
		t.Fatalf("write current shadow.cfg: %v", err)
	}

	// Backup/stage includes a conflicting root@pam definition (must NOT be applied), plus a @pve user.
	stagedDomains := `
pam: pam
  comment backup-pam-should-not-win

ldap: myldap
  base_dn dc=example,dc=com
`
	stagedUser := `
role: MyRole
  privs VM.Audit

user: root@pam
  enable 0

user: alice@pve
  enable 1

acl: 1
  path /
  roles MyRole
  users alice@pve
  propagate 1
`
	stagedToken := `
token: root@pam!old
  comment old-token-should-not-win

token: alice@pve!mytoken
  comment alice-token
`
	stagedTFA := `
totp: root@pam
  user root@pam
  comment old-tfa-should-not-win

totp: alice@pve
  user alice@pve
`
	stagedShadow := `
user: root@pam
  hash old-hash-should-not-win

user: alice@pve
  hash alice-hash
`

	if err := fakeFS.WriteFile(stageRoot+"/etc/pve/domains.cfg", []byte(stagedDomains), 0o640); err != nil {
		t.Fatalf("write staged domains.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/pve/user.cfg", []byte(stagedUser), 0o640); err != nil {
		t.Fatalf("write staged user.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/pve/priv/token.cfg", []byte(stagedToken), 0o600); err != nil {
		t.Fatalf("write staged token.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/pve/priv/tfa.cfg", []byte(stagedTFA), 0o600); err != nil {
		t.Fatalf("write staged tfa.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/pve/priv/shadow.cfg", []byte(stagedShadow), 0o600); err != nil {
		t.Fatalf("write staged shadow.cfg: %v", err)
	}

	if err := applyPVEAccessControlFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPVEAccessControlFromStage error: %v", err)
	}

	// Root realm safety: pam realm must be preserved from fresh install (comment should match currentDomains).
	gotDomains, err := fakeFS.ReadFile(pveDomainsCfgPath)
	if err != nil {
		t.Fatalf("read %s: %v", pveDomainsCfgPath, err)
	}
	if !strings.Contains(string(gotDomains), "comment freshpam") {
		t.Fatalf("expected fresh pam realm to be preserved, got:\n%s", string(gotDomains))
	}
	// pve realm should be present because alice@pve exists.
	if !strings.Contains(string(gotDomains), "pve: pve") {
		t.Fatalf("expected pve realm to be present, got:\n%s", string(gotDomains))
	}

	// Root user safety: root@pam must be preserved from currentUser and not disabled.
	gotUser, err := fakeFS.ReadFile(pveUserCfgPath)
	if err != nil {
		t.Fatalf("read %s: %v", pveUserCfgPath, err)
	}
	if !strings.Contains(string(gotUser), "comment FreshRoot") {
		t.Fatalf("expected root@pam section preserved from fresh install, got:\n%s", string(gotUser))
	}
	if strings.Contains(string(gotUser), "user: root@pam\n  enable 0") {
		t.Fatalf("expected staged root@pam not to be applied, got:\n%s", string(gotUser))
	}
	// Root admin ACL safety rail should be present.
	if !strings.Contains(string(gotUser), "acl: proxsave-root-admin") ||
		!strings.Contains(string(gotUser), "users root@pam") ||
		!strings.Contains(string(gotUser), "roles Administrator") ||
		!strings.Contains(string(gotUser), "path /") {
		t.Fatalf("expected proxsave root admin ACL to be injected, got:\n%s", string(gotUser))
	}

	// Token safety rail: keep fresh root token, do not import staged root token.
	gotToken, err := fakeFS.ReadFile(pveTokenCfgPath)
	if err != nil {
		t.Fatalf("read %s: %v", pveTokenCfgPath, err)
	}
	if !strings.Contains(string(gotToken), "token: root@pam!fresh") {
		t.Fatalf("expected fresh root token to be preserved, got:\n%s", string(gotToken))
	}
	if strings.Contains(string(gotToken), "token: root@pam!old") {
		t.Fatalf("expected staged root token to be excluded, got:\n%s", string(gotToken))
	}
	if !strings.Contains(string(gotToken), "token: alice@pve!mytoken") {
		t.Fatalf("expected alice token to be restored, got:\n%s", string(gotToken))
	}

	// TFA safety rail: keep fresh root TFA, restore alice TFA.
	gotTFA, err := fakeFS.ReadFile(pveTFACfgPath)
	if err != nil {
		t.Fatalf("read %s: %v", pveTFACfgPath, err)
	}
	if !strings.Contains(string(gotTFA), "comment fresh-tfa") {
		t.Fatalf("expected fresh root TFA to be preserved, got:\n%s", string(gotTFA))
	}
	if strings.Contains(string(gotTFA), "comment old-tfa-should-not-win") {
		t.Fatalf("expected staged root TFA to be excluded, got:\n%s", string(gotTFA))
	}
	if !strings.Contains(string(gotTFA), "totp: alice@pve") {
		t.Fatalf("expected alice TFA to be restored, got:\n%s", string(gotTFA))
	}

	// Shadow safety rail: keep fresh root hash, restore alice hash.
	gotShadow, err := fakeFS.ReadFile(pveShadowCfgPath)
	if err != nil {
		t.Fatalf("read %s: %v", pveShadowCfgPath, err)
	}
	if !strings.Contains(string(gotShadow), "hash fresh-hash") {
		t.Fatalf("expected fresh root shadow to be preserved, got:\n%s", string(gotShadow))
	}
	if strings.Contains(string(gotShadow), "hash old-hash-should-not-win") {
		t.Fatalf("expected staged root shadow to be excluded, got:\n%s", string(gotShadow))
	}
	if !strings.Contains(string(gotShadow), "hash alice-hash") {
		t.Fatalf("expected alice shadow to be restored, got:\n%s", string(gotShadow))
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
