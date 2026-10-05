package storage

import (
	"strings"
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

// TestArchiveAdoptedByServerID walks the ownership rule's truth table. The rule is a
// total function over a small tuple - the archive's names, the archive's identity,
// this host's names and this host's identity - so the table IS the specification.
//
// The rule: the same server identity is the same server, whatever name the archive
// carries. The rows that changed when that rule was decided are the ones naming
// another short label, a bare name, a competing spelling and a filename-only name:
// each was refused before and is adopted now. A clone with this host's identity on the
// same location is therefore rotated by this host, which was stated and accepted.
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
			// now resolve only the kernel short name, so it has no alias at all.
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
			// This host still resolves its own FQDN, so it holds a competing spelling
			// of the short label "pve". Same identity, same server.
			name:    "a competing spelling of our short name carrying our identity is adopted",
			meta:    &types.BackupMetadata{BackupFile: "pve.siteb.example-backup-20250102-100000.tar.zst", Hostname: "pve.siteb.example", ServerID: ourServerID},
			id:      hostWithIdentity("pve", ourServerID, "pve.home.arpa"),
			adopted: true,
			owned:   true,
		},
		{
			// A renamed host, or a clone that was renamed outright.
			name:    "an archive under another short label carrying our identity is adopted",
			meta:    &types.BackupMetadata{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", Hostname: "pve.home.arpa", ServerID: ourServerID},
			id:      hostWithIdentity("pve-clone", ourServerID),
			adopted: true,
			owned:   true,
		},
		{
			name:    "a bare name carrying our identity is adopted",
			meta:    &types.BackupMetadata{BackupFile: "srv-backup-20250102-100000.tar.zst", Hostname: "srv", ServerID: ourServerID},
			id:      hostWithIdentity("pve", ourServerID),
			adopted: true,
			owned:   true,
		},
		{
			// The manifest names no host, the filename token does.
			name:    "a name that came only from the filename token is adopted on our identity",
			meta:    &types.BackupMetadata{BackupFile: "pve.home.arpa-backup-20250102-100000.tar.zst", ServerID: ourServerID},
			id:      hostWithIdentity("pve", ourServerID),
			adopted: true,
			owned:   true,
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
			// Owned by the hostname arm, so it is not counted as adopted.
			name:    "an archive this host owns by name and identity is owned, not adopted",
			meta:    &types.BackupMetadata{BackupFile: "pve-backup-20250102-100000.tar.zst", Hostname: "pve", ServerID: ourServerID},
			id:      hostWithIdentity("pve", ourServerID),
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
			// does not change that.
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

// TestAdoptionNeedsTwoEqualValidatedIdentities states the identity clause as a property over the
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
			// the identity clause alone and the property reads as a biconditional.
			comparable := types.NormalizeServerID(archiveID) != "" && types.NormalizeServerID(archiveID) == types.NormalizeServerID(localID)
			if got := archiveAdoptedByServerID(meta, id); got != comparable {
				t.Errorf("archive identity %q against local identity %q: adopted = %v, want %v. Adoption is only ever a confirmation of two validated, equal identities; anything else must fall back to the hostname rule alone", archiveID, localID, got, comparable)
			}
		}
	}
}

// identitySpaceHosts, identitySpaceIDs and identitySpaceIdentities are the space the
// properties below walk: degenerate names NormalizeHostname collapses, this host's own
// spellings, other labels, rename artefacts, and the host shapes that adopt nothing.
var identitySpaceHosts = []string{
	"", "   ", ".", "pve", "pve.", "pve.home.arpa", "pve.siteb.example",
	"nas", "nas.lan", "backup01.lan", "other", "unknown", "unknown.lan",
}

var identitySpaceIDs = []string{ourServerID, anotherServerID, "", "123456789012345"}

func identitySpaceIdentities() []retentionIdentity {
	return []retentionIdentity{
		hostWithIdentity("pve", ourServerID),
		hostWithIdentity("pve", ourServerID, "pve.home.arpa"),
		hostWithIdentity("pve.home.arpa", ourServerID),
		{hostname: "pve", aliases: retentionHostAliases("pve", []string{"nas"}), serverID: ourServerID},
		hostOnly("pve"),
		hostOnly("pve", "pve.home.arpa"),
		hostWithIdentity("", ourServerID),
	}
}

// TestAnArchiveWithoutOurIdentityKeepsTheHostnameRuleAnswer is the half of the rule
// that must not move: an archive recording no identity, a malformed one, or another
// machine's, and every archive read by a host that does not know its own identity, is
// classified exactly as the hostname rule alone classifies it.
func TestAnArchiveWithoutOurIdentityKeepsTheHostnameRuleAnswer(t *testing.T) {
	for _, archiveHost := range identitySpaceHosts {
		for _, archiveID := range identitySpaceIDs {
			for _, id := range identitySpaceIdentities() {
				local := types.NormalizeServerID(id.serverID)
				if local != "" && types.NormalizeServerID(archiveID) == local {
					continue
				}
				for _, file := range []string{"backup01.lan-backup-20250102-100000.tar.zst", "proxmox-backup-20250102-100000.tar.gz"} {
					meta := &types.BackupMetadata{BackupFile: file, Hostname: archiveHost, ServerID: archiveID}
					nameOnly := retentionIdentity{hostname: id.hostname, aliases: id.aliases}
					if got, want := backupBelongsToHost(meta, id), backupBelongsToHost(meta, nameOnly); got != want {
						t.Errorf("archive %q (%s, identity %q) read by host %q (aliases %v, identity %q): owned = %v, the hostname rule alone says %v", archiveHost, file, archiveID, id.hostname, id.aliases, id.serverID, got, want)
					}
				}
			}
		}
	}
}

// TestAnArchiveWithOurIdentityAndANameIsAlwaysOurs is the half that changed: on a host
// that can name itself and knows its identity, an archive carrying that identity and
// any name at all, from the manifest or the filename token, is this host's.
func TestAnArchiveWithOurIdentityAndANameIsAlwaysOurs(t *testing.T) {
	for _, archiveHost := range identitySpaceHosts {
		for _, id := range identitySpaceIdentities() {
			if strings.TrimSpace(id.hostname) == "" || types.NormalizeServerID(id.serverID) == "" {
				continue
			}
			for _, file := range []string{"backup01.lan-backup-20250102-100000.tar.zst", "proxmox-backup-20250102-100000.tar.gz"} {
				meta := &types.BackupMetadata{BackupFile: file, Hostname: archiveHost, ServerID: ourServerID}
				named := types.NormalizeHostname(backupOwnerHost(meta)) != ""
				if got := backupBelongsToHost(meta, id); got != named {
					t.Errorf("archive %q (%s) with this host's identity, read by host %q (aliases %v): owned = %v, want %v (named=%v)", archiveHost, file, id.hostname, id.aliases, got, named, named)
				}
			}
		}
	}
}

// TestAdoptionRefusalNamesTheClauseThatFired pins the per-entry DEBUG prose to the
// clause chain. It is the operator's only per-file explanation in a debug log.
//
// The degenerate row is the point of the table. A manifest hostname of "." survives
// TrimSpace and only collapses inside NormalizeHostname, so it must fall through to
// the filename token exactly as an empty one does.
func TestAdoptionRefusalNamesTheClauseThatFired(t *testing.T) {
	tests := []struct {
		name   string
		meta   *types.BackupMetadata
		id     retentionIdentity
		reason retentionRefusal
		says   string
	}{
		{name: "no entry at all", meta: nil, id: hostWithIdentity("pve", ourServerID), reason: refusalNoEntry, says: "no entry"},
		{name: "this host cannot name itself", meta: &types.BackupMetadata{Hostname: "pve.home.arpa", ServerID: ourServerID}, id: hostWithIdentity("", ourServerID), reason: refusalNoLocalHostname, says: "cannot name itself"},
		{name: "this host does not know its own identity", meta: &types.BackupMetadata{Hostname: "pve.home.arpa", ServerID: ourServerID}, id: hostOnly("pve"), reason: refusalNoLocalIdentity, says: "does not know its own server identity"},
		{name: "nothing names the host", meta: &types.BackupMetadata{BackupFile: "proxmox-backup-20250102-100000.tar.gz", ServerID: ourServerID}, id: hostWithIdentity("pve", ourServerID), reason: refusalNoHostName, says: "nothing names the host"},
		{name: "a manifest of a bare dot names no host either", meta: &types.BackupMetadata{BackupFile: "proxmox-backup-20250102-100000.tar.gz", Hostname: ".", ServerID: ourServerID}, id: hostWithIdentity("pve", ourServerID), reason: refusalNoHostName, says: "nothing names the host"},
		{name: "the archive records no identity", meta: &types.BackupMetadata{Hostname: "pve.home.arpa"}, id: hostWithIdentity("pve", ourServerID), reason: refusalNoArchiveIdentity, says: "no readable server identity"},
		{name: "the archive records somebody else's identity", meta: &types.BackupMetadata{Hostname: "pve.home.arpa", ServerID: anotherServerID}, id: hostWithIdentity("pve", ourServerID), reason: refusalOtherIdentity, says: "another machine's server identity"},
		{name: "this host owns it by name", meta: &types.BackupMetadata{Hostname: "pve", ServerID: ourServerID}, id: hostWithIdentity("pve", ourServerID), reason: refusalOwnedByName, says: "owns it by name"},
		{name: "nothing refused it", meta: &types.BackupMetadata{Hostname: "pve.home.arpa", ServerID: ourServerID}, id: hostWithIdentity("pve", ourServerID), reason: refusalNone, says: "no clause refused it"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retentionAdoptionRefusal(tt.meta, tt.id); got != tt.reason {
				t.Errorf("retentionAdoptionRefusal = %v, want %v", got, tt.reason)
			}
			if got := adoptionRefusal(tt.meta, tt.id); !strings.Contains(got, tt.says) {
				t.Errorf("the Debug line says %q, which does not name the clause that fired (%q)", got, tt.says)
			}
		})
	}
}
