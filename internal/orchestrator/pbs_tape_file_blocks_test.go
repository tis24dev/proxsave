package orchestrator

import (
	"strings"
	"testing"
)

// The tape files, one block each, the same in Clean and Merge mode: applied
// (the keys 0600, the key configuration 0640 as PBS writes them), the write
// refused, absent, and an empty JSON file (target removed).
func TestPBSTapeFileBlocks(t *testing.T) {
	staged := map[string]string{
		"etc/proxmox-backup/tape.cfg":                        "lto: drive0\n\tpath /dev/tape/by-id/x\n",
		"etc/proxmox-backup/media-pool.cfg":                  "pool: p1\n\tallocation continue\n",
		"etc/proxmox-backup/tape-job.cfg":                    "backup: tj1\n\tdrive drive0\n\tpool p1\n\tstore ds1\n",
		"etc/proxmox-backup/tape-encryption-keys.json":       `{"aa:bb":{"key":"x"}}`,
		"etc/proxmox-backup/tape-encryption-key-config.json": `{"aa:bb":{"kdf":"x"}}`,
	}
	for _, behavior := range []PBSRestoreBehavior{PBSRestoreBehaviorClean, PBSRestoreBehaviorMerge} {
		log, warned, fakeFS := runPBSSectionRestoreStep(t, behavior, []string{"pbs_tape"}, staged, &FakeCommandRunner{})
		want := []string{
			"INFO     Restoring PBS tape drives and changers...", "INFO     ✓ PBS tape drives and changers: applied",
			"INFO     Restoring PBS tape media pools...", "INFO     ✓ PBS tape media pools: applied",
			"INFO     Restoring PBS tape backup jobs...", "INFO     ✓ PBS tape backup jobs: applied",
			"INFO     Restoring PBS tape encryption keys...", "INFO     ✓ PBS tape encryption keys: applied",
			"INFO     Restoring PBS tape encryption key configuration...", "INFO     ✓ PBS tape encryption key configuration: applied",
		}
		if got := pbsVisibleLines(log); strings.Join(got, "\n") != strings.Join(want, "\n") || warned {
			t.Fatalf("%v: warned=%v visible\n%s", behavior, warned, strings.Join(got, "\n"))
		}
		// PBS writes the keys 0600 root:root and the key configuration 0640
		// root:backup, which proxmox-backup-proxy (user backup) reads to list the
		// keys and to label an encrypted media set (measured on pve-test, PBS 4.2.8).
		for f, perm := range map[string]uint32{"tape-encryption-keys.json": 0o600, "tape-encryption-key-config.json": 0o640} {
			if st, err := fakeFS.Stat("/etc/proxmox-backup/" + f); err != nil || uint32(st.Mode().Perm()) != perm {
				t.Fatalf("%s: %v, want mode %o", f, err, perm)
			}
		}
	}
	t.Run("write-refused", func(t *testing.T) {
		log, warned, _ := runPBSSectionRestoreStep(t, PBSRestoreBehaviorMerge, []string{"pbs_tape"}, map[string]string{"etc/proxmox-backup/tape-encryption-key-config.json": `{}`}, &FakeCommandRunner{}, func() {
			if err := restoreFS.MkdirAll("/etc/proxmox-backup/tape-encryption-key-config.json/x", 0o755); err != nil {
				t.Fatal(err)
			}
		})
		want := []string{
			"INFO     Restoring PBS tape encryption key configuration...",
			`INFO     The tape encryption key configuration could not be written to this PBS: "write /etc/proxmox-backup/tape-encryption-key-config.json: rename <fakefs>/etc/proxmox-backup/tape-encryption-key-config.json.proxsave.tmp.<n> <fakefs>/etc/proxmox-backup/tape-encryption-key-config.json: file exists"`,
			"WARNING  ⚠ PBS tape encryption key configuration: not applied",
		}
		got := pbsVisibleLines(pbsLogAtomicTemp.ReplaceAllString(log, ".proxsave.tmp.<n>"))
		if strings.Join(got, "\n") != strings.Join(want, "\n") || !warned {
			t.Fatalf("warned=%v visible\n%s", warned, strings.Join(got, "\n"))
		}
	})
	t.Run("absent-and-empty-json", func(t *testing.T) {
		log, warned, fakeFS := runPBSSectionRestoreStep(t, PBSRestoreBehaviorClean, []string{"pbs_tape"}, map[string]string{"etc/proxmox-backup/tape-encryption-keys.json": "  \n"}, &FakeCommandRunner{}, func() {
			if err := restoreFS.WriteFile("/etc/proxmox-backup/tape-encryption-keys.json", []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		})
		want := []string{"INFO     Restoring PBS tape encryption keys...", "INFO     ✓ PBS tape encryption keys: applied"}
		if got := pbsVisibleLines(log); strings.Join(got, "\n") != strings.Join(want, "\n") || warned {
			t.Fatalf("warned=%v visible\n%s", warned, strings.Join(got, "\n"))
		}
		if !strings.Contains(log, "DEBUG    PBS tape encryption keys restore: staged file is empty, target removed\n") ||
			!strings.Contains(log, "DEBUG    PBS tape drives restore: staged=/stage/etc/proxmox-backup/tape.cfg not in the backup action=skip\n") {
			t.Fatalf("missing DEBUG evidence:\n%s", log)
		}
		if _, err := fakeFS.Stat("/etc/proxmox-backup/tape-encryption-keys.json"); err == nil {
			t.Fatal("an empty staged keys file removes the target")
		}
	})
}
