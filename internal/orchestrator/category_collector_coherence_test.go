package orchestrator

import (
	"context"
	"errors"
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

// coherenceFixtureNames gives every directory path of a restore category the name
// of a file its collector really copies: several collectors select by name
// (/etc/apt, /root, /etc/corosync) or by reference (key files named in
// /etc/crypttab), so a made-up name would be a false negative of the test, not of
// the product. A new directory in a category fails the test until it gets an entry
// here.
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
	"etc/pve/":                               "storage.cfg",
	"var/lib/pve-cluster/":                   "config.db",
	"etc/pve/firewall/":                      "cluster.fw",
	"etc/pve/sdn/":                           "zones.cfg",
	"etc/corosync/":                          "authkey",
	"etc/ceph/":                              "ceph.conf",
	"etc/proxmox-backup/":                    "datastore.cfg",
	"etc/proxmox-backup/acme/accounts/":      "default",
}

// coherenceFixtureContent overrides the planted content where a collector reads the
// file to decide what else to copy.
var coherenceFixtureContent = map[string]string{
	"etc/crypttab": "vol1 UUID=a /etc/keys/vol1.key luks\n" +
		"vol2 UUID=b /etc/luks-keys/vol2.key luks\n" +
		"vol3 UUID=c /etc/cryptsetup-keys.d/vol3.key luks\n",
}

// coherenceRuntimePrefix holds what a backup generates rather than copies: command
// outputs, inventories and the boot reference copies are written under it at backup
// time, so no system file sits at the same path to be collected.
const coherenceRuntimePrefix = "./var/lib/proxsave-info/"

// coherenceExceptions are category paths no collector copies from the same path on
// the system, on purpose, each with the reason.
var coherenceExceptions = map[string]string{
	"./manifest.json":                 "written by the archiver after collection, not copied from the system",
	"./etc/default/grub":              "boot lists the live file the merge writes, for the safety backup; proxsave_info exports a copy CUSTOM_BACKUP_PATHS puts there; collected under proxsave-info/boot",
	"./etc/kernel/cmdline":            "boot lists the live file the merge writes, for the safety backup; proxsave_info exports a copy CUSTOM_BACKUP_PATHS puts there; collected under proxsave-info/boot",
	"./etc/default/grub.d/":           "proxsave_info exports a copy CUSTOM_BACKUP_PATHS puts there; collected under proxsave-info/boot",
	"./etc/kernel/proxmox-boot-uuids": "proxsave_info exports a copy CUSTOM_BACKUP_PATHS puts there; collected under proxsave-info/boot",
}

// coherenceRecipes are the collections a backup runs, each on its own collector.
var coherenceRecipes = []struct {
	name    string
	role    types.ProxmoxType
	collect func(*backup.Collector, context.Context) error
}{
	{"system", types.ProxmoxVE, (*backup.Collector).CollectSystemInfo},
	{"pve", types.ProxmoxVE, (*backup.Collector).CollectPVEConfigs},
	{"pbs", types.ProxmoxBS, (*backup.Collector).CollectPBSConfigs},
}

// Every path a restore category lists must be produced by a collector: a category
// path no collector fills restores nothing, silently. That is how ./etc/default/ and
// ./etc/udev/rules.d/ sat in "services" from the first Go commit until the services
// brick collected them. The system, PVE and PBS recipes run over one fixture holding
// a file under every category path, with every Backup* variable on and every
// command stubbed; nothing runs on the host.
func TestCategoryPathsAreCollected(t *testing.T) {
	root := t.TempDir()

	listed := map[string]bool{}
	planted := map[string]string{}
	for _, cat := range GetAllCategories() {
		for _, p := range cat.Paths {
			listed[p] = true
			if _, skip := coherenceExceptions[p]; skip || strings.HasPrefix(p, coherenceRuntimePrefix) {
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
			t.Errorf("exception %s is no longer listed by any category: drop it", p)
		}
	}

	var tempDirs []string
	for _, r := range coherenceRecipes {
		cc := backup.GetDefaultCollectorConfig()
		v := reflect.ValueOf(cc).Elem()
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			if f.Kind() == reflect.Bool && strings.HasPrefix(v.Type().Field(i).Name, "Backup") {
				f.SetBool(true)
			}
		}
		cc.SystemRootPrefix = root

		// Every command exists and prints nothing, except sqlite3: the stub would
		// "succeed" without writing the config.db snapshot, so it is reported missing
		// and the collector falls back to the raw copy it makes on any real host
		// without the tool.
		deps := backup.CollectorDeps{
			LookPath: func(name string) (string, error) {
				if name == "sqlite3" {
					return "", errors.New("not installed")
				}
				return "/usr/bin/" + name, nil
			},
			RunCommand: func(context.Context, string, ...string) ([]byte, error) { return []byte{}, nil },
			RunCommandWithEnv: func(context.Context, []string, string, ...string) ([]byte, error) {
				return []byte{}, nil
			},
			RunCommandCaptured: func(context.Context, []string, string, ...string) ([]byte, []byte, error) {
				return []byte{}, nil, nil
			},
			DetectUnprivilegedContainer: func() (bool, string) { return false, "" },
		}
		tempDir := t.TempDir()
		c := backup.NewCollectorWithDeps(logging.New(types.LogLevelError, false), cc, tempDir, r.role, false, deps)
		if err := r.collect(c, context.Background()); err != nil {
			t.Fatalf("%s recipe: %v", r.name, err)
		}
		tempDirs = append(tempDirs, tempDir)
	}

	// The PVE recipe names its targets after the absolute source path
	// (targetPathFor), so under SYSTEM_ROOT_PREFIX a file lands below the prefix; on
	// a host without one that is the natural path. Both count as collected here.
	rootRel := strings.TrimPrefix(filepath.ToSlash(root), "/")
	var missing []string
	for p, file := range planted {
		found := false
		for _, dir := range tempDirs {
			for _, rel := range []string{file, rootRel + "/" + file} {
				if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel))); err == nil {
					found = true
				}
			}
		}
		if !found {
			missing = append(missing, p+" (planted "+file+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d category path(s) no collector produces:\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}
