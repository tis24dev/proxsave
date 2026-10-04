package storage

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// cloudFlowResult is everything one run of the cloud flow reports, compared between a
// run whose rclone writes nothing on stderr and one whose rclone writes the NOTICE of a
// missing config file there on every call.
type cloudFlowResult struct {
	DetectErr      string
	StoreErr       string
	StoreIssues    []StoreIssue
	VerifyPrimary  string
	VerifyAlt      string
	Deleted        int
	RetentionErr   string
	Summary        RetentionSummary
	Listed         []string
	TotalBackups   int
	StatsErr       string
	LogUploadErr   string
	LogIssues      []StoreIssue
	RemoteContents []string
}

// runCloudFlow runs, with the real CloudStorage and its real rclone runner against
// testdata/fake-rclone-local.sh installed as "rclone" first in PATH, what a run does
// with CLOUD_REMOTE a local directory: the accessibility check, the upload with its
// size and checksum verification, the other verification method, retention (listing,
// manifest reads, deletions, log count and cleanup), the statistics, the listing and
// the log upload of step [8]. It returns what each step reported and the debug log.
func runCloudFlow(t *testing.T, notice bool) (cloudFlowResult, string) {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("testdata", "fake-rclone-local.sh"))
	if err != nil {
		t.Fatalf("read fake rclone: %v", err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "rclone"), script, 0o755); err != nil {
		t.Fatalf("install fake rclone: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if notice {
		t.Setenv("FAKE_RCLONE_NOTICE", "1")
	} else {
		t.Setenv("FAKE_RCLONE_NOTICE", "")
	}

	root := filepath.Join(t.TempDir(), "cloud")
	local := t.TempDir()
	cfg := &config.Config{
		CloudEnabled:        true,
		CloudRemote:         root,
		CloudLogPath:        "/proxsave/log",
		CloudBatchSize:      10,
		CloudVerifyChecksum: true,
	}
	var debug bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&debug)
	cs, err := NewCloudStorage(cfg, logger, "node")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}
	cs.hostname = "node"
	cs.waitForRetry = func(context.Context, time.Duration) error { return nil }
	cs.sleep = func(time.Duration) {}
	ctx := context.Background()
	var res cloudFlowResult
	errText := func(err error) string {
		if err == nil {
			return ""
		}
		return err.Error()
	}

	_, err = cs.DetectFilesystem(ctx)
	res.DetectErr = errText(err)

	// Two older backups of this host already on the remote, each with its manifest and
	// its log; then this run's archive with its sidecars.
	seed := func(name string, at time.Time) {
		for path, body := range map[string]string{
			filepath.Join(root, name):                                   "old archive",
			filepath.Join(root, name+".metadata"):                       `{"hostname":"node"}`,
			filepath.Join(root, name+".sha256"):                         "0000  " + name + "\n",
			filepath.Join(root, "proxsave", "log", logNameFor(t, name)): "old log",
		} {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
			}
			writeTestFile(t, path, body)
			if err := os.Chtimes(path, at, at); err != nil {
				t.Fatalf("chtimes %s: %v", path, err)
			}
		}
	}
	seed("node-backup-20261001-020000.tar.zst", time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC))
	seed("node-backup-20261002-020000.tar.zst", time.Date(2026, 10, 2, 2, 0, 0, 0, time.UTC))
	name := "node-backup-20261004-020000.tar.zst"
	archive := filepath.Join(local, name)
	writeTestFile(t, archive, strings.Repeat("archive data ", 4096))
	writeTestFile(t, archive+".metadata", `{"hostname":"node"}`)
	writeTestFile(t, archive+".sha256", "0000  "+name+"\n")

	res.StoreErr = errText(cs.Store(ctx, archive, &types.BackupMetadata{BackupFile: archive}))
	res.StoreIssues = cs.LastStoreIssues()

	ok, err := cs.VerifyUpload(ctx, archive, cs.remotePathFor(name))
	res.VerifyPrimary = fmt.Sprintf("%v %s", ok, errText(err))
	cfg.RcloneVerifyMethod = "alternative"
	ok, err = cs.VerifyUpload(ctx, archive, cs.remotePathFor(name))
	res.VerifyAlt = fmt.Sprintf("%v %s", ok, errText(err))
	cfg.RcloneVerifyMethod = ""

	res.Deleted, err = cs.ApplyRetention(ctx, RetentionConfig{Policy: "simple", MaxBackups: 1})
	res.RetentionErr = errText(err)
	res.Summary = cs.LastRetentionSummary()

	backups, err := cs.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, b := range backups {
		res.Listed = append(res.Listed, fmt.Sprintf("%s %d %s", b.BackupFile, b.Size, b.Timestamp.UTC().Format(time.RFC3339)))
	}
	stats, err := cs.GetStats(ctx)
	res.StatsErr = errText(err)
	if stats != nil {
		res.TotalBackups = stats.TotalBackups
	}

	logFile := filepath.Join(local, "backup-node-20261004-020000.log")
	writeTestFile(t, logFile, "this run's log")
	res.LogUploadErr = errText(cs.UploadToRemotePath(ctx, logFile, cs.cloudLogPath(cfg.CloudLogPath, filepath.Base(logFile)), true))
	res.LogIssues = cs.LastStoreIssues()

	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(root, path)
			res.RemoteContents = append(res.RemoteContents, fmt.Sprintf("%s %d", rel, info.Size()))
		}
		return err
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(res.RemoteContents)
	return res, debug.String()
}

// logNameFor is the log name the backend pairs with an archive name.
func logNameFor(t *testing.T, archive string) string {
	t.Helper()
	host, ts, ok := extractLogKeyFromBackup(archive)
	if !ok {
		t.Fatalf("no log key in %s", archive)
	}
	return fmt.Sprintf("backup-%s-%s.log", host, ts)
}

// What ProxSave parses from rclone comes from its stdout only. An rclone that writes
// the NOTICE of a missing config file on stderr before every answer (CLOUD_REMOTE a
// local directory on a host with no rclone.conf, live test 2026-10-04: the NOTICE read
// as the archive's size gave "size mismatch: local=548352 remote=2026" and "backup not
// saved") gives the whole cloud flow exactly the results it gives without it. The
// NOTICE is kept in DEBUG.
func TestCloudFlowIgnoresRcloneStderrNotice(t *testing.T) {
	clean, _ := runCloudFlow(t, false)
	noisy, debug := runCloudFlow(t, true)

	// The flow itself works: nothing failed, the old backups went, one is left.
	if clean.DetectErr != "" || clean.StoreErr != "" || clean.RetentionErr != "" || clean.StatsErr != "" || clean.LogUploadErr != "" {
		t.Fatalf("the clean flow failed: %+v", clean)
	}
	// (Owner and mode cannot be set by a test user: permissions not set is the only
	// store issue expected.)
	for _, issues := range [][]StoreIssue{clean.StoreIssues, clean.LogIssues} {
		for _, issue := range issues {
			if issue != StoreIssuePermissionsNotSet {
				t.Fatalf("unexpected store issue %v: %+v", issue, clean)
			}
		}
	}
	if clean.VerifyPrimary != "true " || clean.VerifyAlt != "true " || clean.Deleted != 2 || clean.TotalBackups != 1 ||
		len(clean.Listed) != 1 || clean.Summary.LogsDeleted != 2 {
		t.Fatalf("the clean flow did not do the expected work: %+v", clean)
	}

	cleanText := fmt.Sprintf("%+v", clean)
	noisyText := fmt.Sprintf("%+v", noisy)
	// The two runs use their own temporary directories: compare what does not name them.
	if strings.Join(clean.RemoteContents, "\n") != strings.Join(noisy.RemoteContents, "\n") ||
		strings.Join(clean.Listed, "\n") != strings.Join(noisy.Listed, "\n") {
		t.Fatalf("remote contents or listing differ\nclean: %v %v\nnoisy: %v %v", clean.RemoteContents, clean.Listed, noisy.RemoteContents, noisy.Listed)
	}
	clean.RemoteContents, noisy.RemoteContents, clean.Listed, noisy.Listed = nil, nil, nil, nil
	if fmt.Sprintf("%+v", clean) != fmt.Sprintf("%+v", noisy) {
		t.Fatalf("the NOTICE on stderr changed the cloud flow\nclean: %s\nnoisy: %s", cleanText, noisyText)
	}
	if !strings.Contains(debug, "wrote to stderr while succeeding (not read as data): NOTICE: Config file") {
		t.Fatalf("the NOTICE is not kept in DEBUG:\n%s", debug)
	}
}
