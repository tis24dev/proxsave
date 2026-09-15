package config

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/pkg/utils"
)

// DuplicatedVariable records one variable assigned more than once in backup.env.
//
// Lines holds every assignment line in file order; WinningLine is the one whose
// value survives. For an ordinary variable that is the last assignment, because
// parseEnvFile writes raw[upperKey] on every hit. For a variable written in the
// block form it is the last BLOCK, which replaces what came before it while a
// later single line only concatenates onto it. See discardsAValue.
// The discarded values are deliberately NOT recorded: a duplicated
// TELEGRAM_BOT_TOKEN would put a secret in the log, and the line number is
// enough for the operator to find it.
type DuplicatedVariable struct {
	Name        string
	Lines       []int
	WinningLine int
	// ByBlock records that the winner is a BLOCK, which replaces everything before it,
	// rather than an ordinary last assignment. The two forms resolve in opposite ways
	// and the debug line has to say which one it saw.
	ByBlock bool
	// Discarded holds the lines whose value is thrown away, in file order. It is
	// NOT "every line but the winner": an assignment that follows a block
	// concatenates onto it and loses nothing, so it appears in Lines and not here.
	Discarded []int
}

// LegacyVariable is one variable assigned under a name the loader still reads but no
// longer documents, together with the canonical name it stands in for.
//
// Wins records which of the two the loader consults FIRST, which is the whole reason
// this category exists rather than a single line saying "old name". For seven of them
// the LEGACY name is consulted first, so editing the canonical line in the same file
// changes nothing at all and the backups keep landing wherever the legacy name points.
type LegacyVariable struct {
	Name             string
	Canonical        string
	Wins             bool
	CanonicalAlsoSet bool
}

// legacyAliases is every name the loader reads as a stand-in for a canonical one, with
// the order the loader consults them in: whichever name a call site lists FIRST is the
// one that wins, and that is what Wins records here.
//
// It is hand-written, and it was hand-written from only TWO of the four fallback
// helpers - getStringWithFallback and getBoolWithFallback. getIntWithFallback,
// getStringSliceWithFallback and half the getBoolWithFallback sites were never read,
// so eleven names the loader honours were reported to the operator as "not a known
// variable and is ignored". TestEveryFallbackNameIsRegisteredOrInTheTemplate now
// derives the same list from config.go with go/ast and fails naming the call site, so
// the twelfth cannot arrive in silence.
var legacyAliases = map[string]LegacyVariable{
	"LOCAL_BACKUP_PATH":       {Canonical: "BACKUP_PATH", Wins: true},
	"LOCAL_LOG_PATH":          {Canonical: "LOG_PATH", Wins: true},
	"ENABLE_SECONDARY_BACKUP": {Canonical: "SECONDARY_ENABLED", Wins: true},
	"SECONDARY_BACKUP_PATH":   {Canonical: "SECONDARY_PATH", Wins: true},
	"ENABLE_CLOUD_BACKUP":     {Canonical: "CLOUD_ENABLED", Wins: true},
	"RCLONE_REMOTE":           {Canonical: "CLOUD_REMOTE", Wins: true},
	"PROMETHEUS_ENABLED":      {Canonical: "METRICS_ENABLED", Wins: true},

	// These five are read only when the canonical name is ABSENT, because
	// getBoolWithLegacyAlias lists the canonical one first.
	telegramEnableLegacyKey:   {Canonical: telegramEnabledKey},
	emailEnableLegacyKey:      {Canonical: emailEnabledKey},
	gotifyEnableLegacyKey:     {Canonical: gotifyEnabledKey},
	webhookEnableLegacyKey:    {Canonical: webhookEnabledKey},
	emailFallbackPMFLegacyKey: {Canonical: emailFallbackSendmailKey},

	// The eleven from the helpers this table was never derived from. Every one of
	// these call sites lists the canonical name first and the canonical name is the
	// one the template assigns, so none of them wins.
	"FULL_SECURITY_CHECK":        {Canonical: "SECURITY_CHECK_ENABLED"},
	"CLOUD_CONNECTIVITY_TIMEOUT": {Canonical: "RCLONE_TIMEOUT_CONNECTION"},
	"LOCAL_RETENTION_DAYS":       {Canonical: "MAX_LOCAL_BACKUPS"},
	"SECONDARY_RETENTION_DAYS":   {Canonical: "MAX_SECONDARY_BACKUPS"},
	"CLOUD_RETENTION_DAYS":       {Canonical: "MAX_CLOUD_BACKUPS"},
	"PROMETHEUS_TEXTFILE_DIR":    {Canonical: "METRICS_PATH"},
	"BACKUP_REMOTE_CFG":          {Canonical: "BACKUP_REMOTE_CONFIGS"},
	"BACKUP_NETWORK_CONFIG":      {Canonical: "BACKUP_NETWORK_CONFIGS"},
	"BACKUP_PXAR_FILES":          {Canonical: "PXAR_SCAN_ENABLE"},
	"PXAR_INCLUDE_PATTERN":       {Canonical: "PXAR_FILE_INCLUDE_PATTERN"},
	"BACKUP_CRONTABS":            {Canonical: "BACKUP_CRON_JOBS"},

	// Read by hand rather than through a helper (config.go:690-693 falls back to
	// AGE_RECIPIENTS only when AGE_RECIPIENT yielded nothing), so the AST test above
	// cannot see it and it has to be kept here deliberately.
	"AGE_RECIPIENTS": {Canonical: "AGE_RECIPIENT"},
}

// KnownVariable is one variable the loader reads although the embedded template does
// not assign it, together with the rule that says so in operator-facing words.
type KnownVariable struct {
	Name string
	Rule string
}

// NearMissVariable is an unknown variable whose name is ONE edit away from a name the
// loader does read: a dropped, added, changed or swapped character. It exists because
// "not a known variable and is ignored" is the same sentence for a setting that was
// retired and for one the operator meant to set and mistyped, and only the second one
// silently disables something they asked for. Measured on a live host: a
// CUSTOM_BACKUP_PATHS that lost its leading C kept two custom backup paths out of every
// run for months, reported only as one more unknown name in a list of seven.
type NearMissVariable struct {
	// Name is the name as written in the file.
	Name string
	// Suggestion is the known variable it is one edit away from.
	Suggestion string
}

// The rules templateKnows can accept a variable by, in the words the debug line uses.
const (
	knownDocumented = "documented there as a commented example"
	knownWebhook    = "per-endpoint webhook variable, WEBHOOK_<name>_<field>"
)

// unknownRuleTrace is why a variable matched NO rule, spelled out rather than left as
// the absence of a line: it names every gate that was tried, in the order they were.
const unknownRuleTrace = "not assigned in the template, not documented there, not a webhook endpoint field, not a legacy alias"

// UnknownRuleTrace is the operator-facing list of every gate a variable failed before
// the audit called it unknown, exported so the renderer states the rules rather than
// keeping its own copy of them.
func UnknownRuleTrace() string { return unknownRuleTrace }

// VariableAssignment describes HOW one variable is assigned in the audited file:
// every line that assigns it, the one whose value is in effect, and whether that one
// carries an empty value. The value itself is never recorded, so a duplicated
// TELEGRAM_BOT_TOKEN cannot reach a log through here either.
//
// WinningLine is not simply the last line. It is the last assignment that REPLACES,
// which for an ordinary variable is indeed the last one, but for a variable written
// in the block form is the last block: a line after a block concatenates onto it
// rather than winning over it. See replacingAssignment.
type VariableAssignment struct {
	Lines        []int
	WinningLine  int
	WinningEmpty bool
}

// ConfigIntegrityReport is the verdict of one audit of a backup.env against the
// embedded template. It carries the raw counters the DEBUG lines print and the
// three finding sets the operator-facing lines print, so every surface renders
// the same audit instead of recomputing it.
type ConfigIntegrityReport struct {
	Path              string
	Lines             int
	Assignments       int
	Distinct          int
	TemplateVariables int
	SkippedMultiValue []string
	Duplicated        []DuplicatedVariable
	Absent            []string
	Unknown           []string

	// TemplateDocumented counts the variables the template only DOCUMENTS on a
	// commented line. With TemplateVariables it accounts for both halves of what the
	// audit treats as known, so a wrong verdict can be reconstructed from the block.
	TemplateDocumented int
	// KnownOutsideTemplate names each variable accepted although the template does not
	// assign it, with the rule that accepted it. This is the decision that used to be
	// wrong, so it is the one an operator checking the block will be looking for.
	KnownOutsideTemplate []KnownVariable
	// Legacy names each variable written under a name the loader still reads but no
	// longer documents. It is its own category because neither of the others fits: the
	// loader DOES read it, so it is not unknown, and for seven of them it wins over the
	// canonical name, so it is not harmless either.
	Legacy []LegacyVariable
	// NearMiss is the subset of Unknown that is one character away from a name the
	// loader reads. Every entry is ALSO in Unknown: the classification does not change,
	// only what the operator is told about it.
	NearMiss []NearMissVariable

	// assignments answers "how is this variable written in the file" for callers that
	// need to explain ONE variable rather than list the file's findings, e.g. the
	// daemon diagnostics saying why a personal script reads as NOT CONFIGURED.
	assignments map[string]VariableAssignment
}

// Assignment reports how a variable is assigned in the audited file. The second
// return is false when the variable is not assigned at all, which is a different
// fact from being assigned with an empty value and has to stay distinguishable.
func (r *ConfigIntegrityReport) Assignment(name string) (VariableAssignment, bool) {
	if r == nil || r.assignments == nil {
		return VariableAssignment{}, false
	}
	assignment, ok := r.assignments[strings.ToUpper(strings.TrimSpace(name))]
	return assignment, ok
}

// Clean reports whether the audit found nothing at all.
func (r *ConfigIntegrityReport) Clean() bool {
	if r == nil {
		return true
	}
	return len(r.Duplicated) == 0 && len(r.Absent) == 0 && len(r.Unknown) == 0 && len(r.Legacy) == 0
}

// HasIssues reports whether the audit found something that discards or omits an
// operator value. An unknown variable is deliberately excluded: it is reported,
// but it is not evidence that anything the operator wrote is being lost.
func (r *ConfigIntegrityReport) HasIssues() bool {
	if r == nil {
		return false
	}
	if len(r.Duplicated) > 0 || len(r.Absent) > 0 {
		return true
	}
	// An unknown name on its own is not an issue: it is usually a setting that was
	// retired, and the file is otherwise sound. One character away from a real variable
	// is a different fact - the line the operator wrote has no effect - which is the
	// duplicate's harm under another shape and is graded the same way.
	if len(r.NearMiss) > 0 {
		return true
	}
	// A legacy name alone works and discards nothing. A legacy name WITH its canonical
	// twin means one of the two lines has no effect, which is the duplicate's harm.
	for _, legacy := range r.Legacy {
		if legacy.CanonicalAlsoSet {
			return true
		}
	}
	return false
}

// AuditConfigFile compares an env file against the embedded template.
//
// It deliberately does NOT change parseEnvFile, whose upstream blast radius is the
// whole install/upgrade surface. It re-reads the file with the same primitives
// instead - utils.IsComment, utils.SplitKeyValue, the same "export" prefix rule and
// the same multiValueKeys/blockValueKeys sets - so the two readers agree by
// construction rather than by coincidence. TestAuditAgreesWithTheLoader pins that.
//
// Nothing here rejects a configuration or changes which value wins. Last-wins stays
// the loader's rule; the audit only stops it from being silent.
func AuditConfigFile(path string) (*ConfigIntegrityReport, error) {
	file, err := os.Open(path) // #nosec G304 - operator-supplied configuration path, same one parseEnvFile opens
	if err != nil {
		return nil, fmt.Errorf("cannot open config file: %w", err)
	}
	defer func() { _ = file.Close() }()

	fileScan, err := scanEnvAssignments(bufio.NewScanner(file))
	if err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}
	templateScan, err := scanEnvAssignments(bufio.NewScanner(strings.NewReader(DefaultEnvTemplate())))
	if err != nil {
		return nil, fmt.Errorf("error reading embedded template: %w", err)
	}
	documented := scanDocumentedVariables(bufio.NewScanner(strings.NewReader(DefaultEnvTemplate())))

	report := &ConfigIntegrityReport{
		Path:               path,
		Lines:              fileScan.lines,
		Assignments:        fileScan.assignments,
		Distinct:           len(fileScan.order),
		TemplateVariables:  len(templateScan.order),
		SkippedMultiValue:  skippedMultiValueVariables(),
		TemplateDocumented: len(documented),
		assignments:        make(map[string]VariableAssignment, len(fileScan.order)),
	}

	for _, name := range fileScan.order {
		at := fileScan.assignedAt[name]
		lines := make([]int, 0, len(at))
		for _, entry := range at {
			lines = append(lines, entry.line)
		}
		inEffect := at[replacingAssignment(name, at)]
		report.assignments[name] = VariableAssignment{
			Lines:        lines,
			WinningLine:  inEffect.line,
			WinningEmpty: inEffect.empty,
		}
		if winning, discarded := discardsAValue(name, at); len(discarded) > 0 {
			report.Duplicated = append(report.Duplicated, DuplicatedVariable{
				Name:        name,
				Lines:       lines,
				WinningLine: winning,
				Discarded:   discarded,
				ByBlock:     at[replacingAssignment(name, at)].block,
			})
		}
		if alias, ok := legacyAliases[name]; ok {
			alias.Name = name
			_, alias.CanonicalAlsoSet = fileScan.assignedAt[alias.Canonical]
			report.Legacy = append(report.Legacy, alias)
			continue
		}
		switch rule, known := templateKnows(name, templateScan.assignedAt, documented); {
		case !known:
			report.Unknown = append(report.Unknown, name)
			if suggestion, ok := nearMissKnownName(name, templateScan.assignedAt, documented); ok {
				report.NearMiss = append(report.NearMiss, NearMissVariable{Name: name, Suggestion: suggestion})
			}
		case rule != "":
			report.KnownOutsideTemplate = append(report.KnownOutsideTemplate, KnownVariable{Name: name, Rule: rule})
		}
	}
	for _, name := range templateScan.order {
		if _, ok := fileScan.assignedAt[name]; !ok {
			report.Absent = append(report.Absent, name)
		}
	}
	return report, nil
}

// envAssignment is one KEY=VALUE line: where it is, and whether its value is empty.
// The value is deliberately not kept.
type envAssignment struct {
	line  int
	empty bool
	block bool
}

// envScan is one pass over an env file: which variables are assigned, on which
// lines, and how many lines and assignments the file has.
type envScan struct {
	order       []string
	assignedAt  map[string][]envAssignment
	lines       int
	assignments int
}

// scanEnvAssignments mirrors parseEnvFile's loop exactly, including the multi-line
// block form, so a line the loader ignores is a line the audit ignores.
func scanEnvAssignments(scanner *bufio.Scanner) (envScan, error) {
	scan := envScan{assignedAt: make(map[string][]envAssignment)}
	for scanner.Scan() {
		scan.lines++
		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if utils.IsComment(trimmed) {
			continue
		}
		key, value, ok := utils.SplitKeyValue(line)
		if !ok {
			continue
		}
		if fields := strings.Fields(key); len(fields) >= 2 && fields[0] == "export" {
			key = fields[1]
		}
		upperKey := strings.ToUpper(key)
		if _, seen := scan.assignedAt[upperKey]; !seen {
			scan.order = append(scan.order, upperKey)
		}
		// A block value swallows its own lines in the loader, so the audit has to
		// swallow them too or it would read a value line as an assignment. Which
		// form this is also decides whether repeating the variable discards
		// anything, so the answer is kept on the assignment.
		opensBlock := blockValueKeys[upperKey] && trimmed == fmt.Sprintf("%s=\"", key)
		scan.assignedAt[upperKey] = append(scan.assignedAt[upperKey], envAssignment{
			line:  scan.lines,
			empty: strings.TrimSpace(value) == "",
			block: opensBlock,
		})
		scan.assignments++

		if opensBlock {
			for scanner.Scan() {
				scan.lines++
				next := strings.TrimRight(scanner.Text(), "\r")
				if strings.TrimSpace(next) == "\"" {
					break
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return envScan{}, err
	}
	return scan, nil
}

// discardsAValue reports whether repeating a variable throws away something the
// file already set, and which assignment does the throwing away.
//
// The loader does not resolve every variable by last-wins, so the audit cannot
// either:
//
//   - an ordinary variable is overwritten by every later assignment;
//   - the single-line form of a multi-value variable CONCATENATES, ending its
//     branch with `raw[upperKey] = existing + "\n" + value`, so it discards
//     nothing however often it is repeated;
//   - the block form does NOT concatenate. Its branch ends with a plain
//     `raw[upperKey] = strings.Join(blockLines, "\n")`, so a block replaces
//     everything the file set before it, an earlier block included. Assignments
//     that follow the last block concatenate onto it and lose nothing.
//
// The third case is why exempting the block variables outright was wrong: the
// shipped template writes both of them in block form, so an operator who adds
// their own line above one loses it in silence.
func discardsAValue(upperKey string, at []envAssignment) (winning int, discarded []int) {
	if len(at) < 2 {
		return 0, nil
	}
	replacing := replacingAssignment(upperKey, at)
	if replacing == 0 {
		return 0, nil
	}
	for _, assignment := range at[:replacing] {
		discarded = append(discarded, assignment.line)
	}
	return at[replacing].line, discarded
}

// replacingAssignment returns the index of the assignment whose value is in effect
// as the base: the LAST one that replaces rather than concatenates. Everything
// before it is thrown away; everything after it adds to it.
func replacingAssignment(upperKey string, at []envAssignment) int {
	if blockValueKeys[upperKey] {
		last := -1
		for i := range at {
			if at[i].block {
				last = i
			}
		}
		if last < 0 {
			// No block in the file: every assignment is the single-line form, which
			// concatenates, so the first one sets the value and the rest add to it.
			return 0
		}
		return last
	}
	if multiValueKeys[upperKey] {
		return 0
	}
	return len(at) - 1
}

// templateKnows reports whether the loader reads a variable, which is what "unknown"
// has to mean. The template's ACTIVE assignments are only part of the answer, and
// taking them for the whole of it told operators that working configuration was
// being ignored, which invites them to delete it.
//
// Three families the loader reads never appear as an active template assignment:
// variables the template only DOCUMENTS as a commented example, the per-endpoint
// webhook variables whose names are the operator's own, and the legacy notification
// aliases no template has ever carried.
// It returns the rule that accepted the variable, empty when the template assigns it
// outright and no rule was needed, and reports whether anything accepted it at all.
// nearMissKnownName reports the known variable that `name` is one edit away from: one
// character dropped, added, changed, or two adjacent ones swapped. Those four are the
// typos an operator's editor produces; anything further apart is a different name, not
// a slip, so the check stays at distance one rather than ranking candidates.
//
// The candidate set is the same one templateKnows accepts by, minus the webhook rule,
// which matches by shape rather than by name and has nothing to be near.
//
// The first match in the template's own order wins. Two known names at distance one
// from the same typo is possible in principle (a family like BACKUP_PBS_ACME_ACCOUNTS
// / BACKUP_PBS_ACME_PLUGINS differs by more than one character, so it does not arise
// today), and naming one is still better than naming none: the operator reads the
// suggestion against the line they wrote.
func nearMissKnownName(name string, assigned map[string][]envAssignment, documented map[string]struct{}) (string, bool) {
	best := ""
	for candidate := range assigned {
		if isOneEditApart(name, candidate) && (best == "" || candidate < best) {
			best = candidate
		}
	}
	if best != "" {
		return best, true
	}
	for candidate := range documented {
		if isOneEditApart(name, candidate) && (best == "" || candidate < best) {
			best = candidate
		}
	}
	return best, best != ""
}

// isOneEditApart is Damerau-Levenshtein distance == 1, written out rather than as a
// full distance matrix because only the answer "exactly one" is ever needed: the four
// cases below are the whole of it at that distance.
func isOneEditApart(a, b string) bool {
	if a == b {
		return false
	}
	switch len(a) - len(b) {
	case 0:
		diff := -1
		for i := range len(a) {
			if a[i] == b[i] {
				continue
			}
			if diff >= 0 {
				// A second difference is only still distance one when it is the
				// other half of a swap of two adjacent characters.
				return diff == i-1 && a[diff] == b[i] && a[i] == b[diff] && a[i+1:] == b[i+1:]
			}
			diff = i
		}
		return diff >= 0
	case 1:
		return isOneCharDeletionOf(a, b)
	case -1:
		return isOneCharDeletionOf(b, a)
	}
	return false
}

// isOneCharDeletionOf reports whether removing exactly one character from long yields
// short. It is the dropped-character case, and reversed, the added-character one.
func isOneCharDeletionOf(long, short string) bool {
	for i := range len(long) {
		if long[:i]+long[i+1:] == short {
			return true
		}
	}
	return false
}

func templateKnows(upperKey string, assigned map[string][]envAssignment, documented map[string]struct{}) (string, bool) {
	if _, ok := assigned[upperKey]; ok {
		return "", true
	}
	if _, ok := documented[upperKey]; ok {
		return knownDocumented, true
	}
	if isWebhookEndpointVariable(upperKey) {
		return knownWebhook, true
	}
	return "", false
}

// scanDocumentedVariables collects the names the template DOCUMENTS on a commented
// line rather than assigning, the shape it uses for anything an operator has to opt
// into by hand: `# SAFE_PROCESSES=""`, `# WEBHOOK_PUSHOVER_URL=...`.
//
// Only a bare NAME before the '=' counts, so the prose around those lines is not read
// as a declaration: "# Example: SAFE_PROCESSES=\"ffmpeg\"." and a commented URL with a
// query string both fail that test and are ignored.
func scanDocumentedVariables(scanner *bufio.Scanner) map[string]struct{} {
	names := make(map[string]struct{})
	for scanner.Scan() {
		trimmed := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, _, ok := utils.SplitKeyValue(strings.TrimSpace(strings.TrimLeft(trimmed, "#")))
		if !ok || !isEnvVariableName(key) {
			continue
		}
		names[strings.ToUpper(key)] = struct{}{}
	}
	return names
}

// isEnvVariableName reports whether a token is a bare shell variable name.
func isEnvVariableName(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// webhookEndpointFields are the per-endpoint suffixes BuildWebhookConfig reads off the
// runtime prefix `WEBHOOK_<NAME>_`. The endpoint NAME is the operator's, so it can
// never appear in the template; keeping the FIELD a closed set is what stops this rule
// from accepting a misspelling as known.
var webhookEndpointFields = []string{
	"URL", "FORMAT", "METHOD", "PRIORITY", "HEADERS",
	"AUTH_TYPE", "AUTH_TOKEN", "AUTH_USER", "AUTH_PASS", "AUTH_SECRET",
}

func isWebhookEndpointVariable(upperKey string) bool {
	rest, ok := strings.CutPrefix(upperKey, "WEBHOOK_")
	if !ok {
		return false
	}
	for _, field := range webhookEndpointFields {
		if name, found := strings.CutSuffix(rest, "_"+field); found && name != "" {
			return true
		}
	}
	return false
}

func skippedMultiValueVariables() []string {
	set := make(map[string]struct{}, len(multiValueKeys)+len(blockValueKeys))
	for key := range multiValueKeys {
		set[key] = struct{}{}
	}
	for key := range blockValueKeys {
		set[key] = struct{}{}
	}
	names := make([]string, 0, len(set))
	for key := range set {
		names = append(names, key)
	}
	sort.Strings(names)
	return names
}
