package block

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// PBSName is the operator-visible name of the PBS block.
const PBSName = "PBS Storage"

const (
	// pbsArchiveName is the archive the collected tree becomes in every snapshot.
	pbsArchiveName = "proxsave.pxar"
	// pbsChunkSize is --chunk-size in KiB: measured, 90-93% of the tree reused between
	// two runs, accepted by every measured client (from 3.0.1).
	pbsChunkSize = "64"
	// pbsBackupType is the snapshot type of every ProxSave upload.
	pbsBackupType = "host"
	// pbsExcludeFile is the file pxar reads exclusion rules from, silently.
	pbsExcludeFile = ".pxarexclude"
	// snapshotTimeLayout is how PBS writes a snapshot time: RFC3339, UTC.
	snapshotTimeLayout = "2006-01-02T15:04:05Z"
)

// PBSOptions is the configuration of the PBS block, resolved by the caller at startup.
type PBSOptions struct {
	PVEConfigPath string // PVE_CONFIG_PATH, /etc/pve by default
	StorageID     string // PBS_TARGET_STORAGE
	Hostname      string // FQDN as in bundle names; "unknown" when it could not be resolved
	IsPVEHost     bool
	// Retention is the policy of this storage: simple with MaxBackups =
	// MAX_PBS_TARGET_BACKUPS (0 = no retention), or gfs with the shared RETENTION_*.
	Retention storage.RetentionConfig
	// EncryptArchive is ENCRYPT_ARCHIVE: with a storage that has no key, the upload is
	// made unencrypted and step [7] says so.
	EncryptArchive bool
	Logger         *logging.Logger
}

// PBSRetentionFromConfig is the retention of the PBS storage: the shared GFS settings
// when RETENTION_POLICY=gfs, otherwise MAX_PBS_TARGET_BACKUPS newest snapshots.
func PBSRetentionFromConfig(cfg *config.Config) storage.RetentionConfig {
	if cfg == nil {
		return storage.RetentionConfig{Policy: "simple"}
	}
	if cfg.IsGFSRetentionEnabled() {
		return storage.RetentionConfig{
			Policy:  "gfs",
			Daily:   cfg.RetentionDaily,
			Weekly:  cfg.RetentionWeekly,
			Monthly: cfg.RetentionMonthly,
			Yearly:  cfg.RetentionYearly,
		}
	}
	return storage.RetentionConfig{Policy: "simple", MaxBackups: cfg.MaxPBSTargetBackups}
}

// Snapshot is one entry of "snapshot list --output-format json".
type Snapshot struct {
	BackupType string         `json:"backup-type"`
	BackupID   string         `json:"backup-id"`
	BackupTime int64          `json:"backup-time"`
	Protected  bool           `json:"protected"`
	Files      []SnapshotFile `json:"files"`
}

// SnapshotFile is one file of a snapshot.
type SnapshotFile struct {
	Filename  string `json:"filename"`
	CryptMode string `json:"crypt-mode"`
}

// Name is the snapshot as the client names it: <type>/<id>/<RFC3339 UTC>.
func (s Snapshot) Name() string {
	return s.BackupType + "/" + s.BackupID + "/" + time.Unix(s.BackupTime, 0).UTC().Format(snapshotTimeLayout)
}

// datastoreStatus is the answer of "status --output-format json": bytes of the
// filesystem that holds the datastore.
type datastoreStatus struct {
	Avail uint64 `json:"avail"`
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

// pruneEntry is one entry of "prune --output-format json": keep false = removed.
type pruneEntry struct {
	BackupTime int64 `json:"backup-time"`
	Keep       bool  `json:"keep"`
}

// ServerCause is the short cause of a failed client call, in the approved words, with
// the facts two of the causes carry.
type ServerCause struct {
	Text    string // "connection refused", "permission check failed", ...
	Owner   string // "group owned by another user": the group's owner
	User    string // ... and the auth-id that was refused
	Missing string // "permission check failed": the privileges the server named
	On      string // ... and the ACL path
}

// Facts returns the indented lines of the cause: "  PBS server: <cause>", then
// "  Owner:"/"  User:" or "  Missing:"/"  On:" when the server named them.
func (c ServerCause) Facts() []string {
	lines := []string{"  PBS server: " + c.Text}
	if c.Owner != "" {
		lines = append(lines, "  Owner: "+c.Owner, "  User: "+c.User)
	}
	if c.Missing != "" {
		lines = append(lines, "  Missing: "+c.Missing, "  On: "+c.On)
	}
	return lines
}

// Capitalized is the cause as the startup check prints it under
// "Checking PBS storage accessibility...": first letter upper case.
func (c ServerCause) Capitalized() string {
	if c.Text == "" {
		return ""
	}
	return strings.ToUpper(c.Text[:1]) + c.Text[1:]
}

var (
	ownerCheckPattern      = regexp.MustCompile(`backup owner check failed \((\S+) != (\S+)\)`)
	permissionCheckPattern = regexp.MustCompile(`permission check failed - missing (\S+) on (\S+)`)
	startingBackupPattern  = regexp.MustCompile(`Starting backup: (?:\[[^\]]*\]:)?(\S+)`)
)

// classifyClientError maps the output of a failed call to its short cause. Every
// measured error is rc 255 with a line "Error: ..." and only the text tells them apart.
func classifyClientError(r ClientResult, namespace string) ServerCause {
	text := string(r.Stderr) + "\n" + string(r.Stdout)
	errorLine := ""
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "Error: ") {
			errorLine = strings.TrimPrefix(trimmed, "Error: ")
			break
		}
	}
	switch {
	case strings.Contains(text, "Connection refused"):
		return ServerCause{Text: "connection refused"}
	case strings.Contains(text, "deadline has elapsed"), strings.Contains(text, "http request timed out"):
		return ServerCause{Text: "server not responding"}
	case strings.Contains(text, "Certificate fingerprint was not confirmed"):
		return ServerCause{Text: "certificate fingerprint not confirmed"}
	case strings.Contains(errorLine, "authentication failed"):
		return ServerCause{Text: "authentication failed"}
	}
	if m := ownerCheckPattern.FindStringSubmatch(errorLine); m != nil {
		return ServerCause{Text: "group owned by another user", Owner: m[2], User: m[1]}
	}
	switch {
	case strings.HasPrefix(errorLine, "permission check failed"):
		cause := ServerCause{Text: "permission check failed"}
		if m := permissionCheckPattern.FindStringSubmatch(errorLine); m != nil {
			cause.Missing, cause.On = m[1], m[2]
		}
		return cause
	case errorLine == "ENOENT: No such file or directory", errorLine == "namespace not found":
		return ServerCause{Text: strings.TrimSpace("namespace " + namespace + " not found")}
	case strings.HasPrefix(errorLine, "backup timestamp is older than last backup"):
		return ServerCause{Text: "a newer snapshot already exists"}
	}
	return ServerCause{Text: "unrecognized PBS client error"}
}

// InitReport is what the startup check of the PBS storage found, for the caller's
// lines. At most one of Fact and Cause is set.
type InitReport struct {
	Target PBSTarget
	// Fact is the configuration fact that stopped the check before the server was asked.
	Fact *Fact
	// Contacted is true when the server was asked ("Checking PBS storage accessibility...").
	Contacted bool
	// Cause is the server's answer when the check failed.
	Cause *ServerCause
	// Backups are this host's snapshots, when the check passed.
	Backups []Snapshot
}

// Initialized reports whether the storage can receive this run's upload.
func (r InitReport) Initialized() bool {
	return r.Contacted && r.Fact == nil && r.Cause == nil
}

// PBS is the PBS destination block. An instance that could not be initialized at
// startup stays registered: its step [7] reports the backup not saved.
type PBS struct {
	opts        PBSOptions
	target      PBSTarget
	client      Client
	versions    Versions
	initialized bool
	status      datastoreStatus
	// logger receives the DEBUG evidence: the startup logger, then the run's.
	logger *logging.Logger
	// snapshot is this run's snapshot, once the server listed it after the upload.
	snapshot string
}

// lookPath finds the client in PATH; a test replaces it.
var lookPath = exec.LookPath

// InitPBS runs the startup check of the PBS storage, in order: the configuration
// (ResolvePBSTarget), the client, the encryption key (key show), then the server:
// versions, datastore status, the snapshots of the namespace (which also proves the
// namespace exists). Every call only reads, the dry run included. It never fails: the
// block is returned in every case, initialized or not, with the report for the lines.
func InitPBS(ctx context.Context, opts PBSOptions) (*PBS, InitReport) {
	if ctx == nil {
		ctx = context.Background()
	}
	p := &PBS{opts: opts, logger: opts.Logger}
	log := opts.Logger
	var report InitReport

	target, fact := ResolvePBSTarget(ResolveInput{
		PVEConfigPath: opts.PVEConfigPath,
		StorageID:     opts.StorageID,
		Hostname:      opts.Hostname,
		IsPVEHost:     opts.IsPVEHost,
		Logger:        log,
	})
	report.Target = target
	if fact != nil {
		debugf(log, "pbs init: storage %s, configuration check failed: %s (%v)", opts.StorageID, fact.Line(), fact.Err)
		report.Fact = fact
		return p, report
	}
	p.target = target
	p.client = Client{Password: target.Password, Fingerprint: target.Fingerprint}
	debugf(log, "pbs init: storage=%s server=%s port=%s datastore=%s namespace=%s username=%s repository=%s backup-id=%s keyfile=%q master-pubkey=%q fingerprint=%v",
		target.StorageID, target.Server, target.Port, target.Datastore, target.Namespace, target.Username,
		target.Repository, target.BackupID, target.Keyfile, target.MasterPubkeyFile, target.Fingerprint != "")

	clientPath, err := lookPath(pbsClientName)
	if err != nil {
		debugf(log, "pbs init: %s not found in PATH: %v", pbsClientName, err)
		report.Fact = ClientNotFound(err)
		return p, report
	}
	debugf(log, "pbs init: client %s", clientPath)

	if target.Keyfile != "" {
		if fact := p.checkKey(ctx); fact != nil {
			report.Fact = fact
			return p, report
		}
	}

	report.Contacted = true
	versions, result, err := p.client.ProbeVersion(ctx, target.Repository)
	p.debugCall("pbs init version", result)
	if err != nil {
		debugf(log, "pbs init: version probe failed: %v", err)
		report.Cause = p.cause(result)
		return p, report
	}
	p.versions = versions
	debugf(log, "pbs init: client %s, server %s, change-detection-mode metadata supported=%v",
		versions.Client, versions.Server, versions.Client.SupportsChangeDetectionMode())

	status, cause := p.readStatus(ctx, "pbs init status")
	if cause != nil {
		report.Cause = cause
		return p, report
	}
	p.status = status

	args := append([]string{"snapshot", "list"}, p.namespaceArgs()...)
	args = append(args, "--repository", target.Repository, "--output-format", "json")
	snapshots, cause := p.listSnapshots(ctx, "pbs init snapshot list", args)
	if cause != nil {
		report.Cause = cause
		return p, report
	}
	report.Backups = snapshots
	p.initialized = true
	debugf(log, "pbs init: initialized, own snapshots=%d avail=%d used=%d total=%d", len(snapshots), status.Avail, status.Used, status.Total)
	return p, report
}

// checkKey runs "key show" on the storage's keyfile. A key with a passphrase, or one
// that is not the storage's, is a configuration fact. A key that cannot be examined is
// passed as it is: the upload then fails on it, it never goes out unencrypted.
func (p *PBS) checkKey(ctx context.Context) *Fact {
	result := p.client.Run(ctx, "key", "show", p.target.Keyfile, "--output-format", "json")
	p.debugCall("pbs init key show", result)
	if result.Err != nil {
		debugf(p.logger, "pbs init: key show failed, keyfile passed as it is: %v", result.Err)
		return nil
	}
	fact, err := EvaluateKeyShow(result.Stdout, p.target.EncryptionKey)
	if err != nil {
		debugf(p.logger, "pbs init: key show output unreadable, keyfile passed as it is: %v", err)
		return nil
	}
	if fact != nil {
		debugf(p.logger, "pbs init: encryption key check failed: %s (%v)", fact.Line(), fact.Err)
	}
	return fact
}

// Name implements Backup.
func (p *PBS) Name() string { return PBSName }

// Initialized reports whether the startup check passed.
func (p *PBS) Initialized() bool { return p != nil && p.initialized }

// StorageID is PBS_TARGET_STORAGE.
func (p *PBS) StorageID() string { return p.opts.StorageID }

// Snapshot is this run's snapshot (host/<backup-id>/<RFC3339>), empty when none was
// created.
func (p *PBS) Snapshot() string { return p.snapshot }

// RetentionPolicyLine is the line under the step [7] header, in the form of step [6].
func (p *PBS) RetentionPolicyLine() string {
	rc := p.opts.Retention
	if rc.Policy == "gfs" {
		rc = storage.EffectiveGFSRetentionConfig(rc)
		return fmt.Sprintf("  Retention policy: GFS (daily=%d, weekly=%d, monthly=%d, yearly=%d)", rc.Daily, rc.Weekly, rc.Monthly, rc.Yearly)
	}
	if rc.MaxBackups <= 0 {
		return "  Retention policy: simple (disabled)"
	}
	return fmt.Sprintf("  Retention policy: simple (pbs=%d)", rc.MaxBackups)
}

// AvailableGB asks the server, now, the free space of the filesystem that holds the
// datastore, in GB (1024^3 bytes) as the disk-space checks count it.
func (p *PBS) AvailableGB(ctx context.Context) (float64, *ServerCause) {
	status, cause := p.readStatus(ctx, "pbs disk space status")
	if cause != nil {
		return 0, cause
	}
	return float64(status.Avail) / (1024 * 1024 * 1024), nil
}

// Execute implements Backup: the upload, the check after it, retention and the
// statistics, under the step [7] header the caller printed. A dry run calls nothing and
// prints nothing: its header is the whole step.
func (p *PBS) Execute(in Input) Result {
	result := Result{
		Name:            PBSName,
		Status:          StatusSkipped,
		Location:        p.target.Repository,
		Backups:         -1,
		MaxBackups:      p.opts.Retention.MaxBackups,
		RetentionPolicy: p.opts.Retention.Policy,
	}
	if in.Logger != nil {
		p.logger = in.Logger
	}
	if in.DryRun {
		return result
	}
	ctx := in.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if p.logger == nil {
		p.logger = logging.GetDefaultLogger()
	}
	log := p.logger

	log.Info("%s", p.RetentionPolicyLine())
	log.Info("Storing backup...")
	if !p.initialized {
		log.Debug("pbs: storage %s not initialized at startup, nothing uploaded", p.opts.StorageID)
		log.Info("  PBS storage: not initialized at startup")
		log.Warning("%s %s: backup not saved", theme.SymbolError, PBSName)
		log.Skip("Retention: backup not saved")
		result.Status = StatusError
		return result
	}

	if p.opts.EncryptArchive && p.target.Keyfile == "" {
		log.Debug("pbs: ENCRYPT_ARCHIVE=true and storage %s has no %s.enc: upload with --crypt-mode none", p.opts.StorageID, p.opts.StorageID)
		log.Info("  Encryption: none, storage %s has no key", p.opts.StorageID)
	}
	excludes := findExcludeFiles(log, in.TreeDir)
	for _, path := range excludes {
		log.Info("  Exclusion rules: %s", path)
	}

	upload := p.client.Run(ctx, p.uploadArgs(in.TreeDir)...)
	p.debugCall("pbs backup", upload)
	if upload.Err != nil {
		p.logCause(log, p.cause(upload))
		log.Warning("%s %s: backup not saved", theme.SymbolError, PBSName)
		log.Skip("Retention: backup not saved")
		p.logStatistics(ctx, log, &result)
		result.Status = StatusError
		return result
	}

	saved, cause := p.findUploadedSnapshot(ctx, upload, in.StartTime)
	if cause != nil {
		p.logCause(log, cause)
		log.Warning("%s %s: backup not saved", theme.SymbolError, PBSName)
		log.Skip("Retention: backup not saved")
		p.logStatistics(ctx, log, &result)
		result.Status = StatusError
		return result
	}
	p.snapshot = saved.Name()
	result.Snapshot = p.snapshot
	log.Info("  Snapshot: %s", p.snapshot)

	expected := "none"
	if p.target.Keyfile != "" {
		expected = "encrypt"
	}
	if found := archiveCryptMode(saved, expected); found != expected {
		log.Debug("pbs: snapshot %s files %+v, expected crypt-mode %s", p.snapshot, saved.Files, expected)
		log.Info("  Encryption expected: %s", expected)
		log.Info("  Encryption found: %s", found)
		log.Warning("%s %s: backup not usable", theme.SymbolError, PBSName)
		log.Skip("Retention: backup not usable")
		p.logStatistics(ctx, log, &result)
		result.Status = StatusError
		return result
	}

	// The status says what happened to the backup: saved is ok, also with exclusion rules
	// applied (a WARNING line about what it holds). Only a retention that removed
	// nothing it was meant to makes it a warning.
	result.Status = StatusOK
	if len(excludes) > 0 {
		log.Warning("%s %s: backup saved, exclusion rules applied", theme.SymbolWarning, PBSName)
	} else {
		log.Info("%s %s: backup saved", theme.SymbolSuccess, PBSName)
	}

	deleted, retentionApplied := p.applyRetention(ctx, log)
	result.Deleted = deleted
	if !retentionApplied {
		result.Status = StatusWarning
	}
	p.logStatistics(ctx, log, &result)
	if status, cause := p.readStatus(ctx, "pbs status"); cause == nil {
		p.status = status
	}
	result.FreeBytes, result.UsedBytes, result.TotalBytes = p.status.Avail, p.status.Used, p.status.Total
	return result
}

// uploadArgs is the argv of the upload. The secrets travel in the environment only.
// --change-detection-mode metadata only from client 3.2.5 (older ones reject it);
// --chunk-size 64 always. With a keyfile the upload is encrypted with it (and the
// storage's master public key, as PVE does); without one --crypt-mode none, so a
// default key of the client cannot encrypt it behind the storage's back.
func (p *PBS) uploadArgs(treeDir string) []string {
	args := []string{"backup", pbsArchiveName + ":" + treeDir, "--repository", p.target.Repository}
	args = append(args, p.namespaceArgs()...)
	args = append(args, "--backup-type", pbsBackupType, "--backup-id", p.target.BackupID)
	if p.versions.Client.SupportsChangeDetectionMode() {
		args = append(args, "--change-detection-mode", "metadata")
	} else {
		debugf(p.logger, "pbs: client %s rejects --change-detection-mode, upload without it", p.versions.Client)
	}
	args = append(args, "--chunk-size", pbsChunkSize)
	return append(args, p.cryptArgs()...)
}

// cryptArgs selects the encryption of an upload or a log: the storage's keyfile, or
// none.
func (p *PBS) cryptArgs() []string {
	if p.target.Keyfile == "" {
		return []string{"--crypt-mode", "none"}
	}
	args := []string{"--keyfile", p.target.Keyfile}
	if p.target.MasterPubkeyFile != "" {
		args = append(args, "--master-pubkey-file", p.target.MasterPubkeyFile)
	}
	return args
}

func (p *PBS) namespaceArgs() []string {
	if p.target.Namespace == "" {
		return nil
	}
	return []string{"--ns", p.target.Namespace}
}

// group is the snapshot group of this host: host/proxsave-<hostname>.
func (p *PBS) group() string { return pbsBackupType + "/" + p.target.BackupID }

// findUploadedSnapshot finds this run's snapshot in the group after a successful
// upload: the one the client named in "Starting backup: ..." or, without that line, the
// newest one not older than the run start. Not found = "snapshot not found after upload".
func (p *PBS) findUploadedSnapshot(ctx context.Context, upload ClientResult, start time.Time) (Snapshot, *ServerCause) {
	named := int64(-1)
	if m := startingBackupPattern.FindSubmatch(append(append([]byte{}, upload.Stderr...), upload.Stdout...)); m != nil {
		name := string(m[1])
		if i := strings.LastIndex(name, "/"); i >= 0 {
			if at, err := time.Parse(time.RFC3339, name[i+1:]); err == nil {
				named = at.Unix()
			}
		}
		debugf(p.logger, "pbs: client started snapshot %s (time %d)", name, named)
	}
	snapshots, cause := p.listGroup(ctx, "pbs snapshot list after upload")
	if cause != nil {
		return Snapshot{}, cause
	}
	var found *Snapshot
	for i := range snapshots {
		s := &snapshots[i]
		switch {
		case named >= 0:
			if s.BackupTime == named {
				found = s
			}
		case s.BackupTime >= start.Unix() && (found == nil || s.BackupTime > found.BackupTime):
			found = s
		}
	}
	if found == nil {
		debugf(p.logger, "pbs: no snapshot of %s matches the upload (named=%d start=%d listed=%d)", p.group(), named, start.Unix(), len(snapshots))
		return Snapshot{}, &ServerCause{Text: "snapshot not found after upload"}
	}
	return *found, nil
}

// archiveCryptMode is the crypt-mode of the archive in the snapshot: metadata mode
// writes proxsave.mpxar.didx and proxsave.ppxar.didx, the legacy mode
// proxsave.pxar.didx. It returns expected when every archive file has it, otherwise the
// first different mode found ("missing" when the snapshot has no archive file).
func archiveCryptMode(s Snapshot, expected string) string {
	base := strings.TrimSuffix(pbsArchiveName, ".pxar")
	archives := 0
	for _, f := range s.Files {
		switch f.Filename {
		case base + ".mpxar.didx", base + ".ppxar.didx", base + ".pxar.didx":
			archives++
			if f.CryptMode != expected {
				return f.CryptMode
			}
		}
	}
	if archives == 0 {
		return "missing"
	}
	return expected
}

// applyRetention runs the retention of this storage and prints its lines. It returns
// the snapshots deleted and false when no retention was applied at all ("Retention not
// applied"); a retention that kept protected snapshots or stopped after deleting some
// was applied.
func (p *PBS) applyRetention(ctx context.Context, log *logging.Logger) (int, bool) {
	rc := p.opts.Retention
	if rc.Policy == "gfs" {
		return p.applyGFSRetention(ctx, log, storage.NormalizeGFSRetentionConfig(log, PBSName, rc))
	}
	if rc.MaxBackups <= 0 {
		log.Debug("pbs: MAX_PBS_TARGET_BACKUPS=%d, no retention", rc.MaxBackups)
		return 0, true
	}
	log.Info("Applying retention policy...")
	log.Debug("  Policy: simple (keep %d newest)", rc.MaxBackups)
	args := []string{"prune", p.group()}
	args = append(args, p.namespaceArgs()...)
	args = append(args, "--repository", p.target.Repository, "--keep-last", strconv.Itoa(rc.MaxBackups), "--output-format", "json")
	result := p.client.Run(ctx, args...)
	p.debugCall("pbs prune", result)
	if result.Err != nil {
		p.logCause(log, p.cause(result))
		log.Warning("%s Retention not applied", theme.SymbolWarning)
		return 0, false
	}
	var entries []pruneEntry
	if err := json.Unmarshal(result.Stdout, &entries); err != nil {
		log.Debug("pbs: prune output unreadable: %v", err)
		p.logCause(log, &ServerCause{Text: "unrecognized PBS client error"})
		log.Warning("%s Retention not applied", theme.SymbolWarning)
		return 0, false
	}
	deleted := 0
	for _, e := range entries {
		if !e.Keep {
			deleted++
		}
	}
	log.Debug("pbs: prune listed %d snapshots, removed %d", len(entries), deleted)
	logDeleted(log, deleted)
	return deleted, true
}

// applyGFSRetention decides with ProxSave's own GFS engine, on this host's snapshots,
// which ones go, and has the server remove them one by one (snapshot forget). A
// protected snapshot is kept and the retention goes on; any other refusal stops it. It
// reports the retention applied unless nothing was removed because of a refusal.
func (p *PBS) applyGFSRetention(ctx context.Context, log *logging.Logger, rc storage.RetentionConfig) (int, bool) {
	log.Info("Applying GFS retention policy...")
	log.Debug("  Policy: GFS (daily=%d, weekly=%d, monthly=%d, yearly=%d)", rc.Daily, rc.Weekly, rc.Monthly, rc.Yearly)
	snapshots, cause := p.listGroup(ctx, "pbs snapshot list for retention")
	if cause != nil {
		p.logCause(log, cause)
		log.Warning("%s Retention not applied", theme.SymbolWarning)
		return 0, false
	}
	byMeta := make(map[*types.BackupMetadata]Snapshot, len(snapshots))
	metas := make([]*types.BackupMetadata, 0, len(snapshots))
	for _, s := range snapshots {
		m := &types.BackupMetadata{BackupFile: s.Name(), Timestamp: time.Unix(s.BackupTime, 0)}
		byMeta[m] = s
		metas = append(metas, m)
	}
	classification := storage.ClassifyBackupsGFS(metas, rc)
	var doomed []Snapshot
	for m, category := range classification {
		if category == storage.CategoryDelete {
			doomed = append(doomed, byMeta[m])
		}
	}
	sort.Slice(doomed, func(i, j int) bool { return doomed[i].BackupTime < doomed[j].BackupTime })
	log.Debug("pbs: GFS keeps %d of %d snapshots, deleting %d", len(snapshots)-len(doomed), len(snapshots), len(doomed))

	deleted, protected := 0, 0
	var failure *ServerCause
	for _, s := range doomed {
		if s.Protected {
			log.Debug("pbs: %s is protected, kept", s.Name())
			log.Info("  Kept, protected: %s", s.Name())
			protected++
			continue
		}
		args := []string{"snapshot", "forget", s.Name()}
		args = append(args, p.namespaceArgs()...)
		args = append(args, "--repository", p.target.Repository)
		result := p.client.Run(ctx, args...)
		p.debugCall("pbs snapshot forget", result)
		if result.Err == nil {
			deleted++
			continue
		}
		if strings.Contains(string(result.Stderr), "cannot remove protected snapshot") {
			log.Info("  Kept, protected: %s", s.Name())
			protected++
			continue
		}
		failure = p.cause(result)
		break
	}

	switch {
	case failure != nil:
		p.logCause(log, failure)
		if deleted == 0 {
			log.Warning("%s Retention not applied", theme.SymbolWarning)
			return 0, false
		}
		log.Warning("%s Backups deleted: %d of %d", theme.SymbolWarning, deleted, len(doomed)-protected)
		return deleted, true
	case protected > 0:
		log.Warning("%s Backups deleted: %d, %d protected kept", theme.SymbolWarning, deleted, protected)
		return deleted, true
	}
	logDeleted(log, deleted)
	return deleted, true
}

// logDeleted is the outcome of a retention that went through.
func logDeleted(log *logging.Logger, deleted int) {
	if deleted == 0 {
		log.Info("%s Nothing to delete", theme.SymbolSuccess)
		return
	}
	log.Info("%s Backups deleted: %d", theme.SymbolSuccess, deleted)
}

// logStatistics prints the statistics block when the server answers; otherwise the
// failure stays in DEBUG.
func (p *PBS) logStatistics(ctx context.Context, log *logging.Logger, result *Result) {
	snapshots, cause := p.listGroup(ctx, "pbs snapshot list for statistics")
	if cause != nil {
		log.Debug("pbs: statistics unavailable: %s", cause.Text)
		return
	}
	result.Backups = len(snapshots)
	log.Info("%s statistics:", PBSName)
	log.Info("  Total backups: %d", len(snapshots))
}

// UploadLog attaches the run log to this run's snapshot (snapshot upload-log), with
// the storage's keyfile or unencrypted, as the backup. The caller checks Snapshot()
// first. The cause is nil when the log was attached.
func (p *PBS) UploadLog(ctx context.Context, logPath string) *ServerCause {
	if ctx == nil {
		ctx = context.Background()
	}
	args := []string{"snapshot", "upload-log", p.snapshot, logPath}
	args = append(args, p.namespaceArgs()...)
	args = append(args, "--repository", p.target.Repository)
	// upload-log takes the keyfile (or none) but not the master public key.
	if p.target.Keyfile != "" {
		args = append(args, "--keyfile", p.target.Keyfile)
	} else {
		args = append(args, "--crypt-mode", "none")
	}
	result := p.client.Run(ctx, args...)
	p.debugCall("pbs snapshot upload-log", result)
	if result.Err != nil {
		return p.cause(result)
	}
	return nil
}

// listGroup lists this host's snapshots (snapshot list <group>).
func (p *PBS) listGroup(ctx context.Context, label string) ([]Snapshot, *ServerCause) {
	args := []string{"snapshot", "list", p.group()}
	args = append(args, p.namespaceArgs()...)
	args = append(args, "--repository", p.target.Repository, "--output-format", "json")
	return p.listSnapshots(ctx, label, args)
}

// listSnapshots runs a snapshot list and keeps this host's snapshots.
func (p *PBS) listSnapshots(ctx context.Context, label string, args []string) ([]Snapshot, *ServerCause) {
	result := p.client.Run(ctx, args...)
	p.debugCall(label, result)
	if result.Err != nil {
		return nil, p.cause(result)
	}
	var all []Snapshot
	if err := json.Unmarshal(result.Stdout, &all); err != nil {
		debugf(p.logger, "%s: output unreadable: %v", label, err)
		return nil, &ServerCause{Text: "unrecognized PBS client error"}
	}
	own := make([]Snapshot, 0, len(all))
	for _, s := range all {
		if s.BackupType == pbsBackupType && s.BackupID == p.target.BackupID {
			own = append(own, s)
		}
	}
	debugf(p.logger, "%s: %d snapshots listed, %d of group %s", label, len(all), len(own), p.group())
	return own, nil
}

// readStatus asks the datastore status.
func (p *PBS) readStatus(ctx context.Context, label string) (datastoreStatus, *ServerCause) {
	result := p.client.Run(ctx, "status", "--repository", p.target.Repository, "--output-format", "json")
	p.debugCall(label, result)
	if result.Err != nil {
		return datastoreStatus{}, p.cause(result)
	}
	var status datastoreStatus
	if err := json.Unmarshal(result.Stdout, &status); err != nil {
		debugf(p.logger, "%s: output unreadable: %v", label, err)
		return datastoreStatus{}, &ServerCause{Text: "unrecognized PBS client error"}
	}
	return status, nil
}

func (p *PBS) cause(result ClientResult) *ServerCause {
	cause := classifyClientError(result, p.target.Namespace)
	debugf(p.logger, "pbs: cause %q", cause.Text)
	return &cause
}

// logCause prints the fact lines of a cause.
func (p *PBS) logCause(log *logging.Logger, cause *ServerCause) {
	for _, line := range cause.Facts() {
		log.Info("%s", line)
	}
}

// debugOutputLimit is the largest output debugCall writes line by line.
const debugOutputLimit = 4096

// debugCall writes a client call to DEBUG: argv (no secret ever travels there), exit
// code, duration and every line of output.
func (p *PBS) debugCall(label string, result ClientResult) {
	log := p.logger
	if log == nil {
		return
	}
	log.Debug("%s: %s %s rc=%d duration=%s err=%v", label, pbsClientName, strings.Join(result.Args, " "),
		result.ExitCode, result.Duration.Round(time.Millisecond), result.Err)
	for _, stream := range []struct {
		name string
		data []byte
	}{{"stdout", result.Stdout}, {"stderr", result.Stderr}} {
		if len(stream.data) > debugOutputLimit {
			// A snapshot list of a busy namespace: its count is logged by the caller.
			log.Debug("%s %s: %d bytes, not shown", label, stream.name, len(stream.data))
			continue
		}
		for _, line := range strings.Split(strings.TrimRight(string(stream.data), "\n"), "\n") {
			if strings.TrimSpace(line) != "" {
				log.Debug("%s %s: %s", label, stream.name, strings.TrimRight(line, " "))
			}
		}
	}
}

// findExcludeFiles lists the .pxarexclude files of the tree, as paths in the backup
// ("/etc/ssh/.pxarexclude"): pxar applies their rules without a word.
func findExcludeFiles(log *logging.Logger, treeDir string) []string {
	if treeDir == "" {
		return nil
	}
	var found []string
	err := filepath.WalkDir(treeDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && d.Name() == pbsExcludeFile {
			if rel, relErr := filepath.Rel(treeDir, path); relErr == nil {
				found = append(found, "/"+filepath.ToSlash(rel))
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		debugf(log, "pbs: scanning %s for %s: %v", treeDir, pbsExcludeFile, err)
	}
	debugf(log, "pbs: %d %s files in the collected tree", len(found), pbsExcludeFile)
	return found
}

func debugf(log *logging.Logger, format string, args ...interface{}) {
	if log != nil {
		log.Debug(format, args...)
	}
}
