package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPBSConfigHasHeader_AcceptsAndRejectsExpectedForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name: "HeaderWithSpaceSeparatedName",
			content: strings.Join([]string{
				"# comment",
				"",
				"remote: pbs1",
				"    host 10.0.0.10",
			}, "\n"),
			want: true,
		},
		{
			name: "HeaderWithInlineName",
			content: strings.Join([]string{
				"remote:pbs1",
				"    host 10.0.0.10",
			}, "\n"),
			want: true,
		},
		{
			name: "RejectsHeaderWithoutName",
			content: strings.Join([]string{
				"datastore:",
				"    path /mnt/datastore",
			}, "\n"),
			want: false,
		},
		{
			name:    "RejectsInvalidKeyCharacters",
			content: "foo.bar: baz\n",
			want:    false,
		},
		{
			name:    "RejectsEmptyKey",
			content: ": x\n",
			want:    false,
		},
		{
			name:    "RejectsOnlyComments",
			content: "# comment\n# still comment\n",
			want:    false,
		},
		{
			name:    "AcceptsDashAndUnderscore",
			content: "key-with-dash: v\nkey_with_underscore: v\n",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pbsConfigHasHeader(tt.content); got != tt.want {
				t.Fatalf("pbsConfigHasHeader()=%v want %v", got, tt.want)
			}
		})
	}
}

func TestMaybeApplyPBSConfigsFromStage_EarlyReturns(t *testing.T) {
	ctx := context.Background()
	logger := newTestLogger()

	if err := maybeApplyPBSConfigsFromStage(ctx, logger, nil, "/stage", false); err != nil {
		t.Fatalf("nil plan: expected nil error, got %v", err)
	}

	planWrongSystem := &RestorePlan{SystemType: SystemTypePVE, NormalCategories: []Category{{ID: "pbs_host"}}}
	if err := maybeApplyPBSConfigsFromStage(ctx, logger, planWrongSystem, "/stage", false); err != nil {
		t.Fatalf("wrong system type: expected nil error, got %v", err)
	}

	planNoCategories := &RestorePlan{SystemType: SystemTypePBS, NormalCategories: []Category{{ID: "unrelated"}}}
	if err := maybeApplyPBSConfigsFromStage(ctx, logger, planNoCategories, "/stage", false); err != nil {
		t.Fatalf("no pbs categories: expected nil error, got %v", err)
	}

	plan := &RestorePlan{SystemType: SystemTypePBS, NormalCategories: []Category{{ID: "pbs_host"}}}
	if err := maybeApplyPBSConfigsFromStage(ctx, logger, plan, "   ", false); err != nil {
		t.Fatalf("blank stageRoot: expected nil error, got %v", err)
	}
	if err := maybeApplyPBSConfigsFromStage(ctx, logger, plan, "/stage", true); err != nil {
		t.Fatalf("dryRun: expected nil error, got %v", err)
	}

	origFS := restoreFS
	fakeFS := NewFakeFS()
	t.Cleanup(func() {
		restoreFS = origFS
		_ = os.RemoveAll(fakeFS.Root)
	})
	restoreFS = fakeFS
	if err := maybeApplyPBSConfigsFromStage(ctx, logger, plan, "/stage", false); err != nil {
		t.Fatalf("non-real FS: expected nil error, got %v", err)
	}
}

func TestApplyPBSConfigFileFromStage_SkipsMissingFile(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if err := applyPBSConfigFileFromStage(context.Background(), newTestLogger(), "/stage", "etc/proxmox-backup/s3.cfg"); err != nil {
		t.Fatalf("applyPBSConfigFileFromStage: %v", err)
	}
	if _, err := fakeFS.Stat("/etc/proxmox-backup/s3.cfg"); err == nil {
		t.Fatalf("expected s3.cfg to not be created")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat s3.cfg: %v", err)
	}
}

func TestApplyPBSConfigFileFromStage_SkipsInvalidHeader_LeavesTargetUnchanged(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	const destPath = "/etc/proxmox-backup/remote.cfg"
	existing := "remote: old\n    host 1.2.3.4\n"
	if err := fakeFS.WriteFile(destPath, []byte(existing), 0o640); err != nil {
		t.Fatalf("write existing remote.cfg: %v", err)
	}

	stageRoot := "/stage"
	staged := "this is not a PBS config file\n(no section header)\n"
	if err := fakeFS.WriteFile(filepath.Join(stageRoot, "etc/proxmox-backup/remote.cfg"), []byte(staged), 0o640); err != nil {
		t.Fatalf("write staged remote.cfg: %v", err)
	}

	if err := applyPBSConfigFileFromStage(context.Background(), newTestLogger(), stageRoot, "etc/proxmox-backup/remote.cfg"); err != nil {
		t.Fatalf("applyPBSConfigFileFromStage: %v", err)
	}

	got, err := fakeFS.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read dest remote.cfg: %v", err)
	}
	if string(got) != existing {
		t.Fatalf("dest remote.cfg changed unexpectedly: got=%q want=%q", string(got), existing)
	}
}

func TestApplyPBSConfigFileFromStage_ReturnsErrorOnAtomicWriteFailure(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Unix(123, 456)}

	stageRoot := "/stage"
	rel := "etc/proxmox-backup/s3.cfg"
	staged := "s3: r1\n    bucket test\n"
	if err := fakeFS.WriteFile(filepath.Join(stageRoot, rel), []byte(staged), 0o640); err != nil {
		t.Fatalf("write staged s3.cfg: %v", err)
	}

	destPath := filepath.Join(string(os.PathSeparator), filepath.FromSlash(rel))
	tmpPath := fmt.Sprintf("%s.proxsave.tmp.%d", destPath, nowRestore().UnixNano())
	fakeFS.OpenFileErr[filepath.Clean(tmpPath)] = errors.New("forced OpenFile error")

	if err := applyPBSConfigFileFromStage(context.Background(), newTestLogger(), stageRoot, rel); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestApplyPBSS3CfgFromStage_WritesS3Cfg(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	content := "s3: r1\n    bucket test\n"
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/s3.cfg", []byte(content), 0o640); err != nil {
		t.Fatalf("write staged s3.cfg: %v", err)
	}

	if err := applyPBSS3CfgFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSS3CfgFromStage: %v", err)
	}

	if _, err := fakeFS.Stat("/etc/proxmox-backup/s3.cfg"); err != nil {
		t.Fatalf("expected s3.cfg to exist: %v", err)
	}
}

func TestApplyPBSConfigFileFromStage_PropagatesReadError(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	rel := "etc/proxmox-backup/remote.cfg"
	if err := fakeFS.MkdirAll(filepath.Join(stageRoot, rel), 0o755); err != nil {
		t.Fatalf("mkdir staged remote.cfg dir: %v", err)
	}

	if err := applyPBSConfigFileFromStage(context.Background(), newTestLogger(), stageRoot, rel); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestLoadPBSDatastoreCfgFromInventory_FallsBackToDatastoreList(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	inventory := `{"datastores":[{"name":"DS1","path":"/mnt/ds1","comment":"primary"},{"name":"DS2","path":"/mnt/ds2","comment":""}]}`
	if err := fakeFS.WriteFile(stageRoot+"/var/lib/proxsave-info/commands/pbs/pbs_datastore_inventory.json", []byte(inventory), 0o640); err != nil {
		t.Fatalf("write inventory: %v", err)
	}

	content, src, err := loadPBSDatastoreCfgFromInventory(stageRoot)
	if err != nil {
		t.Fatalf("loadPBSDatastoreCfgFromInventory: %v", err)
	}
	if src != "pbs_datastore_inventory.json.datastores" {
		t.Fatalf("src=%q", src)
	}

	blocks, err := parsePBSDatastoreCfgBlocks(content)
	if err != nil {
		t.Fatalf("parsePBSDatastoreCfgBlocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	paths := map[string]string{}
	for _, b := range blocks {
		paths[b.Name] = b.Path
	}
	if paths["DS1"] != "/mnt/ds1" {
		t.Fatalf("DS1 path=%q", paths["DS1"])
	}
	if paths["DS2"] != "/mnt/ds2" {
		t.Fatalf("DS2 path=%q", paths["DS2"])
	}
	if !strings.Contains(content, "comment primary") {
		t.Fatalf("expected DS1 comment in generated content")
	}
}

func TestLoadPBSDatastoreCfgFromInventory_PropagatesErrors(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	inventoryPath := stageRoot + "/var/lib/proxsave-info/commands/pbs/pbs_datastore_inventory.json"

	if err := fakeFS.WriteFile(inventoryPath, []byte("   \n"), 0o640); err != nil {
		t.Fatalf("write empty inventory: %v", err)
	}
	if _, _, err := loadPBSDatastoreCfgFromInventory(stageRoot); err == nil {
		t.Fatalf("expected error for empty inventory")
	}

	if err := fakeFS.WriteFile(inventoryPath, []byte("not-json"), 0o640); err != nil {
		t.Fatalf("write invalid inventory: %v", err)
	}
	if _, _, err := loadPBSDatastoreCfgFromInventory(stageRoot); err == nil {
		t.Fatalf("expected error for invalid JSON")
	}

	if err := fakeFS.WriteFile(inventoryPath, []byte(`{"datastores":[{"name":"","path":"","comment":""}]}`), 0o640); err != nil {
		t.Fatalf("write unusable inventory: %v", err)
	}
	if _, _, err := loadPBSDatastoreCfgFromInventory(stageRoot); err == nil {
		t.Fatalf("expected error for unusable inventory")
	}
}

func TestDetectPBSDatastoreCfgDuplicateKeys_DetectsDuplicateKeys(t *testing.T) {
	t.Parallel()

	blocks := []pbsDatastoreBlock{{
		Name: "DS1",
		Lines: []string{
			"datastore: DS1",
			"# comment",
			"",
			"    path /mnt/a",
			"    path /mnt/b",
		},
	}}
	if reason := detectPBSDatastoreCfgDuplicateKeys(blocks); reason == "" {
		t.Fatalf("expected duplicate key detection")
	}
}

func TestDetectPBSDatastoreCfgDuplicateKeys_AllowsUniqueKeys(t *testing.T) {
	t.Parallel()

	blocks := []pbsDatastoreBlock{{
		Name: "DS1",
		Lines: []string{
			"datastore: DS1",
			"    comment one",
			"    path /mnt/a",
		},
	}}
	if reason := detectPBSDatastoreCfgDuplicateKeys(blocks); reason != "" {
		t.Fatalf("expected no duplicates, got %q", reason)
	}
}

func TestParsePBSDatastoreCfgBlocks_IgnoresGarbageAndHandlesMissingNames(t *testing.T) {
	t.Parallel()

	content := strings.Join([]string{
		"path /should/be/ignored",
		"datastore:",
		"    path /also/ignored",
		"datastore: DS1",
		"# keep comment",
		"path /mnt/ds1",
		"",
		"datastore: DS2:",
		"    path /mnt/ds2",
		"",
	}, "\n")

	blocks, err := parsePBSDatastoreCfgBlocks(content)
	if err != nil {
		t.Fatalf("parsePBSDatastoreCfgBlocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].Name != "DS1" || blocks[0].Path != "/mnt/ds1" {
		t.Fatalf("block[0]=%+v", blocks[0])
	}
	if blocks[1].Name != "DS2" || blocks[1].Path != "/mnt/ds2" {
		t.Fatalf("block[1]=%+v", blocks[1])
	}
	if gotLines := strings.Join(blocks[0].Lines, "\n"); !strings.Contains(gotLines, "# keep comment") {
		t.Fatalf("expected DS1 block to retain comment line; got=%q", gotLines)
	}
}

func TestParsePBSDatastoreCfgBlocks_DropsEmptyNamedBlocks(t *testing.T) {
	t.Parallel()

	content := strings.Join([]string{
		"datastore: :",
		"    path /mnt/ignored",
		"",
		"datastore: DS1",
		"    path /mnt/ds1",
		"",
	}, "\n")

	blocks, err := parsePBSDatastoreCfgBlocks(content)
	if err != nil {
		t.Fatalf("parsePBSDatastoreCfgBlocks: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Name != "DS1" {
		t.Fatalf("block[0].Name=%q", blocks[0].Name)
	}
}

func TestShouldApplyPBSDatastoreBlock_CoversCommonBranches(t *testing.T) {
	t.Parallel()

	if ok, reason := shouldApplyPBSDatastoreBlock(pbsDatastoreBlock{Name: "ds", Path: "/"}, newTestLogger()); ok || !strings.Contains(reason, "invalid") {
		t.Fatalf("expected invalid path rejection, got ok=%v reason=%q", ok, reason)
	}

	dsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dsDir, ".chunks"), 0o755); err != nil {
		t.Fatalf("mkdir .chunks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dsDir, ".chunks", "c1"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write chunk: %v", err)
	}
	if ok, reason := shouldApplyPBSDatastoreBlock(pbsDatastoreBlock{Name: "ds", Path: dsDir}, newTestLogger()); !ok {
		t.Fatalf("expected hasData datastore to be applied, got ok=false reason=%q", reason)
	}

	tooLong := "/" + strings.Repeat("a", 5000)
	if ok, reason := shouldApplyPBSDatastoreBlock(pbsDatastoreBlock{Name: "ds", Path: tooLong}, newTestLogger()); ok || !strings.Contains(reason, "inspection failed") {
		t.Fatalf("expected inspection failure, got ok=%v reason=%q", ok, reason)
	}
}

func TestWriteDeferredPBSDatastoreCfg_EmptyInputIsNoop(t *testing.T) {
	t.Parallel()

	if path, err := writeDeferredPBSDatastoreCfg(nil); err != nil {
		t.Fatalf("err=%v", err)
	} else if path != "" {
		t.Fatalf("expected empty path, got %q", path)
	}
}

func TestWriteDeferredPBSDatastoreCfg_WritesFile(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}

	blocks := []pbsDatastoreBlock{{
		Name:  "DS1",
		Path:  "/mnt/ds1",
		Lines: []string{"datastore: DS1", "    path /mnt/ds1"},
	}}

	path, err := writeDeferredPBSDatastoreCfg(blocks)
	if err != nil {
		t.Fatalf("writeDeferredPBSDatastoreCfg: %v", err)
	}
	if path == "" {
		t.Fatalf("expected non-empty path")
	}

	raw, err := fakeFS.ReadFile(path)
	if err != nil {
		t.Fatalf("read deferred file: %v", err)
	}
	if !strings.Contains(string(raw), "datastore: DS1") {
		t.Fatalf("unexpected deferred content: %q", string(raw))
	}
}

func TestWriteDeferredPBSDatastoreCfg_PropagatesMkdirError(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	fakeFS.MkdirAllErr = errors.New("forced mkdir error")

	_, err := writeDeferredPBSDatastoreCfg([]pbsDatastoreBlock{{Name: "DS1", Path: "/mnt/ds1", Lines: []string{"datastore: DS1", "    path /mnt/ds1"}}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestWriteDeferredPBSDatastoreCfg_PropagatesWriteError(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	fakeFS.WriteErr = errors.New("forced write error")

	_, err := writeDeferredPBSDatastoreCfg([]pbsDatastoreBlock{{Name: "DS1", Path: "/mnt/ds1", Lines: []string{"datastore: DS1", "    path /mnt/ds1"}}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestWriteDeferredPBSDatastoreCfg_MultipleBlocksAddsSeparator(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)}

	blocks := []pbsDatastoreBlock{
		{Name: "DS1", Lines: []string{"datastore: DS1", "    path /mnt/ds1"}},
		{Name: "DS2", Lines: []string{"datastore: DS2", "    path /mnt/ds2"}},
	}

	path, err := writeDeferredPBSDatastoreCfg(blocks)
	if err != nil {
		t.Fatalf("writeDeferredPBSDatastoreCfg: %v", err)
	}

	raw, err := fakeFS.ReadFile(path)
	if err != nil {
		t.Fatalf("read deferred file: %v", err)
	}
	if !strings.Contains(string(raw), "datastore: DS1\n    path /mnt/ds1\n\ndatastore: DS2\n    path /mnt/ds2") {
		t.Fatalf("expected blank line separator between blocks; got=%q", string(raw))
	}
}

func TestApplyPBSDatastoreCfgFromStage_SkipsMissingStagedFile(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if err := applyPBSDatastoreCfgFromStage(context.Background(), newTestLogger(), "/stage"); err != nil {
		t.Fatalf("applyPBSDatastoreCfgFromStage: %v", err)
	}
	if _, err := fakeFS.Stat("/etc/proxmox-backup/datastore.cfg"); err == nil {
		t.Fatalf("expected datastore.cfg not to be created")
	}
}

func TestApplyPBSDatastoreCfgFromStage_RemovesTargetWhenStagedEmpty(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if err := fakeFS.WriteFile("/etc/proxmox-backup/datastore.cfg", []byte("datastore: old\n    path /mnt/old\n"), 0o640); err != nil {
		t.Fatalf("write existing datastore.cfg: %v", err)
	}
	if err := fakeFS.WriteFile("/stage/etc/proxmox-backup/datastore.cfg", []byte("   \n"), 0o640); err != nil {
		t.Fatalf("write staged datastore.cfg: %v", err)
	}

	if err := applyPBSDatastoreCfgFromStage(context.Background(), newTestLogger(), "/stage"); err != nil {
		t.Fatalf("applyPBSDatastoreCfgFromStage: %v", err)
	}
	if _, err := fakeFS.Stat("/etc/proxmox-backup/datastore.cfg"); err == nil {
		t.Fatalf("expected datastore.cfg removed")
	}
}

func TestApplyPBSDatastoreCfgFromStage_DefersUnsafeAndAppliesSafe(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)}

	safeDir := t.TempDir()
	unsafeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(unsafeDir, "unexpected"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write unexpected file: %v", err)
	}

	stageRoot := "/stage"
	staged := strings.Join([]string{
		"datastore: Safe",
		fmt.Sprintf("path %s", safeDir),
		"",
		"datastore: Unsafe",
		fmt.Sprintf("path %s", unsafeDir),
		"",
	}, "\n")
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/datastore.cfg", []byte(staged), 0o640); err != nil {
		t.Fatalf("write staged datastore.cfg: %v", err)
	}

	if err := applyPBSDatastoreCfgFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSDatastoreCfgFromStage: %v", err)
	}

	out, err := fakeFS.ReadFile("/etc/proxmox-backup/datastore.cfg")
	if err != nil {
		t.Fatalf("read applied datastore.cfg: %v", err)
	}
	if !strings.Contains(string(out), "datastore: Safe") {
		t.Fatalf("expected Safe datastore in output: %q", string(out))
	}
	if strings.Contains(string(out), "datastore: Unsafe") {
		t.Fatalf("did not expect Unsafe datastore in output: %q", string(out))
	}

	entries, err := fakeFS.ReadDir("/tmp/proxsave")
	if err != nil {
		t.Fatalf("readdir deferred dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 deferred file, got %d", len(entries))
	}
	deferredPath := filepath.Join("/tmp/proxsave", entries[0].Name())
	deferred, err := fakeFS.ReadFile(deferredPath)
	if err != nil {
		t.Fatalf("read deferred file: %v", err)
	}
	if !strings.Contains(string(deferred), "datastore: Unsafe") {
		t.Fatalf("expected Unsafe datastore deferred: %q", string(deferred))
	}
}

func TestApplyPBSDatastoreCfgFromStage_AllDeferredLeavesTargetUnchanged(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 3, 4, 5, 7, 0, time.UTC)}

	existing := "datastore: Existing\n    path /mnt/existing\n"
	if err := fakeFS.WriteFile("/etc/proxmox-backup/datastore.cfg", []byte(existing), 0o640); err != nil {
		t.Fatalf("write existing datastore.cfg: %v", err)
	}

	unsafeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(unsafeDir, "unexpected"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write unexpected file: %v", err)
	}

	stageRoot := "/stage"
	staged := strings.Join([]string{
		"datastore: UnsafeOnly",
		fmt.Sprintf("path %s", unsafeDir),
		"",
	}, "\n")
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/datastore.cfg", []byte(staged), 0o640); err != nil {
		t.Fatalf("write staged datastore.cfg: %v", err)
	}

	if err := applyPBSDatastoreCfgFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSDatastoreCfgFromStage: %v", err)
	}

	got, err := fakeFS.ReadFile("/etc/proxmox-backup/datastore.cfg")
	if err != nil {
		t.Fatalf("read datastore.cfg: %v", err)
	}
	if string(got) != existing {
		t.Fatalf("datastore.cfg changed unexpectedly: got=%q want=%q", string(got), existing)
	}
}

func TestApplyPBSDatastoreCfgFromStage_DuplicateKeysWithoutInventoryLeavesTargetUnchanged(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	existing := "datastore: Existing\n    path /mnt/existing\n"
	if err := fakeFS.WriteFile("/etc/proxmox-backup/datastore.cfg", []byte(existing), 0o640); err != nil {
		t.Fatalf("write existing datastore.cfg: %v", err)
	}

	stageRoot := "/stage"
	staged := strings.Join([]string{
		"datastore: Broken",
		"    path /mnt/a",
		"    path /mnt/b",
		"",
	}, "\n")
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/datastore.cfg", []byte(staged), 0o640); err != nil {
		t.Fatalf("write staged datastore.cfg: %v", err)
	}

	if err := applyPBSDatastoreCfgFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSDatastoreCfgFromStage: %v", err)
	}

	got, err := fakeFS.ReadFile("/etc/proxmox-backup/datastore.cfg")
	if err != nil {
		t.Fatalf("read datastore.cfg: %v", err)
	}
	if string(got) != existing {
		t.Fatalf("datastore.cfg changed unexpectedly: got=%q want=%q", string(got), existing)
	}
}

func TestApplyPBSDatastoreCfgFromStage_SkipsWhenNoBlocksDetected(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/datastore.cfg", []byte("# only comments\n\n# nothing else\n"), 0o640); err != nil {
		t.Fatalf("write staged datastore.cfg: %v", err)
	}

	if err := applyPBSDatastoreCfgFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSDatastoreCfgFromStage: %v", err)
	}
	if _, err := fakeFS.Stat("/etc/proxmox-backup/datastore.cfg"); err == nil {
		t.Fatalf("expected datastore.cfg not to be created")
	}
}

func TestApplyPBSDatastoreCfgFromStage_PropagatesReadError(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	if err := fakeFS.MkdirAll(stageRoot+"/etc/proxmox-backup/datastore.cfg", 0o755); err != nil {
		t.Fatalf("mkdir staged datastore.cfg dir: %v", err)
	}

	if err := applyPBSDatastoreCfgFromStage(context.Background(), newTestLogger(), stageRoot); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestApplyPBSDatastoreCfgFromStage_ContinuesWhenDeferredWriteFails(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Unix(10, 0)}

	safeDir := t.TempDir()
	unsafeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(unsafeDir, "unexpected"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write unexpected file: %v", err)
	}

	stageRoot := "/stage"
	staged := strings.Join([]string{
		"datastore: Safe",
		fmt.Sprintf("path %s", safeDir),
		"",
		"datastore: Unsafe",
		fmt.Sprintf("path %s", unsafeDir),
		"",
	}, "\n")
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/datastore.cfg", []byte(staged), 0o640); err != nil {
		t.Fatalf("write staged datastore.cfg: %v", err)
	}

	// Fail deferred file writes but still allow atomic apply.
	fakeFS.WriteErr = errors.New("forced write error")

	if err := applyPBSDatastoreCfgFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSDatastoreCfgFromStage: %v", err)
	}

	out, err := fakeFS.ReadFile("/etc/proxmox-backup/datastore.cfg")
	if err != nil {
		t.Fatalf("read applied datastore.cfg: %v", err)
	}
	if !strings.Contains(string(out), "datastore: Safe") || strings.Contains(string(out), "datastore: Unsafe") {
		t.Fatalf("unexpected apply result: %q", string(out))
	}

	if entries, err := fakeFS.ReadDir("/tmp/proxsave"); err != nil {
		t.Fatalf("readdir /tmp/proxsave: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("expected no deferred files due to forced write error, got %d", len(entries))
	}
}

func TestApplyPBSDatastoreCfgFromStage_ReturnsErrorOnAtomicWriteFailure(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Unix(10, 0)}

	safeDir := t.TempDir()
	stageRoot := "/stage"
	staged := strings.Join([]string{
		"datastore: DS1",
		fmt.Sprintf("path %s", safeDir),
		"",
	}, "\n")
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/datastore.cfg", []byte(staged), 0o640); err != nil {
		t.Fatalf("write staged datastore.cfg: %v", err)
	}

	dest := "/etc/proxmox-backup/datastore.cfg"
	tmp := fmt.Sprintf("%s.proxsave.tmp.%d", dest, nowRestore().UnixNano())
	fakeFS.OpenFileErr[filepath.Clean(tmp)] = errors.New("forced OpenFile error")

	if err := applyPBSDatastoreCfgFromStage(context.Background(), newTestLogger(), stageRoot); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestApplyPBSJobConfigsFromStage_WritesAllJobConfigs(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	for name, content := range map[string]string{
		"sync.cfg":         "sync: job1\n    remote r1\n",
		"verification.cfg": "verification: v1\n    datastore ds1\n",
		"prune.cfg":        "prune: p1\n    keep-last 1\n",
	} {
		if err := fakeFS.WriteFile(filepath.Join(stageRoot, "etc/proxmox-backup", name), []byte(content), 0o640); err != nil {
			t.Fatalf("write staged %s: %v", name, err)
		}
	}

	if err := applyPBSJobConfigsFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSJobConfigsFromStage: %v", err)
	}

	for _, name := range []string{"sync.cfg", "verification.cfg", "prune.cfg"} {
		dest := filepath.Join("/etc/proxmox-backup", name)
		if _, err := fakeFS.Stat(dest); err != nil {
			t.Fatalf("expected %s to exist: %v", dest, err)
		}
	}
}

func TestApplyPBSJobConfigsFromStage_ContinuesOnApplyErrors(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Unix(10, 0)}

	stageRoot := "/stage"
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/sync.cfg", []byte("sync: job1\n    remote r1\n"), 0o640); err != nil {
		t.Fatalf("write staged sync.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/verification.cfg", []byte("verification: v1\n    datastore ds1\n"), 0o640); err != nil {
		t.Fatalf("write staged verification.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/prune.cfg", []byte("prune: p1\n    keep-last 1\n"), 0o640); err != nil {
		t.Fatalf("write staged prune.cfg: %v", err)
	}

	relFail := "etc/proxmox-backup/verification.cfg"
	destFail := filepath.Join(string(os.PathSeparator), filepath.FromSlash(relFail))
	tmpFail := fmt.Sprintf("%s.proxsave.tmp.%d", destFail, nowRestore().UnixNano())
	fakeFS.OpenFileErr[filepath.Clean(tmpFail)] = errors.New("forced OpenFile error")

	if err := applyPBSJobConfigsFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSJobConfigsFromStage: %v", err)
	}

	if _, err := fakeFS.Stat("/etc/proxmox-backup/sync.cfg"); err != nil {
		t.Fatalf("expected sync.cfg to exist: %v", err)
	}
	if _, err := fakeFS.Stat("/etc/proxmox-backup/prune.cfg"); err != nil {
		t.Fatalf("expected prune.cfg to exist: %v", err)
	}
	if _, err := fakeFS.Stat("/etc/proxmox-backup/verification.cfg"); err == nil {
		t.Fatalf("expected verification.cfg not to be created due to forced error")
	}
}

func TestApplyPBSTapeConfigsFromStage_WritesConfigsAndSensitiveKeys(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	for name, content := range map[string]string{
		"tape.cfg":       "drive: d1\n    path /dev/nst0\n",
		"tape-job.cfg":   "tape-job: job1\n    drive d1\n",
		"media-pool.cfg": "media-pool: pool1\n    retention 30\n",
	} {
		if err := fakeFS.WriteFile(filepath.Join(stageRoot, "etc/proxmox-backup", name), []byte(content), 0o640); err != nil {
			t.Fatalf("write staged %s: %v", name, err)
		}
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/tape-encryption-keys.json", []byte(`{"keys":[{"fingerprint":"abc"}]}`), 0o640); err != nil {
		t.Fatalf("write staged tape-encryption-keys.json: %v", err)
	}

	if err := applyPBSTapeConfigsFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSTapeConfigsFromStage: %v", err)
	}

	for _, name := range []string{"tape.cfg", "tape-job.cfg", "media-pool.cfg"} {
		dest := filepath.Join("/etc/proxmox-backup", name)
		if _, err := fakeFS.Stat(dest); err != nil {
			t.Fatalf("expected %s to exist: %v", dest, err)
		}
	}

	if info, err := fakeFS.Stat("/etc/proxmox-backup/tape-encryption-keys.json"); err != nil {
		t.Fatalf("stat tape-encryption-keys.json: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("tape-encryption-keys.json mode=%#o want %#o", info.Mode().Perm(), 0o600)
	}
}

func TestApplyPBSTapeConfigsFromStage_ContinuesOnErrors(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	// Force applyPBSConfigFileFromStage and applySensitiveFileFromStage errors (ReadFile on directory).
	if err := fakeFS.MkdirAll(stageRoot+"/etc/proxmox-backup/tape.cfg", 0o755); err != nil {
		t.Fatalf("mkdir staged tape.cfg dir: %v", err)
	}
	if err := fakeFS.MkdirAll(stageRoot+"/etc/proxmox-backup/tape-encryption-keys.json", 0o755); err != nil {
		t.Fatalf("mkdir staged tape-encryption-keys.json dir: %v", err)
	}

	if err := applyPBSTapeConfigsFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSTapeConfigsFromStage: %v", err)
	}
}

func TestRemoveIfExists_IgnoresMissingAndRemovesExisting(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if err := removeIfExists("/etc/proxmox-backup/missing.cfg"); err != nil {
		t.Fatalf("removeIfExists missing: %v", err)
	}

	if err := fakeFS.WriteFile("/etc/proxmox-backup/existing.cfg", []byte("x"), 0o640); err != nil {
		t.Fatalf("write existing.cfg: %v", err)
	}
	if err := removeIfExists("/etc/proxmox-backup/existing.cfg"); err != nil {
		t.Fatalf("removeIfExists existing: %v", err)
	}
	if _, err := fakeFS.Stat("/etc/proxmox-backup/existing.cfg"); err == nil {
		t.Fatalf("expected existing.cfg removed")
	}
}

func TestRemoveIfExists_PropagatesNonExistErrors(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	// Make Remove fail with a non-ENOENT error.
	if err := fakeFS.MkdirAll("/etc/proxmox-backup/dir", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := fakeFS.WriteFile("/etc/proxmox-backup/dir/file", []byte("x"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := removeIfExists("/etc/proxmox-backup/dir"); err == nil {
		t.Fatalf("expected error, got nil")
	}
}
