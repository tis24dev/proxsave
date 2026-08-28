package storage

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// twinKeyedScenario is one listing read twice, once by a host that knows its own
// server identity and once by the same host without one. The second reading is the
// severity baseline: the identity is an ADDITIONAL signal, so it may add explanation
// and it may adopt, but on a listing where it adopts nothing it must not move a single
// WARNING line or a single unit of the managed-by-nobody count.
type twinKeyedScenario struct {
	name    string
	id      retentionIdentity
	base    retentionIdentity
	backups []*types.BackupMetadata
}

// twinKeyedScenarios are the shapes the twin-keyed report has to get right, each named
// for the operator's situation rather than for the clause number.
func twinKeyedScenarios() []twinKeyedScenario {
	return []twinKeyedScenario{
		{
			// One real spelling mismatch and three archives belonging to a
			// differently named host, all four carrying this host's identity. This is
			// the arithmetic case: the appended clause is read as a subset of the
			// mismatch count printed immediately before it.
			name: "one spelling mismatch beside three contended twin-keyed archives",
			id:   hostWithIdentity("pve", ourServerID, "pve.home.arpa"),
			base: hostOnly("pve", "pve.home.arpa"),
			backups: []*types.BackupMetadata{
				{BackupFile: "pve.siteb.example-backup-20250101-100000.tar.zst", Hostname: "pve.siteb.example", ServerID: ourServerID},
				{BackupFile: "backup01.lan-backup-20250102-100000.tar.zst", Hostname: "backup01.lan", ServerID: ourServerID},
				{BackupFile: "backup01.lan-backup-20250103-100000.tar.zst", Hostname: "backup01.lan", ServerID: ourServerID},
				{BackupFile: "backup01.lan-backup-20250104-100000.tar.zst", Hostname: "backup01.lan", ServerID: ourServerID},
			},
		},
		{
			// The rename artefact: /etc/hostname says "pve", /etc/hosts says
			// "127.0.1.1 nas pve", so "hostname -f" answers "nas". This host answers
			// to no other spelling of "nas", so clause e is TRUE here and the refusal
			// is clause d. The mismatch line exists only because of a second archive
			// carrying no identity at all.
			name: "a renamed host reading archives written under the name it now reports through hostname -f",
			id:   renamedHost("pve", "nas", ourServerID),
			base: renamedHost("pve", "nas", ""),
			backups: []*types.BackupMetadata{
				{BackupFile: "pve.home.arpa-backup-20250101-100000.tar.zst", Hostname: "pve.home.arpa"},
				{BackupFile: "nas.lan-backup-20250102-100000.tar.zst", Hostname: "nas.lan", ServerID: ourServerID},
			},
		},
		{
			// The same host with nothing else in the location. Nothing makes the
			// mismatch count non-zero, so the twin-keyed fact had nowhere to be said.
			name: "a renamed host whose whole location is twin-keyed and contended",
			id:   renamedHost("pve", "nas", ourServerID),
			base: renamedHost("pve", "nas", ""),
			backups: []*types.BackupMetadata{
				{BackupFile: "nas.lan-backup-20250102-100000.tar.zst", Hostname: "nas.lan", ServerID: ourServerID},
				{BackupFile: "nas.lan-backup-20250103-100000.tar.zst", Hostname: "nas.lan", ServerID: ourServerID},
			},
		},
		{
			// An archive nobody can name that still carries an identity, beside a real
			// spelling mismatch. The unattributable entry has no owner, so it is in
			// neither the contended count nor the mismatch count, and counting it into
			// the appended clause put a number there larger than the mismatch itself.
			name: "an unattributable archive carrying this host's identity beside a spelling mismatch",
			id:   hostWithIdentity("pve", ourServerID, "pve.home.arpa"),
			base: hostOnly("pve", "pve.home.arpa"),
			backups: []*types.BackupMetadata{
				{BackupFile: "pve.siteb.example-backup-20250101-100000.tar.zst", Hostname: "pve.siteb.example", ServerID: ourServerID},
				{BackupFile: "proxmox-backup-20250102-100000.tar.gz", ServerID: ourServerID},
			},
		},
		{
			// A host answering to a QUALIFIED alias under a different label, reading
			// archives in a third domain under that alias's label. Clause d refuses
			// them, and clause e would refuse them too, so it is the one shape where
			// the order of the two clauses is observable: read under clause e they
			// would be appended to a mismatch warning that does not contain them.
			name: "a host with a qualified alias reading a third domain under the alias's label",
			id:   renamedHost("pve", "nas.lan", ourServerID),
			base: renamedHost("pve", "nas.lan", ""),
			backups: []*types.BackupMetadata{
				{BackupFile: "pve.home.arpa-backup-20250101-100000.tar.zst", Hostname: "pve.home.arpa"},
				{BackupFile: "nas.siteb.example-backup-20250102-100000.tar.zst", Hostname: "nas.siteb.example", ServerID: ourServerID},
				{BackupFile: "nas.siteb.example-backup-20250103-100000.tar.zst", Hostname: "nas.siteb.example", ServerID: ourServerID},
			},
		},
		{
			// A host whose kernel name is qualified, reading an archive written under
			// the bare label. There is no lost domain to repair, so clause c refuses,
			// and the archive is still inside the mismatch population.
			name: "a qualified host reading an archive written under the bare label",
			id:   hostWithIdentity("pve.home.arpa", ourServerID),
			base: hostOnly("pve.home.arpa"),
			backups: []*types.BackupMetadata{
				{BackupFile: "pve-backup-20250102-100000.tar.zst", Hostname: "pve", ServerID: ourServerID},
			},
		},
		{
			// TWO spelling mismatches of which only ONE is twin-keyed. It is the only
			// shape that separates the clause e group from the mismatch count, so it
			// is the only one that catches the appended clause printing the mismatch
			// total instead of its own group's size. Appended at the end on purpose:
			// other tests index twinKeyedScenarios() by position.
			name: "two spelling mismatches of which one is twin-keyed",
			id:   hostWithIdentity("pve", ourServerID, "pve.home.arpa"),
			base: hostOnly("pve", "pve.home.arpa"),
			backups: []*types.BackupMetadata{
				{BackupFile: "pve.siteb.example-backup-20250101-100000.tar.zst", Hostname: "pve.siteb.example", ServerID: ourServerID},
				{BackupFile: "pve.sitec.example-backup-20250102-100000.tar.zst", Hostname: "pve.sitec.example"},
			},
		},
		{
			// A BARE name under a FOREIGN label. Clause c is tested before clause d,
			// mirroring the predicate, so this is refused at clause c while the fact
			// the operator needs is clause d's: "nas" is not this host's label at all,
			// and no domain was lost. A domain-less LAN with a renamed clone on it is
			// the ordinary way to reach this, and without the split the operator is
			// told the archives have no lost domain to repair, which is true but is
			// not the reason, and is not the reason that tells them to leave the files
			// alone.
			name: "a bare name under a foreign label, carrying this host's identity",
			id:   hostWithIdentity("pve", ourServerID),
			base: hostOnly("pve"),
			backups: []*types.BackupMetadata{
				{BackupFile: "nas-backup-20250101-100000.tar.zst", Hostname: "nas", ServerID: ourServerID},
			},
		},
	}
}

// countedIn extracts the integer a rendered line reports before the given phrase, so a
// test can compare two numbers the operator reads on ONE line instead of asserting a
// literal string. Returns -1 when the phrase is absent.
func countedIn(message, phrase string) int {
	re := regexp.MustCompile(`(\d+) ` + regexp.QuoteMeta(phrase))
	m := re.FindStringSubmatch(message)
	if m == nil {
		return -1
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return -1
	}
	return n
}

// firstMessageContaining returns the first recorded message containing needle.
func (l *levelRecordingLogger) firstMessageContaining(needle string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line.message, needle) {
			return line.message
		}
	}
	return ""
}

// TestTwinKeyedCountNeverExceedsTheSpellingMismatchItRefines is requirement 1 stated
// as an assertion over the rendered line rather than over an internal count. The
// twin-keyed clause is APPENDED to the spelling-mismatch warning and reads "N of
// them", so it presents itself as a subset of the number printed a sentence earlier.
// A number that can exceed the population it claims to be part of is not a rounding
// problem: it tells the operator that more archives carry this host's identity than
// there are archives under this host's name, which is the opposite of what happened.
//
// It kills replacing twinKeyed[refusalCompetingSpelling] with a count over the whole
// foreign set, which is what the shipped code did: on the first scenario that renders
// "4 of them" underneath "1 of those".
func TestTwinKeyedCountNeverExceedsTheSpellingMismatchItRefines(t *testing.T) {
	for _, sc := range twinKeyedScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			logger := &levelRecordingLogger{}
			applyRetentionHostScope("Local storage", sc.id, sc.backups, logger)

			message := logger.firstMessageContaining("of those carry this host's short name")
			if message == "" {
				return
			}
			those := countedIn(message, "of those carry this host's short name")
			them := countedIn(message, "of them also carry this host's own server identity")
			if them < 0 {
				return
			}
			if those < 0 {
				t.Fatalf("the spelling-mismatch line no longer reports a count: %q", message)
			}
			if them > those {
				t.Errorf("the line says %d of those carry this host's short name and then %d of them also carry this host's identity. The second number is presented as a subset of the first and cannot exceed it; the operator is being told more archives are twin-keyed than there are archives in the population being described: %q", those, them, message)
			}
		})
	}
}

// TestTwinKeyedRefusalsAreReportedUnderTheClauseThatFired is requirement 2. The
// appended clause asserts ONE cause, clause e, "this host still answers to another
// spelling of that short name". On the rename artefact that is false: this host
// answers to exactly one spelling of "nas", and the archive is refused because "nas"
// is not the first label this host reports under. Telling an operator to go looking
// for a competing spelling that does not exist sends them after nothing.
//
// It kills widening the appended group to any refusal other than clause e, and it
// kills reusing the clause e sentence for the clause d population.
func TestTwinKeyedRefusalsAreReportedUnderTheClauseThatFired(t *testing.T) {
	sc := twinKeyedScenarios()[1]
	twin := sc.backups[1]

	if got, want := retentionAdoptionRefusal(twin, sc.id), refusalOtherShortLabel; got != want {
		t.Fatalf("the fixture archive is refused at %v, want %v; it no longer describes the rename artefact and proves nothing", got, want)
	}
	if !hostAnswersOnlyToBareLabel("nas", sc.id) {
		t.Fatal("the fixture host answers to a second spelling of \"nas\", so clause e really would be the cause here and the case under test has been lost")
	}

	logger := &levelRecordingLogger{}
	applyRetentionHostScope("Local storage", sc.id, sc.backups, logger)

	for _, message := range logger.messagesAtLevel("WARNING") {
		if strings.Contains(message, "still answers to another spelling of that short name") {
			t.Errorf("the report blames a competing spelling of the archive's short name, but this host answers to exactly one spelling of it and the refusal was that the label is not the one this host reports under. The operator is sent looking for a second spelling that does not exist: %q", message)
		}
	}
	line := logger.firstMessageContaining("the first label this host reports under is")
	if line == "" {
		t.Fatalf("nothing named the cause that actually fired. Reported lines: %q / %q", logger.messagesAtLevel("WARNING"), logger.messagesAtLevel("INFO"))
	}
	if !strings.Contains(line, "\"nas.lan\"") || !strings.Contains(line, "\"pve\"") {
		t.Errorf("the line naming the cause does not name both the spelling the archives carry (nas.lan) and the label this host reports under (pve), which is the pair an operator needs in order to recognise them: %q", line)
	}
}

// TestEveryTwinKeyedArchiveIsReportedWhateverBucketItFellInto is requirement 3. The
// clause used to live inside "if mismatched > 0", so a location where every archive
// carrying this host's own identity is contended rather than a spelling mismatch got
// the generic contended warning and nothing else: the single most useful fact about
// those files, that they hold this machine's identity, was never printed. That is the
// population the containment fix newly strands, so the silence arrived with it.
//
// It kills nesting the twin-keyed lines under the mismatch branch, and it kills
// deleting any one of the three group lines.
func TestEveryTwinKeyedArchiveIsReportedWhateverBucketItFellInto(t *testing.T) {
	for _, sc := range twinKeyedScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			logger := &levelRecordingLogger{}
			applyRetentionHostScope("Local storage", sc.id, sc.backups, logger)

			_, foreign := scopeRetentionToHost(sc.backups, sc.id)
			grouped := retentionTwinKeyedByRefusal(foreign, sc.id)

			// The RENDERED populations, which are not the raw refusal groups. Clause c
			// is tested before clause d, mirroring the predicate, so a bare name that
			// is also a foreign label is refused at clause c while the fact the
			// operator needs is clause d's. Production splits that group before it
			// reports it, and the assertions below have to walk the same three
			// populations or they would pin a partition nothing prints.
			rendered := map[retentionRefusal][]*types.BackupMetadata{
				refusalCompetingSpelling: grouped[refusalCompetingSpelling],
				refusalNoManifestHost:    grouped[refusalNoManifestHost],
			}
			otherLabel := append([]*types.BackupMetadata(nil), grouped[refusalOtherShortLabel]...)
			var bareOwnLabel []*types.BackupMetadata
			for _, b := range grouped[refusalUnqualifiedName] {
				if archiveSharesLocalShortLabel(b, sc.id) {
					bareOwnLabel = append(bareOwnLabel, b)
					continue
				}
				otherLabel = append(otherLabel, b)
			}
			rendered[refusalOtherShortLabel] = otherLabel
			rendered[refusalUnqualifiedName] = bareOwnLabel

			// Nothing may be lost in the split: every twin-keyed archive has to end up
			// in exactly one rendered population, whatever clause refused it.
			total := 0
			for _, g := range rendered {
				total += len(g)
			}
			twinKeyedTotal := 0
			for _, g := range grouped {
				twinKeyedTotal += len(g)
			}
			if total != twinKeyedTotal {
				t.Fatalf("the rendered populations hold %d archive(s) but %d carry this host's identity. The split dropped or duplicated one, so an archive is reported twice or not at all", total, twinKeyedTotal)
			}

			reported := 0
			for reason, group := range rendered {
				if len(group) == 0 {
					continue
				}
				// phrase picks the line out of the log; countPhrase is the text the
				// line's own number sits in front of, so the count is asserted rather
				// than merely the presence of a sentence.
				phrase, countPhrase := "", "backup(s) retention left alone carry this host's own server identity"
				switch reason {
				case refusalCompetingSpelling:
					phrase = "of them also carry this host's own server identity"
					countPhrase = phrase
				case refusalOtherShortLabel:
					phrase = "the first label this host reports under is"
				case refusalUnqualifiedName:
					phrase = "a bare name with no domain that this host does not answer to"
				case refusalNoManifestHost:
					phrase = "the manifest beside them names no host"
				default:
					t.Fatalf("%d out-of-scope archive(s) carrying this host's own identity were refused at %v, which no summary line reports. A clause was added to the adoption rule without a line to explain it, and those archives are now refused in silence", len(group), reason)
				}
				line := logger.firstMessageContaining(phrase)
				if line == "" {
					t.Errorf("%d archive(s) carrying this host's own server identity were refused at %v and nothing said so. The operator sees archives that will never rotate and is never told they hold this machine's identity", len(group), reason)
					continue
				}
				if n := countedIn(line, countPhrase); n != len(group) {
					t.Errorf("the line for %v reports %d archive(s), want %d. The number and the population it is stated over have to be the same set: %q", reason, n, len(group), line)
				}
				// The spelling is the line's only actionable datum: without it the
				// operator cannot find the files. Unpinned, it can be replaced with
				// this host's own label and every other assertion stays green.
				if reason == refusalOtherShortLabel || reason == refusalUnqualifiedName {
					if want := retentionSpellingList(group); !strings.Contains(line, want) {
						t.Errorf("the line for %v does not name the spelling those archives were written under (%s). That name is the only thing an operator can grep for: %q", reason, want, line)
					}
				}
				reported++
			}
			if reported == 0 && len(foreign) > 0 {
				for _, b := range foreign {
					if archiveCarriesLocalServerID(b, sc.id) {
						t.Fatalf("the fixture holds a twin-keyed out-of-scope archive (%s) that the grouping did not return, so this scenario asserts nothing", b.BackupFile)
					}
				}
			}
		})
	}
}

// TestTwinKeyedReportingLeavesSeverityAndTheUnmanagedCountWhereTheyWere is
// requirement 4 and requirement 5 together. Every WARNING line is counted by
// ParseLogCounts and promotes an otherwise clean run to exit 1 through
// applyIssueExitCode, permanently, because no future run prunes any of these
// archives. The same listing read by the same host WITHOUT an identity is the
// baseline, and on these fixtures the identity adopts nothing, so it must add no
// WARNING line, remove none, and move the managed-by-nobody total by nothing.
//
// It kills emitting any of the three group lines at Warning instead of Info, and it
// kills adding any of those populations into the second return.
func TestTwinKeyedReportingLeavesSeverityAndTheUnmanagedCountWhereTheyWere(t *testing.T) {
	for _, sc := range twinKeyedScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			baseLogger := &levelRecordingLogger{}
			baseScoped, baseUnmanaged := applyRetentionHostScope("Local storage", sc.base, sc.backups, baseLogger)

			logger := &levelRecordingLogger{}
			scoped, unmanaged := applyRetentionHostScope("Local storage", sc.id, sc.backups, logger)

			if len(scoped) != len(baseScoped) {
				t.Fatalf("the identity adopted %d entrie(s) on a fixture built to have none adopted, so the severity comparison below would be measuring a classification change instead of a reporting one", len(scoped)-len(baseScoped))
			}
			if got, want := logger.countAtLevel("WARNING"), baseLogger.countAtLevel("WARNING"); got != want {
				t.Errorf("%d WARNING line(s) with an identity, %d without. Every WARNING pins the run at exit 1 through ParseLogCounts and applyIssueExitCode, for a condition no future run clears: %q", got, want, logger.messagesAtLevel("WARNING"))
			}
			if unmanaged != baseUnmanaged {
				t.Errorf("managed-by-nobody = %d with an identity and %d without. This round changes what is REPORTED, and that number is published as RetentionSummary.Owned and in the notification", unmanaged, baseUnmanaged)
			}
		})
	}
}

// TestRefusalReasonAgreesWithTheAdoptionPredicate holds the mirror. The reporting side
// now reads its cause from retentionAdoptionRefusal, which restates the clause order
// of archiveAdoptedByServerID rather than sharing a body with it, so nothing but a
// test stops the two drifting apart. A drift is silent: the archive is still refused,
// the operator is simply told the wrong reason, which is the whole defect being fixed.
//
// The equivalence also witnesses something worth writing down: clause f is
// unreachable once clause e has passed. If this host answered to the archive's own
// qualified name, that name would itself be a second spelling of the shared label and
// clause e would have refused first. The chain therefore needs no f term, and this
// assertion is what says so.
//
// It kills reordering any two clauses, dropping one, or loosening one, because each of
// those makes some cell of the space disagree with the predicate.
func TestRefusalReasonAgreesWithTheAdoptionPredicate(t *testing.T) {
	seen := map[retentionRefusal]int{}
	for _, archiveHost := range twinKeyedArchiveHosts {
		for _, archiveID := range twinKeyedArchiveIDs {
			for _, id := range twinKeyedIdentities() {
				meta := &types.BackupMetadata{
					BackupFile: "backup01.lan-backup-20250102-100000.tar.zst",
					Hostname:   archiveHost,
					ServerID:   archiveID,
					Verified:   true,
				}
				reason := retentionAdoptionRefusal(meta, id)
				seen[reason]++
				if adopted := archiveAdoptedByServerID(meta, id); adopted != (reason == refusalNone) {
					t.Errorf("archive %q (identity %q) read by host %q (aliases %v): adopted=%v but the reported refusal is %v. The Debug line and every summary line now read their cause from that value, so the operator is being told a reason that did not decide anything", archiveHost, archiveID, id.hostname, id.aliases, adopted, reason)
				}
			}
		}
	}
	for _, reason := range []retentionRefusal{refusalNone, refusalNoLocalIdentity, refusalNoManifestHost, refusalNoArchiveIdentity, refusalOtherIdentity, refusalUnqualifiedName, refusalOtherShortLabel, refusalCompetingSpelling} {
		if seen[reason] == 0 {
			t.Errorf("no cell of the space produced %v, so the equivalence above is vacuous for that clause", reason)
		}
	}
}

// TestEveryTwinKeyedRefusalHasALineToReportIt is the structural half of requirement 3,
// stated over the whole space instead of over the fixtures. Restricted to out-of-scope
// archives carrying this host's own identity, the refusal can only ever be one of the
// four clauses the summary lines cover: clause b passes by definition of the
// population, clause f cannot fire on an entry that is out of scope, and refusalNone
// means the archive was adopted. A fifth answer here is an archive the operator would
// never hear about.
//
// It kills adding a clause to archiveAdoptedByServerID, and a matching one to
// retentionAdoptionRefusal, without giving it a line.
func TestEveryTwinKeyedRefusalHasALineToReportIt(t *testing.T) {
	reportable := map[retentionRefusal]bool{
		refusalNoManifestHost:    true,
		refusalUnqualifiedName:   true,
		refusalOtherShortLabel:   true,
		refusalCompetingSpelling: true,
	}
	seen := map[retentionRefusal]int{}
	for _, archiveHost := range twinKeyedArchiveHosts {
		for _, id := range twinKeyedIdentities() {
			meta := &types.BackupMetadata{
				BackupFile: "backup01.lan-backup-20250102-100000.tar.zst",
				Hostname:   archiveHost,
				ServerID:   ourServerID,
				Verified:   true,
			}
			if !archiveCarriesLocalServerID(meta, id) || backupBelongsToHost(meta, id) {
				continue
			}
			reason := retentionAdoptionRefusal(meta, id)
			seen[reason]++
			if !reportable[reason] {
				t.Errorf("archive %q read by host %q (aliases %v) carries this host's own identity, is out of scope, and is refused at %v, which no line reports", archiveHost, id.hostname, id.aliases, reason)
			}
		}
	}
	for reason := range reportable {
		if seen[reason] == 0 {
			t.Errorf("no cell of the space produced %v, so this property does not actually cover it", reason)
		}
	}
}

// twinKeyedArchiveHosts and twinKeyedIdentities are the space the properties above
// walk. They deliberately include the degenerate names NormalizeHostname collapses
// ("." and a trailing dot), the rename artefacts, and the two host shapes that may
// adopt nothing at all.
var twinKeyedArchiveHosts = []string{
	"", "   ", ".", "pve", "pve.", "pve.home.arpa", "pve.siteb.example",
	"nas", "nas.lan", "nas.siteb.example", "backup01.lan", "other", "unknown.lan",
}

var twinKeyedArchiveIDs = []string{ourServerID, anotherServerID, "", "123456789012345"}

func twinKeyedIdentities() []retentionIdentity {
	return []retentionIdentity{
		hostWithIdentity("pve", ourServerID),
		hostWithIdentity("pve", ourServerID, "pve.home.arpa"),
		hostWithIdentity("pve.home.arpa", ourServerID),
		renamedHost("pve", "nas", ourServerID),
		renamedHost("pve", "nas.lan", ourServerID),
		hostOnly("pve"),
		hostWithIdentity("", ourServerID),
	}
}

// TestTheAppendedClauseIsAlwaysASubsetOfTheMismatchPopulation states, over the whole
// space, the one property that makes "N of them" a true sentence: an out-of-scope
// archive refused at clause e is ALWAYS an archive retentionSpellingMismatches counts.
//
// It holds because clause e is only reached after clause d passed, and clause d is
// archiveSharesLocalShortLabel, which is the predicate the mismatch count is defined
// by. The property is written down because that argument depends entirely on the ORDER
// of the two clauses, and the order is one line of code with nothing else holding it:
// evaluating clause e first is compile clean, leaves the ownership rule untouched, and
// on a host answering to a qualified alias under another label it re-attributes those
// archives to a competing spelling and appends them to a warning whose population does
// not contain them, which is defects (a) and (b) reappearing together.
func TestTheAppendedClauseIsAlwaysASubsetOfTheMismatchPopulation(t *testing.T) {
	seen := 0
	for _, archiveHost := range twinKeyedArchiveHosts {
		for _, id := range twinKeyedIdentities() {
			meta := &types.BackupMetadata{
				BackupFile: "backup01.lan-backup-20250102-100000.tar.zst",
				Hostname:   archiveHost,
				ServerID:   ourServerID,
				Verified:   true,
			}
			if backupBelongsToHost(meta, id) {
				continue
			}
			if retentionAdoptionRefusal(meta, id) != refusalCompetingSpelling {
				continue
			}
			seen++
			if n := retentionSpellingMismatches([]*types.BackupMetadata{meta}, id); n != 1 {
				t.Errorf("archive %q read by host %q (aliases %v) is refused at clause e but retentionSpellingMismatches counts it %d time(s), want 1. The clause e count is appended to the spelling-mismatch warning as \"N of them\", so an entry outside that population makes the number describe a set the sentence does not name", archiveHost, id.hostname, id.aliases, n)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no cell of the space reached clause e, so this property asserts nothing")
	}
}

// TestAdoptionRefusalNamesTheClauseThatFired pins the per-entry Debug prose to the
// clause chain now that both it and the summary lines are read off one value. It is
// the operator's only per-file explanation, and it is the text a support thread quotes.
//
// The degenerate row is the point of the table. A manifest hostname of "." survives
// TrimSpace and only collapses inside NormalizeHostname, so the refusal chain must
// normalise before it decides, exactly as clause a does. Deciding on the trimmed value
// instead reports "the archive names an unqualified host" about an archive clause a
// refused for naming no host at all, and it drops that archive into the bare-name
// group, whose line then names it as "".
func TestAdoptionRefusalNamesTheClauseThatFired(t *testing.T) {
	tests := []struct {
		name   string
		meta   *types.BackupMetadata
		id     retentionIdentity
		reason retentionRefusal
		says   string
	}{
		{
			name:   "no entry at all",
			meta:   nil,
			id:     hostWithIdentity("pve", ourServerID),
			reason: refusalNoEntry,
			says:   "no entry",
		},
		{
			name:   "this host does not know its own identity",
			meta:   &types.BackupMetadata{Hostname: "pve.home.arpa", ServerID: ourServerID},
			id:     hostOnly("pve"),
			reason: refusalNoLocalIdentity,
			says:   "does not know its own server identity",
		},
		{
			name:   "the manifest names no host",
			meta:   &types.BackupMetadata{BackupFile: "pve-backup-20250102-100000.tar.zst", ServerID: ourServerID},
			id:     hostWithIdentity("pve", ourServerID),
			reason: refusalNoManifestHost,
			says:   "no manifest named the host",
		},
		{
			name:   "the manifest names a bare dot, which is no host",
			meta:   &types.BackupMetadata{BackupFile: "pve-backup-20250102-100000.tar.zst", Hostname: ".", ServerID: ourServerID},
			id:     hostWithIdentity("pve", ourServerID),
			reason: refusalNoManifestHost,
			says:   "no manifest named the host",
		},
		{
			name:   "the archive records no identity",
			meta:   &types.BackupMetadata{Hostname: "pve.home.arpa"},
			id:     hostWithIdentity("pve", ourServerID),
			reason: refusalNoArchiveIdentity,
			says:   "no readable server identity",
		},
		{
			name:   "the archive records somebody else's identity",
			meta:   &types.BackupMetadata{Hostname: "pve.home.arpa", ServerID: anotherServerID},
			id:     hostWithIdentity("pve", ourServerID),
			reason: refusalOtherIdentity,
			says:   "another machine's server identity",
		},
		{
			name:   "the archive names a bare label",
			meta:   &types.BackupMetadata{Hostname: "pve", ServerID: ourServerID},
			id:     hostWithIdentity("pve.home.arpa", ourServerID),
			reason: refusalUnqualifiedName,
			says:   "unqualified host",
		},
		{
			name:   "the archive names an alias's label rather than this host's own",
			meta:   &types.BackupMetadata{Hostname: "nas.lan", ServerID: ourServerID},
			id:     renamedHost("pve", "nas", ourServerID),
			reason: refusalOtherShortLabel,
			says:   "not the first label this host reports under (pve)",
		},
		{
			name:   "this host holds a competing spelling of that label",
			meta:   &types.BackupMetadata{Hostname: "pve.siteb.example", ServerID: ourServerID},
			id:     hostWithIdentity("pve", ourServerID, "pve.home.arpa"),
			reason: refusalCompetingSpelling,
			says:   "answers to another spelling of that short name",
		},
		{
			name:   "nothing refused it",
			meta:   &types.BackupMetadata{Hostname: "pve.home.arpa", ServerID: ourServerID},
			id:     hostWithIdentity("pve", ourServerID),
			reason: refusalNone,
			says:   "no clause refused it",
		},
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
