package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveGuardTargetWithinAllowlist exercises the shared resolve+recheck gate
// directly through the injectable resolveGuardTarget seam (no real symlinks, which
// the unprivileged test process could not create under /mnt anyway).
func TestResolveGuardTargetWithinAllowlist(t *testing.T) {
	orig := resolveGuardTarget
	t.Cleanup(func() { resolveGuardTarget = orig })

	cases := []struct {
		name           string
		target         string
		resolve        func(string) (string, error)
		wantResolved   string
		wantLeafExists bool
		wantOK         bool
		wantErr        bool
	}{
		{
			name:           "identity in-allowlist",
			target:         "/mnt/ds",
			resolve:        func(p string) (string, error) { return p, nil },
			wantResolved:   "/mnt/ds",
			wantLeafExists: true,
			wantOK:         true,
		},
		{
			name:           "parent symlink escapes allowlist",
			target:         "/mnt/data",
			resolve:        func(string) (string, error) { return "/etc/data", nil },
			wantResolved:   "/etc/data",
			wantLeafExists: true,
			wantOK:         false,
		},
		{
			name:           "allowlist to allowlist",
			target:         "/mnt/x",
			resolve:        func(string) (string, error) { return "/media/real/x", nil },
			wantResolved:   "/media/real/x",
			wantLeafExists: true,
			wantOK:         true,
		},
		{
			name:   "leaf missing, ancestor in-allowlist",
			target: "/mnt/new",
			resolve: func(p string) (string, error) {
				if p == "/mnt/new" {
					return "", os.ErrNotExist
				}
				return p, nil // "/mnt" resolves to itself
			},
			wantResolved:   "/mnt/new",
			wantLeafExists: false,
			wantOK:         true,
		},
		{
			name:   "leaf missing, ancestor escapes allowlist",
			target: "/mnt/new",
			resolve: func(p string) (string, error) {
				if p == "/mnt/new" {
					return "", os.ErrNotExist
				}
				return "/srv/mnt", nil // "/mnt" is a symlink to /srv/mnt
			},
			wantResolved:   "/srv/mnt/new",
			wantLeafExists: false,
			wantOK:         false,
		},
		{
			name:    "generic resolve error",
			target:  "/mnt/x",
			resolve: func(string) (string, error) { return "", errors.New("boom") },
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolveGuardTarget = tc.resolve
			resolved, leafExists, ok, err := resolveGuardTargetWithinAllowlist(tc.target)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got resolved=%q ok=%v", resolved, ok)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resolved != tc.wantResolved {
				t.Errorf("resolved = %q, want %q", resolved, tc.wantResolved)
			}
			if leafExists != tc.wantLeafExists {
				t.Errorf("leafExists = %v, want %v", leafExists, tc.wantLeafExists)
			}
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
		})
	}
}

// installPBSGuardApplySeams wires the seams the PBS apply path consumes so a test
// can drive it to the bind/chattr decision without touching the real host. fstab
// supplies the protectable mountpoint set; resolve is the symlink seam under test.
func installPBSGuardApplySeams(t *testing.T, fstab map[string]struct{}, resolve func(string) (string, error)) {
	t.Helper()
	withTempGuardBaseDir(t)

	origFS := restoreFS
	origGeteuid := mountGuardGeteuid
	origMkdir := mountGuardMkdirAll
	origRootFS := mountGuardIsPathOnRootFilesystem
	origMount := mountGuardSysMount
	origUnmount := mountGuardSysUnmount
	origRead := mountGuardReadFile
	origFstab := mountGuardFstabMountpointsSet
	origResolve := resolveGuardTarget
	t.Cleanup(func() {
		restoreFS = origFS
		mountGuardGeteuid = origGeteuid
		mountGuardMkdirAll = origMkdir
		mountGuardIsPathOnRootFilesystem = origRootFS
		mountGuardSysMount = origMount
		mountGuardSysUnmount = origUnmount
		mountGuardReadFile = origRead
		mountGuardFstabMountpointsSet = origFstab
		resolveGuardTarget = origResolve
	})

	restoreFS = osFS{}
	mountGuardGeteuid = func() int { return 0 }
	mountGuardMkdirAll = func(string, os.FileMode) error { return nil }
	mountGuardIsPathOnRootFilesystem = func(p string) (bool, string, error) { return true, p, nil }               // force offline guard
	mountGuardSysMount = func(string, string, string, uintptr, string) error { return errors.New("bind denied") } // force chattr fallback
	mountGuardSysUnmount = func(string, int) error { return nil }
	mountGuardReadFile = func(string) ([]byte, error) { return []byte(""), nil } // /proc empty -> not mounted
	mountGuardFstabMountpointsSet = func(string) (map[string]struct{}, error) { return fstab, nil }
	resolveGuardTarget = resolve
}

func writePBSDatastoreCfg(t *testing.T, dsPath string) string {
	t.Helper()
	stageRoot := t.TempDir()
	stagePath := filepath.Join(stageRoot, "etc/proxmox-backup/datastore.cfg")
	if err := os.MkdirAll(filepath.Dir(stagePath), 0o755); err != nil {
		t.Fatalf("mkdir staged dir: %v", err)
	}
	cfg := "datastore: ds-test\n    path " + dsPath + "\n"
	if err := os.WriteFile(stagePath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write datastore.cfg: %v", err)
	}
	return stageRoot
}

// TestPBSGuard_SymlinkEscapeRefused: a guard target whose component resolves
// outside the datastore roots must not be mkdir'd / bind-mounted / chattr'd.
func TestPBSGuard_SymlinkEscapeRefused(t *testing.T) {
	installPBSGuardApplySeams(t,
		map[string]struct{}{"/mnt/escape": {}},
		func(p string) (string, error) {
			if filepath.Clean(p) == "/mnt/escape" {
				return "/etc/evil", nil // parent symlink escapes the allowlist
			}
			return p, nil
		},
	)
	cmd := &mountGuardCommandRunner{}
	origCmd := restoreCmd
	restoreCmd = cmd
	t.Cleanup(func() { restoreCmd = origCmd })

	stageRoot := writePBSDatastoreCfg(t, "/mnt/escape/store")
	plan := &RestorePlan{SystemType: SystemTypePBS, NormalCategories: []Category{{ID: "datastore_pbs"}}}
	if err := maybeApplyPBSDatastoreMountGuards(context.Background(), newTestLogger(), plan, stageRoot, "/", false); err != nil {
		t.Fatalf("guard should be non-fatal, got %v", err)
	}

	for _, c := range cmd.calls {
		if c.name == "chattr" {
			t.Fatalf("escape target must not be chattr'd; calls=%v", cmd.calls)
		}
	}
	if got := readGuardIndexLines(t); len(got) != 0 {
		t.Fatalf("escape target must not be recorded; got %#v", got)
	}
}

// TestPBSGuard_AllowlistToAllowlistResolved: a symlink from one allowlisted root
// to another is allowed, and the guard acts on (and records) the RESOLVED path so
// cleanup can later match it.
func TestPBSGuard_AllowlistToAllowlistResolved(t *testing.T) {
	const resolved = "/media/real"
	installPBSGuardApplySeams(t,
		map[string]struct{}{"/mnt/x": {}},
		func(p string) (string, error) {
			if filepath.Clean(p) == "/mnt/x" {
				return resolved, nil // /mnt/x -> /media/real (both allowlisted)
			}
			return p, nil
		},
	)
	cmd := &mountGuardCommandRunner{}
	origCmd := restoreCmd
	restoreCmd = cmd
	t.Cleanup(func() { restoreCmd = origCmd })

	stageRoot := writePBSDatastoreCfg(t, "/mnt/x/store")
	plan := &RestorePlan{SystemType: SystemTypePBS, NormalCategories: []Category{{ID: "datastore_pbs"}}}
	if err := maybeApplyPBSDatastoreMountGuards(context.Background(), newTestLogger(), plan, stageRoot, "/", false); err != nil {
		t.Fatalf("guard should be non-fatal, got %v", err)
	}

	sawChattr := false
	for _, c := range cmd.calls {
		if c.name == "chattr" {
			if len(c.args) != 2 || c.args[0] != "+i" || c.args[1] != resolved {
				t.Fatalf("chattr must target the resolved path %q; got args=%v", resolved, c.args)
			}
			sawChattr = true
		}
	}
	if !sawChattr {
		t.Fatalf("expected chattr +i on resolved path; calls=%v", cmd.calls)
	}
	if got := readGuardIndexLines(t); len(got) != 1 || got[0] != resolved {
		t.Fatalf("index must record the resolved path %q; got %#v", resolved, got)
	}
}

// TestPBSGuard_ResolveErrorFailsSafe: a generic (non-ENOENT) resolution error at
// the apply site must skip the guard, not act on the unresolved path.
func TestPBSGuard_ResolveErrorFailsSafe(t *testing.T) {
	installPBSGuardApplySeams(t,
		map[string]struct{}{"/mnt/x": {}},
		func(p string) (string, error) {
			if filepath.Clean(p) == "/mnt/x" {
				return "", errors.New("boom")
			}
			return p, nil
		},
	)
	cmd := &mountGuardCommandRunner{}
	origCmd := restoreCmd
	restoreCmd = cmd
	t.Cleanup(func() { restoreCmd = origCmd })

	stageRoot := writePBSDatastoreCfg(t, "/mnt/x/store")
	plan := &RestorePlan{SystemType: SystemTypePBS, NormalCategories: []Category{{ID: "datastore_pbs"}}}
	if err := maybeApplyPBSDatastoreMountGuards(context.Background(), newTestLogger(), plan, stageRoot, "/", false); err != nil {
		t.Fatalf("guard should be non-fatal, got %v", err)
	}
	for _, c := range cmd.calls {
		if c.name == "chattr" {
			t.Fatalf("resolve error must skip the guard; calls=%v", cmd.calls)
		}
	}
	if got := readGuardIndexLines(t); len(got) != 0 {
		t.Fatalf("resolve error must not record anything; got %#v", got)
	}
}

// installPVEGuardApplySeams mirrors installPBSGuardApplySeams for the PVE path.
func installPVEGuardApplySeams(t *testing.T, resolve func(string) (string, error)) {
	t.Helper()
	withTempGuardBaseDir(t)

	origFS := restoreFS
	origGeteuid := mountGuardGeteuid
	origMkdir := mountGuardMkdirAll
	origRootFS := mountGuardIsPathOnRootFilesystem
	origMount := mountGuardSysMount
	origUnmount := mountGuardSysUnmount
	origRead := mountGuardReadFile
	origResolve := resolveGuardTarget
	t.Cleanup(func() {
		restoreFS = origFS
		mountGuardGeteuid = origGeteuid
		mountGuardMkdirAll = origMkdir
		mountGuardIsPathOnRootFilesystem = origRootFS
		mountGuardSysMount = origMount
		mountGuardSysUnmount = origUnmount
		mountGuardReadFile = origRead
		resolveGuardTarget = origResolve
	})

	restoreFS = osFS{}
	mountGuardGeteuid = func() int { return 0 }
	mountGuardMkdirAll = func(string, os.FileMode) error { return nil }
	mountGuardIsPathOnRootFilesystem = func(p string) (bool, string, error) { return true, p, nil }
	mountGuardSysMount = func(string, string, string, uintptr, string) error { return errors.New("bind denied") }
	mountGuardSysUnmount = func(string, int) error { return nil }
	mountGuardReadFile = func(string) ([]byte, error) { return []byte(""), nil }
	resolveGuardTarget = resolve
}

func writePVEStorageCfg(t *testing.T, storageID string) string {
	t.Helper()
	stageRoot := t.TempDir()
	stageCfgPath := filepath.Join(stageRoot, "etc/pve/storage.cfg")
	if err := os.MkdirAll(filepath.Dir(stageCfgPath), 0o755); err != nil {
		t.Fatalf("mkdir stage cfg dir: %v", err)
	}
	if err := os.WriteFile(stageCfgPath, []byte("nfs: "+storageID+"\n"), 0o644); err != nil {
		t.Fatalf("write staged storage.cfg: %v", err)
	}
	return stageRoot
}

// TestPVEGuard_SymlinkEscapeRefused: PVE apply must refuse a symlink-escaping target.
func TestPVEGuard_SymlinkEscapeRefused(t *testing.T) {
	id := uniquePveMountTestStorageID(t, "escape")
	target := pveMountTargetForStorageID(id)
	installPVEGuardApplySeams(t, func(p string) (string, error) {
		if filepath.Clean(p) == filepath.Clean(target) {
			return "/etc/evil", nil
		}
		return p, nil
	})
	fakeCmd := &FakeCommandRunner{Errors: map[string]error{"which pvesm": errors.New("missing")}}
	origCmd := restoreCmd
	restoreCmd = fakeCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	stageRoot := writePVEStorageCfg(t, id)
	if err := maybeApplyPVEStorageMountGuardsFromStage(context.Background(), newTestLogger(), pvePlan(false, "storage_pve"), stageRoot, "/"); err != nil {
		t.Fatalf("guard should be non-fatal, got %v", err)
	}
	if strings.Contains(strings.Join(fakeCmd.CallsList(), "\n"), "chattr +i") {
		t.Fatalf("escape target must not be chattr'd; calls=%v", fakeCmd.CallsList())
	}
	if got := readGuardIndexLines(t); len(got) != 0 {
		t.Fatalf("escape target must not be recorded; got %#v", got)
	}
}

// TestPVEGuard_AllowlistToAllowlistResolved: PVE apply acts on and records the
// resolved path when a symlink stays inside the allowlist.
func TestPVEGuard_AllowlistToAllowlistResolved(t *testing.T) {
	const resolved = "/media/pvereal"
	id := uniquePveMountTestStorageID(t, "resolved")
	target := pveMountTargetForStorageID(id)
	installPVEGuardApplySeams(t, func(p string) (string, error) {
		if filepath.Clean(p) == filepath.Clean(target) {
			return resolved, nil
		}
		return p, nil
	})
	fakeCmd := &FakeCommandRunner{
		Errors: map[string]error{
			"which pvesm":           errors.New("missing"),
			"mount " + resolved:     errors.New("offline"),
			"chattr +i " + resolved: nil,
		},
	}
	origCmd := restoreCmd
	restoreCmd = fakeCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	stageRoot := writePVEStorageCfg(t, id)
	if err := maybeApplyPVEStorageMountGuardsFromStage(context.Background(), newTestLogger(), pvePlan(false, "storage_pve"), stageRoot, "/"); err != nil {
		t.Fatalf("guard should be non-fatal, got %v", err)
	}
	if !strings.Contains(strings.Join(fakeCmd.CallsList(), "\n"), "chattr +i "+resolved) {
		t.Fatalf("expected chattr +i on resolved path %q; calls=%v", resolved, fakeCmd.CallsList())
	}
	if got := readGuardIndexLines(t); len(got) != 1 || got[0] != resolved {
		t.Fatalf("index must record the resolved path %q; got %#v", resolved, got)
	}
}

// TestPVEGuard_ResolveErrorFailsSafe: PVE apply must skip on a generic resolve error.
func TestPVEGuard_ResolveErrorFailsSafe(t *testing.T) {
	id := uniquePveMountTestStorageID(t, "resolveerr")
	target := pveMountTargetForStorageID(id)
	installPVEGuardApplySeams(t, func(p string) (string, error) {
		if filepath.Clean(p) == filepath.Clean(target) {
			return "", errors.New("boom")
		}
		return p, nil
	})
	fakeCmd := &FakeCommandRunner{Errors: map[string]error{"which pvesm": errors.New("missing")}}
	origCmd := restoreCmd
	restoreCmd = fakeCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	stageRoot := writePVEStorageCfg(t, id)
	if err := maybeApplyPVEStorageMountGuardsFromStage(context.Background(), newTestLogger(), pvePlan(false, "storage_pve"), stageRoot, "/"); err != nil {
		t.Fatalf("guard should be non-fatal, got %v", err)
	}
	if strings.Contains(strings.Join(fakeCmd.CallsList(), "\n"), "chattr +i") {
		t.Fatalf("resolve error must skip the guard; calls=%v", fakeCmd.CallsList())
	}
	if got := readGuardIndexLines(t); len(got) != 0 {
		t.Fatalf("resolve error must not record anything; got %#v", got)
	}
}
