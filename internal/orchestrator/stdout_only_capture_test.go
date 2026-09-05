package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// streamSplitFake answers like a real process: Run hands back the two streams
// merged (what CombinedOutput produces) and RunStdout hands back stdout alone.
// A command is keyed by its full "name arg arg" line; unkeyed commands succeed
// with empty output, which is what every mutating pvesh/proxmox-backup-manager
// call in these paths expects.
type streamSplitFake struct {
	stdout map[string]string // command line -> stdout
	stderr map[string]string // command line -> stderr
	fail   map[string]bool   // command line -> exits non-zero
	calls  []string
}

func (f *streamSplitFake) key(name string, args []string) string {
	return strings.TrimSpace(name + " " + strings.Join(args, " "))
}

func (f *streamSplitFake) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	k := f.key(name, args)
	f.calls = append(f.calls, "Run "+k)
	if f.fail[k] {
		return []byte(f.stderr[k] + f.stdout[k]), fmt.Errorf("exit status 2")
	}
	return []byte(f.stderr[k] + f.stdout[k]), nil
}

func (f *streamSplitFake) RunStdout(_ context.Context, name string, args ...string) ([]byte, error) {
	k := f.key(name, args)
	f.calls = append(f.calls, "RunStdout "+k)
	if f.fail[k] {
		return []byte(f.stdout[k]), fmt.Errorf("exit status 2: %s", strings.TrimSpace(f.stderr[k]))
	}
	return []byte(f.stdout[k]), nil
}

func (f *streamSplitFake) used(t *testing.T, want string) {
	t.Helper()
	if !strings.Contains(strings.Join(f.calls, "\n"), want) {
		t.Fatalf("expected a %q call, got:\n%s", want, strings.Join(f.calls, "\n"))
	}
}

// A cluster resource mapping is read back through runPveshSensitive after create
// reports the mapping already exists. That read is the ONLY consumer that keeps
// pvesh's output, and it parses it as JSON.
//
// Measured on a live PVE 9.1.9 node (2026-09-05): with LC_ALL set to a locale the
// host does not have, `pvesh get /cluster/mapping/pci --output-format=json` exits
// 0, writes valid JSON to stdout and 542 bytes of Perl locale warning to stderr.
// Merged, json.Unmarshal fails, parsePVEClusterMappingObject returns nothing
// parseable, and the mapping is reported as unreadable: the live entries are never
// merged with the backup ones and the create error is surfaced as the cause, which
// names the wrong problem.
func TestClusterMappingReadSurvivesALocaleWarningOnStderr(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	const id = "gpu0"
	getCmd := "pvesh get /cluster/mapping/pci/" + id + " --output-format=json"
	createCmd := "pvesh create /cluster/mapping/pci --id " + id + " --map node=pve,path=0000:01:00.0"

	fake := &streamSplitFake{
		stdout: map[string]string{
			getCmd: `{"id":"` + id + `","map":["node=pve2,path=0000:02:00.0"]}`,
		},
		stderr: map[string]string{getCmd: measuredPerlLocaleWarning},
		fail:   map[string]bool{createCmd: true},
	}
	restoreCmd = fake

	spec := pveClusterMappingSpec{ID: id, MapEntries: []string{"node=pve,path=0000:01:00.0"}}
	if err := applyPVEClusterResourceMapping(context.Background(), logging.New(types.LogLevelError, false), "pci", spec); err != nil {
		t.Fatalf("a locale warning on stderr made the existing mapping unreadable: %v", err)
	}
	fake.used(t, "RunStdout "+getCmd)

	// The merge is the point: the live entry must survive alongside the backup one,
	// which only happens when the read actually parsed.
	set := ""
	for _, c := range fake.calls {
		if strings.HasPrefix(c, "Run pvesh set ") {
			set = c
		}
	}
	if set == "" {
		t.Fatalf("no set was issued:\n%s", strings.Join(fake.calls, "\n"))
	}
	for _, want := range []string{"node=pve,path=0000:01:00.0", "node=pve2,path=0000:02:00.0"} {
		if !strings.Contains(set, want) {
			t.Fatalf("set dropped %q: %s", want, set)
		}
	}
}

// proxmox-backup-manager is Rust and did NOT reproduce the locale warning on a
// live node (2026-09-05: `datastore list`, `disk list` and `user list` all wrote 0
// bytes to stderr under a missing locale). What is asserted here is not that
// reproduction but the invariant the capture must hold regardless: output a caller
// parses is read from stdout, so anything the tool writes to stderr cannot corrupt
// it. internal/backup reached the same conclusion for this binary and names a real
// trigger for it at collector_deps.go:15 (smartctl failures on `disk list`).
func TestPBSNotificationListsReadStdoutOnly(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	const noise = "WARNING: some backing device is not responding\n"
	targets := "proxmox-backup-manager notification target list --output-format=json"
	matchers := "proxmox-backup-manager notification matcher list --output-format=json"

	fake := &streamSplitFake{
		stdout: map[string]string{
			targets:  `[{"name":"mail-to-root"},{"name":"gotify-ops"}]`,
			matchers: `[{"name":"default-matcher"}]`,
		},
		stderr: map[string]string{targets: noise, matchers: noise},
	}
	restoreCmd = fake

	got, err := pbsNotificationTargetNames(context.Background())
	if err != nil {
		t.Fatalf("stderr noise refused the target list: %v", err)
	}
	for _, want := range []string{"mail-to-root", "gotify-ops"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("target %q missing from %v", want, got)
		}
	}

	gotMatchers, err := pbsNotificationMatcherNames(context.Background())
	if err != nil {
		t.Fatalf("stderr noise refused the matcher list: %v", err)
	}
	if _, ok := gotMatchers["default-matcher"]; !ok {
		t.Fatalf("matcher missing from %v", gotMatchers)
	}
	fake.used(t, "RunStdout "+targets)
	fake.used(t, "RunStdout "+matchers)
}

// Same invariant one layer down, on the shared runPBSManagerRedacted helper that
// every `... list --output-format=json` in pbs_api_apply.go goes through.
func TestPBSManagerListIDsReadStdoutOnly(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	cmd := "proxmox-backup-manager notification endpoint gotify list --output-format=json"
	fake := &streamSplitFake{
		stdout: map[string]string{cmd: `{"data":[{"name":"ops"},{"name":"oncall"}]}`},
		stderr: map[string]string{cmd: "WARNING: proxy configuration is deprecated\n"},
	}
	restoreCmd = fake

	ids, err := listPBSNotificationIDs(context.Background(), "endpoint", "gotify", "list", "--output-format=json")
	if err != nil {
		t.Fatalf("stderr noise refused the endpoint list: %v", err)
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "oncall,ops" {
		t.Fatalf("ids = %v", ids)
	}
	fake.used(t, "RunStdout "+cmd)
}

// listPVEPoolIDs takes fields[0] of every non-empty line as a pool ID, so a
// warning on stderr is ingested as data. Measured on a live PVE 9.1.9 node
// (2026-09-05): `pveum pool list` under a missing locale exits 0 with 556 bytes
// of Perl warning on stderr, and running the loop over the merged bytes yields 17
// phantom IDs, one per LC_ variable the warning enumerates.
//
// The real IDs survive alongside the phantoms, so an assertion that only names
// them passes with the defect in place. The set SIZE is what separates the two.
func TestPoolListIgnoresALocaleWarningOnStderr(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	const cmd = "pveum pool list"
	fake := &streamSplitFake{
		stdout: map[string]string{cmd: "poolid comment\nproduction \nlab \n"},
		stderr: map[string]string{cmd: measuredPerlLocaleWarning},
	}
	restoreCmd = fake

	pools, err := listPVEPoolIDs(context.Background())
	if err != nil {
		t.Fatalf("a locale warning on stderr broke the pool list: %v", err)
	}
	if len(pools) != 2 {
		got := make([]string, 0, len(pools))
		for id := range pools {
			got = append(got, id)
		}
		sort.Strings(got)
		t.Fatalf("pool set has %d entries, want 2: %v", len(pools), got)
	}
	for _, want := range []string{"production", "lab"} {
		if _, ok := pools[want]; !ok {
			t.Fatalf("pool %q missing from %v", want, pools)
		}
	}
	fake.used(t, "RunStdout "+cmd)
}
