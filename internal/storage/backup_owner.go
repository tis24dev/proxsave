package storage

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
)

// retentionHostname is a seam so a test can pin the local hostname.
var retentionHostname = os.Hostname

// resolveRetentionHostname reads the local hostname once, at storage construction,
// so every retention pass of that backend uses a stable value. It is resolved
// per-instance rather than per-call because the tests run in parallel and a shared
// mutable global would let one backend's fixture host leak into another's.
func resolveRetentionHostname() string {
	host, err := retentionHostname()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(host)
}

// retentionIdentity is everything a retention pass knows about the machine running
// it: the name the kernel reports, the other names this run's writer stamped into
// the archives it produced, and this host's own server identity.
//
// It is a struct rather than three parameters for a reason that is not cosmetic. The
// functions taking it used to end in "aliases ...string", so adding a plain
// serverID string parameter would have silently rebound an existing alias argument
// at every two-argument call site and compiled clean, turning a name this host
// answers to into an identity it claims to be. The struct makes the compiler visit
// every call site instead.
type retentionIdentity struct {
	// hostname is what os.Hostname reports, resolved once at backend construction.
	hostname string
	// aliases are the other names this machine answers to, already reduced by
	// retentionHostAliases: blanks, the "unknown" sentinel and repeats of hostname
	// are gone.
	aliases []string
	// serverID is this host's own identity, normalised, or "" when this host does
	// not know it. "" disables the adoption arm entirely: a host that cannot name
	// its own identity may not borrow somebody else's.
	serverID string
}

// names returns every spelling this machine answers to, the kernel name first.
//
// Clause e of the adoption rule reads the whole set, because a host that still
// resolves its FQDN answers to two spellings of one short label and that fact is
// exactly what disqualifies it from adopting a third. The containment clause d does
// NOT: it reads the kernel name alone, through shortLabel below. The two ask
// different questions on purpose, and an earlier version that let clause d read this
// set is the defect shortLabel's own comment describes.
func (id retentionIdentity) names() []string {
	out := make([]string, 0, len(id.aliases)+1)
	if key := types.NormalizeHostname(id.hostname); key != "" {
		out = append(out, key)
	}
	for _, alias := range id.aliases {
		if key := types.NormalizeHostname(alias); key != "" && key != unresolvedHostname {
			out = append(out, key)
		}
	}
	return out
}

// shortLabel is the one first label this host reports under: the first label of the
// name the kernel gives, normalised. Aliases are deliberately NOT folded in.
//
// The reason is not that an alias could never be the lost spelling. It could: a host
// whose "hostname -f" answered "nas.example.com" a year ago and answers "nas" today
// has real work stranded under a label that is an alias's, not the kernel name's.
// The reason is narrower and it is about what retention is allowed to do rather than
// about what is true. This is the key retentionSpellingMismatches counts under, and
// adoption may only ever claim from inside the population retention already reports
// as possibly its own. Sharing the key is what makes that a property of the code.
// Widening the key would widen what is reported as this host's unmanaged work, which
// is a published number (RetentionSummary.Owned), and it would put another machine's
// archives into it.
//
// So an archive under an alias's label carrying this host's identity is left alone
// and reported as contended. That is the fail-closed side of the trade, and it is the
// side to be on: not rotating an archive grows a directory, and deleting one that
// turns out to be another machine's cannot be undone.
func (id retentionIdentity) shortLabel() string {
	return hostShortLabel(types.NormalizeHostname(id.hostname))
}

// backupBelongsToHost reports whether a listed backup was produced by the given
// host, using the manifest's "hostname" - the same authoritative value the restore
// selector renders as its Hostname column (formatBackupCandidateHostname). The
// filename is never parsed for this: it merely embeds the host token, and matching
// it with a wildcard (the "*-backup-*" glob, or the "-backup-" substring on the
// cloud side) accepts every host, which is exactly how retention ended up able to
// delete another machine's archives.
//
// The manifest is preferred but not required. When it cannot be read - a corrupt
// or missing .metadata next to an archive that still carries a valid .sha256 - the
// owner falls back to the host token the filename embeds ("<host>-backup-<ts>").
// That fallback matters: such a backup counts as Verified today and IS pruned, so
// refusing to attribute it would silently stop rotating it and let the location
// grow without bound. The token is still host-specific, so the cross-host guarantee
// survives the degradation.
//
// A machine does not always spell its own name the same way. The writer records
// what "hostname -f" returns (pve.home.arpa) while os.Hostname() reports the
// kernel short name (pve), so retention is told both: hostname is what the kernel
// reports and aliases are the names this run's writer actually stamped into
// archives. Ownership is still an exact match against one of those names. It is
// never a fold to the first label: a host that cannot resolve its own domain must
// not claim "pve.siteB.example" just because it is called "pve", or one machine's
// retention would prune another machine's archives.
//
// Fail-closed only when neither source yields a host, and whenever this machine
// cannot name itself: retention then leaves the entry alone rather than delete on a
// guess. A pre-Go "proxmox-backup-*" name has no token, so when its manifest names
// no host either it is attributable to nobody and owned by nobody.
//
// That last case used to be an exception: a legacy archive with no manifest hostname
// was claimed by whoever listed it, on the reasoning that otherwise nothing would
// ever rotate it. The reasoning was about the DIRECTORY, not about the archive, and
// "whoever lists it owns it" is only sound if listing implies exclusivity, which
// nothing in this process can establish. All three locations are routinely shared:
// the shipped CLOUD_REMOTE_PATH default is a root with no host component
// (internal/config/templates/backup.env), the documented secondary layout is a NAS
// several hosts mount, and a BACKUP_PATH can itself be an NFS or CIFS mount, which is
// what discussion #292 reports. Ownership is therefore a property of the ARCHIVE
// alone and never of the location it sits in, which is why no question about the
// location has to be answered here and why a networked BACKUP_PATH needs no case of
// its own. An archive nobody can name is left alone by every host and reported, which
// applyRetentionHostScope does.
//
// Two mechanisms run side by side here, and the second never replaces the first.
// Everything above is the hostname rule, and it decides every archive that exists
// today: no archive written before this field existed carries a server identity, and
// a pre-Go "proxmox-backup-*" name carries neither. The identity is an ADDITIONAL
// and strictly narrower signal, available only on archives written from this version
// on, and archiveAdoptedByServerID states the whole of what it may do.
func backupBelongsToHost(meta *types.BackupMetadata, id retentionIdentity) bool {
	if meta == nil {
		return false
	}
	if strings.TrimSpace(id.hostname) == "" {
		return false
	}
	owner := backupOwnerHost(meta)
	if owner == "" {
		return false
	}
	// The hostname arm is evaluated first and carries NO server id term. That is the
	// half of the invariant that says the identity can never REMOVE a claim: an
	// archive naming a spelling this machine answers to stays this machine's own
	// however its recorded identity compares, so a host that lost or regenerated its
	// identity file keeps rotating its own archives instead of stranding them for
	// ever at WARNING, which is the very symptom discussion #292 reports.
	//
	// The adoption arm is the other half: it can only ever add, and only inside the
	// narrow population archiveAdoptedByServerID admits.
	return hostOwnsName(owner, id.hostname, id.aliases...) || archiveAdoptedByServerID(meta, id)
}

// archiveServerID is the identity an archive records, validated. "" means the
// archive records none this binary can read, and every caller must treat that as
// "cannot compare" rather than as a match.
func archiveServerID(meta *types.BackupMetadata) string {
	if meta == nil {
		return ""
	}
	return types.NormalizeServerID(meta.ServerID)
}

// hostAnswersOnlyToBareLabel reports whether the ONLY spelling of label this machine
// answers to is the bare label itself. It is the clause that keeps the adoption arm
// from reaching another machine.
//
// A host that still resolves its own qualified name holds a competing spelling of
// its short label, so an archive naming a THIRD spelling of that label is a second
// machine, or a clone in another domain, and not this host under a name it lost. A
// host that has lost qualified resolution holds only the bare label, which is
// precisely the degraded state discussion #292 describes. That difference is the
// only evidence inside this process that separates "me, degraded" from "somebody
// else with the same short name", and it is why the two-site fixture in
// backup_owner_test.go stays green once identities exist.
func hostAnswersOnlyToBareLabel(label string, id retentionIdentity) bool {
	if label == "" {
		return false
	}
	for _, name := range id.names() {
		if hostShortLabel(name) == label && name != label {
			return false
		}
	}
	return true
}

// archiveAdoptedByServerID reports whether an archive this host does not answer to BY
// NAME may nonetheless be claimed because it carries this host's own server identity.
// It is the whole of the discussion #292 fix and it is deliberately narrow: every one
// of the clauses below must hold, and any one of them failing leaves the archive
// classified exactly as it is today.
//
// The identity may only CONFIRM a claim the hostname already makes ambiguously. It
// may never CREATE one (clause a refuses a name that came from the filename, and
// backupOwnerHost returning "" is refused by the caller before this is reached), and
// it may never REMOVE one (this function is only ever OR-ed after the hostname arm).
//
// Containment is clause d, and it is a shared call rather than an argument. Clause d
// IS archiveSharesLocalShortLabel, the same predicate retentionSpellingMismatches
// counts with, so "everything adoption can claim is already inside the population
// retention reports" holds by construction: one function decides both sets, and an
// edit to it cannot move one without moving the other. Adoption can never reach an
// archive attributed to another first label, and never an archive nobody can name,
// because that one predicate refuses both.
//
// It has to be the shared call and not an equivalent expression. An earlier version
// argued containment from a weaker clause, "the archive's short label is a name this
// machine answers to", on the reasoning that the kernel name is one of those names.
// That is a non sequitur: being IN the name set does not make a label EQUAL to the
// kernel name's label, and on a host holding an alias with a different first label
// the two populations came apart, letting adoption claim an archive every printed
// line called another machine's. See clause d for the exact shape.
func archiveAdoptedByServerID(meta *types.BackupMetadata, id retentionIdentity) bool {
	if meta == nil {
		return false
	}

	// a. The name must have come from the MANIFEST. The "<host>-backup-<ts>" token a
	// filename carries is the degraded attribution path, and a file can be renamed by
	// anyone with write access to a shared location; it must not gain the power to
	// pull an identity match along with it.
	archiveHost := types.NormalizeHostname(strings.TrimSpace(meta.Hostname))
	if archiveHost == "" {
		return false
	}

	// b. Two VALIDATED identities, equal as bytes. Absent on either side means
	// "cannot compare" and stops here, which is every archive written before this
	// field existed and every host that does not know its own identity.
	local := types.NormalizeServerID(id.serverID)
	if local == "" {
		return false
	}
	if archiveServerID(meta) != local {
		return false
	}

	// c. The archive must name a QUALIFIED host. The case being repaired is a machine
	// that stamped its FQDN and can now resolve only the short name; an archive naming
	// a bare label this host does not answer to is a different machine's, whatever it
	// carries.
	dot := strings.IndexByte(archiveHost, '.')
	if dot <= 0 || dot == len(archiveHost)-1 {
		return false
	}

	// d. The archive's first label must be THIS HOST'S OWN first label, the one
	// id.shortLabel names. It is the containment clause, and it is deliberately the
	// same call retentionSpellingMismatches makes for its own line: adoption may only
	// ever claim from inside the population retention already reports.
	//
	// It used to read hostOwnsName(label, id.hostname, id.aliases...), which asked
	// whether the archive's label was ANY name this machine answers to. That is a
	// weaker question than the reporting side asks, and the two came apart on a host
	// carrying an alias with a different first label: /etc/hostname says "pve" while
	// /etc/hosts says "127.0.1.1 nas pve", so "hostname -f" returns "nas", the alias
	// set is {nas}, and an archive naming "nas.lan" satisfied the old clause without
	// ever having been reported as a spelling of "pve". That archive was CONTENDED,
	// another machine's as far as every line this file prints is concerned, refused by
	// retention and reported at WARNING. Adoption must not be able to reach it.
	//
	// backupOwnerHost inside the shared predicate resolves to exactly the value clause
	// a validated: a. has already established that meta.Hostname is non-blank, and
	// backupOwnerHost prefers that field whole, falling through to the filename token
	// only when it is blank. So the label this clause tests and the label clause e goes
	// on to use are the same string, and the filename token can no more enter here than
	// it can enter clause a. That depends on a. running FIRST; do not reorder them.
	if !archiveSharesLocalShortLabel(meta, id) {
		return false
	}
	label := hostShortLabel(archiveHost)

	// e. And it must answer to NO OTHER spelling of that label. See
	// hostAnswersOnlyToBareLabel: this is the clause that refuses a second machine or
	// a clone sitting in another domain.
	if !hostAnswersOnlyToBareLabel(label, id) {
		return false
	}

	// f. And, finally, the archive's own name is not one this host answers to. Rule 2
	// already ran in backupBelongsToHost, so this is guaranteed; it is restated so the
	// clause set reads as a closed predicate that can be checked on its own.
	return !hostOwnsName(archiveHost, id.hostname, id.aliases...)
}

// unresolvedHostname is what the writer stamps into an archive when the machine
// could not name itself (resolveHostname falls back to it). It names no host, so it
// never joins the identity set as an ALIAS: two machines that both failed to resolve
// would otherwise each claim the other's archives. It is refused as an alias only.
// The name the kernel reports is taken as given, so a machine the kernel really does
// call "unknown" keeps rotating its own archives exactly as it does today, and on a
// shared location it would also claim a second failed-to-resolve machine's
// "unknown-backup-*" archives. Reaching that needs os.Hostname to return the literal
// string, so it is left as it is rather than special-cased.
const unresolvedHostname = "unknown"

// retentionHostAliases reduces the names this run's writer used into the extra names
// retention accepts as this machine's own. Blanks, the "unknown" sentinel and
// repeats of the local hostname are dropped, so a machine with no domain ends up
// with no aliases and keeps exactly the strict behaviour it has today.
func retentionHostAliases(local string, written []string) []string {
	localKey := types.NormalizeHostname(local)
	var aliases []string
	for _, name := range written {
		key := types.NormalizeHostname(name)
		if key == "" || key == unresolvedHostname || key == localKey {
			continue
		}
		duplicate := false
		for _, seen := range aliases {
			if seen == key {
				duplicate = true
				break
			}
		}
		if !duplicate {
			aliases = append(aliases, key)
		}
	}
	return aliases
}

// hostOwnsName reports whether owner is one of the names this machine answers to:
// the name the kernel reports, plus the name(s) this run's writer stamped into the
// archives it produced, which today is exactly one (writtenHostname, plumbed through
// the three storage constructors). The set does not grow with the archives on disk:
// an archive this machine wrote under a spelling an earlier run resolved and this one
// cannot is not owned, and retentionSpellingMismatches reports it instead of claiming
// it. The match is exact once spelling is normalised. It is deliberately not a fold
// to the first label: "pve" and "pve.siteB.example" are two machines unless this
// machine itself answers to both, and folding them would let one host's retention
// prune the other's archives.
func hostOwnsName(owner, hostname string, aliases ...string) bool {
	key := types.NormalizeHostname(owner)
	if key == "" {
		return false
	}
	if key == types.NormalizeHostname(hostname) {
		return true
	}
	for _, alias := range aliases {
		// The sentinel is refused again here, not only in retentionHostAliases: an
		// alias is a name the writer produced, and "unknown" is what it produces
		// when it could not name the machine at all. Two machines that both failed
		// to resolve must not become owners of each other's archives, whichever
		// path assembled the list. The local hostname is left alone, so a machine
		// the kernel really does call "unknown" keeps rotating its own archives
		// exactly as it does today.
		if alias := types.NormalizeHostname(alias); alias != "" && alias != unresolvedHostname && key == alias {
			return true
		}
	}
	return false
}

// hostShortLabel returns the first label of a hostname, or the whole string when it
// carries no domain. Reporting only: ownership never consults it. A leading dot is
// degenerate input and keeps the whole string, so malformed names do not all
// collapse onto one empty label.
func hostShortLabel(host string) string {
	if idx := strings.IndexByte(host, '.'); idx > 0 {
		return host[:idx]
	}
	return host
}

// archiveSharesLocalShortLabel reports whether an archive is attributed to a host
// whose first label is this host's own first label. It is the single membership test
// of the population this file both REPORTS and may ADOPT from, and it exists as one
// function called from two places rather than as two expressions that happen to
// agree.
//
// That is the whole mechanism behind the containment archiveAdoptedByServerID relies
// on. Written twice, "adoption only ever claims inside what retention already
// reports" is a comment, and comments do not fail. Written once and called from both
// sides it is the code, and the subset relation survives any edit to it: adoption
// passes through this predicate AND through clauses a, b, c, e and f, so widening
// this predicate can only ever widen the reported population by at least as much as
// the adoptable one. The two cannot come apart again without the shared call being
// deliberately unpicked.
//
// The empty local label is refused rather than compared. hostShortLabel("") is "", so
// a machine that cannot name itself would otherwise match every unattributable entry
// at once, and a location full of pre-Go "proxmox-backup-*" archives would be
// reported as this host's own work under a lost spelling.
func archiveSharesLocalShortLabel(meta *types.BackupMetadata, id retentionIdentity) bool {
	if meta == nil {
		return false
	}
	local := id.shortLabel()
	if local == "" {
		return false
	}
	return hostShortLabel(types.NormalizeHostname(backupOwnerHost(meta))) == local
}

// retentionSpellingMismatches counts entries that look like this host's own work
// under a different spelling of its name: the owner shares the local short label
// without being one of the names this machine answers to. They are usually archives
// written while "hostname -f" resolved and it no longer does, so they have stopped
// rotating. They are reported, never claimed BY NAME: from here a name alone cannot
// tell them from a second machine with the same short name, and claiming them on the
// name would delete that machine's backups. A server identity can claim one, and
// archiveAdoptedByServerID may only ever do so from inside this very population,
// which is why the membership test below is a shared call and not a local
// expression.
func retentionSpellingMismatches(foreign []*types.BackupMetadata, id retentionIdentity) int {
	count := 0
	for _, b := range foreign {
		if archiveSharesLocalShortLabel(b, id) {
			count++
		}
	}
	return count
}

// retentionUnattributable counts the out-of-scope entries that name no writer at
// all, usually pre-Go "proxmox-backup-*" archives with no readable manifest beside
// them. It is keyed on ATTRIBUTABILITY rather than on the legacy prefix, because
// that is the real seam: the prefix is the common cause of the property, not the
// property itself, and a "*-backup-*" name whose timestamp does not parse deserves
// the same treatment for the same reason.
//
// The two kinds of out-of-scope entry are reported separately because they differ in
// kind, not only in degree. An archive attributed to another machine is CONTENTION:
// it is live information about what retention will and will not prune here, and it
// belongs in the run status. An archive nobody can name is a fixed backlog fact that
// no future run will change, and since no host will ever prune it, counting it as a
// run issue would promote every affected run to exit 1 for ever through
// applyIssueExitCode (internal/orchestrator/extensions.go), which is the symptom
// discussion #292 reported rather than a report of it.
func retentionUnattributable(foreign []*types.BackupMetadata) int {
	count := 0
	for _, b := range foreign {
		if b == nil {
			continue
		}
		if backupOwnerHost(b) == "" {
			count++
		}
	}
	return count
}

// backupOwnerHost resolves the owning host of a listed backup: the manifest's
// "hostname" when it was readable, otherwise the token the filename embeds.
func backupOwnerHost(meta *types.BackupMetadata) string {
	if owner := strings.TrimSpace(meta.Hostname); owner != "" {
		return owner
	}
	if isLegacyBackupName(meta.BackupFile) {
		// "proxmox-backup-<ts>" predates the "<host>-backup-<ts>" scheme, so its
		// leading token is the product name, not a host. Reading it as one would
		// attribute every legacy archive to a machine called "proxmox", which on a
		// shared location means that one machine pruning everyone else's archives.
		//
		// This branch sits BELOW the manifest check on purpose: a pre-Go archive
		// whose sidecar names its host in full is attributed by that name and keeps
		// rotating on that machine, and only one with no readable host anywhere ends
		// up attributable to nobody. Moving it above the manifest check is compile
		// clean and breaks every well-formed legacy archive, so do not.
		return ""
	}
	if host, _, ok := extractLogKeyFromBackup(meta.BackupFile); ok {
		return strings.TrimSpace(host)
	}
	return ""
}

// isLegacyBackupName reports whether the archive uses the pre-Go naming scheme.
func isLegacyBackupName(backupFile string) bool {
	return strings.HasPrefix(filepath.Base(strings.TrimSpace(backupFile)), legacyBackupPrefix)
}

// legacyBackupPrefix is the pre-Go archive name, which carries no host token.
const legacyBackupPrefix = "proxmox-backup-"

// scopeRetentionToHost splits a listing into the entries this host owns and the
// ones it must not touch. Every backend runs its retention through this so the
// three storage locations behave identically on a directory - or a remote prefix -
// that several ProxSave hosts share.
func scopeRetentionToHost(backups []*types.BackupMetadata, id retentionIdentity) (owned, foreign []*types.BackupMetadata) {
	for _, b := range backups {
		if b == nil {
			continue
		}
		if backupBelongsToHost(b, id) {
			owned = append(owned, b)
			continue
		}
		foreign = append(foreign, b)
	}
	return owned, foreign
}

// retentionAdopted counts the owned entries that are owned ONLY because they carry
// this host's own server identity: without it they would have been reported as
// spelling mismatches and left to grow. It is recomputed over the owned set rather
// than tallied inside the split so the split stays the one plain statement of the
// rule, and the cost is one predicate per owned archive.
func retentionAdopted(owned []*types.BackupMetadata, id retentionIdentity) int {
	count := 0
	for _, b := range owned {
		if archiveAdoptedByServerID(b, id) {
			count++
		}
	}
	return count
}

// retentionIdentityDivergences counts the owned entries this host claims BY NAME
// whose recorded identity is a valid one and is not this host's. They are still
// owned and still pruned: the name is what decides, and it always was. The count
// exists because the two causes are worth telling apart on the operator's side, and
// because silently pruning an archive that says it came from somewhere else is a
// fact worth stating once per pass.
func retentionIdentityDivergences(owned []*types.BackupMetadata, id retentionIdentity) int {
	local := types.NormalizeServerID(id.serverID)
	if local == "" {
		return 0
	}
	count := 0
	for _, b := range owned {
		if b == nil {
			continue
		}
		if archived := archiveServerID(b); archived != "" && archived != local {
			count++
		}
	}
	return count
}

// retentionRefusal names WHICH adoption clause refused an archive. It exists so that
// the per-entry Debug line and every summary line are read off ONE answer per
// archive instead of each re-asserting a cause of its own.
//
// That is not tidiness, it is the defect. The twin-keyed summary clause used to state
// a single reason, clause e, over a population counted with no reference to any clause
// at all, so on a renamed host (kernel name "pve", "hostname -f" answering "nas") it
// told the operator that this machine "still answers to another spelling of that short
// name" about archives clause d had refused for the opposite reason: this host answers
// to no other spelling of "nas" at all, the label simply is not the one it reports
// under. A cause the code can only state by naming a value it computed cannot drift
// from the cause that fired.
type retentionRefusal int

const (
	// refusalNone means every clause passed. It is unreachable for an OUT OF SCOPE
	// entry: clause f is the last one, and an archive that passes it is owned by the
	// hostname rule, so it never reaches the foreign set to be reported on.
	refusalNone retentionRefusal = iota
	// refusalNoEntry is the nil guard. scopeRetentionToHost drops nil entries, so it
	// cannot reach the summary lines either; it exists so the chain is total.
	refusalNoEntry
	// refusalNoLocalIdentity is a property of the HOST, not of the archive: this
	// machine does not know its own identity, so the adoption arm is off for
	// everything in the listing at once.
	refusalNoLocalIdentity
	refusalNoManifestHost    // clause a: the name came from the filename, or nowhere
	refusalNoArchiveIdentity // clause b: the archive records no readable identity
	refusalOtherIdentity     // clause b: the archive records somebody else's identity
	refusalUnqualifiedName   // clause c: the archive names a bare label
	refusalOtherShortLabel   // clause d: not the first label this host reports under
	refusalCompetingSpelling // clause e: this host answers to another spelling of it
)

// retentionAdoptionRefusal walks the adoption clauses in the order
// archiveAdoptedByServerID walks them and returns the FIRST one that refused, or
// refusalNone when none did.
//
// It is the reporting side's single source of cause, and it mirrors the predicate
// rather than sharing a body with it on purpose: archiveAdoptedByServerID must stay a
// plain readable statement of the rule that a reviewer can check clause by clause,
// and a bool is what every ownership caller needs. The mirror is held by
// TestRefusalReasonAgreesWithTheAdoptionPredicate, which walks the same space the
// containment property walks and fails the moment the two orders come apart.
//
// The local-identity check comes before clause a because it is the only whole-host
// refusal in the set: an operator reading "no manifest named the host" about every
// entry in a location would go looking at the archives, when the fact to fix is that
// this machine cannot read its own identity file.
func retentionAdoptionRefusal(meta *types.BackupMetadata, id retentionIdentity) retentionRefusal {
	if meta == nil {
		return refusalNoEntry
	}
	local := types.NormalizeServerID(id.serverID)
	if local == "" {
		return refusalNoLocalIdentity
	}
	// Normalised, not merely trimmed, so this agrees with clause a on the degenerate
	// names NormalizeHostname collapses. A manifest hostname of "." is refused by
	// clause a and used to be reported as "an unqualified host", which named a clause
	// that never ran.
	archiveHost := types.NormalizeHostname(strings.TrimSpace(meta.Hostname))
	if archiveHost == "" {
		return refusalNoManifestHost
	}
	archived := archiveServerID(meta)
	if archived == "" {
		return refusalNoArchiveIdentity
	}
	if archived != local {
		return refusalOtherIdentity
	}
	if dot := strings.IndexByte(archiveHost, '.'); dot <= 0 || dot == len(archiveHost)-1 {
		return refusalUnqualifiedName
	}
	if !archiveSharesLocalShortLabel(meta, id) {
		return refusalOtherShortLabel
	}
	if !hostAnswersOnlyToBareLabel(hostShortLabel(archiveHost), id) {
		return refusalCompetingSpelling
	}
	return refusalNone
}

// archiveCarriesLocalServerID reports whether an archive records THIS host's own
// server identity. It is the twin-keyed test on its own, kept separate from the
// refusal chain because clause a runs BEFORE the identities are compared: an archive
// whose manifest names no host is refused at clause a whatever it carries, and it is
// still an archive holding this machine's identity that the operator has to be told
// about.
func archiveCarriesLocalServerID(meta *types.BackupMetadata, id retentionIdentity) bool {
	local := types.NormalizeServerID(id.serverID)
	if local == "" {
		return false
	}
	return archiveServerID(meta) == local
}

// retentionTwinKeyedByRefusal groups the OUT OF SCOPE entries that carry this host's
// own server identity by the clause that actually refused each one.
//
// The grouping IS the fix. One count over the whole foreign set cannot be reported
// truthfully, because those entries were refused for different reasons and sit in
// different reported populations: a clause e refusal is always a spelling mismatch
// (clause d passed, and clause d IS the predicate the mismatch line counts with), a
// clause d refusal never is, and a clause a refusal may be either or may be
// unattributable. Counting them together and appending the total to the spelling
// mismatch warning produced a number larger than the population it claimed to be part
// of, under a cause that had not fired.
//
// A group is a slice rather than a count so the line reporting it can also name the
// spelling those archives were written under, which is the one thing an operator
// needs in order to recognise them.
func retentionTwinKeyedByRefusal(foreign []*types.BackupMetadata, id retentionIdentity) map[retentionRefusal][]*types.BackupMetadata {
	if types.NormalizeServerID(id.serverID) == "" {
		return nil
	}
	grouped := make(map[retentionRefusal][]*types.BackupMetadata)
	for _, b := range foreign {
		if b == nil || !archiveCarriesLocalServerID(b, id) {
			continue
		}
		reason := retentionAdoptionRefusal(b, id)
		grouped[reason] = append(grouped[reason], b)
	}
	return grouped
}

// retentionScopeLogger is the subset of the logger the scope reporting needs.
type retentionScopeLogger interface {
	Warning(format string, args ...interface{})
	Info(format string, args ...interface{})
	Debug(format string, args ...interface{})
}

// applyRetentionHostScope narrows a listing to this host's own backups and reports
// what it declined to consider. The out-of-scope set is partitioned in two, and the
// severity splits with it, because the run's exit code is derived from its WARNING
// lines (ParseLogCounts feeds applyIssueExitCode).
//
// CONTENDED entries carry an owner this host does not answer to: another machine
// writes here. That changes what retention prunes, it can change from run to run,
// and it reaches the run status at WARNING exactly as it always has. The
// spelling-mismatch line refines that same population: an archive written under a
// spelling of this host's own name that this run cannot confirm ("hostname -f"
// resolved then and does not now) is left alone rather than claimed on a guess.
//
// UNATTRIBUTABLE entries name no writer at all. They are reported on their own line
// at INFO and are deliberately kept out of the warning count: no host will ever
// claim them, so the report is a standing backlog notice rather than something that
// went wrong on this run, and counting it would hold every affected run at exit 1
// permanently. The line still names the case, the count and the one action, so the
// operator is told rather than left with silence.
//
// The second return is how many out-of-scope entries are PRESENT BUT MANAGED BY
// NOBODY: the unattributable ones plus the spelling mismatches. It exists because a
// count of owned archives alone is a false all-clear on the two populations that
// grow without bound. An upgraded host whose directory holds twenty pre-Go
// "proxmox-backup-*" archives beside two new ones owns two and stores twenty-two,
// and no host will ever prune the twenty; a host that stopped resolving its own FQDN
// owns none of its own work and stores all of it. Both are the growth failure
// docs/TROUBLESHOOTING.md documents, and reporting only the owned count hides
// exactly the case the operator needs to see (discussion #292).
//
// Genuinely foreign entries, those carrying another machine's name, are NOT counted:
// they are that machine's to prune and its to report. That is the whole point of
// scoping, and the split between the two is the short-label test
// retentionSpellingMismatches already performs for its own warning line.
//
// FIVE INFO lines report what the server identity did, and every one of them is INFO
// on purpose. Two are about archives this host KEPT: the ones adopted back into
// rotation, which is a recovery and not a problem, and the ones this host owns by name
// whose recorded identity is somebody else's, which is information about the location
// rather than a fault of this run. Three are about archives carrying this host's own
// identity that retention REFUSED, one line per refusing clause.
//
// None of the five may be a WARNING. Every WARNING line is counted by ParseLogCounts
// and pins the run at exit 1 through applyIssueExitCode, permanently for a condition
// no future run clears, which is the symptom discussion #292 reported rather than a
// report of it. The refused archives are not thereby unwarned: an attributable one is
// already inside the contended WARNING, so these lines add the EXPLANATION to a
// warning the operator already has.
//
// The twin-keyed set is reported per refusing clause rather than as one number,
// because its members were refused for different reasons and sit in different reported
// populations. Only the clause e group stays as an appended clause on the EXISTING
// mismatch warning, and only because it is the one group provably inside the
// population that warning names: clause e is reached only after clause d passed, and
// clause d is the predicate the mismatch count is computed with.
func applyRetentionHostScope(location string, id retentionIdentity, backups []*types.BackupMetadata, logger retentionScopeLogger) ([]*types.BackupMetadata, int) {
	if strings.TrimSpace(id.hostname) == "" {
		if logger != nil {
			logger.Warning("%s: the local hostname is unknown; retention will not delete anything this run", location)
		}
		return nil, 0
	}

	hostname := id.hostname
	owned, foreign := scopeRetentionToHost(backups, id)
	unattributable := retentionUnattributable(foreign)
	mismatched := retentionSpellingMismatches(foreign, id)
	// Adoption only ever MOVES an entry out of foreign and into owned, so both counts
	// above, and the second return built from them, keep the arithmetic they always
	// had. The number they produce shrinks by exactly what was adopted, which is the
	// correct report: those archives are managed again.
	adopted := retentionAdopted(owned, id)
	divergent := retentionIdentityDivergences(owned, id)
	if adopted > 0 && logger != nil {
		logger.Info("%s: retention brought %d backup(s) back into rotation. They name %s, which this host no longer resolves, but they carry this host's own server identity and this host answers to %q and to no other spelling of it, so they are this machine's own work under a name it lost. Adoption reaches nothing outside that one short name: every other archive is decided by its name alone, whatever identity it carries.", location, adopted, retentionAdoptedSpelling(owned, id), id.shortLabel())
	}
	if divergent > 0 && logger != nil {
		logger.Info("%s: %d backup(s) this host owns by name record a different server identity. Either a second machine has written under this host's name, or this host's own identity file was regenerated or restored from a different installation. Retention still treats them as this host's own, because the name is what decides ownership and always has, so nothing stops rotating over this.", location, divergent)
	}
	if len(foreign) > 0 && logger != nil {
		// Every out-of-scope entry carrying this host's own identity, grouped by the
		// clause that refused it. The groups are disjoint by construction, because
		// retentionAdoptionRefusal returns exactly one answer per archive, and each
		// line below reports one group under the cause that group actually hit.
		twinKeyed := retentionTwinKeyedByRefusal(foreign, id)
		if contended := len(foreign) - unattributable; contended > 0 {
			logger.Warning("%s: retention ignored %d backup(s) that do not belong to %s", location, contended, hostname)
		}
		// Left outside the contended branch on purpose, and it is still safe to say
		// "N of those": an entry with no owner has no short label either
		// (hostShortLabel("") is "", while the local label is guaranteed non-empty by
		// the empty-label guard in archiveSharesLocalShortLabel), so this can only ever
		// count contended entries, and a non-zero count therefore means the line
		// above it was printed.
		if n := mismatched; n > 0 {
			// One clause is appended rather than a second line emitted, and it stays
			// on this WARNING rather than becoming one of its own. A new WARNING line
			// is a new way to hold a run at exit 1 permanently through ParseLogCounts
			// and applyIssueExitCode, and this population is one no future run will
			// ever prune, so it must not be one.
			//
			// It counts the clause e group ALONE, and that is what makes "N of them"
			// true. Clause e is only ever reached after clause d passed, clause d IS
			// archiveSharesLocalShortLabel, and mismatched counts the foreign entries
			// satisfying exactly that predicate, so this group is a subset of the
			// population the sentence in front of it names and the number can never
			// exceed n. Every other twin-keyed group was refused for a different
			// reason, sits outside this population and gets its own line below. The
			// version that counted the whole foreign set here printed "4 of them"
			// under "1 of those", and blamed clause d and clause a refusals on a
			// clause e this host had never reached.
			competing := ""
			if group := twinKeyed[refusalCompetingSpelling]; len(group) > 0 {
				competing = fmt.Sprintf(" %d of them also carry this host's own server identity, which on its own is not enough: this host still answers to another spelling of that short name, so from here they are indistinguishable from a second machine, or a clone of this one, that inherited the same identity. Retention leaves them alone for that reason.", len(group))
			}
			logger.Warning("%s: %d of those carry this host's short name under a different spelling. If they are this machine's own work, this host no longer resolves the name they were written under (usually what \"hostname -f\" returns, which is what the writer stamps) and they have stopped rotating; if they belong to a second machine with the same short name, this is expected and nothing needs doing. Retention leaves them alone either way rather than guess.%s", location, n, competing)
		}
		// The remaining twin-keyed groups, reported at INFO, one line per cause.
		//
		// INFO because the alternative moves an exit code. Every WARNING is counted by
		// ParseLogCounts and promotes an otherwise clean run to exit 1 through
		// applyIssueExitCode, permanently, since no future run prunes any of these
		// archives, and that permanent red is the symptom discussion #292 reported.
		// Nothing goes unwarned either way: an attributable entry is already inside the
		// contended WARNING above, so these lines add the EXPLANATION to a warning the
		// operator already has, exactly as the clause on the mismatch line does.
		//
		// Separate lines because each names one cause for one population and the next
		// move differs by cause. Folding them together is what produced a count larger
		// than the population it claimed to be part of, under a cause that had not
		// fired.
		//
		// The clause c group is split before it is reported. retentionAdoptionRefusal
		// tests clause c before clause d, mirroring the predicate, so a BARE name that
		// is also a foreign label lands under clause c while the fact the operator
		// needs is clause d's: it is not this host's label at all. A domain-less LAN is
		// the ordinary way to reach that, so the split is not an edge case.
		otherLabel := append([]*types.BackupMetadata(nil), twinKeyed[refusalOtherShortLabel]...)
		var bareOwnLabel []*types.BackupMetadata
		for _, b := range twinKeyed[refusalUnqualifiedName] {
			if archiveSharesLocalShortLabel(b, id) {
				bareOwnLabel = append(bareOwnLabel, b)
				continue
			}
			otherLabel = append(otherLabel, b)
		}
		if len(otherLabel) > 0 {
			logger.Info("%s: %d backup(s) retention left alone carry this host's own server identity but are named %s, and the first label this host reports under is %q. Retention deletes on the NAME, and an identity may only ever confirm a name this host already shares, so an archive labelled for a different host is left alone whatever it carries: that is what stops this machine pruning a clone's, a restored template's or a renamed neighbour's archives. If this machine wrote any of them under a name it has since stopped reporting, giving that name back is what returns them to rotation; otherwise move them aside by hand.", location, len(otherLabel), retentionSpellingList(otherLabel), id.shortLabel())
		}
		if len(bareOwnLabel) > 0 {
			logger.Info("%s: %d backup(s) retention left alone carry this host's own server identity and share its short name, but are named %s, a bare name with no domain that this host does not answer to. The only case an identity repairs is a QUALIFIED name this host has stopped resolving, and a bare name has no lost domain to repair, so nothing here separates this machine's own work from a second machine handed this identity by a clone, a restore or a disk image. Move them aside by hand if they are yours; retention will not touch them.", location, len(bareOwnLabel), retentionSpellingList(bareOwnLabel))
		}
		if group := twinKeyed[refusalNoManifestHost]; len(group) > 0 {
			// Correction on the wording: this group is keyed on the MANIFEST naming no
			// host, which is not the same as nothing naming a writer. backupOwnerHost
			// falls back to the filename token, so a member here can be attributed, be
			// inside the contended warning, and even be counted as a spelling mismatch.
			// Saying "no writer named anywhere" would contradict the line above it.
			logger.Info("%s: %d backup(s) retention left alone carry this host's own server identity, but the manifest beside them names no host. An identity is not a name and may never act alone, so it has nothing to confirm and cannot be spent. The token in the file name may still attribute them for the name rule, but a name anyone with write access to this location can change is not evidence an identity is allowed to lean on. Check whether anything else writes here, then delete them by hand once you no longer need them.", location, len(group))
		}
		if unattributable > 0 {
			logger.Info("%s: retention left %d backup(s) alone because nothing names the host that wrote them, usually pre-Go \"proxmox-backup-*\" archives with no readable manifest beside them: that name carries no host token, so no host can claim them and no host will ever delete them. Delete them by hand once you no longer need them, and check first whether this location is shared, because an archive nobody can name may still be another machine's. Run with --log-level debug to see which files they are.", location, unattributable)
		}
		logger.Debug("%s: retention answers to %s (server identity %s)", location, strings.Join(append([]string{hostname}, id.aliases...), ", "), retentionServerIDLabel(id.serverID))
		for _, b := range foreign {
			logger.Debug("%s: retention out of scope: %s (owner=%q, manifest hostname=%q, server identity %s, %s)", location, b.BackupFile, backupOwnerHost(b), b.Hostname, retentionServerIDLabel(archiveServerID(b)), adoptionRefusal(b, id))
		}
	}
	return owned, unattributable + mismatched
}

// logRetentionServerIdentity records, once per backend construction, whether this
// host knows its own server identity. It is the only observable sign that the
// identity reached retention at all: cfg.ServerID is assigned by
// initializeServerIdentity long before the constructors run, so a broken call order
// in package main would leave every backend on the hostname rule alone with no other
// symptom, and the archives would keep being written correctly the whole time.
func logRetentionServerIdentity(logger *logging.Logger, location, serverID string) {
	if logger == nil {
		return
	}
	logger.Debug("%s: retention server identity %s", location, retentionServerIDLabel(serverID))
}

// retentionServerIDLabel renders a server identity for a log line, naming its
// absence rather than printing an empty pair of quotes: "unknown" is what an
// operator has to be able to see, since an absent identity is what disables the
// adoption arm entirely.
func retentionServerIDLabel(serverID string) string {
	if serverID = types.NormalizeServerID(serverID); serverID != "" {
		return serverID
	}
	return "unknown"
}

// retentionAdoptedSpelling returns the name the adopted archives were written under,
// for the INFO line. The adopted set shares one short label by construction (clause
// d), but not necessarily one full spelling, so a second spelling is reported as
// "and others" rather than silently dropped.
func retentionAdoptedSpelling(owned []*types.BackupMetadata, id retentionIdentity) string {
	adopted := make([]*types.BackupMetadata, 0, len(owned))
	for _, b := range owned {
		if archiveAdoptedByServerID(b, id) {
			adopted = append(adopted, b)
		}
	}
	return retentionSpellingList(adopted)
}

// retentionSpellingList renders the manifest hostname(s) a group of archives was
// written under, ready to drop straight into a log line.
//
// It returns an ALREADY QUOTED string and its format verb is therefore %s, not %q,
// and that is the point rather than a detail. A group is keyed on the clause that
// refused it, not on a name, so it can hold archives from several unrelated machines
// that happen to share this host's identity: two restored clones on one NAS is the
// ordinary way to get there. Rendering that as one string inside one pair of quotes
// printed "backup01.lan and others" where the sentence promised a hostname, and an
// operator who greps for that finds nothing.
//
// Two names are named. Beyond that the count carries the rest, because the line is a
// summary and the per-file Debug lines are where the full list lives.
func retentionSpellingList(entries []*types.BackupMetadata) string {
	var names []string
	seen := make(map[string]struct{}, len(entries))
	for _, b := range entries {
		if b == nil {
			continue
		}
		name := types.NormalizeHostname(strings.TrimSpace(b.Hostname))
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	switch len(names) {
	case 0:
		return "(no manifest hostname)"
	case 1:
		return strconv.Quote(names[0])
	case 2:
		return strconv.Quote(names[0]) + " and " + strconv.Quote(names[1])
	default:
		return fmt.Sprintf("%s, %s and %d others", strconv.Quote(names[0]), strconv.Quote(names[1]), len(names)-2)
	}
}

// adoptionRefusal names, for the per-entry Debug line, which adoption clause refused
// an out-of-scope archive. It exists so an operator reading a debug log can tell
// "this archive carries no identity" from "this host holds a competing spelling of
// its own name", which are the same silence otherwise.
func adoptionRefusal(meta *types.BackupMetadata, id retentionIdentity) string {
	switch retentionAdoptionRefusal(meta, id) {
	case refusalNoEntry:
		return "no entry"
	case refusalNoLocalIdentity:
		return "this host does not know its own server identity, so no archive can be adopted"
	case refusalNoManifestHost:
		return "no manifest named the host, so the server identity may not act"
	case refusalNoArchiveIdentity:
		return "the archive records no readable server identity"
	case refusalOtherIdentity:
		return "the archive records another machine's server identity"
	case refusalUnqualifiedName:
		return "the archive names an unqualified host, which is not the lost-FQDN case"
	case refusalOtherShortLabel:
		// Names the key rather than denying a name. On the rename-artefact host the
		// Debug line above this one lists "nas" among the names retention answers to,
		// so saying "this host does not answer to that short name" of a "nas.lan"
		// archive would contradict it on the very host this text exists to explain.
		return fmt.Sprintf("the archive's short name is not the first label this host reports under (%s)", id.shortLabel())
	case refusalCompetingSpelling:
		return "this host answers to another spelling of that short name, so this could be a second machine"
	default:
		return "no clause refused it"
	}
}

// manifestOwnerFromLocalArchive returns both facts the manifest beside (or inside) a
// local-filesystem archive records about its writer: the host it names and the
// server identity it carries. It mirrors what LocalStorage.loadMetadata already
// does, so the secondary location - which lists with stat() only and never opened a
// manifest - can attribute its backups the same way without duplicating the
// bundle/sidecar handling.
//
// Both come from ONE read of ONE payload. That is not only about cost: a hostname
// taken from one file and an identity taken from another would describe a writer
// that never existed, and retention would be deciding a deletion on it.
//
// Best-effort by design: an empty result means "cannot attribute", which
// backupBelongsToHost then treats as not-ours. Bounded through safefs so a dead or
// stale secondary mount cannot wedge the retention pass.
func manifestOwnerFromLocalArchive(ctx context.Context, archivePath string, timeout time.Duration) (hostname, serverID string) {
	if strings.HasSuffix(archivePath, bundleSuffix) {
		manifest, err := safefs.Run(ctx, "bundle-manifest-host", archivePath, timeout, func() (*backup.Manifest, error) {
			return manifestFromBundle(archivePath)
		})
		if err != nil || manifest == nil {
			return "", ""
		}
		return strings.TrimSpace(manifest.Hostname), strings.TrimSpace(manifest.ServerID)
	}

	metadataFile := archivePath + ".metadata"
	if _, err := safefs.Stat(ctx, metadataFile, timeout); err != nil {
		return "", ""
	}
	manifest, err := safefs.Run(ctx, "manifest-host", metadataFile, timeout, func() (*backup.Manifest, error) {
		return backup.LoadManifest(metadataFile)
	})
	if err != nil || manifest == nil {
		return "", ""
	}
	return strings.TrimSpace(manifest.Hostname), strings.TrimSpace(manifest.ServerID)
}

// manifestFromBundle extracts the manifest entry from a bundle tar.
//
// The bundle path is built from a directory listing, so it reaches os as a variable.
// It is opened through safefs.OpenFileUnderRoot, which confines the open to the
// bundle's own parent directory at the syscall level: gosec G304 is answered by the
// structure rather than by a suppression, and a final component that is an absolute
// symlink - or one escaping that directory - is refused instead of followed. Reading
// the parent directory is already a precondition here, since that is where the
// listing came from.
func manifestFromBundle(bundlePath string) (*backup.Manifest, error) {
	file, err := safefs.OpenFileUnderRoot(bundlePath, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	tr := tar.NewReader(file)
	expectedName := strings.TrimSuffix(filepath.Base(bundlePath), bundleSuffix) + ".metadata"
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("metadata %s not found in bundle %s", expectedName, filepath.Base(bundlePath))
		}
		if err != nil {
			return nil, fmt.Errorf("read bundle %s: %w", filepath.Base(bundlePath), err)
		}
		if filepath.Base(hdr.Name) != expectedName {
			continue
		}
		var manifest backup.Manifest
		if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
			return nil, fmt.Errorf("parse manifest from bundle %s: %w", filepath.Base(bundlePath), err)
		}
		return &manifest, nil
	}
}
