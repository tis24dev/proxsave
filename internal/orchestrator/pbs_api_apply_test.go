package orchestrator

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func setupPBSAPIApplyTestDeps(t *testing.T) (stageRoot string, fs *FakeFS, runner *fakeCommandRunner) {
	t.Helper()

	origCmd := restoreCmd
	origFS := restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})

	fs = NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fs.Root) })
	restoreFS = fs

	runner = &fakeCommandRunner{}
	restoreCmd = runner

	return "/stage", fs, runner
}

func writeStageFile(t *testing.T, fs *FakeFS, stageRoot, relPath, content string, perm os.FileMode) {
	t.Helper()
	if err := fs.WriteFile(stageRoot+"/"+relPath, []byte(content), perm); err != nil {
		t.Fatalf("write staged %s: %v", relPath, err)
	}
}

func TestNormalizeProxmoxCfgKey(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "  Foo_Bar  ", want: "foo-bar"},
		{in: "dns1", want: "dns1"},
		{in: "", want: ""},
		{in: "   ", want: ""},
	}
	for _, tt := range tests {
		if got := normalizeProxmoxCfgKey(tt.in); got != tt.want {
			t.Fatalf("normalizeProxmoxCfgKey(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuildProxmoxManagerFlags_SkipsAndNormalizes(t *testing.T) {
	entries := []proxmoxNotificationEntry{
		{Key: "HOST", Value: " pbs.example "},
		{Key: "digest", Value: "abc"},
		{Key: "name", Value: "ignored"},
		{Key: "foo_bar", Value: "baz"},
		{Key: "skip_me", Value: "nope"},
		{Key: "", Value: "x"},
	}
	got := buildProxmoxManagerFlags(entries, "SKIP_ME")
	want := []string{"--host", "pbs.example", "--foo-bar", "baz"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flags=%v want %v", got, want)
	}
}

func TestBuildProxmoxManagerFlags_EmptyReturnsNil(t *testing.T) {
	if got := buildProxmoxManagerFlags(nil); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
	if got := buildProxmoxManagerFlags([]proxmoxNotificationEntry{}); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestPopEntryValue_FirstMatchOnly(t *testing.T) {
	entries := []proxmoxNotificationEntry{
		{Key: "Foo_Bar", Value: " first "},
		{Key: "foo-bar", Value: "second"},
		{Key: "x", Value: "y"},
	}
	value, remaining, ok := popEntryValue(entries, "foo-bar")
	if !ok || value != "first" {
		t.Fatalf("ok=%v value=%q want ok=true value=first", ok, value)
	}
	wantRemaining := []proxmoxNotificationEntry{
		{Key: "foo-bar", Value: "second"},
		{Key: "x", Value: "y"},
	}
	if !reflect.DeepEqual(remaining, wantRemaining) {
		t.Fatalf("remaining=%v want %v", remaining, wantRemaining)
	}
}

func TestPopEntryValue_NoKeysOrEntries(t *testing.T) {
	value, remaining, ok := popEntryValue(nil, "k")
	if ok || value != "" || remaining != nil {
		t.Fatalf("ok=%v value=%q remaining=%v want ok=false value=\"\" remaining=nil", ok, value, remaining)
	}

	entries := []proxmoxNotificationEntry{{Key: "k", Value: "v"}}
	value, remaining, ok = popEntryValue(entries)
	if ok || value != "" || !reflect.DeepEqual(remaining, entries) {
		t.Fatalf("ok=%v value=%q remaining=%v want ok=false value=\"\" remaining=%v", ok, value, remaining, entries)
	}
}

func TestRunPBSManagerRedacted_RedactsFlagsAndIndexes(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	runner := &fakeCommandRunner{
		outputs: map[string][]byte{},
		errs:    map[string]error{},
	}
	restoreCmd = runner

	args := []string{"remote", "create", "r1", "--password", "secret123", "--token", "token-value-123"}
	key := "proxmox-backup-manager " + strings.Join(args, " ")
	runner.outputs[key] = []byte("boom")
	runner.errs[key] = errors.New("exit 1")

	out, err := runPBSManagerRedacted(context.Background(), args, []string{"--password"}, []int{6})
	if string(out) != "boom" {
		t.Fatalf("out=%q want %q", string(out), "boom")
	}
	if err == nil {
		t.Fatalf("expected error")
	}
	msg := err.Error()
	if strings.Contains(msg, "secret123") || strings.Contains(msg, "token-value-123") {
		t.Fatalf("expected secrets to be redacted, got: %s", msg)
	}
	if !strings.Contains(msg, "<redacted>") {
		t.Fatalf("expected <redacted> in error, got: %s", msg)
	}
}

func TestUnwrapPBSJSONData(t *testing.T) {
	if got := unwrapPBSJSONData([]byte("   ")); got != nil {
		t.Fatalf("expected nil for empty input, got %q", string(got))
	}

	got := string(unwrapPBSJSONData([]byte("  not-json ")))
	if got != "not-json" {
		t.Fatalf("got %q want %q", got, "not-json")
	}

	got = string(unwrapPBSJSONData([]byte(`{"data":[{"id":"a"}]}`)))
	if got != `[{"id":"a"}]` {
		t.Fatalf("got %q want %q", got, `[{"id":"a"}]`)
	}

	got = string(unwrapPBSJSONData([]byte(`{"foo":"bar"}`)))
	if got != `{"foo":"bar"}` {
		t.Fatalf("got %q want %q", got, `{"foo":"bar"}`)
	}
}

func TestParsePBSListIDs(t *testing.T) {
	if _, err := parsePBSListIDs([]byte(`[{"id":"a"}]`)); err == nil {
		t.Fatalf("expected error for missing candidate keys")
	}

	ids, err := parsePBSListIDs([]byte("   "), "id")
	if err != nil || ids != nil {
		t.Fatalf("ids=%v err=%v want nil,nil", ids, err)
	}

	ids, err = parsePBSListIDs([]byte(`{"data":[{"id":"b"},{"id":"a"},{"id":"a"}]}`), "id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("ids=%v want %v", ids, []string{"a", "b"})
	}

	_, err = parsePBSListIDs([]byte(`{"data":[{"id":123,"name":""}]}`), "id", "name")
	if err == nil || !strings.Contains(err.Error(), "failed to parse PBS list row 0") {
		t.Fatalf("expected row parse error, got: %v", err)
	}

	ids, err = parsePBSListIDs([]byte(`[{"name":"x"}]`), "id", "name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(ids, []string{"x"}) {
		t.Fatalf("ids=%v want %v", ids, []string{"x"})
	}

	ids, err = parsePBSListIDs([]byte(`{"data":[{"id":"a"}]}`), " id ", "   ", "name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(ids, []string{"a"}) {
		t.Fatalf("ids=%v want %v", ids, []string{"a"})
	}

	if _, err := parsePBSListIDs([]byte(`{"data":{}}`), "id"); err == nil {
		t.Fatalf("expected unmarshal error for non-array data")
	}
}

func TestPBSAPIApply_ReadStageFileOptionalErrors(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	type applyFunc struct {
		name    string
		relPath string
		apply   func(context.Context, *logging.Logger, string, bool) error
	}

	for _, tt := range []applyFunc{
		{name: "remote", relPath: "etc/proxmox-backup/remote.cfg", apply: applyPBSRemoteCfgViaAPI},
		{name: "s3", relPath: "etc/proxmox-backup/s3.cfg", apply: applyPBSS3CfgViaAPI},
		{name: "datastore", relPath: "etc/proxmox-backup/datastore.cfg", apply: applyPBSDatastoreCfgViaAPI},
		{name: "sync", relPath: "etc/proxmox-backup/sync.cfg", apply: applyPBSSyncCfgViaAPI},
		{name: "verification", relPath: "etc/proxmox-backup/verification.cfg", apply: applyPBSVerificationCfgViaAPI},
		{name: "prune", relPath: "etc/proxmox-backup/prune.cfg", apply: applyPBSPruneCfgViaAPI},
		{name: "traffic-control", relPath: "etc/proxmox-backup/traffic-control.cfg", apply: applyPBSTrafficControlCfgViaAPI},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			stageRoot, fs, _ := setupPBSAPIApplyTestDeps(t)
			if err := fs.AddDir(stageRoot + "/" + tt.relPath); err != nil {
				t.Fatalf("create staged dir: %v", err)
			}
			err := tt.apply(context.Background(), logger, stageRoot, false)
			if err == nil || !strings.Contains(err.Error(), "read staged") {
				t.Fatalf("expected read staged error, got: %v", err)
			}
		})
	}

	t.Run("node", func(t *testing.T) {
		stageRoot, fs, _ := setupPBSAPIApplyTestDeps(t)
		if err := fs.AddDir(stageRoot + "/etc/proxmox-backup/node.cfg"); err != nil {
			t.Fatalf("create staged dir: %v", err)
		}
		err := applyPBSNodeCfgViaAPI(context.Background(), stageRoot)
		if err == nil || !strings.Contains(err.Error(), "read staged") {
			t.Fatalf("expected read staged error, got: %v", err)
		}
	})
}

func TestPBSAPIApply_NoFileBranches(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	type applyFunc struct {
		name  string
		apply func(context.Context, *logging.Logger, string, bool) error
	}

	for _, tt := range []applyFunc{
		{name: "datastore", apply: applyPBSDatastoreCfgViaAPI},
		{name: "sync", apply: applyPBSSyncCfgViaAPI},
		{name: "verification", apply: applyPBSVerificationCfgViaAPI},
		{name: "prune", apply: applyPBSPruneCfgViaAPI},
		{name: "traffic-control", apply: applyPBSTrafficControlCfgViaAPI},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			stageRoot, _, runner := setupPBSAPIApplyTestDeps(t)
			if err := tt.apply(context.Background(), logger, stageRoot, true); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("expected no calls, got %v", runner.calls)
			}
		})
	}
}

func TestPBSAPIApply_StrictListCommandErrors(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	tests := []struct {
		name     string
		relPath  string
		content  string
		listCmd  string
		apply    func(context.Context, *logging.Logger, string, bool) error
	}{
		{
			name:    "remote",
			relPath: "etc/proxmox-backup/remote.cfg",
			content: "remote: r1\n    host pbs.example\n",
			listCmd: "proxmox-backup-manager remote list --output-format=json",
			apply:   applyPBSRemoteCfgViaAPI,
		},
		{
			name:    "s3",
			relPath: "etc/proxmox-backup/s3.cfg",
			content: "s3: e1\n    endpoint https://s3.example\n",
			listCmd: "proxmox-backup-manager s3 endpoint list --output-format=json",
			apply:   applyPBSS3CfgViaAPI,
		},
		{
			name:    "sync",
			relPath: "etc/proxmox-backup/sync.cfg",
			content: "sync-job: job1\n    remote r1\n    store ds1\n",
			listCmd: "proxmox-backup-manager sync-job list --output-format=json",
			apply:   applyPBSSyncCfgViaAPI,
		},
		{
			name:    "verification",
			relPath: "etc/proxmox-backup/verification.cfg",
			content: "verify-job: v1\n    store ds1\n",
			listCmd: "proxmox-backup-manager verify-job list --output-format=json",
			apply:   applyPBSVerificationCfgViaAPI,
		},
		{
			name:    "prune",
			relPath: "etc/proxmox-backup/prune.cfg",
			content: "prune-job: p1\n    store ds1\n",
			listCmd: "proxmox-backup-manager prune-job list --output-format=json",
			apply:   applyPBSPruneCfgViaAPI,
		},
		{
			name:    "traffic-control",
			relPath: "etc/proxmox-backup/traffic-control.cfg",
			content: "traffic-control: tc1\n    rate-in 1000\n",
			listCmd: "proxmox-backup-manager traffic-control list --output-format=json",
			apply:   applyPBSTrafficControlCfgViaAPI,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
			writeStageFile(t, fs, stageRoot, tt.relPath, tt.content, 0o640)
			runner.errs = map[string]error{tt.listCmd: errors.New("boom")}
			if err := tt.apply(context.Background(), logger, stageRoot, true); err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

func TestPBSAPIApply_StrictListParseErrors(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	tests := []struct {
		name          string
		relPath       string
		content       string
		listCmd       string
		wantErrPrefix string
		apply         func(context.Context, *logging.Logger, string, bool) error
	}{
		{
			name:          "remote",
			relPath:       "etc/proxmox-backup/remote.cfg",
			content:       "remote: r1\n    host pbs.example\n",
			listCmd:       "proxmox-backup-manager remote list --output-format=json",
			wantErrPrefix: "parse remote list:",
			apply:         applyPBSRemoteCfgViaAPI,
		},
		{
			name:          "s3",
			relPath:       "etc/proxmox-backup/s3.cfg",
			content:       "s3: e1\n    endpoint https://s3.example\n",
			listCmd:       "proxmox-backup-manager s3 endpoint list --output-format=json",
			wantErrPrefix: "parse s3 endpoint list:",
			apply:         applyPBSS3CfgViaAPI,
		},
		{
			name:          "sync",
			relPath:       "etc/proxmox-backup/sync.cfg",
			content:       "sync-job: job1\n    remote r1\n    store ds1\n",
			listCmd:       "proxmox-backup-manager sync-job list --output-format=json",
			wantErrPrefix: "parse sync-job list:",
			apply:         applyPBSSyncCfgViaAPI,
		},
		{
			name:          "verification",
			relPath:       "etc/proxmox-backup/verification.cfg",
			content:       "verify-job: v1\n    store ds1\n",
			listCmd:       "proxmox-backup-manager verify-job list --output-format=json",
			wantErrPrefix: "parse verify-job list:",
			apply:         applyPBSVerificationCfgViaAPI,
		},
		{
			name:          "prune",
			relPath:       "etc/proxmox-backup/prune.cfg",
			content:       "prune-job: p1\n    store ds1\n",
			listCmd:       "proxmox-backup-manager prune-job list --output-format=json",
			wantErrPrefix: "parse prune-job list:",
			apply:         applyPBSPruneCfgViaAPI,
		},
		{
			name:          "traffic-control",
			relPath:       "etc/proxmox-backup/traffic-control.cfg",
			content:       "traffic-control: tc1\n    rate-in 1000\n",
			listCmd:       "proxmox-backup-manager traffic-control list --output-format=json",
			wantErrPrefix: "parse traffic-control list:",
			apply:         applyPBSTrafficControlCfgViaAPI,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
			writeStageFile(t, fs, stageRoot, tt.relPath, tt.content, 0o640)
			runner.outputs = map[string][]byte{tt.listCmd: []byte("not-json")}
			if err := tt.apply(context.Background(), logger, stageRoot, true); err == nil || !strings.Contains(err.Error(), tt.wantErrPrefix) {
				t.Fatalf("expected %q error, got: %v", tt.wantErrPrefix, err)
			}
		})
	}
}

func TestPBSAPIApply_CreateUpdateBothFailErrors(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	tests := []struct {
		name    string
		relPath string
		content string
		create  string
		update  string
		apply   func(context.Context, *logging.Logger, string, bool) error
	}{
		{
			name:    "sync",
			relPath: "etc/proxmox-backup/sync.cfg",
			content: "sync-job: job1\n    remote r1\n    store ds1\n",
			create:  "proxmox-backup-manager sync-job create job1 --remote r1 --store ds1",
			update:  "proxmox-backup-manager sync-job update job1 --remote r1 --store ds1",
			apply:   applyPBSSyncCfgViaAPI,
		},
		{
			name:    "verification",
			relPath: "etc/proxmox-backup/verification.cfg",
			content: "verify-job: v1\n    store ds1\n",
			create:  "proxmox-backup-manager verify-job create v1 --store ds1",
			update:  "proxmox-backup-manager verify-job update v1 --store ds1",
			apply:   applyPBSVerificationCfgViaAPI,
		},
		{
			name:    "prune",
			relPath: "etc/proxmox-backup/prune.cfg",
			content: "prune-job: p1\n    store ds1\n    keep-last 3\n",
			create:  "proxmox-backup-manager prune-job create p1 --store ds1 --keep-last 3",
			update:  "proxmox-backup-manager prune-job update p1 --store ds1 --keep-last 3",
			apply:   applyPBSPruneCfgViaAPI,
		},
		{
			name:    "traffic-control",
			relPath: "etc/proxmox-backup/traffic-control.cfg",
			content: "traffic-control: tc1\n    rate-in 1000\n",
			create:  "proxmox-backup-manager traffic-control create tc1 --rate-in 1000",
			update:  "proxmox-backup-manager traffic-control update tc1 --rate-in 1000",
			apply:   applyPBSTrafficControlCfgViaAPI,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
			writeStageFile(t, fs, stageRoot, tt.relPath, tt.content, 0o640)
			runner.errs = map[string]error{
				tt.create: errors.New("create failed"),
				tt.update: errors.New("update failed"),
			}
			err := tt.apply(context.Background(), logger, stageRoot, false)
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), "create failed") || !strings.Contains(err.Error(), "update failed") {
				t.Fatalf("expected create/update errors, got: %s", err.Error())
			}
		})
	}
}

func TestEnsurePBSServicesForAPI(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	t.Run("non-system-fs", func(t *testing.T) {
		origFS := restoreFS
		t.Cleanup(func() { restoreFS = origFS })
		restoreFS = NewFakeFS()
		if err := ensurePBSServicesForAPI(context.Background(), logger); err == nil || !strings.Contains(err.Error(), "non-system filesystem") {
			t.Fatalf("expected non-system filesystem error, got: %v", err)
		}
	})

	t.Run("non-root", func(t *testing.T) {
		origFS := restoreFS
		origCmd := restoreCmd
		origGeteuid := pbsAPIApplyGeteuid
		t.Cleanup(func() {
			restoreFS = origFS
			restoreCmd = origCmd
			pbsAPIApplyGeteuid = origGeteuid
		})
		restoreFS = osFS{}
		restoreCmd = &fakeCommandRunner{}
		pbsAPIApplyGeteuid = func() int { return 1000 }

		if err := ensurePBSServicesForAPI(context.Background(), logger); err == nil || !strings.Contains(err.Error(), "requires root privileges") {
			t.Fatalf("expected root privileges error, got: %v", err)
		}
	})

	t.Run("pbs-manager-missing", func(t *testing.T) {
		origFS := restoreFS
		origCmd := restoreCmd
		origGeteuid := pbsAPIApplyGeteuid
		t.Cleanup(func() {
			restoreFS = origFS
			restoreCmd = origCmd
			pbsAPIApplyGeteuid = origGeteuid
		})
		restoreFS = osFS{}

		runner := &fakeCommandRunner{
			errs: map[string]error{
				"proxmox-backup-manager version": errors.New("not found"),
			},
		}
		restoreCmd = runner
		pbsAPIApplyGeteuid = func() int { return 0 }

		if err := ensurePBSServicesForAPI(context.Background(), logger); err == nil || !strings.Contains(err.Error(), "proxmox-backup-manager not available") {
			t.Fatalf("expected pbs manager missing error, got: %v", err)
		}
	})

	t.Run("start-services-fails", func(t *testing.T) {
		origFS := restoreFS
		origCmd := restoreCmd
		origGeteuid := pbsAPIApplyGeteuid
		t.Cleanup(func() {
			restoreFS = origFS
			restoreCmd = origCmd
			pbsAPIApplyGeteuid = origGeteuid
		})
		restoreFS = osFS{}

		runner := &fakeCommandRunner{
			errs: map[string]error{
				"which systemctl": errors.New("no systemctl"),
			},
		}
		restoreCmd = runner
		pbsAPIApplyGeteuid = func() int { return 0 }

		if err := ensurePBSServicesForAPI(context.Background(), logger); err == nil || !strings.Contains(err.Error(), "systemctl not available") {
			t.Fatalf("expected systemctl not available error, got: %v", err)
		}
	})

	t.Run("ok", func(t *testing.T) {
		origFS := restoreFS
		origCmd := restoreCmd
		origGeteuid := pbsAPIApplyGeteuid
		t.Cleanup(func() {
			restoreFS = origFS
			restoreCmd = origCmd
			pbsAPIApplyGeteuid = origGeteuid
		})
		restoreFS = osFS{}
		restoreCmd = &fakeCommandRunner{}
		pbsAPIApplyGeteuid = func() int { return 0 }

		if err := ensurePBSServicesForAPI(context.Background(), nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestApplyPBSRemoteCfgViaAPI_StrictCleanupAndCreate(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/remote.cfg",
		"remote: r1\n"+
			"    HOST pbs1.example\n"+
			"    password secret1\n"+
			"    foo_bar baz\n"+
			"    digest abc\n"+
			"    name ignored\n"+
			"\n"+
			"remote: r2\n"+
			"    host pbs2.example\n"+
			"    username admin\n",
		0o640,
	)

	runner.outputs = map[string][]byte{
		"proxmox-backup-manager remote list --output-format=json": []byte(`{"data":[{"id":"r1"},{"id":"old"}]}`),
	}
	runner.errs = map[string]error{
		"proxmox-backup-manager remote remove old": errors.New("cannot remove old"),
	}

	if err := applyPBSRemoteCfgViaAPI(context.Background(), logger, stageRoot, true); err != nil {
		t.Fatalf("applyPBSRemoteCfgViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager remote list --output-format=json",
		"proxmox-backup-manager remote remove old",
		"proxmox-backup-manager remote create r1 --host pbs1.example --password secret1 --foo-bar baz",
		"proxmox-backup-manager remote create r2 --host pbs2.example --username admin",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestApplyPBSRemoteCfgViaAPI_NoFileNoCalls(t *testing.T) {
	stageRoot, _, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	if err := applyPBSRemoteCfgViaAPI(context.Background(), logger, stageRoot, true); err != nil {
		t.Fatalf("applyPBSRemoteCfgViaAPI error: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("expected no calls, got %v", runner.calls)
	}
}

func TestApplyPBSRemoteCfgViaAPI_CreateFailsThenUpdate(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/remote.cfg",
		"remote: r1\n"+
			"    host pbs.example\n"+
			"    password secret1\n",
		0o640,
	)

	runner.errs = map[string]error{
		"proxmox-backup-manager remote create r1 --host pbs.example --password secret1": errors.New("already exists"),
	}

	if err := applyPBSRemoteCfgViaAPI(context.Background(), logger, stageRoot, false); err != nil {
		t.Fatalf("applyPBSRemoteCfgViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager remote create r1 --host pbs.example --password secret1",
		"proxmox-backup-manager remote update r1 --host pbs.example --password secret1",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestApplyPBSRemoteCfgViaAPI_RedactsPasswordOnFailure(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/remote.cfg",
		"remote: r1\n"+
			"    host pbs.example\n"+
			"    password secret1\n",
		0o640,
	)

	runner.errs = map[string]error{
		"proxmox-backup-manager remote create r1 --host pbs.example --password secret1": errors.New("create failed"),
		"proxmox-backup-manager remote update r1 --host pbs.example --password secret1": errors.New("update failed"),
	}

	err := applyPBSRemoteCfgViaAPI(context.Background(), logger, stageRoot, false)
	if err == nil {
		t.Fatalf("expected error")
	}
	if strings.Contains(err.Error(), "secret1") {
		t.Fatalf("expected password to be redacted, got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("expected <redacted> in error, got: %s", err.Error())
	}
}

func TestApplyPBSS3CfgViaAPI_CreateUpdateAndStrictCleanup(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/s3.cfg",
		"s3: e1\n"+
			"    endpoint https://s3.example\n"+
			"    access-key access1\n"+
			"    secret-key secret1\n",
		0o640,
	)

	runner.outputs = map[string][]byte{
		"proxmox-backup-manager s3 endpoint list --output-format=json": []byte(`{"data":[{"id":"e1"},{"id":"old"}]}`),
	}
	runner.errs = map[string]error{
		"proxmox-backup-manager s3 endpoint remove old": errors.New("cannot remove old"),
		"proxmox-backup-manager s3 endpoint create e1 --endpoint https://s3.example --access-key access1 --secret-key secret1": errors.New("already exists"),
	}

	if err := applyPBSS3CfgViaAPI(context.Background(), logger, stageRoot, true); err != nil {
		t.Fatalf("applyPBSS3CfgViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager s3 endpoint list --output-format=json",
		"proxmox-backup-manager s3 endpoint remove old",
		"proxmox-backup-manager s3 endpoint create e1 --endpoint https://s3.example --access-key access1 --secret-key secret1",
		"proxmox-backup-manager s3 endpoint update e1 --endpoint https://s3.example --access-key access1 --secret-key secret1",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestApplyPBSS3CfgViaAPI_NoFileNoCalls(t *testing.T) {
	stageRoot, _, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	if err := applyPBSS3CfgViaAPI(context.Background(), logger, stageRoot, true); err != nil {
		t.Fatalf("applyPBSS3CfgViaAPI error: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("expected no calls, got %v", runner.calls)
	}
}

func TestApplyPBSS3CfgViaAPI_RedactsKeysOnFailure(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/s3.cfg",
		"s3: e1\n"+
			"    endpoint https://s3.example\n"+
			"    access-key access1\n"+
			"    secret-key secret1\n",
		0o640,
	)

	runner.errs = map[string]error{
		"proxmox-backup-manager s3 endpoint create e1 --endpoint https://s3.example --access-key access1 --secret-key secret1": errors.New("create failed"),
		"proxmox-backup-manager s3 endpoint update e1 --endpoint https://s3.example --access-key access1 --secret-key secret1": errors.New("update failed"),
	}

	err := applyPBSS3CfgViaAPI(context.Background(), logger, stageRoot, false)
	if err == nil {
		t.Fatalf("expected error")
	}
	if strings.Contains(err.Error(), "access1") || strings.Contains(err.Error(), "secret1") {
		t.Fatalf("expected keys to be redacted, got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("expected <redacted> in error, got: %s", err.Error())
	}
}

func TestApplyPBSDatastoreCfgViaAPI_StrictFullFlow(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/datastore.cfg",
		"datastore: ds1\n"+
			"    path /new1\n"+
			"    comment c1\n"+
			"\n"+
			"datastore: ds2\n"+
			"    comment missing-path\n"+
			"\n"+
			"datastore: ds3\n"+
			"    path /p3\n"+
			"    comment c3\n"+
			"\n"+
			"datastore: ds4\n"+
			"    path /p4\n"+
			"    comment c4\n",
		0o640,
	)

	runner.outputs = map[string][]byte{
		"proxmox-backup-manager datastore list --output-format=json": []byte(`{"data":[{"name":"ds1","path":"/old1"},{"name":"ds3","path":"/p3"},{"name":"ds-old","path":"/old"}]}`),
	}
	runner.errs = map[string]error{
		"proxmox-backup-manager datastore create ds4 /p4 --comment c4": errors.New("already exists"),
	}

	if err := applyPBSDatastoreCfgViaAPI(context.Background(), logger, stageRoot, true); err != nil {
		t.Fatalf("applyPBSDatastoreCfgViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager datastore list --output-format=json",
		"proxmox-backup-manager datastore remove ds-old",
		"proxmox-backup-manager datastore remove ds1",
		"proxmox-backup-manager datastore create ds1 /new1 --comment c1",
		"proxmox-backup-manager datastore update ds3 --comment c3",
		"proxmox-backup-manager datastore create ds4 /p4 --comment c4",
		"proxmox-backup-manager datastore update ds4 --comment c4",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestApplyPBSDatastoreCfgViaAPI_CurrentPathsFallbacksAndStrictRemoveWarn(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/datastore.cfg",
		"datastore: ds1\n"+
			"    path /p1\n"+
			"    comment c1\n",
		0o640,
	)

	runner.outputs = map[string][]byte{
		"proxmox-backup-manager datastore list --output-format=json": []byte(
			`{"data":[` +
				`{"store":"store1","path":"/ps"},` +
				`{"id":"id1","path":"/pi"},` +
				`{"path":"/no-id"},` +
				`{"name":"ds1","path":"/p1"}` +
				`]}`,
		),
	}
	runner.errs = map[string]error{
		"proxmox-backup-manager datastore remove id1": errors.New("cannot remove id1"),
	}

	if err := applyPBSDatastoreCfgViaAPI(context.Background(), logger, stageRoot, true); err != nil {
		t.Fatalf("applyPBSDatastoreCfgViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager datastore list --output-format=json",
		"proxmox-backup-manager datastore remove id1",
		"proxmox-backup-manager datastore remove store1",
		"proxmox-backup-manager datastore update ds1 --comment c1",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestApplyPBSDatastoreCfgViaAPI_StrictPathMismatchRemoveFails(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/datastore.cfg",
		"datastore: ds1\n"+
			"    path /new\n"+
			"    comment c1\n",
		0o640,
	)

	runner.outputs = map[string][]byte{
		"proxmox-backup-manager datastore list --output-format=json": []byte(`{"data":[{"name":"ds1","path":"/old"}]}`),
	}
	runner.errs = map[string]error{
		"proxmox-backup-manager datastore remove ds1": errors.New("cannot remove ds1"),
	}

	err := applyPBSDatastoreCfgViaAPI(context.Background(), logger, stageRoot, true)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "path mismatch") || !strings.Contains(err.Error(), "remove failed") {
		t.Fatalf("unexpected error: %s", err.Error())
	}
}

func TestApplyPBSDatastoreCfgViaAPI_StrictPathMismatchRecreateFails(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/datastore.cfg",
		"datastore: ds1\n"+
			"    path /new\n"+
			"    comment c1\n",
		0o640,
	)

	runner.outputs = map[string][]byte{
		"proxmox-backup-manager datastore list --output-format=json": []byte(`{"data":[{"name":"ds1","path":"/old"}]}`),
	}
	runner.errs = map[string]error{
		"proxmox-backup-manager datastore create ds1 /new --comment c1": errors.New("create failed"),
	}

	err := applyPBSDatastoreCfgViaAPI(context.Background(), logger, stageRoot, true)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "recreate after path mismatch failed") {
		t.Fatalf("unexpected error: %s", err.Error())
	}
}

func TestApplyPBSDatastoreCfgViaAPI_UpdateFails(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/datastore.cfg",
		"datastore: ds1\n"+
			"    path /p1\n"+
			"    comment c1\n",
		0o640,
	)

	runner.outputs = map[string][]byte{
		"proxmox-backup-manager datastore list --output-format=json": []byte(`{"data":[{"name":"ds1","path":"/p1"}]}`),
	}
	runner.errs = map[string]error{
		"proxmox-backup-manager datastore update ds1 --comment c1": errors.New("update failed"),
	}

	err := applyPBSDatastoreCfgViaAPI(context.Background(), logger, stageRoot, false)
	if err == nil || !strings.Contains(err.Error(), "update failed") {
		t.Fatalf("expected update failed error, got: %v", err)
	}
}

func TestApplyPBSDatastoreCfgViaAPI_CreateAndUpdateFail(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/datastore.cfg",
		"datastore: ds1\n"+
			"    path /p1\n"+
			"    comment c1\n",
		0o640,
	)

	runner.errs = map[string]error{
		"proxmox-backup-manager datastore create ds1 /p1 --comment c1": errors.New("create failed"),
		"proxmox-backup-manager datastore update ds1 --comment c1":       errors.New("update failed"),
	}

	err := applyPBSDatastoreCfgViaAPI(context.Background(), logger, stageRoot, false)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "create failed") || !strings.Contains(err.Error(), "update failed") {
		t.Fatalf("expected create/update errors, got: %s", err.Error())
	}
}

func TestApplyPBSDatastoreCfgViaAPI_NonStrictPathMismatchKeepsPath(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/datastore.cfg",
		"datastore: ds1\n"+
			"    path /new1\n"+
			"    comment c1\n",
		0o640,
	)

	runner.outputs = map[string][]byte{
		"proxmox-backup-manager datastore list --output-format=json": []byte(`{"data":[{"name":"ds1","path":"/old1"}]}`),
	}

	if err := applyPBSDatastoreCfgViaAPI(context.Background(), logger, stageRoot, false); err != nil {
		t.Fatalf("applyPBSDatastoreCfgViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager datastore list --output-format=json",
		"proxmox-backup-manager datastore update ds1 --comment c1",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestApplyPBSSyncCfgViaAPI_Create(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/sync.cfg",
		"sync-job: job1\n"+
			"    remote r1\n"+
			"    store ds1\n",
		0o640,
	)

	if err := applyPBSSyncCfgViaAPI(context.Background(), logger, stageRoot, false); err != nil {
		t.Fatalf("applyPBSSyncCfgViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager sync-job create job1 --remote r1 --store ds1",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestApplyPBSSyncCfgViaAPI_StrictCleanupAndFallbackUpdate(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/sync.cfg",
		"sync-job: job1\n"+
			"    remote r1\n"+
			"    store ds1\n",
		0o640,
	)

	runner.outputs = map[string][]byte{
		"proxmox-backup-manager sync-job list --output-format=json": []byte(`{"data":[{"id":"job1"},{"id":"old"}]}`),
	}
	runner.errs = map[string]error{
		"proxmox-backup-manager sync-job remove old":                    errors.New("cannot remove old"),
		"proxmox-backup-manager sync-job create job1 --remote r1 --store ds1": errors.New("already exists"),
	}

	if err := applyPBSSyncCfgViaAPI(context.Background(), logger, stageRoot, true); err != nil {
		t.Fatalf("applyPBSSyncCfgViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager sync-job list --output-format=json",
		"proxmox-backup-manager sync-job remove old",
		"proxmox-backup-manager sync-job create job1 --remote r1 --store ds1",
		"proxmox-backup-manager sync-job update job1 --remote r1 --store ds1",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestApplyPBSVerificationCfgViaAPI_Create(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/verification.cfg",
		"verify-job: v1\n"+
			"    store ds1\n",
		0o640,
	)

	if err := applyPBSVerificationCfgViaAPI(context.Background(), logger, stageRoot, false); err != nil {
		t.Fatalf("applyPBSVerificationCfgViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager verify-job create v1 --store ds1",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestApplyPBSVerificationCfgViaAPI_StrictCleanupAndFallbackUpdate(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/verification.cfg",
		"verify-job: v1\n"+
			"    store ds1\n",
		0o640,
	)

	runner.outputs = map[string][]byte{
		"proxmox-backup-manager verify-job list --output-format=json": []byte(`{"data":[{"id":"v1"},{"id":"old"}]}`),
	}
	runner.errs = map[string]error{
		"proxmox-backup-manager verify-job remove old":       errors.New("cannot remove old"),
		"proxmox-backup-manager verify-job create v1 --store ds1": errors.New("already exists"),
	}

	if err := applyPBSVerificationCfgViaAPI(context.Background(), logger, stageRoot, true); err != nil {
		t.Fatalf("applyPBSVerificationCfgViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager verify-job list --output-format=json",
		"proxmox-backup-manager verify-job remove old",
		"proxmox-backup-manager verify-job create v1 --store ds1",
		"proxmox-backup-manager verify-job update v1 --store ds1",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestApplyPBSPruneCfgViaAPI_Create(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/prune.cfg",
		"prune-job: p1\n"+
			"    store ds1\n"+
			"    keep-last 3\n",
		0o640,
	)

	if err := applyPBSPruneCfgViaAPI(context.Background(), logger, stageRoot, false); err != nil {
		t.Fatalf("applyPBSPruneCfgViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager prune-job create p1 --store ds1 --keep-last 3",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestApplyPBSPruneCfgViaAPI_StrictCleanupAndFallbackUpdate(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/prune.cfg",
		"prune-job: p1\n"+
			"    store ds1\n"+
			"    keep-last 3\n",
		0o640,
	)

	runner.outputs = map[string][]byte{
		"proxmox-backup-manager prune-job list --output-format=json": []byte(`{"data":[{"id":"old"},{"id":"p1"}]}`),
	}
	runner.errs = map[string]error{
		"proxmox-backup-manager prune-job remove old":           errors.New("cannot remove old"),
		"proxmox-backup-manager prune-job create p1 --store ds1 --keep-last 3": errors.New("already exists"),
	}

	if err := applyPBSPruneCfgViaAPI(context.Background(), logger, stageRoot, true); err != nil {
		t.Fatalf("applyPBSPruneCfgViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager prune-job list --output-format=json",
		"proxmox-backup-manager prune-job remove old",
		"proxmox-backup-manager prune-job create p1 --store ds1 --keep-last 3",
		"proxmox-backup-manager prune-job update p1 --store ds1 --keep-last 3",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestApplyPBSTrafficControlCfgViaAPI_StrictCleanupAndCreate(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/traffic-control.cfg",
		"traffic-control: tc1\n"+
			"    rate-in 1000\n",
		0o640,
	)

	runner.outputs = map[string][]byte{
		"proxmox-backup-manager traffic-control list --output-format=json": []byte(`{"data":[{"name":"old"},{"name":"tc1"}]}`),
	}
	runner.errs = map[string]error{
		"proxmox-backup-manager traffic-control remove old": errors.New("cannot remove old"),
	}

	if err := applyPBSTrafficControlCfgViaAPI(context.Background(), logger, stageRoot, true); err != nil {
		t.Fatalf("applyPBSTrafficControlCfgViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager traffic-control list --output-format=json",
		"proxmox-backup-manager traffic-control remove old",
		"proxmox-backup-manager traffic-control create tc1 --rate-in 1000",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestApplyPBSTrafficControlCfgViaAPI_CreateFailsThenUpdate(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	logger := logging.New(types.LogLevelDebug, false)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/traffic-control.cfg",
		"traffic-control: tc1\n"+
			"    rate-in 1000\n",
		0o640,
	)

	runner.errs = map[string]error{
		"proxmox-backup-manager traffic-control create tc1 --rate-in 1000": errors.New("already exists"),
	}

	if err := applyPBSTrafficControlCfgViaAPI(context.Background(), logger, stageRoot, false); err != nil {
		t.Fatalf("applyPBSTrafficControlCfgViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager traffic-control create tc1 --rate-in 1000",
		"proxmox-backup-manager traffic-control update tc1 --rate-in 1000",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestApplyPBSNodeCfgViaAPI_UsesFirstSection(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)

	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/node.cfg",
		"node: n1\n"+
			"    dns1 1.1.1.1\n"+
			"    foo_bar baz\n"+
			"\n"+
			"node: n2\n"+
			"    dns1 9.9.9.9\n",
		0o640,
	)

	if err := applyPBSNodeCfgViaAPI(context.Background(), stageRoot); err != nil {
		t.Fatalf("applyPBSNodeCfgViaAPI error: %v", err)
	}

	want := []string{
		"proxmox-backup-manager node update --dns1 1.1.1.1 --foo-bar baz",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls=%v want %v", runner.calls, want)
	}
}

func TestApplyPBSNodeCfgViaAPI_NoFileAndEmptyAndError(t *testing.T) {
	t.Run("no-file", func(t *testing.T) {
		stageRoot, _, runner := setupPBSAPIApplyTestDeps(t)
		if err := applyPBSNodeCfgViaAPI(context.Background(), stageRoot); err != nil {
			t.Fatalf("applyPBSNodeCfgViaAPI error: %v", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("expected no calls, got %v", runner.calls)
		}
	})

	t.Run("empty-sections", func(t *testing.T) {
		stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
		writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/node.cfg", "   \n# comment\n", 0o640)
		if err := applyPBSNodeCfgViaAPI(context.Background(), stageRoot); err != nil {
			t.Fatalf("applyPBSNodeCfgViaAPI error: %v", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("expected no calls, got %v", runner.calls)
		}
	})

	t.Run("command-error", func(t *testing.T) {
		stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
		writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/node.cfg",
			"node: n1\n"+
				"    dns1 1.1.1.1\n",
			0o640,
		)
		runner.errs = map[string]error{
			"proxmox-backup-manager node update --dns1 1.1.1.1": errors.New("boom"),
		}
		if err := applyPBSNodeCfgViaAPI(context.Background(), stageRoot); err == nil {
			t.Fatalf("expected error")
		}
	})
}
