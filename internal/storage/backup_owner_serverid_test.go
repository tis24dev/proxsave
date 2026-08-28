package storage

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// ourServerID and anotherServerID are two well-formed identities of the shape
// internal/identity mints: exactly sixteen decimal digits. They are compared as bytes
// and never parsed, so nothing about their numeric value matters.
const (
	// Leading zero on purpose: identity.normalizeServerID left-pads to sixteen digits,
	// so this is a shape the minting side really produces, and it is the only one that
	// does NOT survive a numeric parse and reformat. A zero-free fixture leaves every
	// plumb free to round-trip the value through a number without any test noticing.
	ourServerID     = "0123456789012345"
	anotherServerID = "6543210987654321"
)

// hostWithIdentity builds the identity of a host that DOES know its own server
// identity. It is the counterpart of hostOnly, which describes a host that does not,
// and the two names are how these files say which of the two mechanisms a row is
// exercising.
func hostWithIdentity(hostname, serverID string, written ...string) retentionIdentity {
	return retentionIdentity{hostname: hostname, aliases: written, serverID: serverID}
}

// TestArchiveAdoptedByServerID walks every cell of the ownership rule's truth table.
// The rule is a total function over a small tuple - the archive's manifest hostname,
// the archive's identity, this host's names and this host's identity - so the table
// IS the specification, and a cell nobody wrote down is a cell nobody decided.
//
// The row that matters most is "a second site sharing our short name is refused even
// with our identity". It is the cell that dies the moment clause e is weakened into a
// short-label fold, and it is what keeps the pinned data-loss boundary in
// backup_owner_test.go intact now that identities exist. On a stock Proxmox node
// os.Hostname returns the kernel SHORT name while the writer stamps the FQDN, which is
// the whole premise of discussion #292, so a rule that only compares the archive's
// name against the local name degenerates to exactly that fold.
func TestArchiveAdoptedByServerID(t *testing.T) {
	tests := []struct {
		name string
		meta *types.BackupMetadata
		id   retentionIdentity
		// adopted is what archiveAdoptedByServerID must answer.
		adopted bool
		// owned is what backupBelongsToHost must answer, which is the hostname arm
		// OR the adoption arm. The two columns differ on every row where the
		// hostname alone already decides.
		owned bool
	}{
		{
			// DISCUSSION #292. This host stamped its FQDN into the archives and can
			// now resolve only the kernel short name, so it has no alias at all. The
			// identity is the only thing left that says the archive is its own work.
			name:    "an archive naming a spelling this host lost is adopted on its own identity",
			meta:    &types.BackupMetadata{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: ourServerID},
			id:      hostWithIdentity("pve", ourServerID),
			adopted: true,
			owned:   true,
		},
		{
			name:    "spelling is normalised before the comparison, case and root dot alike",
			meta:    &types.BackupMetadata{BackupFile: "pve-backup-20250102-100000.tar.zst", Hostname: "PVE.Home.Arpa.", ServerID: " " + ourServerID + " "},
			id:      hostWithIdentity("pve", ourServerID),
			adopted: true,
			owned:   true,
		},
		{
			// THE DATA-LOSS BOUNDARY, with identities. This host still resolves its
			// own FQDN, so it holds a competing spelling of the short label "pve".
			// An archive naming a THIRD spelling is a second machine, or a clone in
			// another domain that inherited this identity, and inheriting an identity
			// is expected behaviour. Refusing here is what stops the clone pruning
			// the source machine's archives on a shared location.
			name:    "a second site sharing our short name is refused even with our identity",
			meta:    &types.BackupMetadata{BackupFile: "pve.siteb.example-backup-20250102-100000.tar.zst", Hostname: "pve.siteb.example", ServerID: ourServerID},
			id:      hostWithIdentity("pve", ourServerID, "pve.home.arpa"),
			adopted: false,
			owned:   false,
		},
		{
			// A clone that was renamed outright. Its identity is ours, its short
			// label is not, and clause d refuses on the label alone.
			name:    "an archive whose short name this host does not answer to is refused",
			meta:    &types.BackupMetadata{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: ourServerID},
			id:      hostWithIdentity("pve-clone", ourServerID),
			adopted: false,
			owned:   false,
		},
		{
			// The case being repaired is a LOST QUALIFICATION. A bare name this host
			// does not answer to is simply another machine, whatever it carries.
			name:    "an unqualified archive name is refused",
			meta:    &types.BackupMetadata{BackupFile: "srv-backup-20250102-100000.tar.zst", Hostname: "srv", ServerID: ourServerID},
			id:      hostWithIdentity("pve", ourServerID),
			adopted: false,
			owned:   false,
		},
		{
			// A trailing dot is a root dot, which NormalizeHostname strips, so this
			// reaches clause c as the bare label it really is.
			name:    "a name that is only a label and a dot is refused",
			meta:    &types.BackupMetadata{BackupFile: "srv-backup-20250102-100000.tar.zst", Hostname: "srv.", ServerID: ourServerID},
			id:      hostWithIdentity("pve", ourServerID),
			adopted: false,
			owned:   false,
		},
		{
			// The filename token is the DEGRADED attribution path, and a file on a
			// shared location can be renamed by anyone who can write there. It must
			// not gain the power to pull an identity match along with it.
			name:    "a name that came only from the filename token is refused",
			meta:    &types.BackupMetadata{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", ServerID: ourServerID},
			id:      hostWithIdentity("pve", ourServerID),
			adopted: false,
			owned:   false,
		},
		{
			name:    "an archive recording no identity keeps exactly its pre-change answer",
			meta:    &types.BackupMetadata{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", Hostname: "pve.home.arpa"},
			id:      hostWithIdentity("pve", ourServerID),
			adopted: false,
			owned:   false,
		},
		{
			// A host that cannot name its own identity may not borrow somebody
			// else's. This is every host whose identity detection failed.
			name:    "a host that does not know its own identity adopts nothing",
			meta:    &types.BackupMetadata{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: ourServerID},
			id:      hostOnly("pve"),
			adopted: false,
			owned:   false,
		},
		{
			name:    "a malformed archive identity compares as absent, never as a match",
			meta:    &types.BackupMetadata{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: "123456789012345"},
			id:      hostWithIdentity("pve", ourServerID),
			adopted: false,
			owned:   false,
		},
		{
			name:    "two different identities never adopt",
			meta:    &types.BackupMetadata{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: anotherServerID},
			id:      hostWithIdentity("pve", ourServerID),
			adopted: false,
			owned:   false,
		},
		{
			// THE NO-VETO PIN. The identity may never REMOVE a claim the hostname
			// makes. A host whose identity file was regenerated - which happens on
			// every reinstall, since the identity seed carries a timestamp - keeps
			// rotating its own archives instead of stranding them for ever.
			name:    "an archive this host owns by name stays owned when the identities differ",
			meta:    &types.BackupMetadata{BackupFile: "pve-backup-20250102-100000.tar.zst", Hostname: "pve", ServerID: anotherServerID},
			id:      hostWithIdentity("pve", ourServerID),
			adopted: false,
			owned:   true,
		},
		{
			name:    "an archive this host owns by alias stays owned when the identities differ",
			meta:    &types.BackupMetadata{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: anotherServerID},
			id:      hostWithIdentity("pve", ourServerID, "pve.home.arpa"),
			adopted: false,
			owned:   true,
		},
		{
			// THE IDENTITY NEVER ACTS ALONE. A pre-Go archive names no host anywhere,
			// so nothing may claim it, and an identity is not a name.
			name:    "an unattributable legacy archive is claimed by nobody, identity or not",
			meta:    &types.BackupMetadata{BackupFile: "proxmox-backup-20250102-100000.tar.gz", ServerID: ourServerID},
			id:      hostWithIdentity("pve", ourServerID),
			adopted: false,
			owned:   false,
		},
		{
			// A machine that cannot name itself deletes nothing, and the identity
			// does not change that. It is refused twice over: the blank-hostname
			// guard in backupBelongsToHost runs before anything else, and clause d
			// has no name to match the archive's short label against either.
			name:    "a host that cannot name itself adopts nothing",
			meta:    &types.BackupMetadata{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: ourServerID},
			id:      hostWithIdentity("", ourServerID),
			adopted: false,
			owned:   false,
		},
		{
			name:    "a nil entry is nobody's",
			meta:    nil,
			id:      hostWithIdentity("pve", ourServerID),
			adopted: false,
			owned:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := archiveAdoptedByServerID(tt.meta, tt.id); got != tt.adopted {
				t.Errorf("archiveAdoptedByServerID = %v, want %v", got, tt.adopted)
			}
			if got := backupBelongsToHost(tt.meta, tt.id); got != tt.owned {
				t.Errorf("backupBelongsToHost = %v, want %v", got, tt.owned)
			}
		})
	}
}

// TestAdoptionNeedsTwoEqualValidatedIdentities states clause b as a property over the
// whole identity space rather than as the handful of rows the table above can hold:
// whatever the names say, adoption is impossible unless BOTH sides carry a validated
// identity and the two are the same string.
//
// The empty and malformed values are the point. "" means ABSENT on both sides, and an
// implementation that compared the raw fields would make every archive written before
// this change match every host that also carries nothing, which is every host in the
// installed base at once.
func TestAdoptionNeedsTwoEqualValidatedIdentities(t *testing.T) {
	values := []string{"", "   ", ourServerID, anotherServerID, "123456789012345", "12345678901234567", "abcdefabcdefabcd", "unknown", "0"}

	for _, archiveID := range values {
		for _, localID := range values {
			meta := &types.BackupMetadata{
				BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst",
				Hostname:   "pve.home.arpa",
				ServerID:   archiveID,
			}
			id := hostWithIdentity("pve", localID)

			// Every other clause holds on this fixture, so the answer is decided by
			// clause b alone and the property reads as a biconditional.
			comparable := types.NormalizeServerID(archiveID) != "" && types.NormalizeServerID(archiveID) == types.NormalizeServerID(localID)
			if got := archiveAdoptedByServerID(meta, id); got != comparable {
				t.Errorf("archive identity %q against local identity %q: adopted = %v, want %v. Adoption is only ever a confirmation of two validated, equal identities; anything else must fall back to the hostname rule alone", archiveID, localID, got, comparable)
			}
		}
	}
}

// TestAdoptedArchivesAreASubsetOfTheReportedSpellingMismatches is the containment
// proof as an executable check. Clause d forces the archive's short label to be a name
// this host answers to, and the kernel name is one of those names, so anything adopted
// must already have shared this host's short label - which is exactly the population
// retentionSpellingMismatches counts and reports today.
//
// It bounds the blast radius of the whole change without reading the rest of the diff:
// adoption can never reach an archive attributed to another label, and never one
// nobody can name.
func TestAdoptedArchivesAreASubsetOfTheReportedSpellingMismatches(t *testing.T) {
	id := hostWithIdentity("pve", ourServerID)
	candidates := []*types.BackupMetadata{
		{BackupFile: "pve.home.arpa-backup-20250105-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: ourServerID},
		{BackupFile: "pve.siteb.example-backup-20250104-100000.tar.zst", Hostname: "pve.siteb.example", ServerID: ourServerID},
		{BackupFile: "pbs.home.arpa-backup-20250103-100000.tar.zst", Hostname: "pbs.home.arpa", ServerID: ourServerID},
		{BackupFile: "proxmox-backup-20250102-100000.tar.gz", ServerID: ourServerID},
		{BackupFile: "other-backup-20250101-100000.tar.zst", Hostname: "other", ServerID: ourServerID},
	}

	for _, meta := range candidates {
		if !archiveAdoptedByServerID(meta, id) {
			continue
		}
		// The mismatch helper is fed this one entry as though it were foreign, which
		// is what it would have been without the identity.
		if n := retentionSpellingMismatches([]*types.BackupMetadata{meta}, id); n != 1 {
			t.Errorf("%s was adopted but is not in the population retentionSpellingMismatches already reports. Adoption must only ever widen inside that set, or it has reached an archive no existing warning ever mentioned", meta.BackupFile)
		}
	}
}
