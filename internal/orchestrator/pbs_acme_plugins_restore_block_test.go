package orchestrator

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Refusals measured on VM 201 (PBS 4.2.8, 2026-10-18), in the shape
// osCommandRunner.RunStdout returns them.
const (
	pbsAcmeDelayRefusedAdd = "exit status 255: Error: parameter verification failed - 'validation-delay': value must have a maximum value of 172800 (got 999999)\nUsage: proxmox-backup-manager acme plugin add <type> <id> --api <string> --data <string> [OPTIONS]"
	pbsAcmeDelayRefusedSet = "exit status 255: Error: parameter verification failed - 'validation-delay': value must have a maximum value of 172800 (got 999999)\nUsage: proxmox-backup-manager acme plugin set <id> [OPTIONS]"
	pbsAcmeListCmd         = "proxmox-backup-manager acme plugin list --output-format=json"
)

// pbsAcmeRunner answers the acme plugin commands by verb and id, and records each
// call with the data file path replaced, the content and mode of that file at the
// time of the call, and the directory it sat in.
type pbsAcmeRunner struct {
	listOut  string
	listErr  error
	errs     map[string]error // "verb id"
	calls    []string
	data     map[string]string
	dataMode map[string]os.FileMode
	dirMode  map[string]os.FileMode
	dataDirs []string
}

func (r *pbsAcmeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	args = append([]string(nil), args...)
	if len(args) >= 3 && args[2] == "list" {
		r.calls = append(r.calls, name+" "+strings.Join(args, " "))
		return []byte(r.listOut), r.listErr
	}
	verb, id := args[2], args[3]
	if verb == "add" {
		id = args[4]
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--data" {
				path := args[i+1]
				content, _ := os.ReadFile(path)
				if r.data == nil {
					r.data, r.dataMode, r.dirMode = map[string]string{}, map[string]os.FileMode{}, map[string]os.FileMode{}
				}
				r.data[id] = string(content)
				if st, err := os.Stat(path); err == nil {
					r.dataMode[id] = st.Mode().Perm()
				}
				if st, err := os.Stat(filepath.Dir(path)); err == nil {
					r.dirMode[id] = st.Mode().Perm()
				}
				r.dataDirs = append(r.dataDirs, filepath.Dir(path))
				args[i+1] = "<data-file>"
			}
		}
	}
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if err, ok := r.errs[verb+" "+id]; ok {
		return nil, err
	}
	return nil, nil
}

func pbsB64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// The ACME plugins through the PBS API, Clean: the stale plugin is removed, p1 is
// added with its data handed over as a 0600 file that is gone afterwards, p2 is
// refused by PBS both ways, and the built-in standalone plugin is left alone.
// No secret reaches a log line or the result.
func TestPBSAcmePluginsRestoreBlock_Clean(t *testing.T) {
	stageRoot, fs, _ := setupPBSAPIApplyTestDeps(t)
	// p1 carries its data unpadded, as PBS 4.2.8 writes it.
	staged := "dns: p1\n\tapi cf\n\tdata " + base64.RawStdEncoding.EncodeToString([]byte("CF_Token=SECRET-P1\n")) + "\n\n" +
		"dns: p2\n\tapi cf\n\tdata " + pbsB64("CF_Token=SECRET-P2\n") + "\n\tvalidation-delay 999999\n\n" +
		"standalone: standalone\n"
	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/acme/plugins.cfg", staged, 0o600)
	runner := &pbsAcmeRunner{
		listOut: `[{"api":"cf","data":"CF_Token=SECRET-OLD\n","plugin":"old","type":"dns"},{"plugin":"standalone","type":"standalone"}]`,
		errs: map[string]error{
			"add p2": errors.New(pbsAcmeDelayRefusedAdd),
			"set p2": errors.New(pbsAcmeDelayRefusedSet),
		},
	}
	restoreCmd = runner
	var logBuf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&logBuf)

	err := applyPBSAcmePluginsCfgViaAPI(context.Background(), logger, stageRoot, true)

	assertPBSBlockLog(t, logBuf.String(), []string{
		`DEBUG    PBS ACME plugin restore: reading current acme-plugins cmd="proxmox-backup-manager acme plugin list --output-format=json"`,
		"INFO     Restoring PBS ACME plugins...",
		"DEBUG    PBS ACME plugin restore: mode=clean staged=/stage/etc/proxmox-backup/acme/plugins.cfg",
		"DEBUG    PBS ACME plugin restore: backup acme-plugins=2 names=p1,p2",
		"INFO       ACME plugins to restore: 2",
		"INFO       Names: p1, p2",
		"DEBUG    PBS ACME plugin restore: current acme-plugins=1 names=old",
		"DEBUG    PBS ACME plugin restore: acme-plugin=old not in backup action=remove",
		"DEBUG    PBS ACME plugin restore: acme-plugin=old removed",
		"INFO     ACME plugin old is configured on this PBS but not present in the backup",
		"INFO     ✓ PBS ACME plugin old: removed",
		"DEBUG    PBS ACME plugin restore: acme-plugin=p1 created",
		"INFO     ✓ PBS ACME plugin p1: applied",
		`DEBUG    PBS ACME plugin restore: acme-plugin=p2 create failed error="exit status 255: Error: parameter verification failed - 'validation-delay': value must have a maximum value of 172800 (got 999999)\nUsage: proxmox-backup-manager acme plugin add <type> <id> --api <string> --data <string> [OPTIONS]" action=update`,
		`DEBUG    PBS ACME plugin restore: acme-plugin=p2 update failed error="exit status 255: Error: parameter verification failed - 'validation-delay': value must have a maximum value of 172800 (got 999999)\nUsage: proxmox-backup-manager acme plugin set <id> [OPTIONS]"`,
		`INFO     ACME plugin p2 was rejected by PBS: "parameter verification failed - 'validation-delay': value must have a maximum value of 172800 (got 999999)"`,
		"WARNING  ⚠ PBS ACME plugin p2: not applied",
		"WARNING  ⚠ PBS ACME plugin configuration: partially applied",
	})
	wantCalls := []string{
		pbsAcmeListCmd,
		"proxmox-backup-manager acme plugin remove old",
		"proxmox-backup-manager acme plugin add dns p1 --api cf --data <data-file>",
		"proxmox-backup-manager acme plugin add dns p2 --api cf --data <data-file> --validation-delay 999999",
		"proxmox-backup-manager acme plugin set p2 --api cf --data " + pbsB64("CF_Token=SECRET-P2\n") + " --validation-delay 999999",
	}
	if strings.Join(runner.calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("calls\n%s\nwant\n%s", strings.Join(runner.calls, "\n"), strings.Join(wantCalls, "\n"))
	}
	for id, want := range map[string]string{"p1": "CF_Token=SECRET-P1\n", "p2": "CF_Token=SECRET-P2\n"} {
		if runner.data[id] != want || runner.dataMode[id] != 0o600 || runner.dirMode[id] != 0o700 {
			t.Fatalf("%s data file %q mode %o dir %o, want the decoded data, 0600 in a 0700 directory", id, runner.data[id], runner.dataMode[id], runner.dirMode[id])
		}
	}
	for _, dir := range runner.dataDirs {
		if _, statErr := os.Stat(dir); statErr == nil {
			t.Fatalf("the data directory %s must be removed after the command", dir)
		}
	}
	var incomplete *pbsSectionRestoreIncompleteError
	if !errors.As(err, &incomplete) || strings.Join(incomplete.notApplied, ",") != "p2" {
		t.Fatalf("err = %T %v, want p2 not applied", err, err)
	}
	text := logBuf.String() + err.Error()
	for _, secret := range []string{"SECRET-P1", "SECRET-P2", "SECRET-OLD", base64.RawStdEncoding.EncodeToString([]byte("CF_Token=SECRET-P1\n")), pbsB64("CF_Token=SECRET-P2\n")} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %q leaked:\n%s", secret, text)
		}
	}
}

// The command text of a refused set carries --data redacted, never the data.
func TestPBSAcmePluginsSetRefusalIsRedacted(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	restoreCmd = &pbsAcmeRunner{errs: map[string]error{"set p2": errors.New(pbsAcmeDelayRefusedSet)}}
	cfg := pbsAcmePluginsSectionCfg()
	secret := pbsB64("CF_Token=SECRET-P2\n")
	_, err := runPBSManagerSensitive(context.Background(), []string{"acme", "plugin", "set", "p2", "--api", "cf", "--data", secret}, cfg.secretFlags...)
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "--data <redacted>") {
		t.Fatalf("err = %v, want --data redacted", err)
	}
}

// Merge mode with the API removes nothing and never lists; the built-in
// standalone plugin of the backup is not applied.
func TestPBSAcmePluginsRestoreBlock_MergeKeepsStale(t *testing.T) {
	stageRoot, fs, _ := setupPBSAPIApplyTestDeps(t)
	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/acme/plugins.cfg", "dns: p1\n\tapi cf\n\tdata "+pbsB64("x")+"\n\nstandalone: standalone\n", 0o600)
	runner := &pbsAcmeRunner{}
	restoreCmd = runner
	var logBuf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&logBuf)
	if err := applyPBSAcmePluginsCfgViaAPI(context.Background(), logger, stageRoot, false); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := pbsVisibleLines(logBuf.String()); strings.Join(got, "\n") != strings.Join([]string{
		"INFO     Restoring PBS ACME plugins...",
		"INFO       ACME plugins to restore: 1",
		"INFO       Names: p1",
		"INFO     ✓ PBS ACME plugin p1: applied",
		"INFO     ✓ PBS ACME plugin configuration: applied",
	}, "\n") {
		t.Fatalf("visible lines:\n%s", strings.Join(got, "\n"))
	}
	if strings.Join(runner.calls, "\n") != "proxmox-backup-manager acme plugin add dns p1 --api cf --data <data-file>" {
		t.Fatalf("calls %v", runner.calls)
	}
}

// Without the PBS API the ACME plugins are written from the backup file in Merge
// mode too, as they always were, as the block: the plugin of this PBS the backup
// does not have is named as removed, the built-in standalone plugin never.
func TestPBSAcmePluginsRestoreBlock_MergeWithoutAPIWritesTheFile(t *testing.T) {
	const target = "/etc/proxmox-backup/acme/plugins.cfg"
	staged := "dns: p1\n\tapi cf\n\tdata " + pbsB64("x") + "\n\nstandalone: standalone\n"
	runner := &FakeCommandRunner{}
	log, warned, fakeFS := runPBSSectionRestoreStep(t, PBSRestoreBehaviorMerge, []string{"pbs_host"}, map[string]string{"etc/proxmox-backup/acme/plugins.cfg": staged}, runner, pbsAPIUnavailable, func() {
		if err := restoreFS.WriteFile(target, []byte("dns: old\n\tapi cf\n\tdata eA==\n\nstandalone: standalone\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	want := []string{
		"INFO     Restoring PBS ACME plugins...",
		"DEBUG    PBS ACME plugin restore: mode=merge staged=/stage/etc/proxmox-backup/acme/plugins.cfg",
		"DEBUG    PBS ACME plugin restore: backup acme-plugins=1 names=p1",
		"INFO       ACME plugins to restore: 1",
		"INFO       Names: p1",
		"INFO     The PBS API is not available",
		"DEBUG    PBS ACME plugin restore: fallback=file target=/etc/proxmox-backup/acme/plugins.cfg source=/stage/etc/proxmox-backup/acme/plugins.cfg",
		"DEBUG    PBS ACME plugin restore: fallback current file acme-plugins=1 names=old",
		"DEBUG    pbs staged apply file: Applied etc/proxmox-backup/acme/plugins.cfg -> /etc/proxmox-backup/acme/plugins.cfg",
		"DEBUG    PBS ACME plugin restore: fallback written",
		"DEBUG    PBS ACME plugin restore: acme-plugin=old not in backup action=drop from file",
		"INFO     ACME plugin old is configured on this PBS but not present in the backup",
		"INFO     ✓ PBS ACME plugin old: removed",
		"WARNING  ⚠ PBS ACME plugin configuration: applied from the backup file",
	}
	lines := pbsBlockLogLines(log)
	start := -1
	for i, line := range lines {
		if line == want[0] {
			start = i
		}
	}
	if start < 0 || start+len(want) > len(lines) || strings.Join(lines[start:start+len(want)], "\n") != strings.Join(want, "\n") {
		t.Fatalf("block lines\n--- want ---\n%s\n--- full log ---\n%s", strings.Join(want, "\n"), strings.Join(lines, "\n"))
	}
	if !warned {
		t.Fatal("restore must end with warnings")
	}
	if strings.Contains(log, "PBS staged apply: etc/proxmox-backup/acme/plugins.cfg") || strings.Contains(log, "failed to apply") {
		t.Fatalf("the caller must add nothing, log:\n%s", log)
	}
	if got, err := fakeFS.ReadFile(target); err != nil || string(got) != strings.TrimSpace(staged)+"\n" {
		t.Fatalf("plugins.cfg %q (err %v), want the staged file", got, err)
	}
}
