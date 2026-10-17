package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
)

// Characterization lock for the retention pass of LocalStorage and SecondaryStorage,
// written before their six twin methods (applyGFSRetention, applySimpleRetention,
// deleteBackupInternal, deleteAssociatedLog, countLogFiles, pruneOrphanLogs) are moved
// into one shared function. Every case runs on BOTH backends, on a real directory tree,
// and pins four things:
//
//   - the exact set of files left on disk (archives, sidecars, logs, and the OTHER
//     destination's log folder, which must never be touched);
//   - the return values (deleted count, error identity);
//   - the RetentionSummary LastRetentionSummary returns, which carries every retTally
//     field, plus retTally.removed;
//   - the full log output at every level, line by line, in one golden file per case
//     and backend.
//
// The goldens record what the code does TODAY, quirks included, and the DEBUG wording
// that differs between the two backends is part of what they record. A golden that has
// to change is a behaviour change and must be justified as one: the centralization
// must leave every one of them byte for byte as it is.
// Regenerate deliberately with: go test ./internal/storage/ -run TestRetentionCharacterization -update
//
// Two sources of nondeterminism are handled here, not papered over:
//
//   - applyGFSRetention deletes while ranging over a map, so the per-backup blocks of
//     its delete loop come out in random order. charSortLoopBlocks cuts that loop
//     region into one block per backup (from its "Deleting old backup:" line to the
//     next) and sorts the blocks; the order INSIDE a block, and every line outside the
//     loop, stay exactly as written.
//   - ClassifyBackupsGFS reads time.Now(). Every fixture is dated in 2022-2024, so
//     every backup is strictly before the current ISO week, month and year whenever
//     these tests run: the classification depends on the fixture alone. All fixture
//     times are 12:00 local wall clock, so the "created:" text, the ISO week and the
//     month of a backup are the same in every time zone and on DST days.
//
// The tests mutate package globals (retentionHostname during construction, the fake
// process table of safefs.HeldOpenIn), so none of them runs in parallel.
var updateRetentionGoldens = flag.Bool("update", false, "rewrite retention characterization golden files")

// charHost is the hostname this machine answers to in every case.
const charHost = "pve"

// charDeletingLine opens the per-backup block of both delete loops, in both backends.
const charDeletingLine = "Deleting old backup: "

// charTS is the run timestamp of a fixture day: "2024-06-20" -> "20240620-120000".
func charTS(day string) string { return strings.ReplaceAll(day, "-", "") + "-120000" }

func charArchiveName(host, day string) string { return host + "-backup-" + charTS(day) + ".tar.zst" }

func charLogName(host, day string) string { return "backup-" + host + "-" + charTS(day) + ".log" }

// charWhen is 12:00 local wall clock on day.
func charWhen(t *testing.T, day string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatalf("bad fixture day %q: %v", day, err)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, time.Local)
}

// Paths relative to the env root, as charEnv.snapshot lists them.
func charArc(day string) string { return "backups/" + charArchiveName(charHost, day) }

// charSet is every file a plain verified backup with a manifest has on disk.
func charSet(day string) []string {
	a := charArc(day)
	return []string{a, a + ".sha256", a + ".metadata"}
}

func charLogRel(dir, day string) string { return dir + "/" + charLogName(charHost, day) }

func charJoin(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

type charLog int

const (
	charNoLog       charLog = iota
	charLogFile             // a regular log file
	charLogHeldOpen         // a regular log file another process holds open
	charLogBlocked          // a non-empty directory under the log's name: its removal fails
)

// charBackup is one archive of the fixture.
type charBackup struct {
	day            string
	host           string // "" is charHost
	inert          bool   // no .sha256 beside it: not verified, retention leaves it alone
	noMetadata     bool   // no .metadata beside it: owner and date come from the name and the mtime
	archiveBlocked bool   // the archive is a non-empty directory: its removal fails
	sidecarBlocked bool   // the .sha256 is a non-empty directory: only that removal fails
	log            charLog
}

// charEnv is one case's directory tree: <root>/backups, the destination's log folder
// (absent when the destination has none), and <root>/decoy, the OTHER destination's
// log folder, which holds a copy of every log and must come out untouched.
type charEnv struct {
	t           *testing.T
	root        string
	backups     string
	logs        string
	decoy       string
	held        []string
	procMissing bool
	ownLog      string
	// hostname is what the backend's constructor reads as this machine's name.
	hostname string
}

func newCharEnv(t *testing.T, logDirName string) *charEnv {
	t.Helper()
	root := t.TempDir()
	e := &charEnv{t: t, root: root, backups: filepath.Join(root, "backups"), decoy: filepath.Join(root, "decoy"), hostname: charHost}
	dirs := []string{e.backups, e.decoy}
	if logDirName != "" {
		e.logs = filepath.Join(root, logDirName)
		dirs = append(dirs, e.logs)
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	return e
}

func charMkdirBlocked(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "blocker"), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func (e *charEnv) addBackup(b charBackup) {
	t := e.t
	t.Helper()
	host := b.host
	if host == "" {
		host = charHost
	}
	when := charWhen(t, b.day)
	archive := filepath.Join(e.backups, charArchiveName(host, b.day))
	if b.archiveBlocked {
		charMkdirBlocked(t, archive)
	} else {
		writeTestFile(t, archive, "archive")
	}
	if !b.noMetadata {
		data, err := json.Marshal(backup.Manifest{Hostname: host, CreatedAt: when, ProxmoxType: "pve"})
		if err != nil {
			t.Fatalf("marshal manifest: %v", err)
		}
		writeTestFile(t, archive+".metadata", string(data))
	}
	if !b.inert {
		if b.sidecarBlocked {
			charMkdirBlocked(t, archive+".sha256")
		} else {
			writeTestFile(t, archive+".sha256", "sum")
		}
	}
	if err := os.Chtimes(archive, when, when); err != nil {
		t.Fatalf("chtimes %s: %v", archive, err)
	}
	e.addLog(host, b.day, b.log)
}

// addLog writes the log of host's run on day into the destination's log folder (when
// it has one and state is not charNoLog) and, always, into the decoy folder.
func (e *charEnv) addLog(host, day string, state charLog) {
	t := e.t
	t.Helper()
	name := charLogName(host, day)
	writeTestFile(t, filepath.Join(e.decoy, name), "decoy")
	if e.logs == "" || state == charNoLog {
		return
	}
	path := filepath.Join(e.logs, name)
	if state == charLogBlocked {
		charMkdirBlocked(t, path)
		return
	}
	writeTestFile(t, path, "log")
	if state == charLogHeldOpen {
		e.held = append(e.held, path)
	}
}

// addLogFolderFile writes a file that is no run log into the destination's log folder.
func (e *charEnv) addLogFolderFile(name string) {
	writeTestFile(e.t, filepath.Join(e.logs, name), "not a run log")
}

// seal installs the process table the open-log checks read.
func (e *charEnv) seal() {
	t := e.t
	t.Helper()
	if e.procMissing {
		t.Cleanup(safefs.SetProcRootForTest(filepath.Join(e.root, "proc-missing")))
	}
	if len(e.held) > 0 {
		holdOpenForTest(t, e.held...)
	}
}

// snapshot lists every file and directory under the root, relative, directories with a
// trailing slash.
func (e *charEnv) snapshot() map[string]bool {
	t := e.t
	t.Helper()
	files := map[string]bool{}
	err := filepath.WalkDir(e.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == e.root {
			return nil
		}
		rel, err := filepath.Rel(e.root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			rel += "/"
		}
		files[rel] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", e.root, err)
	}
	return files
}

// checkFiles asserts that exactly the paths in gone have left the tree since before,
// and that nothing appeared.
func (e *charEnv) checkFiles(before map[string]bool, gone []string) {
	t := e.t
	t.Helper()
	want := map[string]bool{}
	for k := range before {
		want[k] = true
	}
	for _, g := range gone {
		if !before[g] {
			t.Fatalf("fixture error: %s is expected to go but was never there", g)
		}
		delete(want, g)
	}
	got := e.snapshot()
	var missing, extra []string
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("files on disk differ\n removed but expected to stay: %v\n present but expected gone (or new): %v", missing, extra)
	}
}

// charBackend is what both backends expose to these tests.
type charBackend interface {
	ApplyRetention(ctx context.Context, cfg RetentionConfig) (int, error)
	Delete(ctx context.Context, backupFile string) error
	LastRetentionSummary() RetentionSummary
}

type charFlavor struct {
	name  string
	build func(t *testing.T, e *charEnv, logger *logging.Logger) charBackend
}

// withCharHostname constructs a backend that answers to host. The hostname is read
// once, by the constructor.
func withCharHostname(t *testing.T, host string, build func() (charBackend, error)) charBackend {
	t.Helper()
	orig := retentionHostname
	retentionHostname = func() (string, error) { return host, nil }
	defer func() { retentionHostname = orig }()
	b, err := build()
	if err != nil {
		t.Fatalf("construct backend: %v", err)
	}
	return b
}

// charFlavors are the two backends. Each is pointed at the env's log folder for ITS
// variable (LOG_PATH, SECONDARY_LOG_PATH) and at the decoy folder for the other one.
var charFlavors = []charFlavor{
	{name: "local", build: func(t *testing.T, e *charEnv, logger *logging.Logger) charBackend {
		cfg := &config.Config{BackupPath: e.backups, LogPath: e.logs, SecondaryLogPath: e.decoy}
		return withCharHostname(t, e.hostname, func() (charBackend, error) { return NewLocalStorage(cfg, logger, "") })
	}},
	{name: "secondary", build: func(t *testing.T, e *charEnv, logger *logging.Logger) charBackend {
		cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: e.backups, SecondaryLogPath: e.logs, LogPath: e.decoy}
		return withCharHostname(t, e.hostname, func() (charBackend, error) { return NewSecondaryStorage(cfg, logger, "") })
	}},
}

func charTally(t *testing.T, b charBackend) *retentionTally {
	t.Helper()
	switch s := b.(type) {
	case *LocalStorage:
		return &s.retTally
	case *SecondaryStorage:
		return &s.retTally
	}
	t.Fatalf("unexpected backend %T", b)
	return nil
}

// checkRemoved asserts retTally.removed, as archive base names in any order.
func checkRemoved(t *testing.T, b charBackend, want []string) {
	t.Helper()
	var got []string
	for _, f := range charTally(t, b).removed {
		got = append(got, filepath.Base(f))
	}
	sort.Strings(got)
	w := append([]string(nil), want...)
	sort.Strings(w)
	if strings.Join(got, "\n") != strings.Join(w, "\n") {
		t.Errorf("retTally.removed = %v, want %v", got, w)
	}
}

// charSink captures the logger output. Armed, it cancels a context when the at-th line
// containing trigger is written, which is how a case cancels in the middle of the
// delete loop without any hook in production code.
type charSink struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	trigger string
	at      int
	seen    int
	cancel  context.CancelFunc
}

func (s *charSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil && bytes.Contains(p, []byte(s.trigger)) {
		s.seen++
		if s.seen == s.at {
			s.cancel()
		}
	}
	return s.buf.Write(p)
}

func (s *charSink) arm(trigger string, at int, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trigger, s.at, s.seen, s.cancel = trigger, at, 0, cancel
}

func (s *charSink) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.Reset()
}

// mark writes a separator line between two calls of one case.
func (s *charSink) mark(label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.WriteString("--- " + label + " ---\n")
}

func (s *charSink) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func newCharLogger() (*logging.Logger, *charSink) {
	sink := &charSink{}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(sink)
	return logger, sink
}

var (
	charStampRE    = regexp.MustCompile(`^\[[^\]]*\] `)
	charDurationRE = regexp.MustCompile(`duration=[^)]*\)`)
)

// charNormalize drops the timestamp of every line, writes the env root as <ROOT> and
// every duration as <D>. Nothing else is touched.
func charNormalize(raw, root string) []string {
	raw = strings.TrimSuffix(raw, "\n")
	if raw == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = charStampRE.ReplaceAllString(line, "")
		line = strings.ReplaceAll(line, root, "<ROOT>")
		line = charDurationRE.ReplaceAllString(line, "duration=<D>)")
		out = append(out, line)
	}
	return out
}

// charSortLoopBlocks sorts the per-backup blocks of the GFS delete loop (see the file
// comment). The loop region runs from the first "Deleting old backup:" line to the
// line that closes the pass: the "retention applied" summary or, when the pass is
// abandoned, the "End" line of ApplyRetention.
func charSortLoopBlocks(lines []string) []string {
	start := -1
	for i, l := range lines {
		if strings.Contains(l, charDeletingLine) {
			start = i
			break
		}
	}
	if start < 0 {
		return lines
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if strings.Contains(lines[i], "storage retention applied: ") || strings.HasPrefix(lines[i], "DEBUG    End ") {
			end = i
			break
		}
	}
	var blocks [][]string
	for i := start; i < end; i++ {
		if strings.Contains(lines[i], charDeletingLine) {
			blocks = append(blocks, nil)
		}
		blocks[len(blocks)-1] = append(blocks[len(blocks)-1], lines[i])
	}
	sort.SliceStable(blocks, func(a, b int) bool { return blocks[a][0] < blocks[b][0] })
	out := append([]string(nil), lines[:start]...)
	for _, b := range blocks {
		out = append(out, b...)
	}
	return append(out, lines[end:]...)
}

func assertRetentionGolden(t *testing.T, name string, lines []string) {
	t.Helper()
	got := strings.Join(lines, "\n") + "\n"
	path := filepath.Join("testdata", "retention_characterization", name+".golden")
	if *updateRetentionGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
		return
	}
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create): %v", name, err)
	}
	want := string(wantBytes)
	if want == got {
		return
	}
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	first := 0
	for first < len(wl) && first < len(gl) && wl[first] == gl[first] {
		first++
	}
	var wantLine, gotLine string
	if first < len(wl) {
		wantLine = wl[first]
	}
	if first < len(gl) {
		gotLine = gl[first]
	}
	t.Errorf("log golden mismatch for %s, first difference at line %d\n want: %q\n  got: %q\n--- want ---\n%s--- got ---\n%s",
		name, first+1, wantLine, gotLine, want, got)
}

// charWant is everything a case pins besides the log.
type charWant struct {
	deleted int
	// err is the sentinel the call returns, compared by identity; nil means none.
	err error
	// errText, when set, replaces err: the call returns an error whose text, with the
	// root written as <ROOT>, is errText, and which wraps errIs when that is set.
	errText string
	errIs   error
	// gone are the paths, relative to the env root, that the call removes.
	gone []string
	// removed are the base names of the archives retTally.removed lists.
	removed []string
	summary RetentionSummary
}

type charScenario struct {
	name string
	// logDir is the destination's log folder under the root: "logs", "logs[" (a name
	// the log count's glob cannot parse) or "" (the variable is not set).
	logDir   string
	policy   RetentionConfig
	setup    func(e *charEnv)
	cancelAt int // cancel during the call, when the cancelAt-th cancelOn line is written
	// cancelOn is the text of the line cancelAt counts; "" is "Deleting old backup:".
	cancelOn string
	// preCancel cancels the context before the call.
	preCancel bool
	// act replaces ApplyRetention(policy) as the call under test.
	act  func(ctx context.Context, b charBackend, e *charEnv) (int, error)
	want func(flavor string) charWant
}

func runCharScenario(t *testing.T, sc charScenario, fl charFlavor) {
	t.Helper()
	e := newCharEnv(t, sc.logDir)
	sc.setup(e)
	e.seal()
	logger, sink := newCharLogger()
	if e.ownLog != "" {
		if err := logger.OpenLogFile(filepath.Join(e.logs, e.ownLog)); err != nil {
			t.Fatalf("open own log: %v", err)
		}
		t.Cleanup(func() { _ = logger.CloseLogFile() })
	}
	before := e.snapshot()
	backend := fl.build(t, e, logger)
	sink.reset()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if sc.cancelAt > 0 {
		trigger := sc.cancelOn
		if trigger == "" {
			trigger = charDeletingLine
		}
		sink.arm(trigger, sc.cancelAt, cancel)
	}
	if sc.preCancel {
		cancel()
	}
	var deleted int
	var err error
	if sc.act != nil {
		deleted, err = sc.act(ctx, backend, e)
	} else {
		deleted, err = backend.ApplyRetention(ctx, sc.policy)
	}

	want := sc.want(fl.name)
	if deleted != want.deleted {
		t.Errorf("deleted = %d, want %d", deleted, want.deleted)
	}
	switch {
	case want.errText != "":
		if err == nil || strings.ReplaceAll(err.Error(), e.root, "<ROOT>") != want.errText {
			t.Errorf("err = %v, want text %q", err, want.errText)
		}
		if want.errIs != nil && !errors.Is(err, want.errIs) {
			t.Errorf("err = %v, want it to wrap %v", err, want.errIs)
		}
	case err != want.err:
		t.Errorf("err = %v (%T), want %v", err, err, want.err)
	}
	e.checkFiles(before, want.gone)
	checkRemoved(t, backend, want.removed)
	if got := backend.LastRetentionSummary(); got != want.summary {
		t.Errorf("LastRetentionSummary\n got %+v\nwant %+v", got, want.summary)
	}
	assertRetentionGolden(t, sc.name+"."+fl.name, charSortLoopBlocks(charNormalize(sink.text(), e.root)))
}

func TestRetentionCharacterization(t *testing.T) {
	for _, sc := range charScenarios() {
		for _, fl := range charFlavors {
			t.Run(sc.name+"/"+fl.name, func(t *testing.T) { runCharScenario(t, sc, fl) })
		}
	}
}

// charGFS is the GFS policy of the failure, no-log and cancel cases: one daily, one
// yearly, nothing weekly or monthly, so with every fixture in 2024 the newest backup is
// the daily, the next one the yearly, and every older one is deleted.
var charGFS = RetentionConfig{Policy: "gfs", Daily: 1, Weekly: 0, Monthly: 0, Yearly: 1}

// charFailureBackups is the failure fixture shared by the GFS and the simple case:
//   - 2024-06-20 kept;
//   - 2024-06-01 its archive cannot be removed: Not deleted, its sidecars and its log stay;
//   - 2024-05-01 its .sha256 cannot be removed: Left behind, counted deleted, its log goes;
//   - 2024-04-01 its log cannot be removed: Log not deleted;
//   - 2024-03-01 deleted, it has no log.
func charFailureBackups(e *charEnv, withYearly bool) {
	e.addBackup(charBackup{day: "2024-06-20", log: charLogFile})
	if withYearly {
		e.addBackup(charBackup{day: "2024-06-10"})
	}
	e.addBackup(charBackup{day: "2024-06-01", archiveBlocked: true, log: charLogFile})
	e.addBackup(charBackup{day: "2024-05-01", sidecarBlocked: true, log: charLogFile})
	e.addBackup(charBackup{day: "2024-04-01", log: charLogBlocked})
	e.addBackup(charBackup{day: "2024-03-01"})
}

// charFailureGone is what the failure fixture loses.
func charFailureGone() []string {
	leftBehind := charArc("2024-05-01")
	return charJoin(
		[]string{leftBehind, leftBehind + ".metadata", charLogRel("logs", "2024-05-01")},
		charSet("2024-04-01"),
		charSet("2024-03-01"),
	)
}

func charScenarios() []charScenario {
	return []charScenario{
		{
			// GFS with backups in every tier, one inert backup, one backup of another
			// host, the three log states of a deleted backup (present, absent, held
			// open by another process) and the orphan-log pass: an expired orphan is
			// deleted, an expired orphan held open stays, a newer orphan stays, the
			// other host's log and a file that is no run log are not looked at.
			//
			// daily=2 weekly=2 monthly=2 yearly=2 classifies:
			//   06-20 06-19 daily; 06-18 (W25) 06-12 (W24) weekly; 06-17 2024-06,
			//   05-15 2024-05 monthly; 06-05 2024, 2023-12-01 2023 yearly;
			//   04-10, 2023-11-01, 2022-01-10 delete.
			name:   "gfs_mix",
			logDir: "logs",
			policy: RetentionConfig{Policy: "gfs", Daily: 2, Weekly: 2, Monthly: 2, Yearly: 2},
			setup: func(e *charEnv) {
				e.addBackup(charBackup{day: "2024-06-20", log: charLogFile})
				e.addBackup(charBackup{day: "2024-06-19", log: charLogFile})
				e.addBackup(charBackup{day: "2024-06-18", log: charLogFile})
				e.addBackup(charBackup{day: "2024-06-17"})
				e.addBackup(charBackup{day: "2024-06-12"})
				e.addBackup(charBackup{day: "2024-06-05"})
				e.addBackup(charBackup{day: "2024-05-15"})
				e.addBackup(charBackup{day: "2024-04-10", log: charLogFile})
				e.addBackup(charBackup{day: "2024-03-01", inert: true, log: charLogFile})
				e.addBackup(charBackup{day: "2023-12-01"})
				e.addBackup(charBackup{day: "2023-11-01"})
				e.addBackup(charBackup{day: "2022-01-10", log: charLogHeldOpen})
				e.addBackup(charBackup{day: "2022-06-01", host: "other"})
				e.addLog(charHost, "2023-06-01", charLogFile)
				e.addLog(charHost, "2023-03-01", charLogHeldOpen)
				e.addLog(charHost, "2024-01-01", charLogFile)
				e.addLog("other", "2023-01-01", charLogFile)
				e.addLogFolderFile("notes.txt")
			},
			want: func(string) charWant {
				return charWant{
					deleted: 3,
					gone: charJoin(
						charSet("2024-04-10"), []string{charLogRel("logs", "2024-04-10")},
						charSet("2023-11-01"),
						charSet("2022-01-10"),
						[]string{charLogRel("logs", "2023-06-01")},
					),
					removed: []string{charArchiveName(charHost, "2024-04-10"), charArchiveName(charHost, "2023-11-01"), charArchiveName(charHost, "2022-01-10")},
					summary: RetentionSummary{
						BackupsDeleted: 3, BackupsRemaining: 8, LogsDeleted: 2, LogsRemaining: 8, HasLogInfo: true,
						ScopeValid: true, Owned: 9, PassCompleted: true,
						Planned: 3, Skipped: 1, HasLogPath: true,
					},
				}
			},
		},
		{
			// GFS deletes four: one archive cannot be removed, one sidecar cannot be
			// removed, one log cannot be removed, one has no log.
			name:   "gfs_failures",
			logDir: "logs",
			policy: charGFS,
			setup:  func(e *charEnv) { charFailureBackups(e, true) },
			want: func(string) charWant {
				return charWant{
					deleted: 3,
					gone:    charFailureGone(),
					removed: []string{charArchiveName(charHost, "2024-05-01"), charArchiveName(charHost, "2024-04-01"), charArchiveName(charHost, "2024-03-01")},
					summary: RetentionSummary{
						BackupsDeleted: 3, BackupsRemaining: 3, LogsDeleted: 1, LogsRemaining: 3, HasLogInfo: true,
						ScopeValid: true, Owned: 3, PassCompleted: true,
						Planned: 4, NotDeleted: 1, LeftBehind: 1, LogsNotDeleted: 1, HasLogPath: true,
					},
				}
			},
		},
		{
			// GFS with the log folder variable not set: no log is looked for, the decoy
			// folder of the other destination keeps every log, and the summary claims
			// log info with 0 logs remaining.
			name:   "gfs_no_log_path",
			logDir: "",
			policy: charGFS,
			setup: func(e *charEnv) {
				for _, day := range []string{"2024-06-20", "2024-06-10", "2024-05-01", "2024-04-01"} {
					e.addBackup(charBackup{day: day, log: charLogFile})
				}
			},
			want: func(string) charWant {
				return charWant{
					deleted: 2,
					gone:    charJoin(charSet("2024-05-01"), charSet("2024-04-01")),
					removed: []string{charArchiveName(charHost, "2024-05-01"), charArchiveName(charHost, "2024-04-01")},
					summary: RetentionSummary{
						BackupsDeleted: 2, BackupsRemaining: 2, LogsDeleted: 0, HasLogInfo: false,
						ScopeValid: true, Owned: 2, PassCompleted: true, Planned: 2,
					},
				}
			},
		},
		{
			// GFS with a log folder whose name the log count's glob cannot parse: the
			// count fails, the summary has no log info, the logs are still deleted.
			name:   "gfs_log_count_unavailable",
			logDir: "logs[",
			policy: charGFS,
			setup: func(e *charEnv) {
				for _, day := range []string{"2024-06-20", "2024-06-10", "2024-05-01", "2024-04-01"} {
					e.addBackup(charBackup{day: day, log: charLogFile})
				}
			},
			want: func(string) charWant {
				return charWant{
					deleted: 2,
					gone: charJoin(
						charSet("2024-05-01"), []string{charLogRel("logs[", "2024-05-01")},
						charSet("2024-04-01"), []string{charLogRel("logs[", "2024-04-01")},
					),
					removed: []string{charArchiveName(charHost, "2024-05-01"), charArchiveName(charHost, "2024-04-01")},
					summary: RetentionSummary{
						BackupsDeleted: 2, BackupsRemaining: 2, LogsDeleted: 2, LogsRemaining: 0, HasLogInfo: false,
						ScopeValid: true, Owned: 2, PassCompleted: true, Planned: 2, HasLogPath: true,
					},
				}
			},
		},
		{
			// Simple over the limit (5 eligible, limit 3), with an inert backup, a
			// backup without .metadata (local: "No metadata, name used"), a deleted
			// backup with its log and one without, an expired orphan log (deleted) and
			// a newer one (kept).
			name:   "simple_over_limit",
			logDir: "logs",
			policy: RetentionConfig{Policy: "simple", MaxBackups: 3},
			setup: func(e *charEnv) {
				e.addBackup(charBackup{day: "2024-06-20", log: charLogFile})
				e.addBackup(charBackup{day: "2024-06-19"})
				e.addBackup(charBackup{day: "2024-06-18", log: charLogFile})
				e.addBackup(charBackup{day: "2024-06-17", log: charLogFile})
				e.addBackup(charBackup{day: "2024-06-16", noMetadata: true})
				e.addBackup(charBackup{day: "2024-06-01", inert: true, log: charLogFile})
				e.addLog(charHost, "2024-05-01", charLogFile)
				e.addLog(charHost, "2024-06-10", charLogFile)
			},
			want: func(flavor string) charWant {
				noMeta := charArc("2024-06-16")
				w := charWant{
					deleted: 2,
					gone: charJoin(
						charSet("2024-06-17"), []string{charLogRel("logs", "2024-06-17")},
						[]string{noMeta, noMeta + ".sha256"},
						[]string{charLogRel("logs", "2024-05-01")},
					),
					removed: []string{charArchiveName(charHost, "2024-06-17"), charArchiveName(charHost, "2024-06-16")},
					summary: RetentionSummary{
						BackupsDeleted: 2, BackupsRemaining: 3, LogsDeleted: 2, LogsRemaining: 4, HasLogInfo: true,
						ScopeValid: true, Owned: 4, PassCompleted: true,
						Planned: 2, Skipped: 1, HasLogPath: true,
					},
				}
				if flavor == "local" {
					w.summary.NoMetadata = 1
				}
				return w
			},
		},
		{
			// Simple within the limit: nothing is deleted, yet the orphan-log pass
			// runs, deletes an expired orphan and keeps the log this run is writing.
			name:   "simple_within_limit",
			logDir: "logs",
			policy: RetentionConfig{Policy: "simple", MaxBackups: 5},
			setup: func(e *charEnv) {
				e.addBackup(charBackup{day: "2024-06-20", log: charLogFile})
				e.addBackup(charBackup{day: "2024-06-19"})
				e.addBackup(charBackup{day: "2024-06-18", log: charLogFile})
				e.addLog(charHost, "2024-02-01", charLogFile)
				e.ownLog = charLogName(charHost, "2024-01-01")
			},
			want: func(string) charWant {
				return charWant{
					gone: []string{charLogRel("logs", "2024-02-01")},
					summary: RetentionSummary{
						LogsDeleted: 1, ScopeValid: true, Owned: 3, PassCompleted: true, HasLogPath: true,
					},
				}
			},
		},
		{
			// Simple with retention disabled (MaxBackups 0): no backup is deleted and
			// the inert backup is not even reported, but the orphan-log pass still
			// deletes an expired orphan.
			name:   "simple_disabled",
			logDir: "logs",
			policy: RetentionConfig{Policy: "simple", MaxBackups: 0},
			setup: func(e *charEnv) {
				e.addBackup(charBackup{day: "2024-06-20", log: charLogFile})
				e.addBackup(charBackup{day: "2024-06-19", log: charLogFile})
				e.addBackup(charBackup{day: "2024-06-01", inert: true})
				e.addLog(charHost, "2024-01-01", charLogFile)
			},
			want: func(string) charWant {
				return charWant{
					gone: []string{charLogRel("logs", "2024-01-01")},
					summary: RetentionSummary{
						LogsDeleted: 1, ScopeValid: true, Owned: 3, PassCompleted: true, HasLogPath: true,
					},
				}
			},
		},
		{
			// Simple deletes four (limit 1), oldest first: the same failures as
			// gfs_failures, in a deterministic order.
			name:   "simple_failures",
			logDir: "logs",
			policy: RetentionConfig{Policy: "simple", MaxBackups: 1},
			setup:  func(e *charEnv) { charFailureBackups(e, false) },
			want: func(string) charWant {
				return charWant{
					deleted: 3,
					gone:    charFailureGone(),
					removed: []string{charArchiveName(charHost, "2024-05-01"), charArchiveName(charHost, "2024-04-01"), charArchiveName(charHost, "2024-03-01")},
					summary: RetentionSummary{
						BackupsDeleted: 3, BackupsRemaining: 2, LogsDeleted: 1, LogsRemaining: 3, HasLogInfo: true,
						ScopeValid: true, Owned: 2, PassCompleted: true,
						Planned: 4, NotDeleted: 1, LeftBehind: 1, LogsNotDeleted: 1, HasLogPath: true,
					},
				}
			},
		},
		{
			// Simple with the log folder variable not set.
			name:   "simple_no_log_path",
			logDir: "",
			policy: RetentionConfig{Policy: "simple", MaxBackups: 1},
			setup: func(e *charEnv) {
				for _, day := range []string{"2024-06-20", "2024-06-10", "2024-06-01"} {
					e.addBackup(charBackup{day: day, log: charLogFile})
				}
			},
			want: func(string) charWant {
				return charWant{
					deleted: 2,
					gone:    charJoin(charSet("2024-06-10"), charSet("2024-06-01")),
					removed: []string{charArchiveName(charHost, "2024-06-10"), charArchiveName(charHost, "2024-06-01")},
					summary: RetentionSummary{
						BackupsDeleted: 2, BackupsRemaining: 1, LogsDeleted: 0, HasLogInfo: false,
						ScopeValid: true, Owned: 1, PassCompleted: true, Planned: 2,
					},
				}
			},
		},
		{
			// Simple with a log folder the log count's glob cannot parse.
			name:   "simple_log_count_unavailable",
			logDir: "logs[",
			policy: RetentionConfig{Policy: "simple", MaxBackups: 1},
			setup: func(e *charEnv) {
				for _, day := range []string{"2024-06-20", "2024-06-10", "2024-06-01"} {
					e.addBackup(charBackup{day: day, log: charLogFile})
				}
			},
			want: func(string) charWant {
				return charWant{
					deleted: 2,
					gone: charJoin(
						charSet("2024-06-10"), []string{charLogRel("logs[", "2024-06-10")},
						charSet("2024-06-01"), []string{charLogRel("logs[", "2024-06-01")},
					),
					removed: []string{charArchiveName(charHost, "2024-06-10"), charArchiveName(charHost, "2024-06-01")},
					summary: RetentionSummary{
						BackupsDeleted: 2, BackupsRemaining: 1, LogsDeleted: 2, HasLogInfo: false,
						ScopeValid: true, Owned: 1, PassCompleted: true, Planned: 2, HasLogPath: true,
					},
				}
			},
		},
		{
			// Simple, cancelled while the second of three deletions starts: the first
			// backup is gone with its log, the second is counted not deleted, the
			// third is never reached, the pass returns context.Canceled and publishes
			// no counts.
			name:     "simple_cancel",
			logDir:   "logs",
			policy:   RetentionConfig{Policy: "simple", MaxBackups: 1},
			cancelAt: 2,
			setup: func(e *charEnv) {
				for _, day := range []string{"2024-06-20", "2024-06-10", "2024-06-01", "2024-05-01"} {
					e.addBackup(charBackup{day: day, log: charLogFile})
				}
			},
			want: func(string) charWant {
				return charWant{
					deleted: 1,
					err:     context.Canceled,
					gone:    charJoin(charSet("2024-05-01"), []string{charLogRel("logs", "2024-05-01")}),
					removed: []string{charArchiveName(charHost, "2024-05-01")},
					summary: RetentionSummary{
						ScopeValid: true, Owned: 3, PassCompleted: false, Planned: 3, NotDeleted: 1,
					},
				}
			},
		},
		{
			// Simple when the open logs cannot be read: the deleted backup's log is
			// kept, and the orphan-log pass deletes nothing.
			name:   "simple_open_check_failed",
			logDir: "logs",
			policy: RetentionConfig{Policy: "simple", MaxBackups: 1},
			setup: func(e *charEnv) {
				e.addBackup(charBackup{day: "2024-06-20", log: charLogFile})
				e.addBackup(charBackup{day: "2024-06-10", log: charLogFile})
				e.addLog(charHost, "2024-01-01", charLogFile)
				e.procMissing = true
			},
			want: func(string) charWant {
				return charWant{
					deleted: 1,
					gone:    charSet("2024-06-10"),
					removed: []string{charArchiveName(charHost, "2024-06-10")},
					summary: RetentionSummary{
						BackupsDeleted: 1, BackupsRemaining: 1, LogsRemaining: 3, HasLogInfo: true,
						ScopeValid: true, Owned: 1, PassCompleted: true, Planned: 1, HasLogPath: true,
					},
				}
			},
		},
		{
			// Simple, cancelled when the orphan-log pass starts listing: the archive
			// pass has published its counts, the orphan is left, the pass returns
			// context.Canceled and is not completed.
			name:     "simple_cancel_in_orphan_pass",
			logDir:   "logs",
			policy:   RetentionConfig{Policy: "simple", MaxBackups: 1},
			cancelAt: 1,
			cancelOn: "orphan logs: scan path=",
			setup: func(e *charEnv) {
				e.addBackup(charBackup{day: "2024-06-20", log: charLogFile})
				e.addBackup(charBackup{day: "2024-06-10", log: charLogFile})
				e.addLog(charHost, "2024-01-01", charLogFile)
			},
			want: func(string) charWant {
				return charWant{
					deleted: 1,
					err:     context.Canceled,
					gone:    charJoin(charSet("2024-06-10"), []string{charLogRel("logs", "2024-06-10")}),
					removed: []string{charArchiveName(charHost, "2024-06-10")},
					summary: RetentionSummary{
						BackupsDeleted: 1, BackupsRemaining: 1, LogsDeleted: 1, LogsRemaining: 2, HasLogInfo: true,
						ScopeValid: true, Owned: 1, PassCompleted: false, Planned: 1, HasLogPath: true,
					},
				}
			},
		},
		{
			// ApplyRetention with the context already cancelled: nothing is listed and
			// no scope is published.
			name:      "apply_cancelled_before_start",
			logDir:    "logs",
			policy:    RetentionConfig{Policy: "simple", MaxBackups: 1},
			preCancel: true,
			setup: func(e *charEnv) {
				e.addBackup(charBackup{day: "2024-06-20", log: charLogFile})
				e.addBackup(charBackup{day: "2024-06-10", log: charLogFile})
			},
			want: func(string) charWant { return charWant{err: context.Canceled} },
		},
		{
			// Only another host's backup is here: the pass ends before the policy and
			// before the orphan-log pass, so an expired orphan stays.
			name:   "apply_no_owned_backups",
			logDir: "logs",
			policy: RetentionConfig{Policy: "simple", MaxBackups: 1},
			setup: func(e *charEnv) {
				e.addBackup(charBackup{day: "2024-06-20", host: "other"})
				e.addLog(charHost, "2024-01-01", charLogFile)
			},
			want: func(string) charWant {
				return charWant{summary: RetentionSummary{ScopeValid: true, PassCompleted: true}}
			},
		},
		{
			// The backup folder cannot be listed (its name is not a valid glob).
			name:   "apply_list_fails",
			logDir: "logs",
			policy: RetentionConfig{Policy: "simple", MaxBackups: 1},
			setup: func(e *charEnv) {
				e.backups = filepath.Join(e.root, "backups[")
				if err := os.MkdirAll(e.backups, 0o755); err != nil {
					e.t.Fatalf("mkdir: %v", err)
				}
				e.addBackup(charBackup{day: "2024-06-20", log: charLogFile})
				e.addBackup(charBackup{day: "2024-06-10", log: charLogFile})
			},
			want: func(flavor string) charWant {
				if flavor == "local" {
					return charWant{errText: "primary storage apply_retention operation failed for <ROOT>/backups[ (recoverable): primary storage list operation failed for <ROOT>/backups[: syntax error in pattern"}
				}
				return charWant{errText: "secondary storage apply_retention operation failed for <ROOT>/backups[ (recoverable): list: syntax error in pattern"}
			},
		},
		{
			// This machine cannot name itself: no backup is attributed to it and the
			// pass does not start.
			name:   "apply_hostname_unresolved",
			logDir: "logs",
			policy: RetentionConfig{Policy: "simple", MaxBackups: 1},
			setup: func(e *charEnv) {
				e.hostname = ""
				e.addBackup(charBackup{day: "2024-06-20", log: charLogFile})
				e.addBackup(charBackup{day: "2024-06-10", log: charLogFile})
			},
			want: func(flavor string) charWant {
				if flavor == "local" {
					return charWant{errText: "primary storage apply_retention operation failed for <ROOT>/backups (recoverable): the local hostname is not resolved", errIs: errRetentionHostnameNotResolved}
				}
				return charWant{errText: "secondary storage apply_retention operation failed for <ROOT>/backups (recoverable): the local hostname is not resolved", errIs: errRetentionHostnameNotResolved}
			},
		},
		{
			// Delete removes the whole candidate set (bundle, archive, every sidecar)
			// and the log.
			name:   "delete_public",
			logDir: "logs",
			setup: func(e *charEnv) {
				e.addBackup(charBackup{day: "2024-06-20", log: charLogFile})
				a := filepath.Join(e.backups, charArchiveName(charHost, "2024-06-20"))
				for _, suffix := range []string{".bundle.tar", ".manifest.json", ".metadata.sha256"} {
					writeTestFile(e.t, a+suffix, "x")
				}
			},
			act: func(ctx context.Context, b charBackend, e *charEnv) (int, error) {
				return 0, b.Delete(ctx, filepath.Join(e.backups, charArchiveName(charHost, "2024-06-20")))
			},
			want: func(string) charWant {
				a := charArc("2024-06-20")
				return charWant{
					gone: charJoin(charSet("2024-06-20"),
						[]string{a + ".bundle.tar", a + ".manifest.json", a + ".metadata.sha256", charLogRel("logs", "2024-06-20")}),
				}
			},
		},
		{
			// Delete handed the bundle path removes the same set.
			name:   "delete_public_bundle",
			logDir: "logs",
			setup: func(e *charEnv) {
				e.addBackup(charBackup{day: "2024-06-20", log: charLogFile})
				writeTestFile(e.t, filepath.Join(e.backups, charArchiveName(charHost, "2024-06-20")+".bundle.tar"), "x")
			},
			act: func(ctx context.Context, b charBackend, e *charEnv) (int, error) {
				return 0, b.Delete(ctx, filepath.Join(e.backups, charArchiveName(charHost, "2024-06-20")+".bundle.tar"))
			},
			want: func(string) charWant {
				return charWant{
					gone: charJoin(charSet("2024-06-20"),
						[]string{charArc("2024-06-20") + ".bundle.tar", charLogRel("logs", "2024-06-20")}),
				}
			},
		},
		{
			// Delete whose log cannot be removed: the failure is counted in the
			// retention tally although no retention pass is running.
			name:   "delete_public_log_blocked",
			logDir: "logs",
			setup: func(e *charEnv) {
				e.addBackup(charBackup{day: "2024-06-20", log: charLogBlocked})
			},
			act: func(ctx context.Context, b charBackend, e *charEnv) (int, error) {
				return 0, b.Delete(ctx, filepath.Join(e.backups, charArchiveName(charHost, "2024-06-20")))
			},
			want: func(string) charWant {
				return charWant{
					gone:    charSet("2024-06-20"),
					summary: RetentionSummary{LogsNotDeleted: 1},
				}
			},
		},
		{
			// Delete with a cancelled context touches nothing.
			name:      "delete_public_cancelled",
			logDir:    "logs",
			preCancel: true,
			setup: func(e *charEnv) {
				e.addBackup(charBackup{day: "2024-06-20", log: charLogFile})
			},
			act: func(ctx context.Context, b charBackend, e *charEnv) (int, error) {
				return 0, b.Delete(ctx, filepath.Join(e.backups, charArchiveName(charHost, "2024-06-20")))
			},
			want: func(string) charWant {
				return charWant{err: context.Canceled}
			},
		},
	}
}

// TestRetentionCharacterizationGFSCancel cancels the GFS pass while the second of its
// three deletions starts. Which backup the map hands out first is random, so the case
// finds the order in the log, writes the three backups as <TS1> <TS2> <TS3> and
// <DATE1> <DATE2> <DATE3> in processing order from the first loop line on (the three
// are built alike), and pins the result in that form: the first is gone with its log,
// the second is counted not deleted, the third is never reached.
func TestRetentionCharacterizationGFSCancel(t *testing.T) {
	candidates := []string{"2024-06-01", "2024-05-01", "2024-04-01"}
	for _, fl := range charFlavors {
		t.Run(fl.name, func(t *testing.T) {
			e := newCharEnv(t, "logs")
			for _, day := range append([]string{"2024-06-20", "2024-06-10"}, candidates...) {
				e.addBackup(charBackup{day: day, log: charLogFile})
			}
			e.seal()
			logger, sink := newCharLogger()
			before := e.snapshot()
			backend := fl.build(t, e, logger)
			sink.reset()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sink.arm(charDeletingLine, 2, cancel)

			deleted, err := backend.ApplyRetention(ctx, charGFS)

			lines := charNormalize(sink.text(), e.root)
			var order []string
			for _, l := range lines {
				for _, day := range candidates {
					if strings.Contains(l, charDeletingLine+charArchiveName(charHost, day)) {
						order = append(order, day)
					}
				}
			}
			if len(order) != 2 {
				t.Fatalf("expected two deletions to start, got %v", order)
			}
			for _, day := range candidates {
				if day != order[0] && day != order[1] {
					order = append(order, day)
				}
			}

			if deleted != 1 || err != context.Canceled {
				t.Errorf("ApplyRetention = %d, %v; want 1, context.Canceled", deleted, err)
			}
			e.checkFiles(before, charJoin(charSet(order[0]), []string{charLogRel("logs", order[0])}))
			checkRemoved(t, backend, []string{charArchiveName(charHost, order[0])})
			want := RetentionSummary{ScopeValid: true, Owned: 4, PassCompleted: false, Planned: 3, NotDeleted: 1}
			if got := backend.LastRetentionSummary(); got != want {
				t.Errorf("LastRetentionSummary\n got %+v\nwant %+v", got, want)
			}

			// Only the loop and what follows it name the backups in processing order;
			// the listing above it names them in file name order, as it always does.
			loopStart := 0
			for loopStart < len(lines) && !strings.Contains(lines[loopStart], charDeletingLine) {
				loopStart++
			}
			for i := loopStart; i < len(lines); i++ {
				for n, day := range order {
					idx := string(rune('1' + n))
					lines[i] = strings.ReplaceAll(lines[i], charTS(day), "<TS"+idx+">")
					lines[i] = strings.ReplaceAll(lines[i], charWhen(t, day).Format("2006-01-02 15:04:05"), "<DATE"+idx+">")
				}
			}
			assertRetentionGolden(t, "gfs_cancel."+fl.name, lines)
		})
	}
}
