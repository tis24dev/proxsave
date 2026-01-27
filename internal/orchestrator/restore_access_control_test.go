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

