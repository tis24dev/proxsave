package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
)

type fakeAccountsCmd struct{ err error }

func (f fakeAccountsCmd) Run(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return nil, f.err
}

func TestMergePasswdPreservesRootSystemAndProtectsCollisions(t *testing.T) {
	// Host: superuser renamed "superadmin" (uid 0), a system service "svc" (uid 200),
	// and a regular user "alice" (uid 1000).
	current := "superadmin:x:0:0:root:/root:/bin/bash\n" +
		"svc:x:200:200:service:/var/svc:/usr/sbin/nologin\n" +
		"alice:x:1000:1000::/home/alice:/bin/bash\n"
	// Backup tries to clobber the renamed root and the system account with regular
	// users of the same name, imports a real user, overwrites alice, and includes a
	// NIS line and a truncated line.
	backup := "superadmin:x:1500:1500:EVIL:/home/x:/bin/sh\n" +
		"svc:x:1600:1600:EVIL:/home/y:/bin/sh\n" +
		"bob:x:1001:1001::/home/bob:/bin/bash\n" +
		"alice:x:1000:1000::/home/alice:/bin/zsh\n" +
		"+::::::\n" +
		"zoe:x:1500\n"

	hostSystem := lowIDNames(current, 2)
	imported, merged := mergePasswd(current, backup, hostSystem, map[uint64]bool{})

	if !strings.Contains(merged, "superadmin:x:0:0:root:") || strings.Contains(merged, "EVIL") {
		t.Errorf("renamed root (uid 0) must be preserved and never clobbered:\n%s", merged)
	}
	if !strings.Contains(merged, "svc:x:200:200:service:") {
		t.Errorf("host system account 'svc' must be preserved:\n%s", merged)
	}
	if imported["superadmin"] || imported["svc"] {
		t.Errorf("name-colliding system accounts must not be imported: %v", imported)
	}
	if !strings.Contains(merged, "bob:x:1001:1001:") || !imported["bob"] {
		t.Errorf("regular backup user 'bob' must be imported:\n%s", merged)
	}
	if !strings.Contains(merged, "alice:x:1000:1000::/home/alice:/bin/zsh") {
		t.Errorf("existing regular user 'alice' must be overwritten from backup:\n%s", merged)
	}
	if strings.Contains(merged, "zoe") {
		t.Errorf("truncated passwd line (<7 fields) must be rejected:\n%s", merged)
	}
	if strings.Contains(merged, "+::::::") || imported["+"] {
		t.Errorf("NIS compat line must be ignored:\n%s", merged)
	}
}

func TestMergePasswdRejectsOverflowEscalationAndMalformedNames(t *testing.T) {
	current := "root:x:0:0:root:/root:/bin/bash\ndaemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin\n"
	hostSystem := lowIDNames(current, 2)
	hostSystemGIDs := map[uint64]bool{0: true, 27: true} // root, sudo
	backup := strings.Join([]string{
		"over:x:4294967296:1500::/home/over:/bin/sh", // uid overflow -> wraps to 0 on a 32-bit kernel
		"sentinel:x:4294967295:1500::/h:/bin/sh",     // uid == (uid_t)-1 sentinel
		"gid0:x:1001:0::/home/gid0:/bin/sh",          // regular user with PRIMARY group root
		"psudo:x:2001:27::/h:/bin/sh",                // PRIMARY gid = host sudo (escalation)
		"gidover:x:1002:4294967296::/home/g:/bin/sh", // primary gid overflow
		"wsp:x: 1700:1700::/h:/bin/sh",               // whitespace uid field (non-canonical)
		":x:1003:1003::/home/empty:/bin/sh",          // empty name
		"daemon :x:1004:1004:EVIL:/root:/bin/sh",     // whitespace name (collision-bypass attempt)
		"ev/il:x:1900:1900::/h:/bin/sh",              // '/' in name
		"good:x:1005:1005::/home/good:/bin/bash",     // legitimate regular user
	}, "\n") + "\n"

	imported, merged := mergePasswd(current, backup, hostSystem, hostSystemGIDs)

	for _, bad := range []string{"over", "sentinel", "gid0", "psudo", "gidover", "EVIL", "ev/il", "4294967295"} {
		if strings.Contains(merged, bad) {
			t.Errorf("malformed/escalation entry %q must be rejected:\n%s", bad, merged)
		}
	}
	if imported["over"] || imported["sentinel"] || imported["gid0"] || imported["psudo"] ||
		imported["gidover"] || imported["wsp"] || imported[""] || imported["daemon "] || imported["ev/il"] {
		t.Errorf("rejected entries must not be imported: %v", imported)
	}
	if !imported["good"] || !strings.Contains(merged, "good:x:1005:1005:") {
		t.Errorf("legitimate user 'good' must be imported:\n%s", merged)
	}
	if !strings.Contains(merged, "daemon:x:1:1:daemon:") {
		t.Errorf("host 'daemon' must be preserved unchanged:\n%s", merged)
	}
}

func TestMergeGroupRejectsOverflowAndRootGroupMemberMerge(t *testing.T) {
	current := "root:x:0:\nsudo:x:27:\n"
	hostGroupGID := groupGIDsByName(current)
	hostGIDs := gidValueSet(hostGroupGID)
	backup := "root:x:0:bob\nover:x:4294967296:bob\nsudo:x:27:bob\nproj:x:1500:bob\n"

	importedGroups, merged := mergeGroup(current, backup, hostGroupGID, hostGIDs, map[string]bool{"bob": true})

	if strings.Contains(merged, "root:x:0:bob") {
		t.Errorf("imported user must NEVER be merged into the root group:\n%s", merged)
	}
	if strings.Contains(merged, "over") || importedGroups["over"] {
		t.Errorf("gid-overflow group must be rejected:\n%s", merged)
	}
	if !strings.Contains(merged, "sudo:x:27:bob") {
		t.Errorf("bob should be merged into the (non-root) sudo group:\n%s", merged)
	}
	if !strings.Contains(merged, "proj:x:1500:bob") || !importedGroups["proj"] {
		t.Errorf("regular group 'proj' should be imported:\n%s", merged)
	}
}

func TestMergeShadowNeverLeavesImportedUserWithoutEntry(t *testing.T) {
	current := "root:CURHASH:1::::::\nalice:ALICEHASH:1::::::\n"
	// bob has a backup shadow line; carol is imported (passwd) but has NO backup shadow.
	backup := "root:EVIL:1::::::\nbob:BOBHASH:1::::::\n"
	merged := mergeShadow(current, backup, map[string]bool{"bob": true, "carol": true})

	if !strings.Contains(merged, "root:CURHASH") || strings.Contains(merged, "EVIL") {
		t.Errorf("root shadow must be preserved from host:\n%s", merged)
	}
	if !strings.Contains(merged, "bob:BOBHASH") {
		t.Errorf("bob shadow must come from backup:\n%s", merged)
	}
	// carol must NOT be missing from shadow (no passwd<->shadow desync): locked placeholder.
	if !strings.Contains(merged, "carol:*:::::::") {
		t.Errorf("imported user 'carol' without backup shadow must get a locked placeholder, not be absent:\n%s", merged)
	}
}

func TestMergeGroupMergesSystemGroupMembersAndImportsRegular(t *testing.T) {
	current := "root:x:0:\nsudo:x:27:\nalice:x:1000:\n"
	// Backup adds bob (imported) and alice (not imported here) to sudo, imports a
	// regular group, and references a system group the host lacks (docker).
	backup := "sudo:x:27:bob,alice\nbob:x:1001:\ndocker:x:998:bob\n"

	hostGroupGID := groupGIDsByName(current)
	hostGIDs := gidValueSet(hostGroupGID)
	importedGroups, merged := mergeGroup(current, backup, hostGroupGID, hostGIDs, map[string]bool{"bob": true})

	if !strings.Contains(merged, "sudo:x:27:bob") {
		t.Errorf("imported user 'bob' must be merged into the host 'sudo' group, gid preserved:\n%s", merged)
	}
	if strings.Contains(merged, "alice") && strings.Contains(merged, "sudo:x:27:bob,alice") {
		t.Errorf("non-imported user 'alice' must not be added to sudo:\n%s", merged)
	}
	if !strings.Contains(merged, "bob:x:1001:") || !importedGroups["bob"] {
		t.Errorf("regular backup group 'bob' must be imported:\n%s", merged)
	}
	if strings.Contains(merged, "docker") {
		t.Errorf("a system group absent on the host must not be imported (gid clash risk):\n%s", merged)
	}
}

func TestMergeGroupRejectsGidSpoofedSystemGroupInjection(t *testing.T) {
	current := "root:x:0:\nsudo:x:27:\n"
	hostGroupGID := groupGIDsByName(current)
	hostGIDs := gidValueSet(hostGroupGID)
	// Backup references the host 'sudo' group by NAME but with a spoofed gid (1234,
	// not the host's 27), trying to inject an imported user into the real sudo group.
	backup := "sudo:x:1234:mallory\n"

	_, merged := mergeGroup(current, backup, hostGroupGID, hostGIDs, map[string]bool{"mallory": true})

	if strings.Contains(merged, "mallory") {
		t.Errorf("gid-spoofed sudo line must NOT inject a member into the host sudo group:\n%s", merged)
	}
	if !strings.Contains(merged, "sudo:x:27:") {
		t.Errorf("host sudo group must be preserved unchanged:\n%s", merged)
	}

	// With the host's REAL gid, a legitimate imported member IS merged.
	_, merged2 := mergeGroup(current, "sudo:x:27:realbob\n", hostGroupGID, hostGIDs, map[string]bool{"realbob": true})
	if !strings.Contains(merged2, "sudo:x:27:realbob") {
		t.Errorf("gid-matching sudo line should merge the imported member:\n%s", merged2)
	}
}

func TestMergePasswdRejectsPrimaryGidIntoHostSystemGroup(t *testing.T) {
	current := "root:x:0:0:root:/root:/bin/bash\n"
	hostSystem := lowIDNames(current, 2)
	// Set of ALL host group gids: system (root/sudo/shadow) AND a privileged regular
	// group (docker at gid 1001).
	hostGIDs := map[uint64]bool{0: true, 27: true, 42: true, 1001: true}
	backup := "evil:x:1001:27::/home/evil:/bin/bash\n" + // primary gid = sudo
		"reader:x:1002:42::/home/reader:/bin/bash\n" + // primary gid = shadow
		"dock:x:1004:1001::/home/dock:/bin/bash\n" + // primary gid = host docker (>=1000)
		"ok:x:1003:1003::/home/ok:/bin/bash\n" // private primary gid

	imported, merged := mergePasswd(current, backup, hostSystem, hostGIDs)

	if imported["evil"] || strings.Contains(merged, "evil") {
		t.Errorf("user with primary gid=sudo must be rejected:\n%s", merged)
	}
	if imported["reader"] || strings.Contains(merged, "reader") {
		t.Errorf("user with primary gid=shadow must be rejected:\n%s", merged)
	}
	if imported["dock"] || strings.Contains(merged, "dock:x:") {
		t.Errorf("user with primary gid = a host privileged group (gid>=1000) must be rejected:\n%s", merged)
	}
	if !imported["ok"] || !strings.Contains(merged, "ok:x:1003:1003:") {
		t.Errorf("user with a private primary gid must be imported:\n%s", merged)
	}
}

func TestMergeGroupNeverOverwritesExistingHostGroup(t *testing.T) {
	// Host has a privileged regular group 'docker' at gid 1001 with a real member.
	current := "root:x:0:\ndocker:x:1001:realops\n"
	hostGroupGID := groupGIDsByName(current)
	hostGIDs := gidValueSet(hostGroupGID)

	// Backup tries to overwrite docker: change gid to 5000 and replace members.
	_, merged := mergeGroup(current, "docker:x:5000:attacker\n", hostGroupGID, hostGIDs, map[string]bool{"attacker": true})
	if !strings.Contains(merged, "docker:x:1001:realops") {
		t.Errorf("existing host group 'docker' (gid+members) must be preserved, not overwritten:\n%s", merged)
	}
	if strings.Contains(merged, "5000") || strings.Contains(merged, "attacker") {
		t.Errorf("backup must not change host group gid or inject members via a gid-mismatched line:\n%s", merged)
	}

	// A brand-new backup group whose gid collides with the host docker gid is skipped.
	importedGroups, merged2 := mergeGroup(current, "team:x:1001:bob\n", hostGroupGID, hostGIDs, map[string]bool{"bob": true})
	if strings.Contains(merged2, "team") || importedGroups["team"] {
		t.Errorf("a new backup group colliding with an existing host gid must be skipped:\n%s", merged2)
	}
}

func TestApplyAccountsFromStageEndToEnd(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	restoreCmd = fakeAccountsCmd{err: nil}

	stage := "/stage"
	w := func(p, c string, m os.FileMode) {
		t.Helper()
		if err := fakeFS.WriteFile(p, []byte(c), m); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	w(etcPasswdPath, "root:x:0:0:root:/root:/bin/bash\nalice:x:1000:1000::/home/alice:/bin/bash\n", 0o644)
	w(etcShadowPath, "root:CURHASH:1::::::\nalice:ALICEHASH:1::::::\n", 0o640)
	w(etcGroupPath, "root:x:0:\nsudo:x:27:\n", 0o644)
	w(etcGshadowPath, "root:*::\n", 0o640)
	w(etcSudoersPath, "root ALL=(ALL) ALL\n", 0o440)

	w(stage+"/etc/passwd", "root:x:0:0:EVIL:/root:/bin/sh\nbob:x:1001:1001::/home/bob:/bin/bash\n", 0o644)
	w(stage+"/etc/shadow", "root:EVIL:1::::::\nbob:BOBHASH:1::::::\n", 0o640)
	w(stage+"/etc/group", "sudo:x:27:bob\nbob:x:1001:\n", 0o644)
	w(stage+"/etc/gshadow", "bob:!::\n", 0o640)
	w(stage+"/etc/sudoers", "root ALL=(ALL) ALL\nbob ALL=(ALL) NOPASSWD: ALL\n", 0o440)

	if err := applyAccountsFromStage(context.Background(), newTestLogger(), stage); err != nil {
		t.Fatalf("applyAccountsFromStage: %v", err)
	}

	passwd := readFake(t, fakeFS, etcPasswdPath)
	shadow := readFake(t, fakeFS, etcShadowPath)
	group := readFake(t, fakeFS, etcGroupPath)
	sudoers := readFake(t, fakeFS, etcSudoersPath)

	if !strings.Contains(passwd, "root:x:0:0:root:") || strings.Contains(passwd, "EVIL") {
		t.Errorf("root preserved in passwd:\n%s", passwd)
	}
	if !strings.Contains(passwd, "bob:x:1001:1001:") {
		t.Errorf("bob merged into passwd:\n%s", passwd)
	}
	if !strings.Contains(shadow, "root:CURHASH") || strings.Contains(shadow, "EVIL") {
		t.Errorf("root shadow preserved:\n%s", shadow)
	}
	if !strings.Contains(shadow, "bob:BOBHASH") {
		t.Errorf("bob shadow merged:\n%s", shadow)
	}
	// passwd<->shadow consistency: every passwd name must have a shadow line.
	assertPasswdShadowConsistent(t, passwd, shadow)
	if !strings.Contains(group, "sudo:x:27:bob") {
		t.Errorf("bob added to sudo group:\n%s", group)
	}
	if !strings.Contains(sudoers, "bob ALL=(ALL) NOPASSWD") {
		t.Errorf("validated sudoers applied exactly:\n%s", sudoers)
	}
}

func TestApplyAccountsDesyncPreventionMissingStagedShadow(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	restoreCmd = fakeAccountsCmd{}

	_ = fakeFS.WriteFile(etcPasswdPath, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0o644)
	_ = fakeFS.WriteFile(etcShadowPath, []byte("root:CURHASH:1::::::\n"), 0o640)
	_ = fakeFS.WriteFile(etcGroupPath, []byte("root:x:0:\n"), 0o644)
	_ = fakeFS.WriteFile(etcGshadowPath, []byte("root:*::\n"), 0o640)
	// Stage has a new user in passwd but NO staged shadow at all.
	_ = fakeFS.WriteFile("/stage/etc/passwd", []byte("bob:x:1001:1001::/home/bob:/bin/bash\n"), 0o644)

	if err := applyAccountsFromStage(context.Background(), newTestLogger(), "/stage"); err != nil {
		t.Fatalf("applyAccountsFromStage: %v", err)
	}
	passwd := readFake(t, fakeFS, etcPasswdPath)
	shadow := readFake(t, fakeFS, etcShadowPath)
	if !strings.Contains(passwd, "bob:x:1001:") {
		t.Fatalf("bob should be imported:\n%s", passwd)
	}
	if !strings.Contains(shadow, "bob:*:::::::") {
		t.Errorf("bob must have a locked shadow placeholder (no desync), got shadow:\n%s", shadow)
	}
	assertPasswdShadowConsistent(t, passwd, shadow)
}

func TestApplyAccountsSkipsWhenCurrentPasswdEmpty(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	_ = fakeFS.WriteFile(etcPasswdPath, []byte("\n"), 0o644) // empty/unreadable host baseline
	_ = fakeFS.WriteFile("/stage/etc/passwd", []byte("bob:x:1001:1001::/home/bob:/bin/bash\n"), 0o644)

	if err := applyAccountsFromStage(context.Background(), newTestLogger(), "/stage"); err != nil {
		t.Fatalf("applyAccountsFromStage: %v", err)
	}
	if got := readFake(t, fakeFS, etcPasswdPath); strings.Contains(got, "bob") {
		t.Errorf("must not write accounts when current /etc/passwd is empty (anti-lockout), got:\n%s", got)
	}
}

func TestApplyAccountsSkipsWhenCurrentGroupEmpty(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	_ = fakeFS.WriteFile(etcPasswdPath, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0o644)
	_ = fakeFS.WriteFile(etcGroupPath, []byte("\n"), 0o644) // empty/unreadable host group baseline
	// Non-empty shadow so the empty-group guard is the ONLY thing preventing a rewrite
	// (otherwise the empty-shadow guard would mask this case and weaken the anchor).
	_ = fakeFS.WriteFile(etcShadowPath, []byte("root:CURHASH:1::::::\n"), 0o640)
	_ = fakeFS.WriteFile("/stage/etc/passwd", []byte("bob:x:1001:1001::/home/bob:/bin/bash\n"), 0o644)
	_ = fakeFS.WriteFile("/stage/etc/group", []byte("team:x:1001:bob\n"), 0o644)

	if err := applyAccountsFromStage(context.Background(), newTestLogger(), "/stage"); err != nil {
		t.Fatalf("applyAccountsFromStage: %v", err)
	}
	if got := readFake(t, fakeFS, etcPasswdPath); strings.Contains(got, "bob") {
		t.Errorf("must not rewrite accounts when current /etc/group is empty (anti-lockout), passwd:\n%s", got)
	}
}

func TestApplyAccountsSkipsWhenCurrentShadowEmpty(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	_ = fakeFS.WriteFile(etcPasswdPath, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0o644)
	_ = fakeFS.WriteFile(etcGroupPath, []byte("root:x:0:\n"), 0o644)
	_ = fakeFS.WriteFile(etcShadowPath, []byte("\n"), 0o640) // empty/unreadable host shadow baseline
	_ = fakeFS.WriteFile("/stage/etc/passwd", []byte("bob:x:1001:1001::/home/bob:/bin/bash\n"), 0o644)

	if err := applyAccountsFromStage(context.Background(), newTestLogger(), "/stage"); err != nil {
		t.Fatalf("applyAccountsFromStage: %v", err)
	}
	if got := readFake(t, fakeFS, etcShadowPath); strings.Contains(got, "bob") {
		t.Errorf("must not rewrite accounts when current /etc/shadow is empty (anti-lockout), shadow:\n%s", got)
	}
}

// TestMergeGroupNewGroupDropsNonImportedMembers checks that a brand-new backup
// group does not silently enroll an existing host account: its member list is
// restricted to users actually being imported (mirrors the existing-host-group
// member filtering).
func TestMergeGroupNewGroupDropsNonImportedMembers(t *testing.T) {
	current := "root:x:0:\nalice:x:1000:\n" // alice is a host user, NOT being imported
	hostGroupGID := groupGIDsByName(current)
	hostGIDs := gidValueSet(hostGroupGID)

	importedGroups, merged := mergeGroup(current, "team:x:3000:bob,alice\n", hostGroupGID, hostGIDs, map[string]bool{"bob": true})
	if !importedGroups["team"] {
		t.Fatalf("brand-new regular group 'team' should be imported:\n%s", merged)
	}
	var teamMembers string
	for _, line := range strings.Split(merged, "\n") {
		if strings.HasPrefix(line, "team:") {
			if f := strings.Split(line, ":"); len(f) >= 4 {
				teamMembers = f[3]
			}
		}
	}
	if teamMembers != "bob" {
		t.Errorf("new backup group must keep only the imported member 'bob', got members %q (host user 'alice' must not be enrolled):\n%s", teamMembers, merged)
	}
}

func TestApplySudoersSkipsOnVisudoFailure(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	restoreCmd = fakeAccountsCmd{err: os.ErrInvalid}

	const current = "root ALL=(ALL) ALL\n"
	_ = fakeFS.WriteFile(etcSudoersPath, []byte(current), 0o440)
	_ = fakeFS.WriteFile("/stage/etc/sudoers", []byte("garbage !!! invalid\n"), 0o440)

	if err := applySudoersFromStage(context.Background(), newTestLogger(), "/stage"); err != nil {
		t.Fatalf("applySudoersFromStage should not error on invalid sudoers: %v", err)
	}
	if got := readFake(t, fakeFS, etcSudoersPath); got != current {
		t.Errorf("current /etc/sudoers must be kept when staged fails visudo:\n%s", got)
	}
}

func TestMaybeApplyAccountsFromStageGates(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	restoreCmd = fakeAccountsCmd{}

	const baseline = "root:x:0:0:root:/root:/bin/bash\n"
	reset := func() {
		_ = fakeFS.WriteFile(etcPasswdPath, []byte(baseline), 0o644)
		_ = fakeFS.WriteFile("/stage/etc/passwd", []byte("bob:x:1001:1001::/home/bob:/bin/bash\n"), 0o644)
	}
	withAccounts := &RestorePlan{StagedCategories: []Category{{ID: "accounts"}}}
	notWritten := func(label string) {
		t.Helper()
		if got := readFake(t, fakeFS, etcPasswdPath); strings.Contains(got, "bob") {
			t.Errorf("%s: must not apply accounts, got passwd:\n%s", label, got)
		}
	}

	reset()
	if err := maybeApplyAccountsFromStage(context.Background(), newTestLogger(), nil, "/stage", false); err != nil {
		t.Fatalf("nil plan: %v", err)
	}
	notWritten("nil plan")

	reset()
	if err := maybeApplyAccountsFromStage(context.Background(), newTestLogger(), &RestorePlan{}, "/stage", false); err != nil {
		t.Fatalf("no accounts category: %v", err)
	}
	notWritten("no accounts category")

	reset()
	if err := maybeApplyAccountsFromStage(context.Background(), newTestLogger(), withAccounts, "/stage", true); err != nil {
		t.Fatalf("dryRun: %v", err)
	}
	notWritten("dryRun")

	reset()
	// FakeFS is not a real system FS -> isRealRestoreFS gate must skip the apply.
	if err := maybeApplyAccountsFromStage(context.Background(), newTestLogger(), withAccounts, "/stage", false); err != nil {
		t.Fatalf("non-real FS: %v", err)
	}
	notWritten("non-real FS")
}

func assertPasswdShadowConsistent(t *testing.T, passwd, shadow string) {
	t.Helper()
	shadowNames := map[string]bool{}
	for _, l := range splitNonEmptyLines(shadow) {
		shadowNames[colonName(l)] = true
	}
	for _, l := range splitNonEmptyLines(passwd) {
		if name := colonName(l); !shadowNames[name] {
			t.Errorf("passwd<->shadow desync: user %q in passwd has no shadow line", name)
		}
	}
}

func readFake(t *testing.T, fs *FakeFS, path string) string {
	t.Helper()
	data, err := fs.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
