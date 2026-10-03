package block

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

const (
	testBackupID = "proxsave-" + testHostname
	// The run snapshot used below: measured backup-time 1790977976 = 2026-10-02T21:52:56Z.
	testRunTime     = int64(1790977976)
	testRunSnapshot = "host/" + testBackupID + "/2026-10-02T21:52:56Z"
)

var testRunStart = time.Unix(testRunTime-120, 0)

// measuredStderr are the client's answers measured on pve-test, rc 255 each.
var measuredStderr = map[string]string{
	"connection refused": measuredConnectRefusedStderr,
	"server not responding (connect)": "Error: client error (Connect)\n" +
		"Caused by: error connecting to https://192.0.2.1:8007/ - tcp connect error: deadline has elapsed\n",
	"server not responding (http)": "Error: http request timed out\n",
	"certificate fingerprint not confirmed": "WARNING: certificate fingerprint does not match expected fingerprint!\n" +
		"certificate validation failed - Certificate fingerprint was not confirmed.\n" +
		"Error: client error (Connect)\n",
	"authentication failed":   "Error: authentication failed\n",
	"permission check failed": "Error: permission check failed.\n",
	"namespace":               "Error: ENOENT: No such file or directory\n",
	"owner":                   "Error: backup owner check failed (proxsave-probe@pbs!other != proxsave-probe@pbs!pve)\n",
	"newer":                   "Error: backup timestamp is older than last backup.\n",
	"prune denied":            "Error: permission check failed - missing Datastore.Modify|Datastore.Prune on /datastore/proxsave-probe/proxsave-probe\n",
	"protected":               "Error: cannot remove protected snapshot\n",
	"unrecognized":            "Error: something nobody measured\n",
}

func snapshotJSON(id string, at int64, crypt string, protected bool, files ...string) string {
	if len(files) == 0 {
		files = []string{"proxsave.mpxar.didx", "proxsave.ppxar.didx"}
	}
	parts := make([]string, 0, len(files)+1)
	for _, f := range files {
		parts = append(parts, fmt.Sprintf(`{"crypt-mode":%q,"filename":%q,"size":100}`, crypt, f))
	}
	parts = append(parts, `{"crypt-mode":"sign-only","filename":"index.json.blob","size":379}`)
	return fmt.Sprintf(`{"backup-id":%q,"backup-time":%d,"backup-type":"host","files":[%s],"owner":"proxsave-probe@pbs!pve","protected":%v,"size":1243}`,
		id, at, strings.Join(parts, ","), protected)
}

func snapshotListJSON(entries ...string) string { return "[" + strings.Join(entries, ",") + "]\n" }

const measuredStatusJSON = `{"avail":8186691584,"backend-type":"filesystem","total":30080253952,"used":20560846848}` + "\n"

type pbsHarness struct {
	fake   *fakeClient
	log    *logging.Logger
	output *bytes.Buffer
	p      *PBS
	report InitReport
}

// newPBSHarness initializes the block for storage with the fake client answering a
// healthy server: client 4.2.5, the measured status, the run snapshot plus one older
// snapshot of this host and one of another host.
func newPBSHarness(t *testing.T, storageID string, priv map[string]string, retention storage.RetentionConfig, mutate func(*pbsHarness)) *pbsHarness {
	t.Helper()
	h := &pbsHarness{fake: installFakeClient(t), output: &bytes.Buffer{}}
	h.log = logging.New(types.LogLevelDebug, false)
	h.log.SetOutput(h.output)
	h.fake.respond(t, "version", 0, versionJSONOutput("4.2", "5", "4.2", "5"), "")
	h.fake.respond(t, "status", 0, measuredStatusJSON, "")
	h.fake.respond(t, "snapshot-list", 0, snapshotListJSON(
		snapshotJSON(testBackupID, testRunTime-86400, "none", false),
		snapshotJSON("proxsave-other.lan", testRunTime-3600, "none", false),
		snapshotJSON(testBackupID, testRunTime, "none", false),
	), "")
	h.fake.respond(t, "backup", 0, "", "Starting backup: [proxsave-probe]:"+testRunSnapshot+"    \n"+
		"proxsave.ppxar: had to backup 402 B of 3.067 MiB    \n")
	h.fake.respond(t, "prune", 0, `[{"backup-id":"`+testBackupID+`","backup-time":1790977976,"backup-type":"host","keep":true,"protected":false}]`+"\n", "")
	if mutate != nil {
		mutate(h)
	}
	if priv == nil {
		priv = map[string]string{storageID + ".pw": testSecret + "\n"}
	}
	h.p, h.report = InitPBS(context.Background(), PBSOptions{
		PVEConfigPath: newPVEConfigDir(t, priv),
		StorageID:     storageID,
		Hostname:      testHostname,
		IsPVEHost:     true,
		Retention:     retention,
		Logger:        h.log,
	})
	return h
}

func (h *pbsHarness) execute(t *testing.T, tree string) Result {
	t.Helper()
	h.output.Reset()
	return h.p.Execute(Input{Ctx: context.Background(), TreeDir: tree, Hostname: testHostname, StartTime: testRunStart, Logger: h.log})
}

// visible is the output without the DEBUG lines and the timestamps: "LEVEL message",
// the message without its indentation.
func (h *pbsHarness) visible() []string {
	var lines []string
	for _, line := range strings.Split(h.output.String(), "\n") {
		if i := strings.Index(line, "] "); i >= 0 {
			line = line[i+2:]
		}
		level, message, ok := strings.Cut(line, " ")
		if !ok || level == "DEBUG" {
			continue
		}
		lines = append(lines, level+" "+strings.TrimLeft(message, " "))
	}
	return lines
}

func (h *pbsHarness) argvOf(t *testing.T, sub string) []string {
	t.Helper()
	var found []string
	for _, call := range h.fake.calls(t) {
		if len(call.Args) > 0 && callKey(call.Args) == sub {
			found = append(found, strings.Join(call.Args, " "))
		}
	}
	return found
}

func callKey(args []string) string {
	switch args[0] {
	case "snapshot", "key", "namespace":
		if len(args) > 1 {
			return args[0] + "-" + args[1]
		}
	}
	return args[0]
}

func newTree(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range append([]string{"etc/hostname"}, files...) {
		path := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func simpleRetention(n int) storage.RetentionConfig {
	return storage.RetentionConfig{Policy: "simple", MaxBackups: n}
}

func wantLines(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("visible lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestInitPBSHealthyServer(t *testing.T) {
	h := newPBSHarness(t, "proxsave-probe", nil, simpleRetention(15), nil)
	if !h.report.Initialized() || !h.p.Initialized() {
		t.Fatalf("not initialized: fact=%v cause=%v", h.report.Fact, h.report.Cause)
	}
	if len(h.report.Backups) != 2 {
		t.Fatalf("own snapshots = %d, want 2 (the other host's is not counted)", len(h.report.Backups))
	}
	var subs []string
	for _, call := range h.fake.calls(t) {
		subs = append(subs, strings.Join(call.Args, " "))
	}
	want := []string{
		"version --repository " + testRepository + " --output-format json",
		"status --repository " + testRepository + " --output-format json",
		"snapshot list --ns proxsave-probe --repository " + testRepository + " --output-format json",
	}
	wantLines(t, subs, want)
	if out := h.output.String(); strings.Contains(out, testSecret) {
		t.Fatal("the password reached the log")
	}
}

func TestInitPBSKeyChecks(t *testing.T) {
	for _, tc := range []struct {
		name, keyShow string
		want          FactKind
	}{
		{"passphrase", `{"kdf":"scrypt","fingerprint":"` + testEncKeyFP + `"}`, FactKeyNeedsPassphrase},
		{"mismatch", `{"kdf":"none","fingerprint":"87:18:b3:4c:11:80:0d:32"}`, FactKeyMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPBSHarness(t, "proxsave-probe-enc", map[string]string{
				"proxsave-probe-enc.pw":  testSecret + "\n",
				"proxsave-probe-enc.enc": testEncKeyBody,
			}, simpleRetention(15), func(h *pbsHarness) {
				h.fake.respond(t, "key-show", 0, tc.keyShow, "")
			})
			if h.report.Fact == nil || h.report.Fact.Kind != tc.want || h.report.Contacted {
				t.Fatalf("fact = %+v contacted=%v, want %s before any server call", h.report.Fact, h.report.Contacted, tc.want)
			}
			if calls := h.fake.calls(t); len(calls) != 1 || callKey(calls[0].Args) != "key-show" {
				t.Fatalf("calls = %+v, want key show only", calls)
			}
		})
	}
}

func TestInitPBSKeyAcceptedThenServer(t *testing.T) {
	h := newPBSHarness(t, "proxsave-probe-enc", map[string]string{
		"proxsave-probe-enc.pw":  testSecret + "\n",
		"proxsave-probe-enc.enc": testEncKeyBody,
	}, simpleRetention(15), func(h *pbsHarness) {
		h.fake.respond(t, "key-show", 0, string(keyShowJSON("none", testEncKeyFP)), "")
	})
	if !h.report.Initialized() {
		t.Fatalf("not initialized: fact=%v cause=%v", h.report.Fact, h.report.Cause)
	}
}

func TestInitPBSClientNotFound(t *testing.T) {
	orig := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("executable file not found in $PATH") }
	t.Cleanup(func() { lookPath = orig })
	h := newPBSHarness(t, "proxsave-probe", nil, simpleRetention(15), nil)
	if h.report.Fact == nil || h.report.Fact.Line() != "proxmox-backup-client: not found" {
		t.Fatalf("fact = %+v", h.report.Fact)
	}
	if len(h.fake.calls(t)) != 0 {
		t.Fatal("the client was called")
	}
}

func TestInitPBSServerCauses(t *testing.T) {
	for _, tc := range []struct {
		failing, stderr, want string
	}{
		{"version", measuredStderr["connection refused"], "Connection refused"},
		{"version", measuredStderr["server not responding (connect)"], "Server not responding"},
		{"version", measuredStderr["server not responding (http)"], "Server not responding"},
		{"version", measuredStderr["certificate fingerprint not confirmed"], "Certificate fingerprint not confirmed"},
		{"status", measuredStderr["authentication failed"], "Authentication failed"},
		{"status", measuredStderr["permission check failed"], "Permission check failed"},
		{"snapshot-list", measuredStderr["namespace"], "Namespace proxsave-probe not found"},
		{"status", measuredStderr["unrecognized"], "Unrecognized PBS client error"},
	} {
		t.Run(tc.failing+" "+tc.want, func(t *testing.T) {
			h := newPBSHarness(t, "proxsave-probe", nil, simpleRetention(15), func(h *pbsHarness) {
				h.fake.respond(t, tc.failing, 255, "", tc.stderr)
			})
			if h.report.Initialized() || h.report.Cause == nil || !h.report.Contacted {
				t.Fatalf("report = %+v", h.report)
			}
			if got := h.report.Cause.Capitalized(); got != tc.want {
				t.Fatalf("cause = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExecuteUploadsPlainWithMeasuredFlags(t *testing.T) {
	h := newPBSHarness(t, "proxsave-probe", nil, simpleRetention(15), nil)
	tree := newTree(t)
	result := h.execute(t, tree)
	if result.Status != StatusOK || result.Snapshot != testRunSnapshot || result.Backups != 2 {
		t.Fatalf("result = %+v", result)
	}
	wantLines(t, h.argvOf(t, "backup"), []string{
		"backup proxsave.pxar:" + tree + " --repository " + testRepository + " --ns proxsave-probe --backup-type host --backup-id " +
			testBackupID + " --change-detection-mode metadata --chunk-size 64 --crypt-mode none",
	})
	wantLines(t, h.argvOf(t, "prune"), []string{
		"prune host/" + testBackupID + " --ns proxsave-probe --repository " + testRepository + " --keep-last 15 --output-format json",
	})
	wantLines(t, h.visible(), []string{
		"INFO Retention policy: simple (pbs=15)",
		"INFO Storing backup...",
		"INFO Snapshot: " + testRunSnapshot,
		"INFO ✓ PBS Storage: backup saved",
		"INFO Applying retention policy...",
		"INFO ✓ Nothing to delete",
		"INFO PBS Storage statistics:",
		"INFO Total backups: 2",
	})
	if h.p.Snapshot() != testRunSnapshot {
		t.Fatalf("Snapshot() = %q", h.p.Snapshot())
	}
}

func TestExecuteWithKeyAndOldClient(t *testing.T) {
	h := newPBSHarness(t, "proxsave-r2-enc", map[string]string{
		"proxsave-r2-enc.pw":         testSecret + "\n",
		"proxsave-r2-enc.enc":        testEncKeyBody,
		"proxsave-r2-enc.master.pem": "-----BEGIN PUBLIC KEY-----\n",
	}, simpleRetention(15), func(h *pbsHarness) {
		h.fake.respond(t, "key-show", 0, string(keyShowJSON("none", "d6:2a:96:b8:86:6a:65:3b:07:e4:19:c0:5f:a2:8d:31:72:4e:b6:09:d1:5c:e8:33:9a:0f:64:c7:28:b5:31:bc")), "")
		h.fake.respond(t, "version", 0, versionJSONOutput("3.2", "4", "3.4", "9"), "")
		h.fake.respond(t, "snapshot-list", 0, snapshotListJSON(snapshotJSON(testBackupID, testRunTime, "encrypt", false, "proxsave.pxar.didx", "catalog.pcat1.didx")), "")
		h.fake.respond(t, "backup", 0, "", "Starting backup: [r2-enc]:"+testRunSnapshot+"\n")
	})
	if !h.report.Initialized() {
		t.Fatalf("not initialized: %+v", h.report)
	}
	tree := newTree(t)
	if result := h.execute(t, tree); result.Status != StatusOK {
		t.Fatalf("result = %+v\n%s", result, h.output.String())
	}
	privDir := filepath.Dir(h.p.target.Keyfile)
	wantLines(t, h.argvOf(t, "backup"), []string{
		"backup proxsave.pxar:" + tree + " --repository proxsave-r2@pbs!pve@127.0.0.1:proxsave-r2 --ns r2-enc --backup-type host --backup-id " +
			testBackupID + " --chunk-size 64 --keyfile " + filepath.Join(privDir, "proxsave-r2-enc.enc") +
			" --master-pubkey-file " + filepath.Join(privDir, "proxsave-r2-enc.master.pem"),
	})
}

func TestExecuteUploadRefused(t *testing.T) {
	for _, tc := range []struct {
		stderr string
		facts  []string
	}{
		{measuredStderr["owner"], []string{"INFO PBS server: group owned by another user", "INFO Owner: proxsave-probe@pbs!pve", "INFO User: proxsave-probe@pbs!other"}},
		{measuredStderr["newer"], []string{"INFO PBS server: a newer snapshot already exists"}},
		{"Error: namespace not found\n", []string{"INFO PBS server: namespace proxsave-probe not found"}},
	} {
		h := newPBSHarness(t, "proxsave-probe", nil, simpleRetention(15), nil)
		h.fake.respond(t, "backup", 255, "", tc.stderr)
		result := h.execute(t, newTree(t))
		if result.Status != StatusError || result.Snapshot != "" || h.p.Snapshot() != "" {
			t.Fatalf("result = %+v", result)
		}
		want := append([]string{"INFO Retention policy: simple (pbs=15)", "INFO Storing backup..."}, tc.facts...)
		want = append(want, "WARNING ✗ PBS Storage: backup not saved", "SKIP Retention: backup not saved",
			"INFO PBS Storage statistics:", "INFO Total backups: 2")
		wantLines(t, h.visible(), want)
		if len(h.argvOf(t, "prune")) != 0 {
			t.Fatal("retention ran after a failed upload")
		}
	}
}

func TestExecuteUploadRefusedServerGoneHasNoStatistics(t *testing.T) {
	h := newPBSHarness(t, "proxsave-probe", nil, simpleRetention(15), nil)
	h.fake.respond(t, "backup", 255, "", measuredStderr["connection refused"])
	h.fake.respond(t, "snapshot-list", 255, "", measuredStderr["connection refused"])
	h.execute(t, newTree(t))
	wantLines(t, h.visible(), []string{
		"INFO Retention policy: simple (pbs=15)", "INFO Storing backup...",
		"INFO PBS server: connection refused",
		"WARNING ✗ PBS Storage: backup not saved", "SKIP Retention: backup not saved",
	})
}

func TestExecuteCheckAfterUpload(t *testing.T) {
	h := newPBSHarness(t, "proxsave-probe", nil, simpleRetention(15), nil)
	h.fake.respond(t, "snapshot-list", 0, snapshotListJSON(snapshotJSON(testBackupID, testRunTime-86400, "none", false)), "")
	if result := h.execute(t, newTree(t)); result.Status != StatusError {
		t.Fatalf("missing snapshot accepted: %+v", result)
	}
	if got := h.visible(); got[2] != "INFO PBS server: snapshot not found after upload" {
		t.Fatalf("visible = %q", got)
	}

	h = newPBSHarness(t, "proxsave-probe", nil, simpleRetention(15), nil)
	h.fake.respond(t, "snapshot-list", 0, snapshotListJSON(snapshotJSON(testBackupID, testRunTime, "encrypt", false)), "")
	if result := h.execute(t, newTree(t)); result.Status != StatusError {
		t.Fatalf("crypt-mode mismatch accepted: %+v", result)
	}
	wantLines(t, h.visible(), []string{
		"INFO Retention policy: simple (pbs=15)", "INFO Storing backup...",
		"INFO Snapshot: " + testRunSnapshot,
		"INFO Encryption expected: none", "INFO Encryption found: encrypt",
		"WARNING ✗ PBS Storage: backup not usable", "SKIP Retention: backup not usable",
		"INFO PBS Storage statistics:", "INFO Total backups: 1",
	})
	if len(h.argvOf(t, "prune")) != 0 {
		t.Fatal("retention ran on an unusable snapshot")
	}
}

func TestExecuteRetention(t *testing.T) {
	h := newPBSHarness(t, "proxsave-probe", nil, simpleRetention(1), nil)
	h.fake.respond(t, "prune", 255, "", measuredStderr["prune denied"])
	if result := h.execute(t, newTree(t)); result.Status != StatusWarning {
		t.Fatalf("result = %+v", result)
	}
	wantLines(t, h.visible()[4:], []string{
		"INFO Applying retention policy...",
		"INFO PBS server: permission check failed",
		"INFO Missing: Datastore.Modify|Datastore.Prune",
		"INFO On: /datastore/proxsave-probe/proxsave-probe",
		"WARNING ⚠ Retention not applied",
		"INFO PBS Storage statistics:", "INFO Total backups: 2",
	})

	h = newPBSHarness(t, "proxsave-probe", nil, simpleRetention(1), nil)
	h.fake.respond(t, "prune", 0, `[{"backup-time":1,"keep":false},{"backup-time":2,"keep":false},{"backup-time":3,"keep":true}]`, "")
	if result := h.execute(t, newTree(t)); result.Status != StatusOK || result.Deleted != 2 {
		t.Fatalf("result = %+v", result)
	}
	if got := h.visible()[5]; got != "INFO ✓ Backups deleted: 2" {
		t.Fatalf("outcome = %q", got)
	}

	h = newPBSHarness(t, "proxsave-probe", nil, simpleRetention(0), nil)
	h.execute(t, newTree(t))
	if len(h.argvOf(t, "prune")) != 0 {
		t.Fatal("MAX_PBS_TARGET_BACKUPS=0 pruned")
	}
	if got := h.visible()[0]; got != "INFO Retention policy: simple (disabled)" {
		t.Fatalf("header = %q", got)
	}
}

func TestExecuteGFSRetentionForgetsWhatTheEngineDrops(t *testing.T) {
	gfs := storage.RetentionConfig{Policy: "gfs", Daily: 1}
	old := testRunTime - 30*86400
	older := testRunTime - 60*86400
	h := newPBSHarness(t, "proxsave-probe", nil, gfs, func(h *pbsHarness) {
		h.fake.respond(t, "snapshot-list", 0, snapshotListJSON(
			snapshotJSON(testBackupID, testRunTime, "none", false),
			snapshotJSON(testBackupID, old, "none", true),
			snapshotJSON(testBackupID, older, "none", false),
		), "")
	})
	result := h.execute(t, newTree(t))
	if result.Status != StatusWarning || result.Deleted != 1 {
		t.Fatalf("result = %+v\n%s", result, h.output.String())
	}
	olderName := Snapshot{BackupType: "host", BackupID: testBackupID, BackupTime: older}.Name()
	oldName := Snapshot{BackupType: "host", BackupID: testBackupID, BackupTime: old}.Name()
	wantLines(t, h.argvOf(t, "snapshot-forget"), []string{
		"snapshot forget " + olderName + " --ns proxsave-probe --repository " + testRepository,
	})
	if len(h.argvOf(t, "prune")) != 0 {
		t.Fatal("GFS ran prune")
	}
	wantLines(t, h.visible()[0:1], []string{"INFO Retention policy: GFS (daily=1, weekly=0, monthly=0, yearly=0)"})
	wantLines(t, h.visible()[4:7], []string{
		"INFO Applying GFS retention policy...",
		"INFO Kept, protected: " + oldName,
		"WARNING ⚠ Backups deleted: 1, 1 protected kept",
	})
}

func TestExecuteNotInitializedAndDryRun(t *testing.T) {
	h := newPBSHarness(t, "proxsave-probe", nil, simpleRetention(15), func(h *pbsHarness) {
		h.fake.respond(t, "version", 255, "", measuredStderr["connection refused"])
	})
	before := len(h.fake.calls(t))
	result := h.execute(t, newTree(t))
	if result.Status != StatusError || len(h.fake.calls(t)) != before {
		t.Fatalf("result = %+v, calls %d -> %d", result, before, len(h.fake.calls(t)))
	}
	wantLines(t, h.visible(), []string{
		"INFO Retention policy: simple (pbs=15)", "INFO Storing backup...",
		"INFO PBS storage: not initialized at startup",
		"WARNING ✗ PBS Storage: backup not saved", "SKIP Retention: backup not saved",
	})

	h = newPBSHarness(t, "proxsave-probe", nil, simpleRetention(15), nil)
	before = len(h.fake.calls(t))
	h.output.Reset()
	result = h.p.Execute(Input{Ctx: context.Background(), TreeDir: newTree(t), StartTime: testRunStart, DryRun: true, Logger: h.log})
	if result.Status != StatusSkipped || len(h.fake.calls(t)) != before || h.output.Len() != 0 {
		t.Fatalf("dry run: result=%+v calls %d -> %d output=%q", result, before, len(h.fake.calls(t)), h.output.String())
	}
}

func TestExecuteFactsUnderStoring(t *testing.T) {
	h := newPBSHarness(t, "proxsave-probe", nil, simpleRetention(15), nil)
	h.p.opts.EncryptArchive = true
	result := h.execute(t, newTree(t, ".pxarexclude", "etc/ssh/.pxarexclude"))
	if result.Status != StatusWarning {
		t.Fatalf("result = %+v", result)
	}
	wantLines(t, h.visible()[:7], []string{
		"INFO Retention policy: simple (pbs=15)", "INFO Storing backup...",
		"INFO Encryption: none, storage proxsave-probe has no key",
		"INFO Exclusion rules: /.pxarexclude",
		"INFO Exclusion rules: /etc/ssh/.pxarexclude",
		"INFO Snapshot: " + testRunSnapshot,
		"WARNING ⚠ PBS Storage: backup saved, exclusion rules applied",
	})
}

func TestUploadLogArgs(t *testing.T) {
	h := newPBSHarness(t, "proxsave-probe", nil, simpleRetention(15), nil)
	h.execute(t, newTree(t))
	if cause := h.p.UploadLog(context.Background(), "/opt/proxsave/log/run.log"); cause != nil {
		t.Fatalf("cause = %+v", cause)
	}
	wantLines(t, h.argvOf(t, "snapshot-upload-log"), []string{
		"snapshot upload-log " + testRunSnapshot + " /opt/proxsave/log/run.log --ns proxsave-probe --repository " + testRepository + " --crypt-mode none",
	})
	h.fake.respond(t, "snapshot-upload-log", 255, "", "Error: backup already contains a log.\n")
	if cause := h.p.UploadLog(context.Background(), "/opt/proxsave/log/run.log"); cause == nil || cause.Text != "unrecognized PBS client error" {
		t.Fatalf("cause = %+v", cause)
	}
}

func TestAvailableGB(t *testing.T) {
	h := newPBSHarness(t, "proxsave-probe", nil, simpleRetention(15), nil)
	gb, cause := h.p.AvailableGB(context.Background())
	if cause != nil || gb < 7.62 || gb > 7.63 {
		t.Fatalf("gb=%v cause=%v", gb, cause)
	}
	h.fake.respond(t, "status", 255, "", measuredStderr["connection refused"])
	if _, cause := h.p.AvailableGB(context.Background()); cause == nil || cause.Facts()[0] != "  PBS server: connection refused" {
		t.Fatalf("cause = %+v", cause)
	}
}

func TestRootNamespaceHasNoNsFlag(t *testing.T) {
	cfg := "pbs: root-ns\n\tdatastore ds\n\tserver 10.0.0.1\n\tusername u@pbs!t\n"
	dir := newPVEConfigDir(t, map[string]string{"root-ns.pw": testSecret})
	if err := os.WriteFile(filepath.Join(dir, "storage.cfg"), []byte(cfg), 0o640); err != nil {
		t.Fatal(err)
	}
	fake := installFakeClient(t)
	fake.respond(t, "version", 0, versionJSONOutput("4.2", "5", "4.2", "5"), "")
	fake.respond(t, "status", 0, measuredStatusJSON, "")
	fake.respond(t, "snapshot-list", 0, "[]", "")
	_, report := InitPBS(context.Background(), PBSOptions{PVEConfigPath: dir, StorageID: "root-ns", Hostname: testHostname, IsPVEHost: true})
	if !report.Initialized() {
		t.Fatalf("report = %+v", report)
	}
	for _, call := range fake.calls(t) {
		for _, arg := range call.Args {
			if arg == "--ns" {
				t.Fatalf("root namespace passed --ns: %q", call.Args)
			}
		}
	}
}
