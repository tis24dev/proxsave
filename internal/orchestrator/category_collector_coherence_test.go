package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// coherenceFixtureNames gives every directory path of a common restore category the
// name of a file its collector really copies: several collectors select by name
// (/etc/apt, /root) or by reference (key files named in /etc/crypttab), so a made-up
// name would be a false negative of the test, not of the product. A new directory in
// a common category fails the test until it gets an entry here.
var coherenceFixtureNames = map[string]string{
	"etc/systemd/system/":                    "example.service",
	"etc/default/":                           "zfs",
	"etc/udev/rules.d/":                      "70-persistent-net.rules",
	"etc/apt/":                               "sources.list",
	"etc/logrotate.d/":                       "app",
	"etc/sysctl.d/":                          "99-local.conf",
	"etc/modprobe.d/":                        "vfio.conf",
	"etc/iptables/":                          "rules.v4",
	"etc/nftables.d/":                        "filter.nft",
	"etc/iscsi/":                             "iscsid.conf",
	"var/lib/iscsi/":                         "nodes.txt",
	"etc/multipath/":                         "wwids",
	"etc/mdadm/":                             "mdadm.conf",
	"etc/lvm/backup/":                        "pve",
	"etc/lvm/archive/":                       "pve_00001.vg",
	"etc/auto.master.d/":                     "x.autofs",
	"etc/keys/":                              "vol1.key",
	"etc/luks-keys/":                         "vol2.key",
	"etc/cryptsetup-keys.d/":                 "vol3.key",
	"etc/network/":                           "interfaces",
	"etc/netplan/":                           "01.yaml",
	"etc/systemd/network/":                   "10-x.link",
	"etc/NetworkManager/system-connections/": "x.nmconnection",
	"etc/ssl/":                               "openssl.cnf",
	"etc/proxmox-backup/ssl/":                "x.pem",
	"root/.ssh/":                             "authorized_keys",
	"etc/ssh/":                               "sshd_config",
	"usr/local/bin/":                         "tool",
	"usr/local/sbin/":                        "tool",
	"etc/cron.d/":                            "job",
	"var/spool/cron/":                        "crontabs/root",
	"etc/cron.daily/":                        "job",
	"etc/cron.hourly/":                       "job",
	"etc/cron.weekly/":                       "job",
	"etc/cron.monthly/":                      "job",
	"root/":                                  ".bashrc",
	"home/":                                  "alice/notes.txt",
	"etc/zfs/":                               "zpool.cache",
}

// coherenceFixtureContent overrides the planted content where a collector reads the
// file to decide what else to copy.
var coherenceFixtureContent = map[string]string{
	"etc/crypttab": "vol1 UUID=a /etc/keys/vol1.key luks\n" +
		"vol2 UUID=b /etc/luks-keys/vol2.key luks\n" +
		"vol3 UUID=c /etc/cryptsetup-keys.d/vol3.key luks\n",
}

// coherenceExceptions are common category paths the system recipe does not produce
// on purpose, each with the reason.
var coherenceExceptions = map[string]string{
	"./etc/proxmox-backup/proxy.pem": "collected by the PBS recipe, not the system one",
	"./etc/proxmox-backup/proxy.key": "collected by the PBS recipe, not the system one",
	"./etc/proxmox-backup/ssl/":      "collected by the PBS recipe, not the system one",
	"./etc/default/grub":             "boot: the live file the merge writes, listed so the safety backup covers it; GRUB settings are collected under proxsave-info/boot",
	"./etc/kernel/cmdline":           "boot: the live file the merge writes, listed so the safety backup covers it; the kernel command line is collected under proxsave-info/boot",
}

// Every path a common restore category lists must be produced by the system
// recipe: a category path no collector fills restores nothing, silently. That is
// how ./etc/default/ and ./etc/udev/rules.d/ sat in "services" from the first Go
// commit until the services brick collected them.
func TestCommonCategoryPathsAreCollected(t *testing.T) {
	root := t.TempDir()
	tempDir := t.TempDir()

	listed := map[string]bool{}
	planted := map[string]string{}
	for _, cat := range GetAllCategories() {
		if cat.Type != CategoryTypeCommon || cat.ExportOnly {
			continue
		}
		for _, p := range cat.Paths {
			listed[p] = true
			if _, skip := coherenceExceptions[p]; skip {
				continue
			}
			rel := strings.TrimPrefix(p, "./")
			file := rel
			switch {
			case strings.HasSuffix(rel, "/"):
				name, ok := coherenceFixtureNames[rel]
				if !ok {
					t.Fatalf("category %s lists %s: add the name of a file its collector copies to coherenceFixtureNames", cat.ID, p)
				}
				file = rel + name
			case strings.Contains(rel, "*"):
				file = strings.Replace(rel, "*", "misc", 1)
			}
			content, ok := coherenceFixtureContent[file]
			if !ok {
				content = "fixture\n"
			}
			abs := filepath.Join(root, filepath.FromSlash(file))
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			planted[p] = file
		}
	}
	for p := range coherenceExceptions {
		if !listed[p] {
			t.Errorf("exception %s is no longer listed by any common category: drop it", p)
		}
	}

	cc := backup.GetDefaultCollectorConfig()
	v := reflect.ValueOf(cc).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() == reflect.Bool && strings.HasPrefix(v.Type().Field(i).Name, "Backup") {
			f.SetBool(true)
		}
	}
	cc.SystemRootPrefix = root

	// Every command exists and prints nothing: nothing runs on the host.
	deps := backup.CollectorDeps{
		LookPath:   func(name string) (string, error) { return "/usr/bin/" + name, nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) { return []byte{}, nil },
		RunCommandWithEnv: func(context.Context, []string, string, ...string) ([]byte, error) {
			return []byte{}, nil
		},
		RunCommandCaptured: func(context.Context, []string, string, ...string) ([]byte, []byte, error) {
			return []byte{}, nil, nil
		},
		DetectUnprivilegedContainer: func() (bool, string) { return false, "" },
	}
	c := backup.NewCollectorWithDeps(logging.New(types.LogLevelError, false), cc, tempDir, types.ProxmoxVE, false, deps)
	if err := c.CollectSystemInfo(context.Background()); err != nil {
		t.Fatalf("CollectSystemInfo: %v", err)
	}

	var missing []string
	for p, file := range planted {
		if _, err := os.Lstat(filepath.Join(tempDir, filepath.FromSlash(file))); err != nil {
			missing = append(missing, p+" (planted "+file+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d common category path(s) no system collector produces:\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}
