package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// renderIntegrityBlock drives the real renderer through the real bootstrap logger at
// DEBUG, so the assertions below see the block exactly as it lands in the run log.
func renderIntegrityBlock(t *testing.T, report *config.ConfigIntegrityReport) string {
	t.Helper()
	boot := logging.NewBootstrapLogger()
	boot.SetConsoleQuiet(true)
	buf := &bytes.Buffer{}
	mirror := logging.New(types.LogLevelDebug, false)
	mirror.SetOutput(buf)
	boot.SetMirrorLogger(mirror)
	renderConfigIntegrityReport(boot, report, 3100*time.Microsecond)
	return buf.String()
}

func integrityReportWithFindings() *config.ConfigIntegrityReport {
	return &config.ConfigIntegrityReport{
		Path:              "/opt/proxsave/configs/backup.env",
		Lines:             461,
		Assignments:       182,
		Distinct:          181,
		TemplateVariables: 182,
		SkippedMultiValue: []string{"AGE_RECIPIENT", "BACKUP_BLACKLIST", "BACKUP_EXCLUDE_PATTERNS", "CUSTOM_BACKUP_PATHS"},
		Duplicated:        []config.DuplicatedVariable{{Name: "PERSONAL_SCRIPT_PRE_RUN", Lines: []int{120, 455}, WinningLine: 455, Discarded: []int{120}}},
		Absent:            []string{"HEALTHCHECK_UPDATES_ID", "SCHEDULER_TIME"},
		Unknown:           []string{"PERSONAL_SCRIPTS_PRERUN"},
	}
}

// The levels ARE the design: a discarded value and a variable the merge never added
// are WARNINGs because something the operator wrote is not in effect, while a variable
// the binary does not read is INFO because nothing was lost by the loader.
func TestIntegrityFindingsCarryTheAgreedLevels(t *testing.T) {
	logged := renderIntegrityBlock(t, integrityReportWithFindings())
	for _, want := range []string{
		"WARNING    PERSONAL_SCRIPT_PRE_RUN is set twice; line 455 wins and the value on line 120 is discarded",
		"WARNING    HEALTHCHECK_UPDATES_ID is absent and falls back to its default",
		"WARNING    SCHEDULER_TIME is absent and falls back to its default",
		"INFO       PERSONAL_SCRIPTS_PRERUN is not a known variable and is ignored",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("missing line %q in:\n%s", want, logged)
		}
	}
}

// The raw counters exist so the operator can reconstruct the verdict, and they are
// emitted BEFORE it: the audit knows the numbers before it renders them.
func TestIntegrityDebugCountsPrecedeTheVerdict(t *testing.T) {
	logged := renderIntegrityBlock(t, integrityReportWithFindings())
	counts := strings.Index(logged, "Configuration integrity: 1 duplicated, 2 absent, 1 unknown, 0 legacy (duration=")
	verdict := strings.Index(logged, "⚠ Configuration file: 1 duplicated, 2 absent, 1 unknown")
	if counts < 0 || verdict < 0 {
		t.Fatalf("expected both the DEBUG counts and the verdict:\n%s", logged)
	}
	if counts > verdict {
		t.Fatalf("the DEBUG counts must precede the verdict:\n%s", logged)
	}
	if !strings.Contains(logged, "WARNING  ⚠ Configuration file:") {
		t.Fatalf("the verdict must be a WARNING when a value is discarded or missing:\n%s", logged)
	}
}

// Every DEBUG detail line carries the subsystem prefix, like the other DEBUG blocks in
// the run log: at DEBUG the subsystems interleave and a bare variable name has no owner.
func TestIntegrityDebugLinesAllNameTheirSubsystem(t *testing.T) {
	logged := renderIntegrityBlock(t, integrityReportWithFindings())
	for _, line := range strings.Split(logged, "\n") {
		if !strings.Contains(line, "DEBUG") {
			continue
		}
		message := line[strings.Index(line, "DEBUG")+len("DEBUG"):]
		if !strings.Contains(message, "Configuration integrity:") {
			t.Fatalf("DEBUG line without the subsystem prefix: %q", line)
		}
	}
}

// A run whose configuration is intact must not spend a WARNING on saying so, or the
// block would promote every clean backup to a non-zero exit.
func TestCleanConfigurationKeepsTheVerdictAtInfo(t *testing.T) {
	logged := renderIntegrityBlock(t, &config.ConfigIntegrityReport{
		Path: "/opt/proxsave/configs/backup.env", Lines: 461, Assignments: 182, Distinct: 182, TemplateVariables: 182,
		SkippedMultiValue: []string{"AGE_RECIPIENT"},
	})
	if !strings.Contains(logged, "INFO     ✓ Configuration file ok") {
		t.Fatalf("expected the clean verdict at INFO:\n%s", logged)
	}
	if strings.Contains(logged, "WARNING") {
		t.Fatalf("a clean configuration must emit no WARNING at all:\n%s", logged)
	}
}

// An unknown variable is reported but is NOT an issue: nothing the operator wrote is
// being discarded, so the verdict stays green and the run's exit code is untouched.
func TestAnUnknownVariableAloneDoesNotTurnTheVerdictRed(t *testing.T) {
	logged := renderIntegrityBlock(t, &config.ConfigIntegrityReport{
		Path: "/opt/proxsave/configs/backup.env", Lines: 462, Assignments: 183, Distinct: 183, TemplateVariables: 182,
		Unknown: []string{"PERSONAL_SCRIPTS_PRERUN"},
	})
	if !strings.Contains(logged, "INFO     ✓ Configuration file ok (1 unknown)") {
		t.Fatalf("expected a green verdict naming the unknown count:\n%s", logged)
	}
	if strings.Contains(logged, "WARNING") {
		t.Fatalf("an unknown variable must not raise a WARNING:\n%s", logged)
	}
}

// More than two assignments must not read as "set twice".
func TestThreeAssignmentsNameEveryDiscardedLine(t *testing.T) {
	logged := renderIntegrityBlock(t, &config.ConfigIntegrityReport{
		Path: "/opt/proxsave/configs/backup.env",
		Duplicated: []config.DuplicatedVariable{
			{Name: "PERSONAL_SCRIPT_PRE_RUN", Lines: []int{10, 20, 30}, WinningLine: 30, Discarded: []int{10, 20}},
		},
	})
	want := "PERSONAL_SCRIPT_PRE_RUN is set 3 times; line 30 wins and the values on lines 10, 20 are discarded"
	if !strings.Contains(logged, want) {
		t.Fatalf("expected %q in:\n%s", want, logged)
	}
}

// A duplicated secret must never put its value in the log.
func TestTheBlockNeverPrintsAValue(t *testing.T) {
	logged := renderIntegrityBlock(t, &config.ConfigIntegrityReport{
		Path: "/opt/proxsave/configs/backup.env",
		Duplicated: []config.DuplicatedVariable{
			{Name: "TELEGRAM_BOT_TOKEN", Lines: []int{40, 50}, WinningLine: 50, Discarded: []int{40}},
		},
	})
	if !strings.Contains(logged, "TELEGRAM_BOT_TOKEN is set twice; line 50 wins and the value on line 40 is discarded") {
		t.Fatalf("expected the finding without its value:\n%s", logged)
	}
}

// The block is worth nothing if it is not wired into the run, and no unit test of the
// renderer can see that. Its position is the design: BEFORE printDryRunBootstrapStatus,
// which closes the configuration section with a blank line, so the findings sit with the
// file they describe and ahead of the effective-settings recap.
func TestTheIntegrityBlockIsWiredAheadOfTheConfigurationSectionEnd(t *testing.T) {
	source, err := os.ReadFile("main_runtime.go")
	if err != nil {
		t.Fatalf("read main_runtime.go: %v", err)
	}
	body := string(source)
	start := strings.Index(body, "func validateRunConfig(")
	if start < 0 {
		t.Fatalf("validateRunConfig not found")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("end of validateRunConfig not found")
	}
	fn := body[start : start+end]
	audit := strings.Index(fn, "auditRunConfigFile(rt)")
	dryRun := strings.Index(fn, "printDryRunBootstrapStatus(rt)")
	if audit < 0 {
		t.Fatalf("validateRunConfig no longer runs the integrity block:\n%s", fn)
	}
	if dryRun < 0 || audit > dryRun {
		t.Fatalf("the integrity block must run before printDryRunBootstrapStatus:\n%s", fn)
	}
}

// The list names the variables whose repetition CAN be legitimate, not the ones this
// run left alone. The two forms differ: the single-line form of CUSTOM_BACKUP_PATHS
// concatenates, its block form replaces, so the same variable is listed here AND
// reported as duplicated two lines below. Calling the list "skipped" makes the block
// contradict its own finding.
func TestTheRepeatableListDoesNotClaimAReportedVariableWasSkipped(t *testing.T) {
	logged := renderIntegrityBlock(t, &config.ConfigIntegrityReport{
		Path: "/opt/proxsave/configs/backup.env", Lines: 462, Assignments: 183, Distinct: 182, TemplateVariables: 182,
		SkippedMultiValue: []string{"AGE_RECIPIENT", "BACKUP_BLACKLIST", "BACKUP_EXCLUDE_PATTERNS", "CUSTOM_BACKUP_PATHS"},
		Duplicated:        []config.DuplicatedVariable{{Name: "CUSTOM_BACKUP_PATHS", Lines: []int{395, 396}, WinningLine: 396, Discarded: []int{395}}},
	})
	if strings.Contains(logged, "skipped") {
		t.Fatalf("the block calls a variable skipped and then reports it:\n%s", logged)
	}
	want := "variables that may repeat without discarding: AGE_RECIPIENT, BACKUP_BLACKLIST, BACKUP_EXCLUDE_PATTERNS, CUSTOM_BACKUP_PATHS"
	if !strings.Contains(logged, want) {
		t.Fatalf("missing %q in:\n%s", want, logged)
	}
}

// A block does not have to be the LAST assignment of its variable. With a single
// line after it the loader concatenates onto the block, so the block still discards
// what came before it while that later line loses nothing. Deriving the discarded
// lines as "everything but the last" then names the winner among the discarded and
// the debug line credits the wrong assignment.
func TestABlockThatIsNotTheLastAssignmentNamesOnlyWhatItDiscards(t *testing.T) {
	path := t.TempDir() + "/backup.env"
	body := "CUSTOM_BACKUP_PATHS=/srv/important\nCUSTOM_BACKUP_PATHS=\"\n/etc/a\n\"\nCUSTOM_BACKUP_PATHS=/srv/other\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	report, err := config.AuditConfigFile(path)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	logged := renderIntegrityBlock(t, report)

	want := "CUSTOM_BACKUP_PATHS is set 3 times; line 2 wins and the value on line 1 is discarded"
	if !strings.Contains(logged, want) {
		t.Fatalf("missing %q in:\n%s", want, logged)
	}
	if strings.Contains(logged, "last assignment wins") {
		t.Fatalf("the debug line credits the last assignment, but line 2 is the winner:\n%s", logged)
	}
}

// A finding is readable without the code only if the block says WHY the audit decided
// what it decided. Three of those decisions are invisible today: why a variable that
// the template never assigns was accepted anyway, why one that it never assigns was
// NOT accepted, and which of the two write forms won a duplicate. All three are the
// ones that were wrong before, so they are the ones an operator will be checking.
func TestTheDebugBlockExplainsEveryDecisionItMade(t *testing.T) {
	body := config.DefaultEnvTemplate() +
		"WEBHOOK_ENDPOINTS=mine\n" +
		"WEBHOOK_MINE_URL=https://example.invalid/hook\n" +
		"SAFE_PROCESSES=\"ffmpeg\"\n" +
		"EMAIL_ENABLE=true\n" +
		"PERSONAL_SCRIPTS_PRERUN=/tmp/x\n" +
		"CUSTOM_BACKUP_PATHS=\"\n/etc/a\n\"\n"
	path := t.TempDir() + "/backup.env"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	report, err := config.AuditConfigFile(path)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	logged := renderIntegrityBlock(t, report)

	for _, want := range []string{
		// why each variable outside the template's active assignments was accepted
		"SAFE_PROCESSES is read although the template does not assign it: documented there as a commented example",
		"WEBHOOK_MINE_URL is read although the template does not assign it: per-endpoint webhook variable, WEBHOOK_<name>_<field>",
		// A legacy alias is no longer explained as "read although the template does not
		// assign it": it has its own category, and its own line says which of the two
		// names the loader consults first.
		"EMAIL_ENABLE is a legacy alias for EMAIL_ENABLED; the loader consults EMAIL_ENABLED first, canonical also assigned=true",
		// why the one that was NOT accepted failed every rule
		"PERSONAL_SCRIPTS_PRERUN is assigned in the file and matched no rule: not assigned in the template, not documented there, not a webhook endpoint field, not a legacy alias",
		// which write form won, since the two resolve in opposite ways
		"CUSTOM_BACKUP_PATHS assigned on lines 409, 481; line 481 wins (block form: it replaces everything before it)",
		// the template's own two halves, so the counts above can be reconstructed
		"template assigns 182 variables and documents",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("missing %q in:\n%s", want, logged)
		}
	}
}

// The legacy category has two levels because it describes two different facts. Both
// names set means one of them has no effect at all, which is the same harm as a
// duplicate and earns a WARNING. The legacy name alone works, so it is an INFO that
// names the canonical one to move to.
func TestALegacyAliasIsWarnedAboutOnlyWhenItSilencesTheCanonicalName(t *testing.T) {
	logged := renderIntegrityBlock(t, &config.ConfigIntegrityReport{
		Path: "/opt/proxsave/configs/backup.env",
		Legacy: []config.LegacyVariable{
			{Name: "LOCAL_BACKUP_PATH", Canonical: "BACKUP_PATH", Wins: true, CanonicalAlsoSet: true},
			{Name: "EMAIL_ENABLE", Canonical: "EMAIL_ENABLED", CanonicalAlsoSet: true},
			{Name: "RCLONE_REMOTE", Canonical: "CLOUD_REMOTE", Wins: true},
		},
	})
	for _, want := range []string{
		"WARNING    LOCAL_BACKUP_PATH is the legacy name for BACKUP_PATH and both are set; LOCAL_BACKUP_PATH wins and BACKUP_PATH has no effect",
		"WARNING    EMAIL_ENABLE is the legacy name for EMAIL_ENABLED and both are set; EMAIL_ENABLED wins and EMAIL_ENABLE has no effect",
		"INFO       RCLONE_REMOTE is the legacy name for CLOUD_REMOTE and is still read; rename it to CLOUD_REMOTE when convenient",
		"WARNING  ⚠ Configuration file: 3 legacy",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("missing %q in:\n%s", want, logged)
		}
	}
}

// A legacy name that works on its own must not turn the verdict red: nothing the
// operator wrote is being ignored, so it is the same shape as an unknown variable.
func TestALegacyAliasAloneKeepsTheVerdictGreen(t *testing.T) {
	logged := renderIntegrityBlock(t, &config.ConfigIntegrityReport{
		Path:   "/opt/proxsave/configs/backup.env",
		Legacy: []config.LegacyVariable{{Name: "RCLONE_REMOTE", Canonical: "CLOUD_REMOTE", Wins: true}},
	})
	if !strings.Contains(logged, "INFO     ✓ Configuration file ok (1 legacy)") {
		t.Fatalf("expected a green verdict naming the legacy count:\n%s", logged)
	}
	if strings.Contains(logged, "WARNING") {
		t.Fatalf("a legacy name that is the only one set must not raise a WARNING:\n%s", logged)
	}
}
