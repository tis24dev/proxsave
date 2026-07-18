package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestApplySensitiveFileFromStage_Branches(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	logger := newTestLogger()
	stageRoot := "/stage"
	rel := "etc/proxmox-backup/secret.cfg"
	dest := "/etc/proxmox-backup/secret.cfg"
	stagePath := filepath.Join(stageRoot, rel)

	if err := applySensitiveFileFromStage(logger, stageRoot, rel, dest, 0o600); err != nil {
		t.Fatalf("missing staged file should be ignored: %v", err)
	}

	restoreFS = readFileFailFS{FS: fakeFS, failPath: stagePath, err: errors.New("boom")}
	if err := applySensitiveFileFromStage(logger, stageRoot, rel, dest, 0o600); err == nil || !strings.Contains(err.Error(), "read staged "+rel) {
		t.Fatalf("expected staged read error, got %v", err)
	}
	restoreFS = fakeFS

	if err := fakeFS.WriteFile(dest, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("seed destination file: %v", err)
	}
	if err := fakeFS.WriteFile(stagePath, []byte(" \n\t"), 0o600); err != nil {
		t.Fatalf("write empty staged file: %v", err)
	}
	if err := applySensitiveFileFromStage(logger, stageRoot, rel, dest, 0o600); err != nil {
		t.Fatalf("empty staged file should remove destination: %v", err)
	}
	if _, err := fakeFS.Stat(dest); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected destination removed, stat err=%v", err)
	}

	if err := fakeFS.WriteFile(stagePath, []byte("  token=abc123  \n"), 0o600); err != nil {
		t.Fatalf("write staged non-empty file: %v", err)
	}
	if err := applySensitiveFileFromStage(logger, stageRoot, rel, dest, 0o600); err != nil {
		t.Fatalf("apply non-empty staged file: %v", err)
	}
	got, err := fakeFS.ReadFile(dest)
	if err != nil {
		t.Fatalf("read applied destination: %v", err)
	}
	if string(got) != "token=abc123\n" {
		t.Fatalf("destination=%q want %q", string(got), "token=abc123\n")
	}
}

func TestMaybeApplyAccessControlFromStage_Branches(t *testing.T) {
	ctx := context.Background()
	logger := newTestLogger()

	if err := maybeApplyAccessControlFromStage(ctx, logger, nil, "/stage", false); err != nil {
		t.Fatalf("nil plan should be ignored: %v", err)
	}

	planNoCategories := &RestorePlan{
		SystemType:       SystemTypePVE,
		NormalCategories: []Category{{ID: "unrelated", Type: CategoryTypePVE}},
	}
	if err := maybeApplyAccessControlFromStage(ctx, logger, planNoCategories, "/stage", false); err != nil {
		t.Fatalf("plan without access-control categories should be ignored: %v", err)
	}
	if err := maybeApplyAccessControlFromStage(ctx, logger, planNoCategories, "   ", false); err != nil {
		t.Fatalf("blank stage root should be ignored: %v", err)
	}

	pveClusterSafe := &RestorePlan{
		SystemType:          SystemTypePVE,
		ClusterBackup:       true,
		NeedsClusterRestore: false,
		NormalCategories:    []Category{{ID: "pve_access_control", Type: CategoryTypePVE}},
	}
	if err := maybeApplyAccessControlFromStage(ctx, logger, pveClusterSafe, "/stage", true); err != nil {
		t.Fatalf("dry-run PVE cluster-safe plan should return nil: %v", err)
	}

	pbsDryRun := &RestorePlan{
		SystemType:       SystemTypePBS,
		NormalCategories: []Category{{ID: "pbs_access_control", Type: CategoryTypePBS}},
	}
	if err := maybeApplyAccessControlFromStage(ctx, logger, pbsDryRun, "/stage", true); err != nil {
		t.Fatalf("dry-run PBS plan should return nil: %v", err)
	}

	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if err := maybeApplyAccessControlFromStage(ctx, logger, pbsDryRun, "/stage", false); err != nil {
		t.Fatalf("non-real FS should skip staged access-control apply: %v", err)
	}
}

func TestApplyPBSAccessControlFromStage_EarlyAndReadErrors(t *testing.T) {
	logger := newTestLogger()

	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := applyPBSAccessControlFromStage(canceled, logger, "/stage"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	var nilCtx context.Context // deliberately nil: verifies nil-context handling is safe
	if err := applyPBSAccessControlFromStage(nilCtx, logger, "/stage"); err != nil {
		t.Fatalf("nil context with missing staged files should be safe, got %v", err)
	}

	restoreFS = readFileFailFS{FS: fakeFS, failPath: "/stage/etc/proxmox-backup/user.cfg", err: syscall.EIO}
	if err := applyPBSAccessControlFromStage(context.Background(), logger, "/stage"); err == nil || !strings.Contains(err.Error(), "read staged etc/proxmox-backup/user.cfg") {
		t.Fatalf("expected staged user.cfg read error, got %v", err)
	}
}

func TestPBSACLApplyAndHelpers_Branches(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	logger := newTestLogger()

	if err := fakeFS.WriteFile(pbsACLCfgPath, []byte("old\n"), 0o640); err != nil {
		t.Fatalf("seed old acl file: %v", err)
	}
	if err := applyPBSACLFromStage(logger, " \n\t"); err != nil {
		t.Fatalf("empty ACL should remove current file: %v", err)
	}
	if _, err := fakeFS.Stat(pbsACLCfgPath); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected ACL removed, stat err=%v", err)
	}

	if err := fakeFS.WriteFile(pbsACLCfgPath, []byte("keep-me\n"), 0o640); err != nil {
		t.Fatalf("seed acl file for unknown format: %v", err)
	}
	if err := applyPBSACLFromStage(logger, "this-is-not-known-format"); err != nil {
		t.Fatalf("unknown ACL format should be ignored, got %v", err)
	}
	kept, err := fakeFS.ReadFile(pbsACLCfgPath)
	if err != nil {
		t.Fatalf("read acl after unknown format: %v", err)
	}
	if string(kept) != "keep-me\n" {
		t.Fatalf("unknown format should keep previous acl content, got %q", string(kept))
	}

	sectionACL := strings.Join([]string{
		"acl: backup-acl",
		"  path /",
		"  users root@pam alice@pbs",
		"  roles Admin",
		"",
	}, "\n")
	if err := applyPBSACLFromStage(logger, sectionACL); err != nil {
		t.Fatalf("apply section ACL format: %v", err)
	}
	gotSection, err := fakeFS.ReadFile(pbsACLCfgPath)
	if err != nil {
		t.Fatalf("read section ACL output: %v", err)
	}
	if !strings.Contains(string(gotSection), "users alice@pbs") {
		t.Fatalf("expected root user filtered from staged users, got:\n%s", string(gotSection))
	}
	if !strings.Contains(string(gotSection), "acl: proxsave-root-admin") {
		t.Fatalf("expected root admin safety ACL injected, got:\n%s", string(gotSection))
	}

	lineACL := strings.Join([]string{
		"acl:1:/:root@pam!old,alice@pbs:Admin",
		"acl:0:/datastore:bob@pbs:DatastoreAdmin",
		"keep-verbatim-line",
		"",
	}, "\n")
	if err := applyPBSACLFromStage(logger, lineACL); err != nil {
		t.Fatalf("apply line ACL format: %v", err)
	}
	gotLine, err := fakeFS.ReadFile(pbsACLCfgPath)
	if err != nil {
		t.Fatalf("read line ACL output: %v", err)
	}
	if !strings.Contains(string(gotLine), "acl:1:/:alice@pbs:Admin") {
		t.Fatalf("expected root token filtered from ACL user list, got:\n%s", string(gotLine))
	}
	if !strings.Contains(string(gotLine), "keep-verbatim-line") {
		t.Fatalf("expected unknown non-empty lines preserved, got:\n%s", string(gotLine))
	}
	if !strings.Contains(string(gotLine), "acl:1:/:root@pam:Admin") {
		t.Fatalf("expected root admin ACL appended in line format, got:\n%s", string(gotLine))
	}

	if isPBSACLLineFormat("user: root@pam") {
		t.Fatalf("expected false for non-acl line format")
	}
	if !isPBSACLLineFormat("acl:1:/:root@pam:Admin") {
		t.Fatalf("expected true for valid acl line format")
	}
	if isPBSACLLineFormat("# comment only\n\n") {
		t.Fatalf("expected false for comment-only content")
	}

	if _, ok := parsePBSACLLine("   "); ok {
		t.Fatalf("empty line should return ok=false")
	}
	if entry, ok := parsePBSACLLine("custom line"); !ok || entry.Raw != "custom line" {
		t.Fatalf("non-acl line parse mismatch: ok=%v entry=%+v", ok, entry)
	}
	if entry, ok := parsePBSACLLine("acl:1:/:missingparts"); !ok || entry.Raw == "" || entry.Path != "" {
		t.Fatalf("invalid acl line should be preserved as raw: ok=%v entry=%+v", ok, entry)
	}
	if entry, ok := parsePBSACLLine("acl:1:/:alice@pbs:Admin"); !ok || entry.Path != "/" || entry.UserList != "alice@pbs" {
		t.Fatalf("valid acl line parse mismatch: ok=%v entry=%+v", ok, entry)
	}

	if got, ok := filterPBSACLUsers("root@pam, root@pam!token"); ok || got != "" {
		t.Fatalf("expected all-root ACL users to be filtered out, got=%q ok=%v", got, ok)
	}

	if err := applyPBSACLLineFormat("acl:1:/:root@pam:Admin\n"); err != nil {
		t.Fatalf("applyPBSACLLineFormat with root already present: %v", err)
	}
	withRoot, err := fakeFS.ReadFile(pbsACLCfgPath)
	if err != nil {
		t.Fatalf("read ACL with preexisting root admin line: %v", err)
	}
	if strings.Count(string(withRoot), "acl:1:/:root@pam:Admin") != 1 {
		t.Fatalf("root admin line should not be duplicated, got:\n%s", string(withRoot))
	}

	entries := []proxmoxNotificationEntry{
		{Key: "users", Value: "old"},
		{Key: "users", Value: "duplicate"},
		{Key: "roles", Value: "Admin"},
	}
	replaced := setSectionEntryValue(entries, "users", "alice@pbs")
	if findSectionEntryValue(replaced, "users") != "alice@pbs" {
		t.Fatalf("setSectionEntryValue should replace first matching key")
	}
	countUsers := 0
	for _, e := range replaced {
		if strings.TrimSpace(e.Key) == "users" {
			countUsers++
		}
	}
	if countUsers != 1 {
		t.Fatalf("setSectionEntryValue should collapse duplicate keys, count=%d", countUsers)
	}
	appended := setSectionEntryValue(replaced, "path", "/")
	if findSectionEntryValue(appended, "path") != "/" {
		t.Fatalf("setSectionEntryValue should append missing key")
	}
	unchanged := setSectionEntryValue(replaced, "   ", "ignored")
	if len(unchanged) != len(replaced) {
		t.Fatalf("empty key should not modify entries")
	}

	sectionsWithRoot := []proxmoxNotificationSection{
		{
			Type: "acl",
			Name: "rule /",
			Entries: []proxmoxNotificationEntry{
				{Key: "users", Value: "root@pam"},
				{Key: "roles", Value: "Admin"},
			},
		},
	}
	if !hasPBSRootAdminOnRootSectionFormat(sectionsWithRoot) {
		t.Fatalf("expected root Admin ACL on / to be detected")
	}
	if hasPBSRootAdminOnRootSectionFormat([]proxmoxNotificationSection{{Type: "acl", Name: "rule /vms"}}) {
		t.Fatalf("unexpected root Admin ACL detection")
	}

	if got := aclPathFromSectionName("rule-name /pool/one extra"); got != "/pool/one" {
		t.Fatalf("aclPathFromSectionName=%q want %q", got, "/pool/one")
	}
	if got := aclPathFromSectionName("rule-name-without-path"); got != "" {
		t.Fatalf("aclPathFromSectionName=%q want empty", got)
	}
}

func TestPBSSecretFilesFromStage_Branches(t *testing.T) {
	t.Run("shadow.json branches", func(t *testing.T) {
		origFS := restoreFS
		t.Cleanup(func() { restoreFS = origFS })
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		logger := newTestLogger()
		stageRoot := "/stage"
		stagePath := filepath.Join(stageRoot, "etc/proxmox-backup/shadow.json")

		if err := applyPBSShadowJSONFromStage(logger, stageRoot); err != nil {
			t.Fatalf("missing staged shadow.json should be ignored: %v", err)
		}

		restoreFS = readFileFailFS{FS: fakeFS, failPath: stagePath, err: syscall.EIO}
		if err := applyPBSShadowJSONFromStage(logger, stageRoot); err == nil || !strings.Contains(err.Error(), "read staged shadow.json") {
			t.Fatalf("expected staged read error, got %v", err)
		}
		restoreFS = fakeFS

		if err := fakeFS.WriteFile(pbsShadowJSONPath, []byte(`{"old":"keep"}`), 0o600); err != nil {
			t.Fatalf("seed current shadow.json: %v", err)
		}
		if err := fakeFS.WriteFile(stagePath, []byte(" \n\t"), 0o600); err != nil {
			t.Fatalf("write empty staged shadow.json: %v", err)
		}
		if err := applyPBSShadowJSONFromStage(logger, stageRoot); err != nil {
			t.Fatalf("empty staged shadow.json should remove destination: %v", err)
		}
		if _, err := fakeFS.Stat(pbsShadowJSONPath); err == nil || !os.IsNotExist(err) {
			t.Fatalf("expected %s removed, stat err=%v", pbsShadowJSONPath, err)
		}

		if err := fakeFS.WriteFile(stagePath, []byte(`{"broken"`), 0o600); err != nil {
			t.Fatalf("write invalid staged shadow.json: %v", err)
		}
		if err := applyPBSShadowJSONFromStage(logger, stageRoot); err == nil || !strings.Contains(err.Error(), "parse staged shadow.json") {
			t.Fatalf("expected parse error, got %v", err)
		}

		if err := fakeFS.WriteFile(stagePath, []byte(`{"root@pbs":"old-root","alice@pbs":"alice-hash"}`), 0o600); err != nil {
			t.Fatalf("write valid staged shadow.json: %v", err)
		}
		restoreFS = readFileFailFS{FS: fakeFS, failPath: pbsShadowJSONPath, err: syscall.EPERM}
		if err := applyPBSShadowJSONFromStage(logger, stageRoot); err == nil || !strings.Contains(err.Error(), "read current shadow.json") {
			t.Fatalf("expected current shadow read error, got %v", err)
		}
		restoreFS = fakeFS

		if err := fakeFS.WriteFile(pbsShadowJSONPath, []byte(`{"root@pam":"fresh-root-hash","bob@pbs":"old-bob"}`), 0o600); err != nil {
			t.Fatalf("seed current shadow.json with root: %v", err)
		}
		if err := applyPBSShadowJSONFromStage(logger, stageRoot); err != nil {
			t.Fatalf("apply valid shadow.json merge: %v", err)
		}
		got, err := fakeFS.ReadFile(pbsShadowJSONPath)
		if err != nil {
			t.Fatalf("read merged shadow.json: %v", err)
		}
		if strings.Contains(string(got), "root@pbs") {
			t.Fatalf("root@pbs from staged backup should be filtered, got=%s", string(got))
		}
		if !strings.Contains(string(got), "root@pam") || !strings.Contains(string(got), "fresh-root-hash") {
			t.Fatalf("current root@pam hash should be preserved, got=%s", string(got))
		}
		if !strings.Contains(string(got), "alice@pbs") {
			t.Fatalf("non-root staged user should be restored, got=%s", string(got))
		}
	})

	t.Run("token.shadow branches", func(t *testing.T) {
		origFS := restoreFS
		t.Cleanup(func() { restoreFS = origFS })
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		logger := newTestLogger()
		stageRoot := "/stage"
		stagePath := filepath.Join(stageRoot, "etc/proxmox-backup/token.shadow")

		if err := applyPBSTokenShadowFromStage(logger, stageRoot); err != nil {
			t.Fatalf("missing staged token.shadow should be ignored: %v", err)
		}

		restoreFS = readFileFailFS{FS: fakeFS, failPath: stagePath, err: syscall.EIO}
		if err := applyPBSTokenShadowFromStage(logger, stageRoot); err == nil || !strings.Contains(err.Error(), "read staged token.shadow") {
			t.Fatalf("expected staged read error, got %v", err)
		}
		restoreFS = fakeFS

		if err := fakeFS.WriteFile(pbsTokenShadowPath, []byte(`{"old":"keep"}`), 0o600); err != nil {
			t.Fatalf("seed token.shadow: %v", err)
		}
		if err := fakeFS.WriteFile(stagePath, []byte(" \n\t"), 0o600); err != nil {
			t.Fatalf("write empty staged token.shadow: %v", err)
		}
		if err := applyPBSTokenShadowFromStage(logger, stageRoot); err != nil {
			t.Fatalf("empty staged token.shadow should remove destination: %v", err)
		}
		if _, err := fakeFS.Stat(pbsTokenShadowPath); err == nil || !os.IsNotExist(err) {
			t.Fatalf("expected %s removed, stat err=%v", pbsTokenShadowPath, err)
		}

		if err := fakeFS.WriteFile(stagePath, []byte(`{"broken"`), 0o600); err != nil {
			t.Fatalf("write invalid staged token.shadow: %v", err)
		}
		if err := applyPBSTokenShadowFromStage(logger, stageRoot); err == nil || !strings.Contains(err.Error(), "parse staged token.shadow") {
			t.Fatalf("expected parse error, got %v", err)
		}

		if err := fakeFS.WriteFile(stagePath, []byte(`{"root@pam!old":"old-secret","alice@pbs!tok":"alice-secret"}`), 0o600); err != nil {
			t.Fatalf("write valid staged token.shadow: %v", err)
		}
		restoreFS = readFileFailFS{FS: fakeFS, failPath: pbsTokenShadowPath, err: syscall.EPERM}
		if err := applyPBSTokenShadowFromStage(logger, stageRoot); err == nil || !strings.Contains(err.Error(), "read current token.shadow") {
			t.Fatalf("expected current token.shadow read error, got %v", err)
		}
		restoreFS = fakeFS

		if err := fakeFS.WriteFile(pbsTokenShadowPath, []byte(`{"root@pam!fresh":"fresh-secret","bob@pbs!old":"old"}`), 0o600); err != nil {
			t.Fatalf("seed current token.shadow: %v", err)
		}
		if err := applyPBSTokenShadowFromStage(logger, stageRoot); err != nil {
			t.Fatalf("apply valid token.shadow merge: %v", err)
		}
		got, err := fakeFS.ReadFile(pbsTokenShadowPath)
		if err != nil {
			t.Fatalf("read merged token.shadow: %v", err)
		}
		if strings.Contains(string(got), "root@pam!old") {
			t.Fatalf("staged root token should be filtered, got=%s", string(got))
		}
		if !strings.Contains(string(got), "root@pam!fresh") {
			t.Fatalf("current root token should be preserved, got=%s", string(got))
		}
		if !strings.Contains(string(got), "alice@pbs!tok") {
			t.Fatalf("non-root staged token should be restored, got=%s", string(got))
		}
	})

	t.Run("tfa.json branches", func(t *testing.T) {
		origFS := restoreFS
		t.Cleanup(func() { restoreFS = origFS })
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		logger := newTestLogger()
		stageRoot := "/stage"
		stagePath := filepath.Join(stageRoot, "etc/proxmox-backup/tfa.json")

		if err := applyPBSTFAJSONFromStage(logger, stageRoot); err != nil {
			t.Fatalf("missing staged tfa.json should be ignored: %v", err)
		}

		restoreFS = readFileFailFS{FS: fakeFS, failPath: stagePath, err: syscall.EIO}
		if err := applyPBSTFAJSONFromStage(logger, stageRoot); err == nil || !strings.Contains(err.Error(), "read staged tfa.json") {
			t.Fatalf("expected staged read error, got %v", err)
		}
		restoreFS = fakeFS

		if err := fakeFS.WriteFile(pbsTFAJSONPath, []byte(`{"users":{}}`), 0o600); err != nil {
			t.Fatalf("seed tfa.json: %v", err)
		}
		if err := fakeFS.WriteFile(stagePath, []byte(" \n\t"), 0o600); err != nil {
			t.Fatalf("write empty staged tfa.json: %v", err)
		}
		if err := applyPBSTFAJSONFromStage(logger, stageRoot); err != nil {
			t.Fatalf("empty staged tfa.json should remove destination: %v", err)
		}
		if _, err := fakeFS.Stat(pbsTFAJSONPath); err == nil || !os.IsNotExist(err) {
			t.Fatalf("expected %s removed, stat err=%v", pbsTFAJSONPath, err)
		}

		if err := fakeFS.WriteFile(stagePath, []byte(`{"broken"`), 0o600); err != nil {
			t.Fatalf("write invalid staged tfa.json: %v", err)
		}
		if err := applyPBSTFAJSONFromStage(logger, stageRoot); err == nil || !strings.Contains(err.Error(), "parse staged tfa.json") {
			t.Fatalf("expected parse error, got %v", err)
		}

		stageTFA := `{"users":{"root@pam":{"totp":[1]},"alice@pbs":{"webauthn":[{"id":"x"}]}},"webauthn":{"rp":"backup"}}`
		if err := fakeFS.WriteFile(stagePath, []byte(stageTFA), 0o600); err != nil {
			t.Fatalf("write valid staged tfa.json: %v", err)
		}
		restoreFS = readFileFailFS{FS: fakeFS, failPath: pbsTFAJSONPath, err: syscall.EPERM}
		if err := applyPBSTFAJSONFromStage(logger, stageRoot); err == nil || !strings.Contains(err.Error(), "read current tfa.json") {
			t.Fatalf("expected current tfa.json read error, got %v", err)
		}
		restoreFS = fakeFS

		currentTFA := `{"users":{"root@pam":{"totp":[9]},"keep@pbs":{"totp":[7]}},"totp":{"digits":6}}`
		if err := fakeFS.WriteFile(pbsTFAJSONPath, []byte(currentTFA), 0o600); err != nil {
			t.Fatalf("seed current tfa.json: %v", err)
		}
		if err := applyPBSTFAJSONFromStage(logger, stageRoot); err != nil {
			t.Fatalf("apply valid tfa.json merge: %v", err)
		}
		got, err := fakeFS.ReadFile(pbsTFAJSONPath)
		if err != nil {
			t.Fatalf("read merged tfa.json: %v", err)
		}
		if strings.Contains(string(got), `"root@pam":{"totp":[1]}`) {
			t.Fatalf("staged root@pam TFA should be replaced by current one, got=%s", string(got))
		}
		if !strings.Contains(string(got), `"root@pam":{"totp":[9]}`) {
			t.Fatalf("current root@pam TFA should be preserved, got=%s", string(got))
		}
		if !strings.Contains(string(got), `"alice@pbs"`) {
			t.Fatalf("non-root staged user should be restored, got=%s", string(got))
		}
	})
}

func TestApplyPVEAccessControlFromStage_AdditionalBranches(t *testing.T) {
	t.Run("context handling and no staged files", func(t *testing.T) {
		origFS := restoreFS
		t.Cleanup(func() { restoreFS = origFS })
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if err := applyPVEAccessControlFromStage(canceled, newTestLogger(), "/stage"); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}

		var nilCtx context.Context // deliberately nil: verifies nil-context handling is safe
		if err := applyPVEAccessControlFromStage(nilCtx, newTestLogger(), "/stage"); err != nil {
			t.Fatalf("nil context with no staged files should be safe, got %v", err)
		}
	})

	t.Run("staged read error surfaces", func(t *testing.T) {
		origFS := restoreFS
		t.Cleanup(func() { restoreFS = origFS })
		base := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(base.Root) })
		restoreFS = readFileFailFS{FS: base, failPath: "/stage/etc/pve/user.cfg", err: syscall.EIO}

		if err := applyPVEAccessControlFromStage(context.Background(), newTestLogger(), "/stage"); err == nil || !strings.Contains(err.Error(), "read staged etc/pve/user.cfg") {
			t.Fatalf("expected staged read error, got %v", err)
		}
	})

	t.Run("root bootstrap and realm merge with pve user", func(t *testing.T) {
		origFS := restoreFS
		t.Cleanup(func() { restoreFS = origFS })
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		stageRoot := "/stage"
		stagedUser := strings.Join([]string{
			"user: alice@pve",
			"  enable 1",
			"",
		}, "\n")
		stagedDomains := strings.Join([]string{
			"pam: pam",
			"  comment backup-pam",
			"",
		}, "\n")
		stagedTFA := strings.Join([]string{
			"webauthn: alice-key",
			"  user alice@pve",
			"",
		}, "\n")

		if err := fakeFS.WriteFile(stageRoot+"/etc/pve/user.cfg", []byte(stagedUser), 0o640); err != nil {
			t.Fatalf("write staged user.cfg: %v", err)
		}
		if err := fakeFS.WriteFile(stageRoot+"/etc/pve/domains.cfg", []byte(stagedDomains), 0o640); err != nil {
			t.Fatalf("write staged domains.cfg: %v", err)
		}
		if err := fakeFS.WriteFile(stageRoot+"/etc/pve/priv/tfa.cfg", []byte(stagedTFA), 0o600); err != nil {
			t.Fatalf("write staged tfa.cfg: %v", err)
		}

		if err := applyPVEAccessControlFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
			t.Fatalf("applyPVEAccessControlFromStage: %v", err)
		}

		gotUser, err := fakeFS.ReadFile(pveUserCfgPath)
		if err != nil {
			t.Fatalf("read merged user.cfg: %v", err)
		}
		if !strings.Contains(string(gotUser), "user: root@pam") || !strings.Contains(string(gotUser), "enable 1") {
			t.Fatalf("expected root@pam bootstrap user to be present, got:\n%s", string(gotUser))
		}
		if !strings.Contains(string(gotUser), "acl: proxsave-root-admin") {
			t.Fatalf("expected root admin ACL to be injected, got:\n%s", string(gotUser))
		}

		gotDomains, err := fakeFS.ReadFile(pveDomainsCfgPath)
		if err != nil {
			t.Fatalf("read merged domains.cfg: %v", err)
		}
		if !strings.Contains(string(gotDomains), "pam: pam") || !strings.Contains(string(gotDomains), "pve: pve") {
			t.Fatalf("expected required pam/pve realms, got:\n%s", string(gotDomains))
		}
	})
}

func TestAccessControlUtilityFunctions_Branches(t *testing.T) {
	t.Run("stage/read helpers and renderer", func(t *testing.T) {
		origFS := restoreFS
		t.Cleanup(func() { restoreFS = origFS })

		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		if content, present, err := readStageFileOptional("/stage", "etc/pve/user.cfg"); err != nil || present || content != "" {
			t.Fatalf("missing staged file mismatch: content=%q present=%v err=%v", content, present, err)
		}

		stagePath := "/stage/etc/pve/user.cfg"
		restoreFS = readFileFailFS{FS: fakeFS, failPath: stagePath, err: syscall.EPERM}
		if _, _, err := readStageFileOptional("/stage", "etc/pve/user.cfg"); err == nil || !strings.Contains(err.Error(), "read staged etc/pve/user.cfg") {
			t.Fatalf("expected staged read error, got %v", err)
		}
		restoreFS = fakeFS

		if err := fakeFS.WriteFile(stagePath, []byte(" \n\t"), 0o640); err != nil {
			t.Fatalf("write empty staged user.cfg: %v", err)
		}
		if content, present, err := readStageFileOptional("/stage", "etc/pve/user.cfg"); err != nil || !present || content != "" {
			t.Fatalf("empty staged file mismatch: content=%q present=%v err=%v", content, present, err)
		}

		if err := fakeFS.WriteFile(stagePath, []byte(" user: alice@pve \n"), 0o640); err != nil {
			t.Fatalf("write non-empty staged user.cfg: %v", err)
		}
		if content, present, err := readStageFileOptional("/stage", "etc/pve/user.cfg"); err != nil || !present || content != "user: alice@pve" {
			t.Fatalf("trimmed staged file mismatch: content=%q present=%v err=%v", content, present, err)
		}

		if sections, err := readProxmoxConfigSectionsOptional("/missing.cfg"); err != nil || len(sections) != 0 {
			t.Fatalf("missing proxmox cfg mismatch: sections=%v err=%v", sections, err)
		}

		restoreFS = readFileFailFS{FS: fakeFS, failPath: "/etc/pve/user.cfg", err: syscall.EIO}
		if _, err := readProxmoxConfigSectionsOptional("/etc/pve/user.cfg"); err == nil {
			t.Fatalf("expected read error from readProxmoxConfigSectionsOptional")
		}
		restoreFS = fakeFS

		if err := fakeFS.WriteFile("/etc/pve/user.cfg", []byte(" \n\t"), 0o640); err != nil {
			t.Fatalf("write empty /etc/pve/user.cfg: %v", err)
		}
		if sections, err := readProxmoxConfigSectionsOptional("/etc/pve/user.cfg"); err != nil || len(sections) != 0 {
			t.Fatalf("empty proxmox cfg mismatch: sections=%v err=%v", sections, err)
		}

		if err := fakeFS.WriteFile("/etc/pve/user.cfg", []byte("user: alice@pve\n  enable 1\n"), 0o640); err != nil {
			t.Fatalf("write valid /etc/pve/user.cfg: %v", err)
		}
		if sections, err := readProxmoxConfigSectionsOptional("/etc/pve/user.cfg"); err != nil || len(sections) != 1 {
			t.Fatalf("valid proxmox cfg parse mismatch: sections=%v err=%v", sections, err)
		}

		rendered := renderProxmoxConfig([]proxmoxNotificationSection{
			{Type: " ", Name: "ignored"},
			{
				Type: "user",
				Name: "alice@pve",
				Entries: []proxmoxNotificationEntry{
					{Key: "enable", Value: "1"},
					{Key: "comment", Value: ""},
					{Key: " ", Value: "ignored"},
				},
			},
		})
		if !strings.Contains(rendered, "user: alice@pve") || !strings.Contains(rendered, "  enable 1") || !strings.Contains(rendered, "  comment") {
			t.Fatalf("renderProxmoxConfig output unexpected:\n%s", rendered)
		}
		if !strings.HasSuffix(rendered, "\n") {
			t.Fatalf("renderProxmoxConfig should end with newline")
		}
		if renderProxmoxConfig(nil) != "" {
			t.Fatalf("renderProxmoxConfig(nil) should be empty")
		}
	})

	t.Run("ids, realms and ACL predicates", func(t *testing.T) {
		if !isRootPBSUserID(" root@pbs ") {
			t.Fatalf("isRootPBSUserID should detect root regardless of realm/spacing")
		}
		if isRootPBSUserID("   ") {
			t.Fatalf("isRootPBSUserID should reject empty ids")
		}
		if !isRootPBSAuthID(" root@pam!tok ") {
			t.Fatalf("isRootPBSAuthID should detect root-bound auth ids")
		}
		if isRootPBSAuthID("   ") {
			t.Fatalf("isRootPBSAuthID should reject empty auth ids")
		}

		withRoot := []proxmoxNotificationSection{
			{Type: "role", Name: "Admin"},
			{Type: "user", Name: "root@pam"},
			{Type: "user", Name: "alice@pbs"},
		}
		if root := findPBSRootUserSection(withRoot); root == nil || root.Name != "root@pam" {
			t.Fatalf("findPBSRootUserSection should find root user, got %+v", root)
		}
		if root := findPBSRootUserSection([]proxmoxNotificationSection{{Type: "user", Name: "alice@pbs"}}); root != nil {
			t.Fatalf("findPBSRootUserSection should return nil when root is missing, got %+v", root)
		}

		if realm := userRealm("alice@pve"); realm != "pve" {
			t.Fatalf("userRealm=%q want pve", realm)
		}
		if realm := userRealm("alice"); realm != "" {
			t.Fatalf("userRealm without @ should be empty, got %q", realm)
		}

		sections := []proxmoxNotificationSection{
			{Type: "user", Name: "alice@pve"},
			{Type: "role", Name: "Admin"},
		}
		if !anyUserInRealm(sections, "pve") {
			t.Fatalf("expected anyUserInRealm to detect pve user")
		}
		if anyUserInRealm(sections, "pbs") {
			t.Fatalf("did not expect pbs realm match")
		}
		if anyUserInRealm(sections, "   ") {
			t.Fatalf("empty realm should never match")
		}

		backup := []proxmoxNotificationSection{{Type: "pam", Name: "pam", Entries: []proxmoxNotificationEntry{{Key: "comment", Value: "backup"}}}}
		current := []proxmoxNotificationSection{
			{Type: "pam", Name: "pam", Entries: []proxmoxNotificationEntry{{Key: "comment", Value: "current"}}},
			{Type: "pve", Name: "pve", Entries: []proxmoxNotificationEntry{{Key: "comment", Value: "current-pve"}}},
		}
		merged := mergeRequiredRealms(backup, current, []string{"pam", "pve"})
		if got := findSectionEntryValue(findSection(merged, "pam", "pam").Entries, "comment"); got != "current" {
			t.Fatalf("required pam realm should be overridden by current safety config, got %q", got)
		}
		if findSection(merged, "pve", "pve") == nil {
			t.Fatalf("required pve realm should be present")
		}

		if !listContains("root@pam,alice@pve", "alice@pve") {
			t.Fatalf("listContains should parse comma-separated lists")
		}
		if listContains("root@pam alice@pve", "bob@pve") {
			t.Fatalf("listContains returned true for missing entry")
		}
		if listContains("root@pam alice@pve", "   ") {
			t.Fatalf("listContains should reject empty needle")
		}
		if listContains("   ", "root@pam") {
			t.Fatalf("listContains should reject empty haystack")
		}

		if !hasRootAdminOnRoot([]proxmoxNotificationSection{
			{
				Type: "acl",
				Name: "rule /",
				Entries: []proxmoxNotificationEntry{
					{Key: "users", Value: "root@pam"},
					{Key: "roles", Value: "Administrator"},
				},
			},
		}) {
			t.Fatalf("hasRootAdminOnRoot should detect root Administrator ACL on /")
		}
	})

	t.Run("token/tfa/shadow identity helpers", func(t *testing.T) {
		if user := tokenSectionUserID(proxmoxNotificationSection{Type: "role", Name: "x"}); user != "" {
			t.Fatalf("tokenSectionUserID for non-token should be empty, got %q", user)
		}
		if user := tokenSectionUserID(proxmoxNotificationSection{Type: "token", Name: "broken-name"}); user != "" {
			t.Fatalf("tokenSectionUserID for invalid token names should be empty, got %q", user)
		}
		if user := tokenSectionUserID(proxmoxNotificationSection{Type: "token", Name: "alice@pve!tok1"}); user != "alice@pve" {
			t.Fatalf("tokenSectionUserID=%q want alice@pve", user)
		}

		if user := tfaSectionUserID(proxmoxNotificationSection{Name: "alice@pve"}); user != "alice@pve" {
			t.Fatalf("tfaSectionUserID by name=%q want alice@pve", user)
		}
		if user := tfaSectionUserID(proxmoxNotificationSection{
			Name: "entry-only",
			Entries: []proxmoxNotificationEntry{
				{Key: "user", Value: "bob@pve"},
			},
		}); user != "bob@pve" {
			t.Fatalf("tfaSectionUserID by entry=%q want bob@pve", user)
		}
		if user := tfaSectionUserID(proxmoxNotificationSection{Name: "no-user"}); user != "" {
			t.Fatalf("tfaSectionUserID expected empty, got %q", user)
		}

		if user := shadowSectionUserID(proxmoxNotificationSection{Type: "user", Name: "alice@pve"}); user != "alice@pve" {
			t.Fatalf("shadowSectionUserID by user type=%q want alice@pve", user)
		}
		if user := shadowSectionUserID(proxmoxNotificationSection{Type: "hash", Name: "bob@pve"}); user != "bob@pve" {
			t.Fatalf("shadowSectionUserID by name=%q want bob@pve", user)
		}
		if user := shadowSectionUserID(proxmoxNotificationSection{
			Type: "hash",
			Name: "fallback",
			Entries: []proxmoxNotificationEntry{
				{Key: "userid", Value: "carol@pve"},
			},
		}); user != "carol@pve" {
			t.Fatalf("shadowSectionUserID by userid entry=%q want carol@pve", user)
		}
		if user := shadowSectionUserID(proxmoxNotificationSection{Name: "nobody"}); user != "" {
			t.Fatalf("shadowSectionUserID expected empty, got %q", user)
		}

		if _, _, ok := splitPVETokenSectionName("invalid"); ok {
			t.Fatalf("splitPVETokenSectionName should reject invalid token name")
		}
		if _, _, ok := splitPVETokenSectionName("   "); ok {
			t.Fatalf("splitPVETokenSectionName should reject empty token name")
		}
		if _, _, ok := splitPVETokenSectionName("!tok"); ok {
			t.Fatalf("splitPVETokenSectionName should reject missing user part")
		}
		if _, _, ok := splitPVETokenSectionName("alice@pve!"); ok {
			t.Fatalf("splitPVETokenSectionName should reject missing token part")
		}
		userID, tokenID, ok := splitPVETokenSectionName("alice@pve!tok")
		if !ok || userID != "alice@pve" || tokenID != "tok" {
			t.Fatalf("splitPVETokenSectionName parse mismatch: user=%q token=%q ok=%v", userID, tokenID, ok)
		}
	})

	t.Run("webauthn extractors and misc helpers", func(t *testing.T) {
		pveUsers := extractWebAuthnUsersFromPVETFA([]proxmoxNotificationSection{
			{Type: "webauthn", Name: "k1", Entries: []proxmoxNotificationEntry{{Key: "user", Value: "alice@pve"}}},
			{Type: "u2f", Name: "k2", Entries: []proxmoxNotificationEntry{{Key: "user", Value: "alice@pve"}}},
			{Type: "webauthn", Name: "k3", Entries: []proxmoxNotificationEntry{{Key: "user", Value: "root@pam"}}},
			{Type: "totp", Name: "k4", Entries: []proxmoxNotificationEntry{{Key: "user", Value: "bob@pve"}}},
			{Type: "u2f", Name: "k5", Entries: []proxmoxNotificationEntry{{Key: "user", Value: "carol@pve"}}},
		})
		if len(pveUsers) != 2 || pveUsers[0] != "alice@pve" || pveUsers[1] != "carol@pve" {
			t.Fatalf("extractWebAuthnUsersFromPVETFA unexpected users: %v", pveUsers)
		}

		pbsUsers := map[string]json.RawMessage{
			"root@pam":  json.RawMessage(`{"webauthn":[{}]}`),
			"alice@pbs": json.RawMessage(`{"webauthn":[{"id":"a"}]}`),
			"bob@pbs":   json.RawMessage(`{"u2f":[{"id":"b"}]}`),
			"bad@pbs":   json.RawMessage(`{"broken"`),
			"none@pbs":  json.RawMessage(`{"totp":[1]}`),
		}
		extracted := extractWebAuthnUsersFromPBSTFAUsers(pbsUsers)
		if len(extracted) != 2 || extracted[0] != "alice@pbs" || extracted[1] != "bob@pbs" {
			t.Fatalf("extractWebAuthnUsersFromPBSTFAUsers unexpected users: %v", extracted)
		}

		if jsonRawNonNull(json.RawMessage(`null`)) {
			t.Fatalf("jsonRawNonNull should be false for null")
		}
		if jsonRawNonNull(json.RawMessage(`{}`)) {
			t.Fatalf("jsonRawNonNull should be false for empty object")
		}
		if !jsonRawNonNull(json.RawMessage(`{"x":1}`)) {
			t.Fatalf("jsonRawNonNull should be true for non-empty object")
		}

		if s := summarizeUserIDs([]string{"a", "b"}, 0); s != "a, b" {
			t.Fatalf("summarizeUserIDs with max<=0 should keep full list when small, got %q", s)
		}
		if s := summarizeUserIDs([]string{"a", "b", "c"}, 2); s != "a, b (+1 more)" {
			t.Fatalf("summarizeUserIDs truncation mismatch, got %q", s)
		}
		if s := summarizeUserIDs(nil, 3); s != "" {
			t.Fatalf("summarizeUserIDs(nil) should be empty, got %q", s)
		}

		if got := mustMarshalRaw(func() {}); string(got) != "{}" {
			t.Fatalf("mustMarshalRaw fallback mismatch, got %q", string(got))
		}
		if users := parseTFAUsersMap(nil); len(users) != 0 {
			t.Fatalf("parseTFAUsersMap(nil) should be empty, got %v", users)
		}
		if users := parseTFAUsersMap(map[string]json.RawMessage{"users": json.RawMessage(`{"alice@pbs":{"totp":[1]}}`)}); len(users) != 1 {
			t.Fatalf("parseTFAUsersMap valid payload mismatch, got %v", users)
		}
		if users := parseTFAUsersMap(map[string]json.RawMessage{"users": json.RawMessage(`{"broken"`)}); len(users) != 0 {
			t.Fatalf("parseTFAUsersMap invalid payload should be empty, got %v", users)
		}
	})
}

func TestMaybeApplyAccessControlFromStage_RealFSPaths(t *testing.T) {
	origFS := restoreFS
	origReadFile := mountGuardReadFile
	origGeteuid := accessControlApplyGeteuid
	t.Cleanup(func() {
		restoreFS = origFS
		mountGuardReadFile = origReadFile
		accessControlApplyGeteuid = origGeteuid
	})
	restoreFS = osFS{}
	accessControlApplyGeteuid = func() int { return 0 }

	logger := newTestLogger()
	ctx := context.Background()

	t.Run("pbs read failure bubbles", func(t *testing.T) {
		stageRoot := t.TempDir()
		userPath := filepath.Join(stageRoot, "etc/proxmox-backup/user.cfg")
		if err := os.MkdirAll(userPath, 0o755); err != nil {
			t.Fatalf("create directory at staged user.cfg path: %v", err)
		}

		plan := &RestorePlan{
			SystemType:       SystemTypePBS,
			NormalCategories: []Category{{ID: "pbs_access_control", Type: CategoryTypePBS}},
		}
		err := maybeApplyAccessControlFromStage(ctx, logger, plan, stageRoot, false)
		if err == nil || !strings.Contains(err.Error(), "read staged etc/proxmox-backup/user.cfg") {
			t.Fatalf("expected staged user.cfg read error, got %v", err)
		}
	})

	t.Run("dual cluster-safe path returns after pbs apply", func(t *testing.T) {
		stageRoot := t.TempDir()
		plan := &RestorePlan{
			SystemType:          SystemTypeDual,
			ClusterBackup:       true,
			NeedsClusterRestore: false,
			NormalCategories: []Category{
				{ID: "pbs_access_control", Type: CategoryTypePBS},
				{ID: "pve_access_control", Type: CategoryTypePVE},
			},
		}
		if err := maybeApplyAccessControlFromStage(ctx, logger, plan, stageRoot, false); err != nil {
			t.Fatalf("expected dual cluster-safe branch to return nil, got %v", err)
		}
	})

	t.Run("pve cluster recovery skip", func(t *testing.T) {
		stageRoot := t.TempDir()
		plan := &RestorePlan{
			SystemType:          SystemTypePVE,
			ClusterBackup:       true,
			NeedsClusterRestore: true,
			NormalCategories:    []Category{{ID: "pve_access_control", Type: CategoryTypePVE}},
		}
		if err := maybeApplyAccessControlFromStage(ctx, logger, plan, stageRoot, false); err != nil {
			t.Fatalf("expected cluster recovery skip path, got %v", err)
		}
	})

	t.Run("pve apply failure bubbles when pmxcfs missing", func(t *testing.T) {
		stageRoot := t.TempDir()
		plan := &RestorePlan{
			SystemType:       SystemTypePVE,
			NormalCategories: []Category{{ID: "pve_access_control", Type: CategoryTypePVE}},
		}

		mountGuardReadFile = func(path string) ([]byte, error) {
			if path == "/proc/self/mountinfo" || path == "/proc/mounts" {
				return []byte(""), nil
			}
			return nil, os.ErrNotExist
		}
		err := maybeApplyAccessControlFromStage(ctx, logger, plan, stageRoot, false)
		if err == nil || !strings.Contains(err.Error(), "/etc/pve is not mounted") {
			t.Fatalf("expected /etc/pve mount protection error, got %v", err)
		}
	})
}

func TestApplyPBSAccessControlFromStage_WriteErrorBranches(t *testing.T) {
	logger := newTestLogger()

	t.Run("token.cfg write failure", func(t *testing.T) {
		origFS := restoreFS
		t.Cleanup(func() { restoreFS = origFS })

		base := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(base.Root) })

		stageRoot := "/stage"
		tokenCfg := strings.Join([]string{
			"token: alice@pbs!tok",
			"  comment demo",
			"",
		}, "\n")
		if err := base.WriteFile(stageRoot+"/etc/proxmox-backup/token.cfg", []byte(tokenCfg), 0o640); err != nil {
			t.Fatalf("write staged token.cfg: %v", err)
		}

		restoreFS = &ErrorInjectingFS{
			base:        base,
			openFileErr: errors.New("open failed"),
		}
		err := applyPBSAccessControlFromStage(context.Background(), logger, stageRoot)
		if err == nil || !strings.Contains(err.Error(), "write /etc/proxmox-backup/token.cfg") {
			t.Fatalf("expected token.cfg write error, got %v", err)
		}
	})

	t.Run("acl.cfg write failure via ACL apply", func(t *testing.T) {
		origFS := restoreFS
		t.Cleanup(func() { restoreFS = origFS })

		base := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(base.Root) })

		stageRoot := "/stage"
		aclCfg := strings.Join([]string{
			"acl: staged-acl",
			"  path /",
			"  users alice@pbs",
			"  roles Admin",
			"",
		}, "\n")
		if err := base.WriteFile(stageRoot+"/etc/proxmox-backup/acl.cfg", []byte(aclCfg), 0o640); err != nil {
			t.Fatalf("write staged acl.cfg: %v", err)
		}

		restoreFS = &ErrorInjectingFS{
			base:        base,
			openFileErr: errors.New("open failed"),
		}
		err := applyPBSAccessControlFromStage(context.Background(), logger, stageRoot)
		if err == nil || !strings.Contains(err.Error(), "write /etc/proxmox-backup/acl.cfg") {
			t.Fatalf("expected acl.cfg write error, got %v", err)
		}
	})
}

func TestApplyPVEAccessControlFromStage_MountProbeBranches(t *testing.T) {
	origFS := restoreFS
	origReadFile := mountGuardReadFile
	t.Cleanup(func() {
		restoreFS = origFS
		mountGuardReadFile = origReadFile
	})
	restoreFS = osFS{}

	logger := newTestLogger()

	t.Run("mount probe error refuses (fail-safe)", func(t *testing.T) {
		mountGuardReadFile = func(path string) ([]byte, error) {
			if path == "/proc/self/mountinfo" || path == "/proc/mounts" {
				return nil, errors.New("probe failed")
			}
			return nil, os.ErrNotExist
		}
		// cand-#5: a mount probe error must fail-safe (refuse), like a confirmed
		// unmounted /etc/pve, so access-control/secret files are never shadow-written
		// onto the root filesystem. Matches restore_sdn/firewall/ha.
		err := applyPVEAccessControlFromStage(context.Background(), logger, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "/etc/pve is not mounted") {
			t.Fatalf("mount probe error must fail-safe refuse, got %v", err)
		}
	})

	t.Run("pmxcfs not mounted returns explicit refusal", func(t *testing.T) {
		mountGuardReadFile = func(path string) ([]byte, error) {
			if path == "/proc/self/mountinfo" || path == "/proc/mounts" {
				return []byte(""), nil
			}
			return nil, os.ErrNotExist
		}
		err := applyPVEAccessControlFromStage(context.Background(), logger, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "/etc/pve is not mounted") {
			t.Fatalf("expected mount refusal error, got %v", err)
		}
	})
}

func TestApplyPVEAccessControlFromStage_WriteErrorBranches(t *testing.T) {
	cases := []struct {
		name     string
		stageRel string
		content  string
		wantPath string
	}{
		{
			name:     "domains write failure",
			stageRel: "etc/pve/domains.cfg",
			content:  "pam: pam\n  comment builtin\n",
			wantPath: pveDomainsCfgPath,
		},
		{
			name:     "user write failure",
			stageRel: "etc/pve/user.cfg",
			content:  "user: alice@pve\n  enable 1\n",
			wantPath: pveUserCfgPath,
		},
		{
			name:     "shadow write failure",
			stageRel: "etc/pve/priv/shadow.cfg",
			content:  "user: alice@pve\n  hash abc\n",
			wantPath: pveShadowCfgPath,
		},
		{
			name:     "token write failure",
			stageRel: "etc/pve/priv/token.cfg",
			content:  "token: alice@pve!tok\n  comment x\n",
			wantPath: pveTokenCfgPath,
		},
		{
			name:     "tfa write failure",
			stageRel: "etc/pve/priv/tfa.cfg",
			content:  "totp: alice@pve\n  user alice@pve\n",
			wantPath: pveTFACfgPath,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origFS := restoreFS
			t.Cleanup(func() { restoreFS = origFS })

			base := NewFakeFS()
			t.Cleanup(func() { _ = os.RemoveAll(base.Root) })

			stageRoot := "/stage"
			stagePath := filepath.Join(stageRoot, tc.stageRel)
			if err := base.WriteFile(stagePath, []byte(tc.content), 0o640); err != nil {
				t.Fatalf("write staged file %s: %v", tc.stageRel, err)
			}

			restoreFS = &ErrorInjectingFS{
				base:        base,
				openFileErr: errors.New("open failed"),
			}
			err := applyPVEAccessControlFromStage(context.Background(), newTestLogger(), stageRoot)
			if err == nil || !strings.Contains(err.Error(), "write "+tc.wantPath) {
				t.Fatalf("expected write error for %s, got %v", tc.wantPath, err)
			}
		})
	}
}
