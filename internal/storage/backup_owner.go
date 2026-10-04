package storage

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// the archives it produced, this host's own server identity, and which archive at
// the location this run produced.
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
	// thisRunArchive is the base name of the archive this run handed to Store at this
	// location, "" when no Store ran in this process. Ownership never reads it. It is
	// the one archive retentionSharedIdentity must not take for this host's PREVIOUS
	// backup: this run's archive is always newer than whatever another writer left.
	thisRunArchive string
}

// shortLabel is the one first label this host reports under: the first label of the
// name the kernel gives, normalised. Aliases are deliberately NOT folded in. It is the
// key the not-rotated fact is reported under: an archive whose first label is this
// one, under a spelling this host does not answer to and without this host's server
// identity, is named as not rotated. Widening the key would put another machine's
// archives into that report.
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
// its own. An archive nobody can name is left alone by every host, and
// applyRetentionHostScope writes it at DEBUG.
//
// Two mechanisms run side by side here, and the second never replaces the first.
// Everything above is the hostname rule, and it decides every archive that carries no
// server identity: every archive written before that field existed, and every pre-Go
// "proxmox-backup-*" name. The identity is an ADDITIONAL signal, available only on
// archives written from that version on, and archiveAdoptedByServerID states the whole
// of what it may do: the same server identity is the same server, whatever name the
// archive carries.
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
	// The adoption arm is the other half: it can only ever add, and only archives
	// carrying this host's own server identity.
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

// archiveAdoptedByServerID reports whether an archive this host does not answer to BY
// NAME is nonetheless this host's because it records this host's own server identity.
// The rule is that the same server identity is the same server, whatever name the
// archive carries: another short label, a bare name, a competing spelling of this
// host's own name, or a name that came only from the filename token. It follows, and
// was stated and accepted when the rule was decided, that a clone carrying the same
// identity and writing to the same location has its backups rotated by this host too.
//
// The identity may never act alone and it may never REMOVE a claim: an archive nothing
// names stays nobody's, and this function is only ever OR-ed after the hostname arm.
// retentionAdoptionRefusal is the rule clause by clause; this is its yes/no answer.
func archiveAdoptedByServerID(meta *types.BackupMetadata, id retentionIdentity) bool {
	return retentionAdoptionRefusal(meta, id) == refusalNone
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
// whose first label is this host's own first label, under a spelling this host does
// not answer to. It is the membership test of the population reported as not rotated.
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

// retentionUnattributable counts the out-of-scope entries that name no writer at
// all, usually pre-Go "proxmox-backup-*" archives with no readable manifest beside
// them. It is keyed on ATTRIBUTABILITY rather than on the legacy prefix, because
// that is the real seam: the prefix is the common cause of the property, not the
// property itself, and a "*-backup-*" name whose timestamp does not parse deserves
// the same treatment for the same reason.
//
// Neither kind of out-of-scope entry gets a visible line. An archive attributed to
// another machine is that machine's to prune and its to report. An archive nobody can
// name is a fixed backlog fact that no future run will change, and since no host will
// ever prune it, counting it as a run issue would promote every affected run to exit 1
// for ever through applyIssueExitCode (internal/orchestrator/extensions.go), which is
// the symptom discussion #292 reported rather than a report of it. It is counted for
// the DEBUG line that names the case.
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

// retentionAdopted returns the owned entries that are owned ONLY because they carry
// this host's own server identity: without it they would have been left alone and
// left to grow. It is recomputed over the owned set rather than tallied inside the
// split so the split stays the one plain statement of the rule, and the cost is one
// predicate per owned archive.
func retentionAdopted(owned []*types.BackupMetadata, id retentionIdentity) []*types.BackupMetadata {
	var adopted []*types.BackupMetadata
	for _, b := range owned {
		if archiveAdoptedByServerID(b, id) {
			adopted = append(adopted, b)
		}
	}
	return adopted
}

// retentionIdentityDivergences returns the owned entries this host claims BY NAME
// whose recorded identity is a valid one and is not this host's. They are still
// owned and still pruned: the name is what decides, and it always was. They are
// reported because silently pruning an archive that says it came from somewhere else
// is a fact worth stating once per pass.
func retentionIdentityDivergences(owned []*types.BackupMetadata, id retentionIdentity) []*types.BackupMetadata {
	local := types.NormalizeServerID(id.serverID)
	if local == "" {
		return nil
	}
	var divergent []*types.BackupMetadata
	for _, b := range owned {
		if b == nil {
			continue
		}
		if archived := archiveServerID(b); archived != "" && archived != local {
			divergent = append(divergent, b)
		}
	}
	return divergent
}

// retentionRefusal names WHICH adoption clause refused an archive. The per-entry DEBUG
// line reads it, so a debug log says why an out-of-scope archive was not adopted.
type retentionRefusal int

const (
	// refusalNone means every clause passed: the archive is adopted.
	refusalNone retentionRefusal = iota
	// refusalNoEntry is the nil guard. scopeRetentionToHost drops nil entries, so it
	// cannot reach the DEBUG line; it exists so the chain is total.
	refusalNoEntry
	// refusalNoLocalHostname is a property of the HOST: a machine that cannot name
	// itself attributes nothing, so it adopts nothing either.
	refusalNoLocalHostname
	// refusalNoLocalIdentity is a property of the HOST, not of the archive: this
	// machine does not know its own identity, so the adoption arm is off for
	// everything in the listing at once.
	refusalNoLocalIdentity
	refusalNoHostName        // nothing names the host that wrote the archive
	refusalNoArchiveIdentity // the archive records no readable identity
	refusalOtherIdentity     // the archive records somebody else's identity
	refusalOwnedByName       // the archive is this host's by name: nothing to adopt
)

// retentionAdoptionRefusal is the adoption rule, clause by clause, and returns the
// FIRST clause that refused, or refusalNone when the archive is adopted.
//
// The local checks come first because they refuse every archive in the listing at
// once: an operator reading "the archive records no identity" about every entry would
// go looking at the archives, when the fact to fix is this machine's own.
func retentionAdoptionRefusal(meta *types.BackupMetadata, id retentionIdentity) retentionRefusal {
	if meta == nil {
		return refusalNoEntry
	}
	if strings.TrimSpace(id.hostname) == "" {
		return refusalNoLocalHostname
	}
	local := types.NormalizeServerID(id.serverID)
	if local == "" {
		return refusalNoLocalIdentity
	}
	// Something has to name the writer, the manifest or the filename token. An
	// identity is not a name, so an archive nothing names stays nobody's whatever it
	// carries, exactly as one with no identity at all. Normalised, so a degenerate
	// manifest name such as "." names nobody here either, and the fact line, which
	// prints the normalised name, never has an empty one to print.
	owner := types.NormalizeHostname(backupOwnerHost(meta))
	if owner == "" {
		return refusalNoHostName
	}
	// Two VALIDATED identities, equal as bytes. Absent on either side means "cannot
	// compare", which is every archive written before the field existed.
	archived := archiveServerID(meta)
	if archived == "" {
		return refusalNoArchiveIdentity
	}
	if archived != local {
		return refusalOtherIdentity
	}
	// An archive this host answers to by name is owned by the hostname arm already;
	// it is not counted as adopted.
	if hostOwnsName(owner, id.hostname, id.aliases...) {
		return refusalOwnedByName
	}
	return refusalNone
}

// retentionScopeLogger is the subset of the logger the scope reporting needs. It has
// no Warning: the scope prints facts under "Applying retention policy..." and the
// outcome lines that follow them are the caller's.
type retentionScopeLogger interface {
	Info(format string, args ...interface{})
	Debug(format string, args ...interface{})
}

// discardScopeLogger is the logger a caller passing nil gets.
type discardScopeLogger struct{}

func (discardScopeLogger) Info(string, ...interface{})  {}
func (discardScopeLogger) Debug(string, ...interface{}) {}

// errRetentionHostnameNotResolved is what a retention pass returns when this machine
// cannot name itself: no backup can be attributed to it, so the pass does not start
// and nothing is deleted. The caller closes the block with "Retention not applied".
var errRetentionHostnameNotResolved = errors.New("the local hostname is not resolved")

// retentionScope is what applyRetentionHostScope hands back to a retention pass.
type retentionScope struct {
	// owned are the backups this host rotates: owned by name or adopted by server
	// identity. Their count is RetentionSummary.Owned.
	owned []*types.BackupMetadata
	// notRotated are the backups named under this host's short name in a spelling it
	// does not answer to, one entry per name, in the order the names were first met.
	// Retention leaves them alone and the pass outcome says so.
	notRotated []retentionNameCount
	// sharedWith are the names under which a second machine carrying this host's
	// server identity is still writing here (retentionSharedIdentity), in the order
	// the names were first met. Those archives are adopted and rotate as this host's
	// own; the pass outcome says the identity is shared.
	sharedWith []string
}

// retentionNameCount is how many backups of one group carry one name.
type retentionNameCount struct {
	name  string
	count int
}

// countByName groups backups by the name they are attributed to (backupOwnerHost,
// normalised), in the order the names are first met. A backup no name attributes is
// skipped: every caller passes a group whose members all carry one.
func countByName(backups []*types.BackupMetadata) []retentionNameCount {
	var out []retentionNameCount
	for _, b := range backups {
		if b == nil {
			continue
		}
		name := types.NormalizeHostname(backupOwnerHost(b))
		if name == "" {
			continue
		}
		found := false
		for i := range out {
			if out[i].name == name {
				out[i].count++
				found = true
				break
			}
		}
		if !found {
			out = append(out, retentionNameCount{name: name, count: 1})
		}
	}
	return out
}

// applyRetentionHostScope narrows a listing to this host's own backups and prints, as
// facts under "Applying retention policy...", what changes what retention sees. Every
// fact is preceded by DEBUG lines naming the archives it is about.
//
// A host that cannot name itself attributes nothing: the scope prints
// "  Hostname: not resolved" and returns errRetentionHostnameNotResolved, and the
// caller returns it so the block closes with "Retention not applied".
//
// The facts, in this order:
//
//   - "  Adopted: <N> backups named <name>, same server identity", one per name: archives
//     this host owns only because they carry its own server identity.
//   - "  Still writing here: <names>": the adopted names under which an archive is newer
//     than this host's previous own backup here, so a second machine with the same
//     server identity is still writing (retentionSharedIdentity). They are returned in
//     sharedWith and the caller's outcome line names them.
//   - "  Other server identity: <N> backups owned by name, still rotated": archives this
//     host owns by name whose recorded identity is another one. The name decides, so
//     they rotate.
//   - "  Named <name>, not rotated: <N> backups", one per name: archives carrying this
//     host's short name in a spelling it does not answer to. They are returned in
//     notRotated and the caller's outcome line counts them.
//
// Backups of other hosts are not reported (they are that host's to rotate), and nor are
// backups nothing names: both are left alone and only written at DEBUG.
func applyRetentionHostScope(location string, id retentionIdentity, backups []*types.BackupMetadata, logger retentionScopeLogger) (retentionScope, error) {
	if logger == nil {
		logger = discardScopeLogger{}
	}
	if strings.TrimSpace(id.hostname) == "" {
		logger.Debug("%s: retention - the local hostname could not be read, so no backup can be attributed to this host and nothing is deleted", location)
		logger.Info("  Hostname: not resolved")
		return retentionScope{}, errRetentionHostnameNotResolved
	}

	owned, foreign := scopeRetentionToHost(backups, id)
	logger.Debug("%s: retention answers to %s (server identity %s)", location, strings.Join(append([]string{id.hostname}, id.aliases...), ", "), retentionServerIDLabel(id.serverID))

	var sharedWith []string
	if adopted := retentionAdopted(owned, id); len(adopted) > 0 {
		for _, b := range adopted {
			logger.Debug("%s: retention - adopted %s (owner=%q, server identity %s)", location, b.BackupFile, backupOwnerHost(b), retentionServerIDLabel(archiveServerID(b)))
		}
		logger.Debug("%s: retention - %d backup(s) carry this host's own server identity under a name this host does not answer to: the same server identity is the same server, so they rotate with this host's own", location, len(adopted))
		for _, g := range countByName(adopted) {
			logger.Info("  Adopted: %d backups named %s, same server identity", g.count, g.name)
		}
		if sharedWith = retentionSharedIdentity(location, owned, adopted, id, logger); len(sharedWith) > 0 {
			logger.Info("  Still writing here: %s", joinNames(sharedWith))
		}
	}

	if divergent := retentionIdentityDivergences(owned, id); len(divergent) > 0 {
		for _, b := range divergent {
			logger.Debug("%s: retention - %s is owned by name (owner=%q) and records server identity %s, this host's is %s", location, b.BackupFile, backupOwnerHost(b), retentionServerIDLabel(archiveServerID(b)), retentionServerIDLabel(id.serverID))
		}
		logger.Debug("%s: retention - either a second machine has written under this host's name, or this host's identity file was regenerated or restored from a different installation; the name decides ownership, so they still rotate", location)
		logger.Info("  Other server identity: %d backups owned by name, still rotated", len(divergent))
	}

	// Every out-of-scope entry, at DEBUG only: the backups of other hosts are theirs to
	// rotate and are never reported.
	for _, b := range foreign {
		logger.Debug("%s: retention out of scope: %s (owner=%q, manifest hostname=%q, server identity %s, %s)", location, b.BackupFile, backupOwnerHost(b), b.Hostname, retentionServerIDLabel(archiveServerID(b)), adoptionRefusal(b, id))
	}

	unattributable := retentionUnattributable(foreign)
	if unattributable > 0 {
		logger.Debug("%s: retention - %d backup(s) left alone because nothing names the host that wrote them, usually pre-Go \"proxmox-backup-*\" archives with no readable manifest beside them: no host can claim them and no host will ever delete them", location, unattributable)
	}

	var mismatched []*types.BackupMetadata
	for _, b := range foreign {
		if archiveSharesLocalShortLabel(b, id) {
			mismatched = append(mismatched, b)
		}
	}
	notRotated := countByName(mismatched)
	if len(mismatched) > 0 {
		logger.Debug("%s: retention - %d backup(s) carry this host's short name %q under a spelling this host does not answer to, and not this host's server identity. If they are this machine's own work, this host no longer resolves the name they were written under (usually what \"hostname -f\" returns, which is what the writer stamps); if they belong to a second machine with the same short name, this is expected. Retention leaves them alone either way", location, len(mismatched), id.shortLabel())
		for _, g := range notRotated {
			logger.Info("  Named %s, not rotated: %d backups", g.name, g.count)
		}
	}

	return retentionScope{owned: owned, notRotated: notRotated, sharedWith: sharedWith}, nil
}

// retentionSharedIdentity returns the adopted names under which a second machine
// carrying this host's server identity is still writing here: a clone that kept the
// identity (a copied disk, a restored container, a template). Adoption rotates those
// archives as this host's own either way, against this host's limit; this only finds
// the names so the pass can say so.
//
// A name is returned when its newest adopted archive is newer than this host's
// PREVIOUS own backup here: the most recent archive this host owns by name, other than
// the one this run created. A rename never qualifies, because every archive under the
// old name predates the first one under the new name and therefore the previous own
// backup. With no previous own backup here, a rename and a clone cannot be told apart
// yet and nothing is returned. Archives with no date are left out on both sides,
// since they cannot be ordered, and so is this run's own archive.
func retentionSharedIdentity(location string, owned, adopted []*types.BackupMetadata, id retentionIdentity, logger retentionScopeLogger) []string {
	thisRun := retentionArchiveKey(id.thisRunArchive)
	isThisRun := func(b *types.BackupMetadata) bool {
		return thisRun != "" && retentionArchiveKey(b.BackupFile) == thisRun
	}
	isAdopted := make(map[*types.BackupMetadata]bool, len(adopted))
	for _, b := range adopted {
		isAdopted[b] = true
	}

	var previous time.Time
	for _, b := range owned {
		if b == nil || isAdopted[b] || isThisRun(b) || b.Timestamp.IsZero() {
			continue
		}
		if b.Timestamp.After(previous) {
			previous = b.Timestamp
		}
	}
	if previous.IsZero() {
		logger.Debug("%s: retention shared identity: previous_own=none (this run's archive=%q); with no earlier backup of this host here, a second writer cannot be told apart from a rename, so nothing is reported", location, id.thisRunArchive)
		return nil
	}

	type newestByName struct {
		name   string
		newest time.Time
	}
	var groups []newestByName
	for _, b := range adopted {
		if b == nil || isThisRun(b) || b.Timestamp.IsZero() {
			continue
		}
		name := types.NormalizeHostname(backupOwnerHost(b))
		if name == "" {
			continue
		}
		found := false
		for i := range groups {
			if groups[i].name == name {
				if b.Timestamp.After(groups[i].newest) {
					groups[i].newest = b.Timestamp
				}
				found = true
				break
			}
		}
		if !found {
			groups = append(groups, newestByName{name: name, newest: b.Timestamp})
		}
	}

	var shared []string
	for _, g := range groups {
		logger.Debug("%s: retention shared identity: name=%s newest=%s previous_own=%s", location, g.name, g.newest.Format(time.RFC3339), previous.Format(time.RFC3339))
		if g.newest.After(previous) {
			shared = append(shared, g.name)
		}
	}
	return shared
}

// retentionArchiveKey is the name an archive is matched by against this run's own:
// its base name without the bundle suffix, because a destination may list the bundle
// of the archive Store was handed ("<archive>.bundle.tar") or the archive itself.
func retentionArchiveKey(file string) string {
	file = strings.TrimSpace(file)
	if file == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(file), bundleSuffix)
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

// adoptionRefusal names, for the per-entry Debug line, which adoption clause refused
// an out-of-scope archive.
func adoptionRefusal(meta *types.BackupMetadata, id retentionIdentity) string {
	switch retentionAdoptionRefusal(meta, id) {
	case refusalNoEntry:
		return "no entry"
	case refusalNoLocalHostname:
		return "this host cannot name itself, so no archive can be adopted"
	case refusalNoLocalIdentity:
		return "this host does not know its own server identity, so no archive can be adopted"
	case refusalNoHostName:
		return "nothing names the host that wrote it, so the server identity may not act"
	case refusalNoArchiveIdentity:
		return "the archive records no readable server identity"
	case refusalOtherIdentity:
		return "the archive records another machine's server identity"
	case refusalOwnedByName:
		return "this host owns it by name"
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
