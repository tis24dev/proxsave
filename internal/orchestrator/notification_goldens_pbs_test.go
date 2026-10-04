package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/block"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// The PBS cases of the notification goldens run the real PBS block: block.InitPBS at
// startup, the real blockAdapter at step [7], against the fake proxmox-backup-client the
// block tests use (internal/block/testdata/fake-proxmox-backup-client.sh), installed first
// in PATH and reached through the real safeexec allowlist. The fake answers every call of
// one subcommand with the same file, so a case fixes the server's answers, not a sequence.
const (
	notifyGoldenPBSStorage    = "pbs-offsite"
	notifyGoldenPBSRepository = "backup@pbs!proxsave@192.0.2.10:offsite"
	notifyGoldenPBSFakeClient = "../block/testdata/fake-proxmox-backup-client.sh"
	notifyGoldenPBSClientName = "proxmox-backup-client"
	notifyGoldenPBSPassword   = "6f0c2d9e-41b7-4a8e-9c35-d27e80b1f4a6"
	// notifyGoldenPBSConnectionRefused is the client's answer when the server port is
	// closed (measured on pve-test, internal/block/client_test.go), for this server.
	notifyGoldenPBSConnectionRefused = "Error: client error (Connect)\n" +
		"Caused by: error connecting to https://192.0.2.10:8007/ - tcp connect error: Connection refused (os error 111)\n"
)

// notifyGoldenPBSRunSnapshotTime is the backup-time of this run's snapshot, after the
// start of the run.
var notifyGoldenPBSRunSnapshotTime = notifyGoldenStart.Add(2*time.Minute + 20*time.Second)

// notifyGoldenPBSAnswer is what the fake client answers to one subcommand.
type notifyGoldenPBSAnswer struct {
	rc     int
	stdout string
	stderr string
}

// notifyGoldenPBSSnapshot is one snapshot of this host's group.
type notifyGoldenPBSSnapshot struct {
	at        time.Time
	protected bool
}

// notifyGoldenPBS is the PBS server of a PBS case: a healthy server (client and server
// 4.2.5, the datastore space, this host's snapshots, an upload that names the run
// snapshot, a prune that keeps everything), with the answers the case overrides.
type notifyGoldenPBS struct {
	// answers overrides the healthy answer per subcommand key ("version", "prune", ...);
	// "<key>.<n>" answers only the n-th call of that key (fake client <key>.<n>.rc).
	answers map[string]notifyGoldenPBSAnswer
	// snapshots is this host's group; nil = the run snapshot and 11 older daily ones.
	snapshots []notifyGoldenPBSSnapshot
	// treeFiles are files of the collected tree besides etc/hostname.
	treeFiles []string
	// namespace is the storage's namespace; empty = the datastore root.
	namespace string
	// status is the datastore space the server reports (bytes); zero = 1210 GiB free,
	// 578 GiB used of 1788 GiB.
	avail, used, total uint64
}

// storageCfg is the PVE storage.cfg section of the PBS storage, in the form PVE writes
// it (internal/block/testdata/pve/storage.cfg).
func (p *notifyGoldenPBS) storageCfg() string {
	cfg := "pbs: " + notifyGoldenPBSStorage + "\n" +
		"\tdatastore offsite\n" +
		"\tserver 192.0.2.10\n" +
		"\tcontent backup\n" +
		"\tfingerprint 3e:41:9a:07:c2:5d:88:16:f0:2b:6c:d4:91:ae:57:03:bb:68:1f:e9:24:7a:c5:30:0d:96:4f:e2:b8:15:73:ca\n"
	if p.namespace != "" {
		cfg += "\tnamespace " + p.namespace + "\n"
	}
	return cfg + "\tusername backup@pbs!proxsave\n"
}

// statusJSON is the answer of "status --output-format json".
func (p *notifyGoldenPBS) statusJSON() string {
	avail, used, total := p.avail, p.used, p.total
	if total == 0 {
		avail, used, total = uint64(1210)<<30, uint64(578)<<30, uint64(1788)<<30
	}
	return fmt.Sprintf(`{"avail":%d,"backend-type":"filesystem","total":%d,"used":%d}`+"\n", avail, total, used)
}

// notifyGoldenPBSOlderSnapshots are 11 daily snapshots of this host before the run, the
// group as it stands when the run's upload never lands.
func notifyGoldenPBSOlderSnapshots() []notifyGoldenPBSSnapshot {
	var snapshots []notifyGoldenPBSSnapshot
	for day := 1; day <= 11; day++ {
		snapshots = append(snapshots, notifyGoldenPBSSnapshot{at: notifyGoldenPBSRunSnapshotTime.AddDate(0, 0, -day)})
	}
	return snapshots
}

func (p *notifyGoldenPBS) groupSnapshots() []notifyGoldenPBSSnapshot {
	if p.snapshots != nil {
		return p.snapshots
	}
	snapshots := []notifyGoldenPBSSnapshot{{at: notifyGoldenPBSRunSnapshotTime}}
	for day := 1; day <= 11; day++ {
		snapshots = append(snapshots, notifyGoldenPBSSnapshot{at: notifyGoldenPBSRunSnapshotTime.AddDate(0, 0, -day)})
	}
	return snapshots
}

// snapshotListJSON is the answer of "snapshot list --output-format json", in the
// measured form (internal/block/block_pbs_test.go snapshotJSON).
func (p *notifyGoldenPBS) snapshotListJSON() string {
	entries := make([]string, 0, len(p.groupSnapshots()))
	for _, s := range p.groupSnapshots() {
		entries = append(entries, fmt.Sprintf(`{"backup-id":%q,"backup-time":%d,"backup-type":"host","files":[`+
			`{"crypt-mode":"none","filename":"proxsave.mpxar.didx","size":100},`+
			`{"crypt-mode":"none","filename":"proxsave.ppxar.didx","size":100},`+
			`{"crypt-mode":"sign-only","filename":"index.json.blob","size":379}],"owner":"backup@pbs!proxsave","protected":%v,"size":1243}`,
			"proxsave-"+notifyGoldenHost, s.at.Unix(), s.protected))
	}
	return "[" + strings.Join(entries, ",") + "]\n"
}

// pruneJSON is the answer of a prune that keeps every snapshot of the group.
func (p *notifyGoldenPBS) pruneJSON() string {
	entries := make([]string, 0, len(p.groupSnapshots()))
	for _, s := range p.groupSnapshots() {
		entries = append(entries, fmt.Sprintf(`{"backup-id":%q,"backup-time":%d,"backup-type":"host","keep":true,"protected":%v}`,
			"proxsave-"+notifyGoldenHost, s.at.Unix(), s.protected))
	}
	return "[" + strings.Join(entries, ",") + "]\n"
}

// install puts the fake client first in PATH with the case's answers and returns the
// PVE_CONFIG_PATH tree holding the storage.
func (p *notifyGoldenPBS) install(t *testing.T) string {
	t.Helper()
	script, err := os.ReadFile(notifyGoldenPBSFakeClient)
	if err != nil {
		t.Fatalf("read fake client: %v", err)
	}
	bin := t.TempDir()
	write := func(dir, name, body string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write(bin, notifyGoldenPBSClientName, string(script), 0o755)
	answers := map[string]notifyGoldenPBSAnswer{
		"version":       {stdout: `{"client":{"release":"5","version":"4.2"},"server":{"release":"5","version":"4.2"}}` + "\n"},
		"status":        {stdout: p.statusJSON()},
		"snapshot-list": {stdout: p.snapshotListJSON()},
		"backup": {stderr: "Starting backup: [offsite]:host/proxsave-" + notifyGoldenHost + "/" +
			notifyGoldenPBSRunSnapshotTime.UTC().Format("2006-01-02T15:04:05Z") + "    \n"},
		"prune": {stdout: p.pruneJSON()},
	}
	for key, answer := range p.answers {
		answers[key] = answer
	}
	for key, answer := range answers {
		write(bin, key+".rc", strconv.Itoa(answer.rc), 0o600)
		write(bin, key+".stdout", answer.stdout, 0o600)
		write(bin, key+".stderr", answer.stderr, 0o600)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	pveDir := t.TempDir()
	privDir := filepath.Join(pveDir, "priv", "storage")
	if err := os.MkdirAll(privDir, 0o700); err != nil {
		t.Fatalf("mkdir priv/storage: %v", err)
	}
	write(pveDir, "storage.cfg", p.storageCfg(), 0o640)
	write(privDir, notifyGoldenPBSStorage+".pw", notifyGoldenPBSPassword+"\n", 0o600)
	return pveDir
}

// tree is the collected workspace the block uploads.
func (p *notifyGoldenPBS) tree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range append([]string{"etc/hostname"}, p.treeFiles...) {
		path := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", f, err)
		}
		if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	return dir
}

// notifyGoldenPBSConfigure switches PBS on the way backup.env does for a PBS case.
func notifyGoldenPBSConfigure(cfg *config.Config) {
	cfg.PBSTargetEnabled = true
	cfg.PBSTargetStorage = notifyGoldenPBSStorage
}

// notifyGoldenInitPBS is cmd/proxsave initializePBSTarget: the real startup check, the
// outcome lines it prints (the INFO lines between them reach no channel and are left
// out), and the block registered whether it initialized or not.
func notifyGoldenInitPBS(t *testing.T, ctx context.Context, logger *logging.Logger, cfg *config.Config, o *Orchestrator, pveDir string) {
	t.Helper()
	cfg.PVEConfigPath = pveDir
	retention := block.PBSRetentionFromConfig(cfg)
	pbs, report := block.InitPBS(ctx, block.PBSOptions{
		PVEConfigPath:  cfg.PVEConfigPath,
		StorageID:      cfg.PBSTargetStorage,
		Hostname:       notifyGoldenHost,
		IsPVEHost:      true,
		Retention:      retention,
		EncryptArchive: cfg.EncryptArchive,
		Logger:         logger,
	})
	logger.Info("Path PBS: %s", cfg.PBSTargetStorage)
	if retention.Policy != "gfs" {
		logger.Info("  Retention policy: simple (keep %d newest)", retention.MaxBackups)
	}
	switch {
	case report.Fact != nil:
		logger.Info("  %s", report.Fact.Line())
		logger.Warning("%s PBS storage: not initialized", theme.SymbolError)
	case report.Cause != nil:
		logger.Info("Checking PBS storage accessibility...")
		logger.Info("  %s", report.Cause.Capitalized())
		logger.Warning("%s PBS storage: not initialized", theme.SymbolError)
	default:
		logger.Info("Checking PBS storage accessibility...")
		logger.Info("  Accessible")
		logger.Info("  Backups: %d", len(report.Backups))
		logger.Info("%s PBS storage: initialized", theme.SymbolSuccess)
	}
	if report.Target.Repository != "" && report.Target.Repository != notifyGoldenPBSRepository {
		t.Fatalf("PBS repository %q, want %q", report.Target.Repository, notifyGoldenPBSRepository)
	}
	o.RegisterBackupBlock(pbs)
}

// notifyGoldenRunPBSStep is step [7]: every destination block through the real adapter.
func notifyGoldenRunPBSStep(t *testing.T, ctx context.Context, o *Orchestrator, stats *BackupStats, tree string) {
	t.Helper()
	run := &backupRunContext{ctx: ctx, hostname: notifyGoldenHost, startTime: notifyGoldenStart, stats: stats}
	for _, b := range o.destinationBlocks() {
		if err := b.execute(run, &backupWorkspace{tempDir: tree}); err != nil {
			t.Fatalf("destination block: %v", err)
		}
	}
}

// notifyGoldenPBSRetentionGFS is RETENTION_POLICY=gfs with one daily snapshot kept, the
// policy every destination then shares (storage.NewRetentionConfigFromConfig,
// block.PBSRetentionFromConfig).
func notifyGoldenPBSRetentionGFS(cfg *config.Config) {
	cfg.RetentionPolicy = "gfs"
	cfg.RetentionDaily = 1
	cfg.RetentionWeekly = 0
	cfg.RetentionMonthly = 0
	cfg.RetentionYearly = 0
}

// The sidecar cases need what a real backend reports around a saved archive, which
// backupCharBackend cannot say.

// notifyGoldenSidecarBackend is a backupCharBackend whose Store saves the archive and not
// one of its sidecar files: the Secondary copy returns nil and reports the sidecar in
// LastStoreIssues (internal/storage/secondary.go), the Cloud upload returns a
// StorageError with PrimarySaved (internal/storage/cloud.go, a remote: no issue recorded).
type notifyGoldenSidecarBackend struct {
	*backupCharBackend
	primarySaved bool
	issues       []storage.StoreIssue
}

func (b *notifyGoldenSidecarBackend) Store(ctx context.Context, backupFile string, metadata *types.BackupMetadata) error {
	if err := b.backupCharBackend.Store(ctx, backupFile, metadata); err != nil {
		return err
	}
	if b.primarySaved {
		return &storage.StorageError{
			Location:     b.location,
			Operation:    "upload_associated",
			Path:         backupFile,
			Err:          fmt.Errorf("injected failure"),
			IsCritical:   false,
			Recoverable:  true,
			PrimarySaved: true,
		}
	}
	return nil
}

// LastStoreIssues implements storage.StoreReporter.
func (b *notifyGoldenSidecarBackend) LastStoreIssues() []storage.StoreIssue { return b.issues }
