package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/pkg/utils"
)

func writeEnvFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// The shape reported in issue #306: the operator adds their own line ABOVE the
// template's empty one, parseEnvFile's last-wins rule keeps the empty value, and
// nothing anywhere says so. The same file with the two lines swapped works, which
// is why two machines out of four behaved differently.
func TestAuditFindsTheDuplicateThatSilentlyDiscardsAnOperatorValue(t *testing.T) {
	path := writeEnvFile(t, "PERSONAL_SCRIPT_PRE_RUN=/home/howard/dd/mount-pbs\nPERSONAL_SCRIPT_PRE_RUN=\n")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PersonalScriptPreRun != "" {
		t.Fatalf("precondition: expected the loader to discard the operator value, got %q", cfg.PersonalScriptPreRun)
	}

	report, err := AuditConfigFile(path)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if len(report.Duplicated) != 1 {
		t.Fatalf("expected exactly one duplicated variable, got %+v", report.Duplicated)
	}
	got := report.Duplicated[0]
	if got.Name != "PERSONAL_SCRIPT_PRE_RUN" {
		t.Fatalf("expected PERSONAL_SCRIPT_PRE_RUN, got %q", got.Name)
	}
	if len(got.Lines) != 2 || got.Lines[0] != 1 || got.Lines[1] != 2 {
		t.Fatalf("expected assignments on lines 1 and 2, got %v", got.Lines)
	}
	if got.WinningLine != 2 {
		t.Fatalf("expected the LAST assignment to win, got line %d", got.WinningLine)
	}
	if !report.HasIssues() || report.Clean() {
		t.Fatalf("a discarded operator value must count as an issue: %+v", report)
	}
}

// The post-run script is not special-cased anywhere: the audit iterates over the
// file, so both operator scripts are covered by the same code path.
func TestAuditCoversBothPersonalScriptsIdentically(t *testing.T) {
	path := writeEnvFile(t, strings.Join([]string{
		"PERSONAL_SCRIPT_PRE_RUN=/home/howard/dd/mount-pbs",
		"PERSONAL_SCRIPT_POST_RUN=/home/howard/dd/umount-pbs",
		"PERSONAL_SCRIPT_PRE_RUN=",
		"PERSONAL_SCRIPT_POST_RUN=",
	}, "\n")+"\n")

	report, err := AuditConfigFile(path)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	found := map[string]bool{}
	for _, duplicated := range report.Duplicated {
		found[duplicated.Name] = true
	}
	for _, name := range []string{"PERSONAL_SCRIPT_PRE_RUN", "PERSONAL_SCRIPT_POST_RUN"} {
		if !found[name] {
			t.Fatalf("expected %s among the duplicates, got %+v", name, report.Duplicated)
		}
	}
}

// The audit re-reads the file instead of changing parseEnvFile, whose upstream blast
// radius is the whole install/upgrade surface. This is the guard against the two
// readers drifting apart: whatever the audit calls the winning line must hold the
// value the loader actually kept.
func TestAuditAndLoaderAgreeOnTheWinningLine(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"template line after the operator line": {"PERSONAL_SCRIPT_PRE_RUN=/first\nPERSONAL_SCRIPT_PRE_RUN=\n", ""},
		"operator line after the template line": {"PERSONAL_SCRIPT_PRE_RUN=\nPERSONAL_SCRIPT_PRE_RUN=/second\n", "/second"},
		"three assignments":                     {"PERSONAL_SCRIPT_PRE_RUN=/a\nPERSONAL_SCRIPT_PRE_RUN=/b\nPERSONAL_SCRIPT_PRE_RUN=/c\n", "/c"},
		"export prefix wins":                    {"PERSONAL_SCRIPT_PRE_RUN=/a\nexport PERSONAL_SCRIPT_PRE_RUN=/b\n", "/b"},
		"lower case key wins":                   {"PERSONAL_SCRIPT_PRE_RUN=/a\npersonal_script_pre_run=/b\n", "/b"},
		"inline comment on the winner":          {"PERSONAL_SCRIPT_PRE_RUN=/a\nPERSONAL_SCRIPT_PRE_RUN=/b   # note\n", "/b"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeEnvFile(t, tc.body)
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.PersonalScriptPreRun != tc.want {
				t.Fatalf("loader kept %q, expected %q", cfg.PersonalScriptPreRun, tc.want)
			}
			report, err := AuditConfigFile(path)
			if err != nil {
				t.Fatalf("audit: %v", err)
			}
			if len(report.Duplicated) != 1 {
				t.Fatalf("expected one duplicate, got %+v", report.Duplicated)
			}
			lines := strings.Split(strings.TrimRight(tc.body, "\n"), "\n")
			winning := lines[report.Duplicated[0].WinningLine-1]
			_, value, ok := utils.SplitKeyValue(winning)
			if !ok || value != tc.want {
				t.Fatalf("the audit's winning line %d holds %q, the loader kept %q",
					report.Duplicated[0].WinningLine, value, tc.want)
			}
		})
	}
}

// A variable that CONCATENATES instead of overwriting loses nothing when it is
// repeated, so reporting it would be a false positive on a perfectly normal file.
func TestAuditIgnoresVariablesThatConcatenateInsteadOfOverwriting(t *testing.T) {
	path := writeEnvFile(t, "BACKUP_EXCLUDE_PATTERNS=*.tmp\nBACKUP_EXCLUDE_PATTERNS=*.log\nAGE_RECIPIENT=age1aaa\nAGE_RECIPIENT=age1bbb\n")
	report, err := AuditConfigFile(path)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if len(report.Duplicated) != 0 {
		t.Fatalf("multi-value variables must not be reported as duplicates: %+v", report.Duplicated)
	}
	for _, name := range []string{"AGE_RECIPIENT", "BACKUP_BLACKLIST", "BACKUP_EXCLUDE_PATTERNS", "CUSTOM_BACKUP_PATHS"} {
		if !containsString(report.SkippedMultiValue, name) {
			t.Fatalf("expected %s among the skipped variables, got %v", name, report.SkippedMultiValue)
		}
	}
}

// The template ships with the binary, so "absent" can only mean a new binary whose
// configuration merge never ran. An operator who has not upgraded carries an older
// template and sees nothing here.
func TestAuditReportsAbsentAndUnknownAgainstTheEmbeddedTemplate(t *testing.T) {
	var kept []string
	for _, line := range strings.Split(DefaultEnvTemplate(), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "SCHEDULER_TIME=") || strings.HasPrefix(trimmed, "HEALTHCHECK_UPDATES_ID=") {
			continue
		}
		kept = append(kept, line)
	}
	kept = append(kept, "PERSONAL_SCRIPTS_PRERUN=/home/howard/dd/typo")

	report, err := AuditConfigFile(writeEnvFile(t, strings.Join(kept, "\n")))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if len(report.Absent) != 2 || !containsString(report.Absent, "SCHEDULER_TIME") || !containsString(report.Absent, "HEALTHCHECK_UPDATES_ID") {
		t.Fatalf("expected exactly the two removed variables as absent, got %v", report.Absent)
	}
	if len(report.Unknown) != 1 || report.Unknown[0] != "PERSONAL_SCRIPTS_PRERUN" {
		t.Fatalf("expected only the misspelled variable as unknown, got %v", report.Unknown)
	}
	if report.HasIssues() != true {
		t.Fatalf("absent variables must count as issues")
	}
}

// The shipped template is the baseline every operator starts from: it must audit clean,
// or the block would cry wolf on a fresh install.
func TestTheEmbeddedTemplateAuditsClean(t *testing.T) {
	report, err := AuditConfigFile(writeEnvFile(t, DefaultEnvTemplate()))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if !report.Clean() {
		t.Fatalf("the shipped template must audit clean: duplicated=%+v absent=%v unknown=%v",
			report.Duplicated, report.Absent, report.Unknown)
	}
	if report.TemplateVariables == 0 || report.Distinct != report.TemplateVariables {
		t.Fatalf("template variables=%d distinct=%d", report.TemplateVariables, report.Distinct)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// The audit exists to say when the loader discards a value the operator wrote, so
// it has to model how the loader actually resolves a repeated variable - which is
// NOT last-wins for every variable. CUSTOM_BACKUP_PATHS accepts two legal shapes
// and parseEnvFile treats them in opposite ways: the single-line form concatenates
// (config.go:1768 `raw[upperKey] = existing + "\n" + value`), the block form
// replaces (config.go:1764 `raw[upperKey] = strings.Join(blockLines, "\n")`). An
// audit that exempts the variable outright stays silent exactly where a value is
// lost.
//
// This is the guard integrity.go's own doc comment names.
func TestAuditAgreesWithTheLoader(t *testing.T) {
	cases := map[string]struct {
		body string
		// wantPaths is what the loader resolves CUSTOM_BACKUP_PATHS to.
		wantPaths []string
		// wantLines is empty when the loader discards nothing, so the audit must
		// stay silent; otherwise it is every assignment line of the variable and
		// wantWinning is the line whose value survives.
		wantLines   []int
		wantWinning int
	}{
		"two single lines concatenate": {
			body:      "CUSTOM_BACKUP_PATHS=/etc/a\nCUSTOM_BACKUP_PATHS=/srv/important\n",
			wantPaths: []string{"/etc/a", "/srv/important"},
		},
		"a line after a block concatenates onto it": {
			body:      "CUSTOM_BACKUP_PATHS=\"\n/etc/a\n\"\nCUSTOM_BACKUP_PATHS=/srv/important\n",
			wantPaths: []string{"/etc/a", "/srv/important"},
		},
		"a block after a line replaces it": {
			body:        "CUSTOM_BACKUP_PATHS=/srv/important\nCUSTOM_BACKUP_PATHS=\"\n/etc/a\n\"\n",
			wantPaths:   []string{"/etc/a"},
			wantLines:   []int{1, 2},
			wantWinning: 2,
		},
		"a block after a block replaces it": {
			body:        "CUSTOM_BACKUP_PATHS=\"\n/etc/a\n\"\nCUSTOM_BACKUP_PATHS=\"\n/etc/b\n\"\n",
			wantPaths:   []string{"/etc/b"},
			wantLines:   []int{1, 4},
			wantWinning: 4,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeEnvFile(t, tc.body)

			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := strings.Join(cfg.CustomBackupPaths, "|"); got != strings.Join(tc.wantPaths, "|") {
				t.Fatalf("the loader kept %q, the case says it keeps %q", got, strings.Join(tc.wantPaths, "|"))
			}

			report, err := AuditConfigFile(path)
			if err != nil {
				t.Fatalf("audit: %v", err)
			}
			var found *DuplicatedVariable
			for i := range report.Duplicated {
				if report.Duplicated[i].Name == "CUSTOM_BACKUP_PATHS" {
					found = &report.Duplicated[i]
				}
			}

			if len(tc.wantLines) == 0 {
				if found != nil {
					t.Fatalf("the loader discards nothing here, but the audit reported %+v", *found)
				}
				return
			}
			if found == nil {
				t.Fatalf("the loader discarded the value on line %d and the audit said nothing",
					tc.wantLines[0])
			}
			if got := joinInts(found.Lines); got != joinInts(tc.wantLines) {
				t.Fatalf("the audit reported lines %s, expected %s", got, joinInts(tc.wantLines))
			}
			if found.WinningLine != tc.wantWinning {
				t.Fatalf("the audit says line %d wins, the loader kept the value on line %d",
					found.WinningLine, tc.wantWinning)
			}
		})
	}
}

func joinInts(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

// Assignment answers "how is this variable written", and for a variable written in
// the block form the answer is not the last line. The block replaces, a line after it
// concatenates onto it, so the assignment whose value is in effect as the base is the
// block, not whatever happens to come last.
func TestTheAssignmentInEffectIsTheBlockNotTheLastLine(t *testing.T) {
	path := writeEnvFile(t, "CUSTOM_BACKUP_PATHS=/srv/important\nCUSTOM_BACKUP_PATHS=\"\n/etc/a\n\"\nCUSTOM_BACKUP_PATHS=/srv/other\n")
	report, err := AuditConfigFile(path)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	assignment, ok := report.Assignment("CUSTOM_BACKUP_PATHS")
	if !ok {
		t.Fatal("CUSTOM_BACKUP_PATHS is assigned, the report says it is not")
	}
	if got := joinInts(assignment.Lines); got != "1,2,5" {
		t.Fatalf("lines %s, expected 1,2,5", got)
	}
	if assignment.WinningLine != 2 {
		t.Fatalf("WinningLine is %d, but line 2 is the block that replaces and line 5 only appends to it",
			assignment.WinningLine)
	}
}

// "Unknown" must mean "the loader does not read this", not "the template does not
// assign it on an uncommented line". Three families of variable the loader really
// reads never appear as an active template assignment, and calling them ignored
// invites the operator to delete working configuration.
func TestAVariableTheLoaderReadsIsNotReportedAsUnknown(t *testing.T) {
	cases := map[string]string{
		"documented as a commented example": "SAFE_PROCESSES=\"ffmpeg\"\n",
		"a second commented example":        "SAFE_BRACKET_PROCESSES=\"[kworker]\"\n",
		"per-endpoint webhook, templated":   "WEBHOOK_ENDPOINTS=pushover\nWEBHOOK_PUSHOVER_URL=https://example.invalid/hook\n",
		"per-endpoint webhook, own name":    "WEBHOOK_ENDPOINTS=mine\nWEBHOOK_MINE_URL=https://example.invalid/hook\nWEBHOOK_MINE_AUTH_TOKEN=t\n",
		"legacy notification alias":         "EMAIL_ENABLE=true\n",
		"legacy fallback alias":             "EMAIL_FALLBACK_PMF=true\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			report, err := AuditConfigFile(writeEnvFile(t, DefaultEnvTemplate()+body))
			if err != nil {
				t.Fatalf("audit: %v", err)
			}
			if len(report.Unknown) != 0 {
				t.Fatalf("the loader reads these, the audit calls them unknown and ignored: %v", report.Unknown)
			}
		})
	}
}

// The counterpart: a name nothing reads must still be reported, or the category
// stops meaning anything.
func TestAVariableNothingReadsIsStillReportedAsUnknown(t *testing.T) {
	report, err := AuditConfigFile(writeEnvFile(t, DefaultEnvTemplate()+"PERSONAL_SCRIPTS_PRERUN=/tmp/x\nWEBHOOK_MINE_NOSUCHFIELD=1\n"))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if joinStrings(report.Unknown) != "PERSONAL_SCRIPTS_PRERUN,WEBHOOK_MINE_NOSUCHFIELD" {
		t.Fatalf("unknown = %v, expected both misspellings", report.Unknown)
	}
}

func joinStrings(values []string) string {
	return strings.Join(values, ",")
}

// A legacy alias is neither unknown nor unremarkable. The loader reads it, and for
// seven of them it reads it FIRST, so a canonical line written in the same file has no
// effect at all: the backups keep landing wherever the legacy name points. Calling that
// "not a known variable and is ignored" was false, and saying nothing hides the trap.
func TestALegacyAliasGetsItsOwnCategory(t *testing.T) {
	cases := map[string]struct {
		body          string
		wantName      string
		wantCanonical string
		wantWins      bool
		wantBothSet   bool
	}{
		"legacy wins and both are set": {
			body:          "LOCAL_BACKUP_PATH=/srv/legacy\nBACKUP_PATH=/srv/canonical\n",
			wantName:      "LOCAL_BACKUP_PATH",
			wantCanonical: "BACKUP_PATH",
			wantWins:      true,
			wantBothSet:   true,
		},
		"legacy wins and it is the only one": {
			body:          "RCLONE_REMOTE=myremote\n",
			wantName:      "RCLONE_REMOTE",
			wantCanonical: "CLOUD_REMOTE",
			wantWins:      true,
		},
		"canonical wins and both are set": {
			body:          "EMAIL_ENABLE=true\nEMAIL_ENABLED=false\n",
			wantName:      "EMAIL_ENABLE",
			wantCanonical: "EMAIL_ENABLED",
			wantBothSet:   true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			report, err := AuditConfigFile(writeEnvFile(t, tc.body))
			if err != nil {
				t.Fatalf("audit: %v", err)
			}
			if containsString(report.Unknown, tc.wantName) {
				t.Fatalf("%s is read by the loader, the audit still calls it unknown", tc.wantName)
			}
			var found *LegacyVariable
			for i := range report.Legacy {
				if report.Legacy[i].Name == tc.wantName {
					found = &report.Legacy[i]
				}
			}
			if found == nil {
				t.Fatalf("%s is not in the legacy category: %+v", tc.wantName, report.Legacy)
			}
			if found.Canonical != tc.wantCanonical {
				t.Fatalf("canonical %q, expected %q", found.Canonical, tc.wantCanonical)
			}
			if found.Wins != tc.wantWins {
				t.Fatalf("Wins = %v, expected %v", found.Wins, tc.wantWins)
			}
			if found.CanonicalAlsoSet != tc.wantBothSet {
				t.Fatalf("CanonicalAlsoSet = %v, expected %v", found.CanonicalAlsoSet, tc.wantBothSet)
			}
		})
	}
}

// A name one character away from a real variable is a typo that silently disables the
// setting, so the audit names the variable it was probably meant to be instead of
// filing it with the retired names. Taken from a live host, where CUSTOM_BACKUP_PATHS
// had lost its leading C and kept two custom paths out of every run.
func TestAuditReportsNearMissAsPossibleTypo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.env")
	content := "USTOM_BACKUP_PATHS=\"\n/etc/nut/\n\"\nSOMETHING_ENTIRELY_ELSE=1\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	report, err := AuditConfigFile(path)
	if err != nil {
		t.Fatalf("AuditConfigFile: %v", err)
	}

	if len(report.NearMiss) != 1 {
		t.Fatalf("expected exactly one near miss, got %+v", report.NearMiss)
	}
	if got := report.NearMiss[0].Name; got != "USTOM_BACKUP_PATHS" {
		t.Fatalf("unexpected near-miss name %q", got)
	}
	if got := report.NearMiss[0].Suggestion; got != "CUSTOM_BACKUP_PATHS" {
		t.Fatalf("unexpected suggestion %q", got)
	}
	if !report.HasIssues() {
		t.Fatal("a typo leaves a line with no effect and has to count as an issue")
	}

	var unknownHasIt bool
	for _, name := range report.Unknown {
		if name == "USTOM_BACKUP_PATHS" {
			unknownHasIt = true
		}
	}
	if !unknownHasIt {
		t.Fatal("a near miss must stay classified as unknown; only the wording changes")
	}
}

func TestIsOneEditApart(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"USTOM_BACKUP_PATHS", "CUSTOM_BACKUP_PATHS", true},  // dropped first character
		{"CUSTOM_BACKUP_PATHSS", "CUSTOM_BACKUP_PATHS", true}, // one character too many
		{"CUSTOM_BACKUP_PATHZ", "CUSTOM_BACKUP_PATHS", true},  // one character changed
		{"CUSTOM_BACKUP_PATSH", "CUSTOM_BACKUP_PATHS", true},  // two adjacent swapped
		{"CUSTOM_BACKUP_PATHS", "CUSTOM_BACKUP_PATHS", false}, // identical is not a typo
		{"CUSTOM_BACKUP_PATH", "CUSTOM_BLACKLIST", false},     // a different name
		{"USTOM_BACKUP_PATH", "CUSTOM_BACKUP_PATHS", false},   // two edits away
		{"", "A", true},
		{"", "", false},
	} {
		if got := isOneEditApart(tc.a, tc.b); got != tc.want {
			t.Fatalf("isOneEditApart(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
