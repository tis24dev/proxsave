package storage

import (
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// TestAdoptedArchivesAreReportedAtInfoAndLeaveTheWarningCountAlone pins the severity
// of the recovery report. Bringing archives back into rotation is good news, and good
// news must not reach the exit code: ParseLogCounts counts only ERROR and WARNING
// lines and applyIssueExitCode promotes an otherwise clean run to exit 1 on any of
// them, so a WARNING here would hold a healthy, fully-managed location red for ever.
//
// It also pins the SECOND RETURN, which feeds the notification's "managed by nobody"
// count. Adoption moves entries out of the foreign set, so that count must drop by
// exactly the number adopted and by nothing else: those archives are managed again.
func TestAdoptedArchivesAreReportedAtInfoAndLeaveTheWarningCountAlone(t *testing.T) {
	backups := []*types.BackupMetadata{
		{BackupFile: "pve-backup-20250103-100000.tar.zst", Hostname: "pve", ServerID: ourServerID},
		{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: ourServerID},
		{BackupFile: "pve.home.arpa-backup-20250101-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: ourServerID},
	}

	// The same listing read by the same host WITHOUT an identity is the baseline:
	// two archives out of scope, both counted as spelling mismatches, two warnings.
	baseLogger := &levelRecordingLogger{}
	baseScoped, baseUnmanaged := applyRetentionHostScope("Local storage", hostOnly("pve"), backups, baseLogger)
	if len(baseScoped) != 1 || baseUnmanaged != 2 {
		t.Fatalf("baseline scoped %d entries with %d unmanaged, want 1 and 2; the fixture no longer describes the case it is meant to", len(baseScoped), baseUnmanaged)
	}

	logger := &levelRecordingLogger{}
	scoped, unmanaged := applyRetentionHostScope("Local storage", hostWithIdentity("pve", ourServerID), backups, logger)

	if len(scoped) != 3 {
		t.Fatalf("scoped %d of 3 entries; every archive here carries this host's own identity under a spelling of its own short name, and this host answers to no other spelling of it", len(scoped))
	}
	if unmanaged != baseUnmanaged-2 {
		t.Errorf("the managed-by-nobody count is %d, want %d (the baseline %d less the 2 adopted). Adoption moves entries into the owned set, so the count must drop by exactly what it adopted", unmanaged, baseUnmanaged-2, baseUnmanaged)
	}
	// The closing sentence, pinned. It used to read "Nothing written by another
	// machine can be claimed this way", which is false for the operator most likely to
	// be reading it: a clone, a pct restore, a template instantiation or an imaged disk
	// carries the source machine's identity, and internal/identity rules that expected.
	// It is also contradicted by the same run on a renamed host, where the hostname arm
	// legitimately owns and prunes archives under a different short name. Unpinned, the
	// false sentence comes back on the next refactor with no signal.
	if line := logger.firstMessageContaining("back into rotation"); line != "" {
		if !strings.Contains(line, "Adoption reaches nothing outside that one short name") {
			t.Errorf("the adoption line no longer scopes its claim to the adoption mechanism: %q", line)
		}
		if strings.Contains(line, "Nothing written by another machine") {
			t.Errorf("the adoption line claims nothing written by another machine can be claimed this way. A clone carries this machine's identity by design, and on a renamed host the hostname rule owns archives under another short name in the same pass: %q", line)
		}
	}
	if level := logger.levelOf("back into rotation"); level == "" {
		t.Error("nothing reported that archives had been brought back into rotation. The operator sees rotation resume with no explanation of why it stopped or why it restarted")
	} else if level != "INFO" {
		t.Errorf("the adoption line was emitted at %s, want INFO. A WARNING line is counted by ParseLogCounts and promotes the run to exit 1, which would make a successful recovery look like a fault", level)
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
	scoped, unmanaged := applyRetentionHostScope("Local storage", hostWithIdentity("pve", ourServerID), backups, logger)

	if len(scoped) != 2 {
		t.Fatalf("scoped %d of 2 entries; an archive naming a name this host answers to must stay owned however its identity compares, or a host that regenerated its identity file strands its own work for ever", len(scoped))
	}
	if unmanaged != 0 {
		t.Errorf("managed-by-nobody = %d, want 0: both archives are this host's own", unmanaged)
	}
	if level := logger.levelOf("record a different server identity"); level == "" {
		t.Error("nothing reported the identity divergence. Retention is pruning an archive that says it came from somewhere else, and that is worth saying once")
	} else if level != "INFO" {
		t.Errorf("the divergence line was emitted at %s, want INFO. Reporting it at WARNING would pin every run of a reinstalled host at exit 1 for a condition nothing can clear", level)
	}
	if n := logger.countAtLevel("WARNING"); n != 0 {
		t.Errorf("%d WARNING line(s) for a location this host owns entirely: %q", n, logger.messagesAtLevel("WARNING"))
	}
}

// TestTwinKeyedForeignArchivesExtendTheExistingWarningWithoutAddingOne pins the one
// place the identity changes an existing message. When out-of-scope archives carry this
// host's own identity but this host still answers to another spelling of that short
// name, the refusal deserves an explanation: the operator is otherwise told that the
// archives look like their own work and given no reason for the refusal beyond a guess.
//
// The explanation is APPENDED to the existing warning rather than emitted as a second
// one. A new WARNING line is a new way to hold a run at exit 1 permanently, and this
// population is one no future run will ever prune.
func TestTwinKeyedForeignArchivesExtendTheExistingWarningWithoutAddingOne(t *testing.T) {
	backups := []*types.BackupMetadata{
		{BackupFile: "pve.home.arpa-backup-20250103-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: ourServerID},
		{BackupFile: "pve.siteb.example-backup-20250102-100000.tar.zst", Hostname: "pve.siteb.example", ServerID: ourServerID},
	}
	id := hostWithIdentity("pve", ourServerID, "pve.home.arpa")

	baseLogger := &levelRecordingLogger{}
	baseScoped, baseUnmanaged := applyRetentionHostScope("Local storage", hostOnly("pve", "pve.home.arpa"), backups, baseLogger)

	logger := &levelRecordingLogger{}
	scoped, unmanaged := applyRetentionHostScope("Local storage", id, backups, logger)

	if len(scoped) != 1 || scoped[0] != backups[0] {
		t.Fatalf("scoped %d entries (%+v), want exactly this host's own archive. The second site carries this host's identity, but this host answers to another spelling of that short name, so it may be a clone and must not be claimed", len(scoped), scoped)
	}
	if len(scoped) != len(baseScoped) || unmanaged != baseUnmanaged {
		t.Errorf("the identity changed the classification of a fixture it must only annotate: scoped %d/%d, unmanaged %d/%d (with identity/without)", len(scoped), len(baseScoped), unmanaged, baseUnmanaged)
	}
	if got, want := logger.countAtLevel("WARNING"), baseLogger.countAtLevel("WARNING"); got != want {
		t.Errorf("%d WARNING line(s) with an identity, %d without. The twin-keyed case must extend the existing warning, never add one: %q", got, want, logger.messagesAtLevel("WARNING"))
	}
	warning := logger.levelOf("different spelling")
	if warning != "WARNING" {
		t.Fatalf("the spelling-mismatch line was emitted at %q, want WARNING; another machine writing here is live information that has to reach the run status", warning)
	}
	found := false
	for _, message := range logger.messagesAtLevel("WARNING") {
		if strings.Contains(message, "different spelling") && strings.Contains(message, "carry this host's own server identity") {
			found = true
		}
	}
	if !found {
		t.Errorf("the spelling-mismatch warning did not say that some of those archives carry this host's own identity, which is the only fact that explains the refusal: %q", logger.messagesAtLevel("WARNING"))
	}
}

// TestUnattributableArchivesStayInfoWhenIdentitiesArePresent extends the pre-existing
// guarantee into the identity-bearing population: an archive that names no host is
// claimed by nobody, whatever it carries, and saying so must not raise the run's
// severity.
//
// The identity may never act alone. A pre-Go "proxmox-backup-*" name carries no host
// token and this fixture's manifest names no host either, so nothing anywhere says
// which machine wrote it; an identity is not a name, and on a shared location claiming
// the archive means deleting another machine's backup.
func TestUnattributableArchivesStayInfoWhenIdentitiesArePresent(t *testing.T) {
	backups := []*types.BackupMetadata{
		{BackupFile: "pve-backup-20250103-100000.tar.zst", Hostname: "pve", ServerID: ourServerID},
		{BackupFile: "proxmox-backup-20250102-100000.tar.gz", ServerID: ourServerID},
	}

	logger := &levelRecordingLogger{}
	scoped, unmanaged := applyRetentionHostScope("Local storage", hostWithIdentity("pve", ourServerID), backups, logger)

	if len(scoped) != 1 || scoped[0] != backups[0] {
		t.Fatalf("scoped %d entries (%+v), want exactly the archive this host can name. An identity with no hostname beside it must claim nothing", len(scoped), scoped)
	}
	if unmanaged != 1 {
		t.Errorf("managed-by-nobody = %d, want 1: the pre-Go archive is present and no host will ever prune it", unmanaged)
	}
	if level := logger.levelOf("no host will ever delete them"); level != "INFO" {
		t.Errorf("the unclaimed-archive line was emitted at %q, want INFO. It is a standing backlog fact, and counting it would hold every affected run at exit 1 for ever", level)
	}
	if n := logger.countAtLevel("WARNING"); n != 0 {
		t.Errorf("%d WARNING line(s) for a location where nothing is contended: %q", n, logger.messagesAtLevel("WARNING"))
	}
}
