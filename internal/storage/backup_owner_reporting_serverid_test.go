package storage

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// TestAdoptedArchivesAreReportedAtInfoAndLeaveTheWarningCountAlone pins the fact that
// reports the recovery and its severity. Bringing archives back into rotation is good
// news, and good news must not reach the exit code: ParseLogCounts counts only ERROR
// and WARNING lines and applyIssueExitCode promotes an otherwise clean run to exit 1
// on any of them, so a WARNING here would hold a healthy, fully-managed location red
// for ever.
//
// It also pins the not-rotated set the outcome line reads: adoption moves entries out
// of the foreign set, so they are no longer reported as not rotated.
func TestAdoptedArchivesAreReportedAtInfoAndLeaveTheWarningCountAlone(t *testing.T) {
	backups := []*types.BackupMetadata{
		{BackupFile: "pve-backup-20250103-100000.tar.zst", Hostname: "pve", ServerID: ourServerID},
		{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: ourServerID},
		{BackupFile: "pve.home.arpa-backup-20250101-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: ourServerID},
	}

	// The same listing read by the same host WITHOUT an identity is the baseline: two
	// archives out of scope, both named under this host's short name, not rotated.
	baseLogger := &levelRecordingLogger{}
	base, err := applyRetentionHostScope("Local storage", hostOnly("pve"), backups, baseLogger)
	if err != nil {
		t.Fatalf("applyRetentionHostScope: %v", err)
	}
	if len(base.owned) != 1 || len(base.notRotated) != 1 || base.notRotated[0].count != 2 {
		t.Fatalf("baseline scoped %d entries, not rotated %+v; want 1 and 2 under one name: the fixture no longer describes the case it is meant to", len(base.owned), base.notRotated)
	}
	if level := baseLogger.levelOf("  Named pve.home.arpa, not rotated: 2 backups"); level != "INFO" {
		t.Fatalf("baseline not-rotated fact emitted at %q, want INFO. Lines: %+v", level, baseLogger.lines)
	}

	logger := &levelRecordingLogger{}
	scope, err := applyRetentionHostScope("Local storage", hostWithIdentity("pve", ourServerID), backups, logger)
	if err != nil {
		t.Fatalf("applyRetentionHostScope: %v", err)
	}

	if len(scope.owned) != 3 {
		t.Fatalf("scoped %d of 3 entries; every archive here carries this host's own identity", len(scope.owned))
	}
	if len(scope.notRotated) != 0 {
		t.Errorf("not rotated = %+v, want none: the adopted archives rotate", scope.notRotated)
	}
	if level := logger.levelOf("  Adopted: 2 backups named pve.home.arpa, same server identity"); level != "INFO" {
		t.Errorf("the adoption fact was emitted at %q, want INFO. Lines: %+v", level, logger.lines)
	}
	if level := logger.levelOf("not rotated"); level != "" {
		t.Errorf("a not-rotated line was emitted at %s for archives this host now rotates", level)
	}
	if n := logger.countAtLevel("WARNING"); n != 0 {
		t.Errorf("%d WARNING line(s) for a location this host now fully manages: %q", n, logger.messagesAtLevel("WARNING"))
	}
}

// TestDivergentIdentitiesAreReportedAtInfoAndStillPruned is the no-veto pin at the
// reporting seam. An archive this host owns BY NAME whose recorded identity is another
// one stays owned and stays prunable: the name decides ownership and always has.
//
// Vetoing it would be a behaviour change with a permanent failure mode. The identity
// seed carries a timestamp (internal/identity), so a reinstall or a restored BASE_DIR
// mints a DIFFERENT identity on identical hardware, and a veto would strand every
// archive that host had ever written, reported at WARNING on every future run. That is
// structurally the symptom discussion #292 reported, newly manufactured by the fix.
func TestDivergentIdentitiesAreReportedAtInfoAndStillPruned(t *testing.T) {
	backups := []*types.BackupMetadata{
		{BackupFile: "pve-backup-20250103-100000.tar.zst", Hostname: "pve", ServerID: anotherServerID},
		{BackupFile: "pve-backup-20250102-100000.tar.zst", Hostname: "pve", ServerID: ourServerID},
	}

	logger := &levelRecordingLogger{}
	scoped := scopeListing(t, "Local storage", hostWithIdentity("pve", ourServerID), backups, logger)

	if len(scoped) != 2 {
		t.Fatalf("scoped %d of 2 entries; an archive naming a name this host answers to must stay owned however its identity compares, or a host that regenerated its identity file strands its own work for ever", len(scoped))
	}
	if level := logger.levelOf("  Other server identity: 1 backups owned by name, still rotated"); level == "" {
		t.Errorf("nothing reported the identity divergence. Retention is pruning an archive that says it came from somewhere else, and that is worth saying once. Lines: %+v", logger.lines)
	} else if level != "INFO" {
		t.Errorf("the divergence fact was emitted at %s, want INFO. Reporting it at WARNING would pin every run of a reinstalled host at exit 1 for a condition nothing can clear", level)
	}
	if n := logger.countAtLevel("WARNING"); n != 0 {
		t.Errorf("%d WARNING line(s) for a location this host owns entirely: %q", n, logger.messagesAtLevel("WARNING"))
	}
}

// TestASecondSiteCarryingOurIdentityIsAdopted pins the rule at the reporting seam:
// the same server identity is the same server, even under a competing spelling of this
// host's short name. Read without an identity the second site is not rotated and named
// as such; read with this host's identity it is adopted, reported as adopted, and
// leaves the not-rotated set.
func TestASecondSiteCarryingOurIdentityIsAdopted(t *testing.T) {
	backups := []*types.BackupMetadata{
		{BackupFile: "pve.home.arpa-backup-20250103-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: ourServerID},
		{BackupFile: "pve.siteb.example-backup-20250102-100000.tar.zst", Hostname: "pve.siteb.example", ServerID: ourServerID},
	}

	baseLogger := &levelRecordingLogger{}
	base, err := applyRetentionHostScope("Local storage", hostOnly("pve", "pve.home.arpa"), backups, baseLogger)
	if err != nil {
		t.Fatalf("applyRetentionHostScope: %v", err)
	}
	if len(base.owned) != 1 || len(base.notRotated) != 1 {
		t.Fatalf("baseline scoped %d, not rotated %+v; want 1 and one name: the fixture no longer describes the case", len(base.owned), base.notRotated)
	}
	if level := baseLogger.levelOf("  Named pve.siteb.example, not rotated: 1 backups"); level != "INFO" {
		t.Errorf("baseline not-rotated fact emitted at %q, want INFO. Lines: %+v", level, baseLogger.lines)
	}

	logger := &levelRecordingLogger{}
	scope, err := applyRetentionHostScope("Local storage", hostWithIdentity("pve", ourServerID, "pve.home.arpa"), backups, logger)
	if err != nil {
		t.Fatalf("applyRetentionHostScope: %v", err)
	}
	if len(scope.owned) != 2 {
		t.Fatalf("scoped %d of 2 entries; both carry this host's own server identity", len(scope.owned))
	}
	if len(scope.notRotated) != 0 {
		t.Errorf("not rotated %+v; want none: the second site is adopted", scope.notRotated)
	}
	if level := logger.levelOf("  Adopted: 1 backups named pve.siteb.example, same server identity"); level != "INFO" {
		t.Errorf("the adoption fact was emitted at %q, want INFO. Lines: %+v", level, logger.lines)
	}
	if level := logger.levelOf("not rotated"); level != "" {
		t.Errorf("a not-rotated line was emitted at %s for an archive this host now rotates", level)
	}
	if n := logger.countAtLevel("WARNING"); n != 0 {
		t.Errorf("%d WARNING line(s) from the scope: %q", n, logger.messagesAtLevel("WARNING"))
	}
}

// TestUnattributableArchivesStayInvisibleWhenIdentitiesArePresent extends the
// pre-existing guarantee into the identity-bearing population: an archive that names
// no host is claimed by nobody, whatever it carries, and it gets no visible line.
//
// The identity may never act alone. A pre-Go "proxmox-backup-*" name carries no host
// token and this fixture's manifest names no host either, so nothing anywhere says
// which machine wrote it; an identity is not a name, and on a shared location claiming
// the archive means deleting another machine's backup.
func TestUnattributableArchivesStayInvisibleWhenIdentitiesArePresent(t *testing.T) {
	backups := []*types.BackupMetadata{
		{BackupFile: "pve-backup-20250103-100000.tar.zst", Hostname: "pve", ServerID: ourServerID},
		{BackupFile: "proxmox-backup-20250102-100000.tar.gz", ServerID: ourServerID},
	}

	logger := &levelRecordingLogger{}
	scoped := scopeListing(t, "Local storage", hostWithIdentity("pve", ourServerID), backups, logger)

	if len(scoped) != 1 || scoped[0] != backups[0] {
		t.Fatalf("scoped %d entries (%+v), want exactly the archive this host can name. An identity with no hostname beside it must claim nothing", len(scoped), scoped)
	}
	if level := logger.levelOf("no host will ever delete them"); level != "DEBUG" {
		t.Errorf("the unclaimed-archive line was emitted at %q, want DEBUG only", level)
	}
	if n := logger.countAtLevel("INFO") + logger.countAtLevel("WARNING"); n != 0 {
		t.Errorf("%d visible line(s) for a location where nothing changes what retention sees: %+v", n, logger.lines)
	}
}
