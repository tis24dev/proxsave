package storage

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// renamedHost builds the identity of a machine carrying an alias whose FIRST LABEL is
// not the kernel name's first label. It is a common Debian and Proxmox rename
// artefact rather than an invented shape: /etc/hostname says "pve" while /etc/hosts
// still says "127.0.1.1 nas pve", so os.Hostname reports "pve", "hostname -f" returns
// "nas", and the writer stamps "nas" into every archive it produces. The alias set
// then holds a name that shares no label at all with the name retention reports
// under.
//
// It goes through retentionHostAliases rather than assigning the slice directly, so
// the fixture describes what the constructors really assemble instead of a shape only
// a test can produce.
func renamedHost(kernel, resolved, serverID string) retentionIdentity {
	return retentionIdentity{
		hostname: kernel,
		aliases:  retentionHostAliases(kernel, []string{resolved}),
		serverID: serverID,
	}
}

// TestAdoptionIsBoundedByThePopulationRetentionReports is the containment property,
// run over the whole small space the rule is defined on rather than over the handful
// of rows a table can hold. For EVERY archive the adoption rule claims, the reporting
// helper must already count that same archive: adoption may only ever widen inside
// the set retention already tells the operator about, never outside it.
//
// The space is built to contain the counterexample the previous clause set admitted.
// TestAdoptedArchivesAreASubsetOfTheReportedSpellingMismatches walks a similar
// property but every one of its identities has NO alias, and an identity with no
// alias is exactly the shape where matching the archive's label against the whole
// name set and matching it against the kernel name's label happen to agree. The
// disagreement needs an alias whose first label differs, which renamedHost supplies.
//
// The second assertion is the narrowing pin. Clause d used to ask whether the
// archive's first label was ANY name this machine answers to; it now asks whether it
// is this host's OWN first label. Those two are not ordered in general, but with
// clause e in place the new one implies the old: if the kernel name's label equals the
// archive's label and no name this host answers to is a longer spelling of that label,
// then the kernel name IS that bare label, so the host answers to it. Asserting the
// implication here states that this change can only ever REMOVE adoptions from the
// shipped behaviour and never add one, and it fails the moment clause e is deleted,
// which would let a host with a qualified kernel name adopt a third spelling of its
// label.
//
// The manifest hostname and the filename token deliberately disagree on every case,
// so the property also witnesses that both sides read the owner through
// backupOwnerHost and land on the same string.
func TestAdoptionIsBoundedByThePopulationRetentionReports(t *testing.T) {
	archiveHosts := []string{
		"", "   ", "pve", "pve.", "pve.home.arpa", "pve.siteb.example",
		"nas", "nas.lan", "nas.siteb.example",
		"backup01", "backup01.lan",
		"pbs.home.arpa", "other", "unknown", "unknown.lan",
	}
	archiveIDs := []string{ourServerID, anotherServerID, "", "123456789012345"}
	identities := []retentionIdentity{
		// The discussion #292 shape: nothing resolves any more, so no alias at all.
		hostWithIdentity("pve", ourServerID),
		// Still resolving its own FQDN, which is the clone defence's premise.
		hostWithIdentity("pve", ourServerID, "pve.home.arpa"),
		// The rename artefacts: an alias sharing no label with the kernel name.
		renamedHost("pve", "nas", ourServerID),
		renamedHost("pve", "backup01", ourServerID),
		renamedHost("pve", "nas.lan", ourServerID),
		renamedHost("pve", "pbs", ourServerID),
		// Two aliases at once, which no other row covers.
		retentionIdentity{hostname: "pve", aliases: retentionHostAliases("pve", []string{"nas", "backup01"}), serverID: ourServerID},
		// The sentinel is NOT an alias-bearing row: retentionHostAliases drops
		// "unknown", so this host falls back to the no-alias shape. It is here to pin
		// that, not to cover a fourth rename artefact.
		renamedHost("pve", "unknown", ourServerID),
		// A qualified kernel name, which the reporting side folds to "pve" too.
		hostWithIdentity("pve.home.arpa", ourServerID),
		hostWithIdentity("nas", ourServerID),
		// The two shapes that must adopt nothing whatever the names say.
		hostOnly("pve"),
		hostWithIdentity("", ourServerID),
	}

	for _, archiveHost := range archiveHosts {
		for _, archiveID := range archiveIDs {
			for _, id := range identities {
				meta := &types.BackupMetadata{
					BackupFile: "backup01.lan-backup-20250102-100000.tar.zst",
					Hostname:   archiveHost,
					ServerID:   archiveID,
					Verified:   true,
				}
				if !archiveAdoptedByServerID(meta, id) {
					continue
				}

				if n := retentionSpellingMismatches([]*types.BackupMetadata{meta}, id); n != 1 {
					t.Errorf("archive %q (identity %q) was adopted by host %q (aliases %v) but retentionSpellingMismatches does not count it. Adoption has reached an archive no existing line ever mentioned: it was reported as another machine's at WARNING and refused, and it is now silently deletable", archiveHost, archiveID, id.hostname, id.aliases)
				}

				label := hostShortLabel(types.NormalizeHostname(archiveHost))
				if !hostOwnsName(label, id.hostname, id.aliases...) {
					t.Errorf("archive %q was adopted by host %q (aliases %v) which answers to no name spelled %q. The clause set has been widened past what it shipped with, not narrowed", archiveHost, id.hostname, id.aliases, label)
				}
			}
		}
	}
}

// TestAdoptionRefusesAnArchiveUnderAnAliasShortLabel is the named regression, stated
// where an operator would feel it rather than at the predicate alone.
//
// The host is the rename artefact: it answers to "pve" and to "nas", and it reports
// under "pve" because that is the name the kernel gives. An archive named "nas.lan"
// carrying this host's own identity used to satisfy the old clause d, because "nas" IS
// one of the names this machine answers to. But the reporting side keys on the KERNEL
// name's label alone, so that archive was never in the spelling-mismatch population:
// it was CONTENDED, reported at WARNING as not belonging to "pve", and refused by
// retention. Adoption reached it anyway, the warning vanished, and the archive became
// deletable.
//
// The end-to-end half is the part that matters. It pins all three consequences at once:
// the entry stays out of scope, so it is not deletable; the CONTENDED warning is still
// printed, so ParseLogCounts still sees it and the run's exit code is unchanged; and
// the second return stays 0, so RetentionSummary.Owned does not absorb an archive this
// host was told belongs to somebody else.
func TestAdoptionRefusesAnArchiveUnderAnAliasShortLabel(t *testing.T) {
	id := renamedHost("pve", "backup01", ourServerID)
	if len(id.aliases) != 1 || id.aliases[0] != "backup01" {
		t.Fatalf("fixture aliases = %v, want exactly [backup01]; the identity no longer describes a host answering to a second, differently labelled name", id.aliases)
	}

	contended := &types.BackupMetadata{
		BackupFile: "backup01.lan-backup-20250102-100000.tar.zst",
		Hostname:   "backup01.lan",
		ServerID:   ourServerID,
		Verified:   true,
	}
	own := &types.BackupMetadata{
		BackupFile: "pve-backup-20250103-100000.tar.zst",
		Hostname:   "pve",
		ServerID:   ourServerID,
		Verified:   true,
	}

	if archiveAdoptedByServerID(contended, id) {
		t.Error("an archive named \"backup01.lan\" was adopted by a host reporting as \"pve\". Its first label is an ALIAS's label, not this host's own, so nothing retention prints has ever called it this machine's work")
	}
	if n := retentionSpellingMismatches([]*types.BackupMetadata{contended}, id); n != 0 {
		// Errorf, not Fatalf: widening the reported population is one of the shapes
		// this test exists to kill, and stopping here would skip the assertions below
		// that catch it at the operator-visible level.
		t.Errorf("retentionSpellingMismatches = %d, want 0; the fixture no longer describes an archive OUTSIDE the reported population and proves nothing", n)
	}

	logger := &levelRecordingLogger{}
	scoped, unmanaged := applyRetentionHostScope("Local storage", id, []*types.BackupMetadata{own, contended}, logger)

	if len(scoped) != 1 {
		t.Errorf("scoped %d of 2 entries, want 1. Only the archive naming \"pve\" is this host's; claiming the other one makes another machine's backup deletable on a shared location", len(scoped))
	}
	if unmanaged != 0 {
		t.Errorf("managed-by-nobody = %d, want 0. A contended archive is the other machine's to prune and its to report, so counting it here inflates this host's owned total, which is the \"40/7\" summary discussion #292 opened with", unmanaged)
	}
	if level := logger.levelOf("do not belong to pve"); level != "WARNING" {
		t.Errorf("the contended line was emitted at %q, want WARNING. Adoption swallowing it removes the operator's only notice that another machine writes here, and with it the run's exit code promotion", level)
	}
	if level := logger.levelOf("back into rotation"); level != "" {
		t.Errorf("an adoption line was printed at %s for an archive this host may not claim", level)
	}
}

// TestAdoptionStillRepairsTheKernelNameOnAnAliasBearingHost is the other half of the
// boundary: the containment clause narrows what may be claimed under an ALIAS's label,
// and it must leave what may be claimed under the host's OWN label exactly where it
// was. A fix that refused adoption whenever the machine holds any alias would pass
// every containment check in this file and quietly reopen discussion #292 for every
// host whose "hostname -f" still answers.
func TestAdoptionStillRepairsTheKernelNameOnAnAliasBearingHost(t *testing.T) {
	tests := []struct {
		name    string
		meta    *types.BackupMetadata
		id      retentionIdentity
		adopted bool
	}{
		{
			// DISCUSSION #292, unchanged: the machine resolves only its kernel name.
			name:    "the lost FQDN of a host with no alias is adopted",
			meta:    &types.BackupMetadata{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: ourServerID},
			id:      hostWithIdentity("pve", ourServerID),
			adopted: true,
		},
		{
			// The same repair on a host that ALSO answers to an unrelated name. The
			// alias says nothing about the label "pve", so it must not block it.
			name:    "the lost FQDN is still adopted when the host also answers to a differently labelled alias",
			meta:    &types.BackupMetadata{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: ourServerID},
			id:      renamedHost("pve", "nas", ourServerID),
			adopted: true,
		},
		{
			// THE DEFECT. The label is the alias's, not this host's.
			name:    "an archive qualifying an alias's label is refused",
			meta:    &types.BackupMetadata{BackupFile: "nas.lan-backup-20250102-100000.tar.zst", Hostname: "nas.lan", ServerID: ourServerID},
			id:      renamedHost("pve", "nas", ourServerID),
			adopted: false,
		},
		{
			// The maintainer's reproduction, at the predicate.
			name:    "an archive qualifying a second alias's label is refused",
			meta:    &types.BackupMetadata{BackupFile: "backup01.lan-backup-20250102-100000.tar.zst", Hostname: "backup01.lan", ServerID: ourServerID},
			id:      renamedHost("pve", "backup01", ourServerID),
			adopted: false,
		},
		{
			// The alias is itself qualified, so the host answers to "nas.lan" whole.
			// Adopting "nas.siteb.example" would be the clone in another domain, and
			// clause e refuses it; the containment clause refuses it first.
			name:    "an archive in a third domain under a qualified alias's label is refused",
			meta:    &types.BackupMetadata{BackupFile: "nas.siteb.example-backup-20250102-100000.tar.zst", Hostname: "nas.siteb.example", ServerID: ourServerID},
			id:      renamedHost("pve", "nas.lan", ourServerID),
			adopted: false,
		},
		{
			// A qualified kernel name holds a competing spelling of its own label, so
			// there is no lost qualification to repair and clause e refuses.
			name:    "a host whose kernel name is qualified adopts no third spelling of its label",
			meta:    &types.BackupMetadata{BackupFile: "pve.siteb.example-backup-20250102-100000.tar.zst", Hostname: "pve.siteb.example", ServerID: ourServerID},
			id:      hostWithIdentity("pve.home.arpa", ourServerID),
			adopted: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := archiveAdoptedByServerID(tt.meta, tt.id); got != tt.adopted {
				t.Errorf("archiveAdoptedByServerID = %v, want %v", got, tt.adopted)
			}
		})
	}
}

// TestTheContainmentPredicateRefusesAHostThatCannotNameItself pins the empty-label
// guard inside the shared predicate, which is the one place the refactor could have
// lost something silently. The two populations are now decided by one function, so
// that function carries the guard for both, and dropping it is compile clean.
//
// hostShortLabel("") is "", and an unattributable archive has no owner and therefore
// no label either, so without the guard a machine that cannot name itself matches
// every pre-Go "proxmox-backup-*" file in the location at once. Those entries are
// already counted as unattributable, and applyRetentionHostScope adds the two counts,
// so each of them would be added TWICE to the number RetentionSummary.Owned publishes
// and the notification would report more archives than the directory holds.
//
// A bare "." is not a decorative case: it survives the TrimSpace guard in
// applyRetentionHostScope and only collapses to the empty string inside
// NormalizeHostname, so it is the shape that actually reaches this predicate with no
// label to compare.
func TestTheContainmentPredicateRefusesAHostThatCannotNameItself(t *testing.T) {
	unattributable := &types.BackupMetadata{BackupFile: "proxmox-backup-20250102-100000.tar.gz"}
	listing := []*types.BackupMetadata{
		unattributable,
		{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", Hostname: "pve.home.arpa"},
	}

	for _, hostname := range []string{"", "   ", "."} {
		id := hostWithIdentity(hostname, ourServerID)
		if archiveSharesLocalShortLabel(unattributable, id) {
			t.Errorf("host %q claims an archive nobody can name shares its short label. Both labels are empty, and equal emptiness is not a shared name", hostname)
		}
		if n := retentionSpellingMismatches(listing, id); n != 0 {
			t.Errorf("host %q reports %d spelling mismatch(es), want 0. It cannot name itself, so nothing can share its name; counting the unattributable entry here adds it a second time to the managed-by-nobody total that RetentionSummary.Owned publishes", hostname, n)
		}
	}
}
