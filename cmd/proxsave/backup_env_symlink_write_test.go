package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A configs/backup.env that is an absolute or escaping symlink was refused by the
// daemon-mode writes ("path escapes from parent") while the loader followed it: the
// switch to the daemon removed the cron line and recorded nothing. The write now
// follows the link to its target, and the link stays a link.
func TestSetBackupEnvKeysFollowsASymlinkedConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		link func(target, configs string) string
	}{
		{"absolute link outside", func(target, _ string) string { return target }},
		{"relative link escaping", func(target, configs string) string {
			rel, _ := filepath.Rel(configs, target)
			return rel
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "etc", "proxsave", "backup.env")
			configs := filepath.Join(root, "base", "configs")
			for _, d := range []string{filepath.Dir(target), configs} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(target, []byte("SCHEDULER_MODE=cron\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(configs, "backup.env")
			if err := os.Symlink(tc.link(target, configs), link); err != nil {
				t.Fatal(err)
			}
			if err := setBackupEnvKeys(link, map[string]string{"SCHEDULER_MODE": "daemon"}); err != nil {
				t.Fatalf("setBackupEnvKeys: %v", err)
			}
			data, err := os.ReadFile(target)
			if err != nil || !strings.Contains(string(data), "SCHEDULER_MODE=daemon") {
				t.Fatalf("target = %q (%v), want SCHEDULER_MODE=daemon", data, err)
			}
			if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("the link was replaced: %v %v", fi.Mode(), err)
			}
		})
	}
}

// The adoption that cannot read backup.env logged only DEBUG while the cron line was
// removed anyway; it is now the WARNING a failed write already gets.
func TestScheduleAdoptionWarnsWhenBackupEnvCannotBeRead(t *testing.T) {
	var out bytes.Buffer
	bootstrap := logging.NewBootstrapLogger()
	bootstrap.SetConsoleQuiet(true)
	mirror := logging.New(types.LogLevelDebug, false)
	mirror.SetOutput(&out)
	bootstrap.SetMirrorLogger(mirror)
	adoptSchedulerTimeForDaemon(filepath.Join(t.TempDir(), "missing", "backup.env"), []string{"30 21 * * * /usr/local/bin/proxsave"}, bootstrap)
	log := out.String()
	for _, want := range []string{"Backup schedule: not adopted from cron entry", "backup.env could not be read"} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q; log:\n%s", want, log)
		}
	}
}
