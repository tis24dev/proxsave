package orchestrator

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// pbsRealDatastoreLayout builds the root of a datastore the way PBS leaves it
// (measured on PBS 4.2.8 and 3.4.9): .chunks with its hash directories, .lock,
// .gc-status after a garbage collection, and a backup type directory. There is
// no .index directory.
func pbsRealDatastoreLayout(t *testing.T) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "ds")
	for _, dir := range []string{".chunks/0000", ".chunks/ffff", "vm/100"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for _, file := range []string{".lock", ".gc-status"} {
		if err := os.WriteFile(filepath.Join(base, file), nil, 0o644); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
	}
	return base
}

func TestValidatePBSDatastoreReadOnlyAcceptsTheRealPBSLayout(t *testing.T) {
	base := pbsRealDatastoreLayout(t)
	if warn := validatePBSDatastoreReadOnly(base); warn != "" {
		t.Fatalf("a datastore laid out by PBS must pass the preflight, got %q", warn)
	}
}

func TestValidatePBSDatastoreReadOnlyStillReportsAMissingChunkStore(t *testing.T) {
	base := pbsRealDatastoreLayout(t)
	if err := os.RemoveAll(filepath.Join(base, ".chunks")); err != nil {
		t.Fatal(err)
	}
	if warn := validatePBSDatastoreReadOnly(base); warn == "" {
		t.Fatal("a datastore without .chunks must fail the preflight")
	}
}

func TestPBSDatastoreHasDataLooksOnlyAtTheChunkStore(t *testing.T) {
	base := pbsRealDatastoreLayout(t)
	has, err := pbsDatastoreHasData(base)
	if err != nil || !has {
		t.Fatalf("pbsDatastoreHasData=%v err=%v; want true for a chunk store with hash directories", has, err)
	}

	// The scaffold earlier ProxSave versions created: an empty .chunks, an
	// .index that PBS never uses, .lock. Something inside .index is not data.
	legacy := filepath.Join(t.TempDir(), "legacy")
	for _, dir := range []string{".chunks", ".index/stray"} {
		if err := os.MkdirAll(filepath.Join(legacy, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(legacy, ".lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	has, err = pbsDatastoreHasData(legacy)
	if err != nil || has {
		t.Fatalf("pbsDatastoreHasData=%v err=%v; want false for the legacy scaffold", has, err)
	}
	unexpected, err := pbsDatastoreHasUnexpectedEntries(legacy)
	if err != nil || unexpected {
		t.Fatalf("pbsDatastoreHasUnexpectedEntries=%v err=%v; the legacy scaffold is not unrelated data", unexpected, err)
	}
}

func TestInitializePBSDatastoreDoesNotCreateAnIndexDirectory(t *testing.T) {
	base := filepath.Join(t.TempDir(), "datastore")
	if _, err := initializePBSDatastore(base, "ds", newDirTestLogger()); err != nil {
		t.Fatalf("initializePBSDatastore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, ".index")); !os.IsNotExist(err) {
		t.Fatalf(".index must not be created, stat err=%v", err)
	}
	if info, err := os.Stat(filepath.Join(base, ".chunks")); err != nil || !info.IsDir() {
		t.Fatalf(".chunks must be created, err=%v", err)
	}
}

// pbsChunkStoreDirCountAtStart is the production value, read before init lowers
// it: creating 65536 directories in every recreation test costs about 30s each.
var pbsChunkStoreDirCountAtStart = pbsChunkStoreDirCount

func init() { pbsChunkStoreDirCount = 16 }

func TestPBSChunkStoreDirsAreTheOnesPBSCreates(t *testing.T) {
	if pbsChunkStoreDirCountAtStart != 0x10000 {
		t.Fatalf("pbsChunkStoreDirCount=%d; PBS creates 65536 hash directories", pbsChunkStoreDirCountAtStart)
	}
	if first, last := pbsChunkStoreDirName(0), pbsChunkStoreDirName(pbsChunkStoreDirCountAtStart-1); first != "0000" || last != "ffff" {
		t.Fatalf("hash directory names %q..%q; PBS uses 0000..ffff", first, last)
	}
}

func TestInitializePBSDatastoreCreatesTheChunkStoreHashDirectories(t *testing.T) {
	base := filepath.Join(t.TempDir(), "datastore")
	changed, err := initializePBSDatastore(base, "ds", newDirTestLogger())
	if err != nil || !changed {
		t.Fatalf("initializePBSDatastore changed=%v err=%v", changed, err)
	}
	entries, err := os.ReadDir(filepath.Join(base, ".chunks"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != pbsChunkStoreDirCount {
		t.Fatalf(".chunks holds %d entries; want %d", len(entries), pbsChunkStoreDirCount)
	}
	for i, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if e.Name() != pbsChunkStoreDirName(i) || !e.IsDir() || info.Mode().Perm() != 0o750 {
			t.Fatalf("entry %d: %s dir=%v mode=%o; want %s dir 0750", i, e.Name(), e.IsDir(), info.Mode().Perm(), pbsChunkStoreDirName(i))
		}
	}
}

// The scaffold earlier versions created, an empty .chunks, is completed: the
// datastore holds no data, so the recreation runs and adds the hash directories.
func TestCreatePBSDatastoreStructureCompletesAnEmptyChunkStore(t *testing.T) {
	base := filepath.Join(t.TempDir(), "datastore")
	for _, dir := range []string{".chunks", ".index"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, ".lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := createPBSDatastoreStructure(base, "ds", newDirTestLogger())
	if err != nil || !changed {
		t.Fatalf("createPBSDatastoreStructure changed=%v err=%v", changed, err)
	}
	entries, err := os.ReadDir(filepath.Join(base, ".chunks"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != pbsChunkStoreDirCount {
		t.Fatalf(".chunks holds %d entries; want %d", len(entries), pbsChunkStoreDirCount)
	}
}

func TestEnsurePBSChunkStoreDirsKeepsExistingAndReportsAFailure(t *testing.T) {
	base := filepath.Join(t.TempDir(), "datastore")
	if err := os.MkdirAll(filepath.Join(base, ".chunks", pbsChunkStoreDirName(0)), 0o750); err != nil {
		t.Fatal(err)
	}
	changed, err := ensurePBSChunkStoreDirs(base, newDirTestLogger())
	if err != nil || !changed {
		t.Fatalf("ensurePBSChunkStoreDirs changed=%v err=%v", changed, err)
	}
	changed, err = ensurePBSChunkStoreDirs(base, newDirTestLogger())
	if err != nil || changed {
		t.Fatalf("second run changed=%v err=%v; want nothing to do", changed, err)
	}

	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.MkdirAll(blocked, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, ".chunks"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ensurePBSChunkStoreDirs(blocked, newDirTestLogger()); err == nil {
		t.Fatal("a .chunks that is not a directory must fail")
	}
}

// The datastore.cfg file writer removes, with each datastore it takes out of
// datastore.cfg, its jobs and acl.cfg entries too, also when the staged file has
// no datastores and the target is removed.
func TestRunPBSDatastoreCfgFile_RemovesJobsAndACL(t *testing.T) {
	for _, staged := range []string{"datastore: ds1\n\tpath /mnt/ds1\n", "# no datastores\n"} {
		origFS := restoreFS
		fakeFS := NewFakeFS()
		restoreFS = fakeFS
		write := func(path, content string) {
			if err := fakeFS.WriteFile(path, []byte(content), 0o640); err != nil {
				t.Fatal(err)
			}
		}
		write("/stage/etc/proxmox-backup/datastore.cfg", staged)
		write("/etc/proxmox-backup/datastore.cfg", "datastore: old\n\tpath /mnt/old\n\ndatastore: ds1\n\tpath /mnt/ds1\n")
		write("/etc/proxmox-backup/prune.cfg", "prune: pj-old\n\tstore old\n\nprune: pj-ds1\n\tstore ds1\n")
		write("/etc/proxmox-backup/acl.cfg", "acl:1:/datastore/old:tst@pbs:DatastoreReader\nacl:1:/datastore/ds1:tst@pbs:DatastoreReader\n")
		if err := runPBSDatastoreCfgFileErr("/stage"); err != nil {
			t.Fatalf("staged=%q: %v", staged, err)
		}
		wantPrune, wantACL := "prune: pj-ds1\n\tstore ds1\n", "acl:1:/datastore/ds1:tst@pbs:DatastoreReader\n"
		if staged == "# no datastores\n" {
			// Every datastore of this PBS is removed with the target file.
			wantPrune, wantACL = "", ""
		}
		prune, _ := fakeFS.ReadFile("/etc/proxmox-backup/prune.cfg")
		acl, _ := fakeFS.ReadFile("/etc/proxmox-backup/acl.cfg")
		restoreFS = origFS
		_ = os.RemoveAll(fakeFS.Root)
		if string(prune) != wantPrune || string(acl) != wantACL {
			t.Fatalf("staged=%q prune=%q acl=%q; want %q and %q", staged, prune, acl, wantPrune, wantACL)
		}
	}
}

// A datastore PBS already initialized, empty as PBS leaves it (.chunks with its
// hash directories and .lock, no backups), is left as it is, and the visible line
// says that, not that it holds data.
func TestCreatePBSDatastoreStructureLeavesAnInitializedDatastore(t *testing.T) {
	base := filepath.Join(t.TempDir(), "ds1")
	for _, dir := range []string{".chunks/0000", ".chunks/ffff"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, ".lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	changed, err := createPBSDatastoreStructure(base, "ds1", logger)
	if err != nil || changed {
		t.Fatalf("createPBSDatastoreStructure changed=%v err=%v; want left as it is", changed, err)
	}
	var visible []string
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		line = regexp.MustCompile(`^\[[^\]]*\] `).ReplaceAllString(line, "")
		if !strings.HasPrefix(line, "DEBUG") {
			visible = append(visible, line)
		}
	}
	want := []string{"INFO     Datastore ds1 is already initialized, its directory is left as it is"}
	if strings.Join(visible, "\n") != strings.Join(want, "\n") {
		t.Fatalf("visible lines\n--- got ---\n%s\n--- want ---\n%s", strings.Join(visible, "\n"), strings.Join(want, "\n"))
	}
	entries, err := os.ReadDir(filepath.Join(base, ".chunks"))
	if err != nil || len(entries) != 2 {
		t.Fatalf(".chunks entries=%d err=%v; nothing may be added", len(entries), err)
	}
}

// A chunk store whose creation stopped half-way (disk or inodes exhausted, the
// run killed) holds some hash directories and no .lock, which PBS always writes:
// the next restore completes it instead of calling it initialized.
func TestCreatePBSDatastoreStructureCompletesAHalfBuiltChunkStore(t *testing.T) {
	base := filepath.Join(t.TempDir(), "ds1")
	for i := range pbsChunkStoreDirCount / 2 {
		if err := os.MkdirAll(filepath.Join(base, ".chunks", pbsChunkStoreDirName(i)), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	changed, err := createPBSDatastoreStructure(base, "ds1", logger)
	if err != nil || !changed {
		t.Fatalf("createPBSDatastoreStructure changed=%v err=%v; want completed", changed, err)
	}
	var visible []string
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		line = regexp.MustCompile(`^\[[^\]]*\] `).ReplaceAllString(line, "")
		if !strings.HasPrefix(line, "DEBUG") {
			visible = append(visible, line)
		}
	}
	want := []string{"INFO     Datastore ds1 initialization is incomplete, its missing directories are created"}
	if strings.Join(visible, "\n") != strings.Join(want, "\n") {
		t.Fatalf("visible lines\n--- got ---\n%s\n--- want ---\n%s", strings.Join(visible, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(buf.String(), "DEBUG    PBS datastore preflight: datastore=ds1 .chunks has entries, .lock missing action=complete") {
		t.Fatalf("missing DEBUG evidence:\n%s", buf.String())
	}
	entries, err := os.ReadDir(filepath.Join(base, ".chunks"))
	if err != nil || len(entries) != pbsChunkStoreDirCount {
		t.Fatalf(".chunks entries=%d err=%v; want %d", len(entries), err, pbsChunkStoreDirCount)
	}
	if _, err := os.Stat(filepath.Join(base, ".lock")); err != nil {
		t.Fatalf(".lock: %v", err)
	}
}
