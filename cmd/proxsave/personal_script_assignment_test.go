package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func auditFixture(t *testing.T, body string) *config.ConfigIntegrityReport {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	report, err := config.AuditConfigFile(path)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	return report
}

func annotatedCurrentSide(t *testing.T, body string) personalScriptsDiagnostics {
	t.Helper()
	report := auditFixture(t, body)
	scripts := personalScriptsDiagnostics{
		Pre:  personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_PRE_RUN", State: personalScriptNotConfigured},
		Post: personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_POST_RUN", State: personalScriptNotConfigured},
	}
	annotatePersonalScriptAssignment(&scripts.Pre, report)
	annotatePersonalScriptAssignment(&scripts.Post, report)
	return scripts
}

// NOT CONFIGURED alone cannot tell three different files apart, and the difference is
// exactly what issue #306 turned on: a variable missing from the file, one assigned
// empty, and one whose value a later line overwrites.
func TestNotConfiguredSaysWhatTheFileActuallyContains(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"not assigned at all": {
			"EMAIL_ENABLED=true\n",
			"PERSONAL_SCRIPT_PRE_RUN is not in the file",
		},
		"assigned once with an empty value": {
			"EMAIL_ENABLED=true\nPERSONAL_SCRIPT_PRE_RUN=\n",
			"PERSONAL_SCRIPT_PRE_RUN is on line 2 & is empty",
		},
		"operator line overwritten by a later empty one": {
			"PERSONAL_SCRIPT_PRE_RUN=/home/howard/dd/mount-pbs\nEMAIL_ENABLED=true\nPERSONAL_SCRIPT_PRE_RUN=\n",
			"PERSONAL_SCRIPT_PRE_RUN is on lines 1 and 3 & line 3 wins and is empty",
		},
		"three assignments read as a sentence": {
			"PERSONAL_SCRIPT_PRE_RUN=/a\nPERSONAL_SCRIPT_PRE_RUN=/b\nPERSONAL_SCRIPT_PRE_RUN=\n",
			"PERSONAL_SCRIPT_PRE_RUN is on lines 1, 2 and 3 & line 3 wins and is empty",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := annotatedCurrentSide(t, tc.body).Pre.Assignment; got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Both operator scripts are explained, not just the pre-run one.
func TestBothPersonalScriptsGetTheirOwnExplanation(t *testing.T) {
	scripts := annotatedCurrentSide(t, "PERSONAL_SCRIPT_POST_RUN=\n")
	if scripts.Pre.Assignment != "PERSONAL_SCRIPT_PRE_RUN is not in the file" {
		t.Fatalf("pre-run: %q", scripts.Pre.Assignment)
	}
	if scripts.Post.Assignment != "PERSONAL_SCRIPT_POST_RUN is on line 1 & is empty" {
		t.Fatalf("post-run: %q", scripts.Post.Assignment)
	}
}

// The note describes the file as it is RIGHT NOW, so it cannot be attached to a verdict
// the daemon reached from its own copy when it started.
func TestOnlyTheCurrentSideIsAnnotated(t *testing.T) {
	report := auditFixture(t, "PERSONAL_SCRIPT_PRE_RUN=\n")
	current := personalScriptsDiagnostics{
		Pre:  personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_PRE_RUN", State: personalScriptNotConfigured},
		Post: personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_POST_RUN", State: personalScriptNotConfigured},
	}
	annotatePersonalScriptAssignments(&current, report.Path)
	if current.Pre.Assignment == "" {
		t.Fatalf("the current side must be annotated")
	}

	running := personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_PRE_RUN", State: personalScriptNotConfigured}
	comparison := comparePersonalScript(
		daemonRuntimeDiagnostic{Availability: daemonRuntimeAvailable, ConfigPath: report.Path},
		report.Path, running, current.Pre,
	)
	if comparison.Running.Assignment != "" {
		t.Fatalf("the running side must carry no file note, got %q", comparison.Running.Assignment)
	}
}

// The regression this design exists to avoid: comparePersonalScript compares Reason
// between the two sides, so putting the note there would make every explained
// NOT CONFIGURED report PATH STATE CHANGED, a warning about a daemon that is fine.
func TestExplainingTheVerdictDoesNotBreakSynchronization(t *testing.T) {
	report := auditFixture(t, "PERSONAL_SCRIPT_PRE_RUN=\n")
	current := personalScriptsDiagnostics{
		Pre:  personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_PRE_RUN", State: personalScriptNotConfigured},
		Post: personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_POST_RUN", State: personalScriptNotConfigured},
	}
	annotatePersonalScriptAssignments(&current, report.Path)

	comparison := comparePersonalScript(
		daemonRuntimeDiagnostic{Availability: daemonRuntimeAvailable, ConfigPath: report.Path},
		report.Path,
		personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_PRE_RUN", State: personalScriptNotConfigured},
		current.Pre,
	)
	if comparison.Synchronization != personalScriptInSync {
		t.Fatalf("two sides that agree must stay IN SYNC, got %q (%s)",
			comparison.Synchronization, comparison.SyncReason)
	}
}

// A configured script needs no explanation, and a verdict the file contradicts gets
// none either rather than arguing with itself.
func TestOnlyANotConfiguredVerdictIsExplained(t *testing.T) {
	report := auditFixture(t, "PERSONAL_SCRIPT_PRE_RUN=/opt/ops/pre.sh\n")
	ready := personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_PRE_RUN", State: personalScriptReady, Path: "/opt/ops/pre.sh"}
	annotatePersonalScriptAssignment(&ready, report)
	if ready.Assignment != "" {
		t.Fatalf("a READY verdict must carry no note, got %q", ready.Assignment)
	}
	contradicted := personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_PRE_RUN", State: personalScriptNotConfigured}
	annotatePersonalScriptAssignment(&contradicted, report)
	if contradicted.Assignment != "" {
		t.Fatalf("a non-empty assignment must not be described as the reason for NOT CONFIGURED, got %q", contradicted.Assignment)
	}

	// The state guard, exercised where it actually bites: an empty variable whose
	// verdict is NOT not-configured. Explaining a REFUSED verdict with "the variable is
	// empty" would contradict the refusal it sits next to.
	emptyInFile := auditFixture(t, "PERSONAL_SCRIPT_PRE_RUN=\n")
	refused := personalScriptDiagnostic{
		Key: "PERSONAL_SCRIPT_PRE_RUN", State: personalScriptRefused,
		Path: "/opt/ops/pre.sh", Reason: "path is not owned by root",
	}
	annotatePersonalScriptAssignment(&refused, emptyInFile)
	if refused.Assignment != "" {
		t.Fatalf("only a NOT CONFIGURED verdict may carry the file note, got %q", refused.Assignment)
	}
}

// The note describes the file read NOW, so the collector must hand it the current side
// and nothing else. No unit test of the annotator can see which side it was given.
func TestTheCollectorAnnotatesTheCurrentSideOnly(t *testing.T) {
	source, err := os.ReadFile("daemon_diagnostics.go")
	if err != nil {
		t.Fatalf("read daemon_diagnostics.go: %v", err)
	}
	body := string(source)
	if got := strings.Count(body, "annotatePersonalScriptAssignments("); got != 1 {
		t.Fatalf("expected exactly one annotation call, got %d", got)
	}
	if !strings.Contains(body, "annotatePersonalScriptAssignments(&currentScripts, currentConfigPath)") {
		t.Fatalf("the annotation must be applied to the current side only")
	}
	if strings.Contains(body, "annotatePersonalScriptAssignments(&runningScripts") {
		t.Fatalf("the running side must never be annotated from today's file")
	}
}

// The note has to reach the operator through both renderers, or only half the surfaces
// answer the question the verdict raises.
func TestBothRenderersShowTheExplanation(t *testing.T) {
	diagnostic := personalScriptDiagnostic{
		Key:        "PERSONAL_SCRIPT_PRE_RUN",
		State:      personalScriptNotConfigured,
		Assignment: "PERSONAL_SCRIPT_PRE_RUN is on lines 120 and 456 & line 456 wins and is empty",
	}
	want := "NOT CONFIGURED (PERSONAL_SCRIPT_PRE_RUN is on lines 120 and 456 & line 456 wins and is empty)"

	if got := stripStyling(buildDashboardPersonalScriptLine("  Configuration", diagnostic)); !strings.Contains(got, want) {
		t.Fatalf("dashboard line missing the explanation: %q", got)
	}
	if got := captureDaemonDiagnosticLine(t, diagnostic); !strings.Contains(got, want) {
		t.Fatalf("CLI line missing the explanation: %q", got)
	}
}

func captureDaemonDiagnosticLine(t *testing.T, diagnostic personalScriptDiagnostic) string {
	t.Helper()
	buf := &bytes.Buffer{}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(buf)
	logPersonalScriptDiagnostic(logger, "  Configuration", diagnostic)
	return buf.String()
}

// stripStyling removes the terminal colour codes the dashboard renderer emits, so the
// assertion reads the sentence the operator reads rather than the escape sequences
// interleaved with it.
func stripStyling(line string) string {
	return ansiEscape.ReplaceAllString(line, "")
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// The advisory is the mitigation the accepted foreign-owned ancestor RESTS ON, and
// inspectPersonalScript's own comment says it is reported with the ancestor, never
// separately. The CLI does that; the dashboard read Path and Reason and dropped it,
// so the one screen an operator is most likely to be looking at showed the trust
// decision without what stands behind it.
func TestBothRenderersShowTheHardlinkAdvisory(t *testing.T) {
	diagnostic := personalScriptDiagnostic{
		Key:              "PERSONAL_SCRIPT_PRE_RUN",
		State:            personalScriptReadyWithWarning,
		Path:             "/home/me/dd/hook.sh",
		Reason:           "/home/me, /home/me/dd: UID 1000-owned; owner can replace descendants run as UID 0",
		HardlinkAdvisory: "fs.protected_hardlinks=0 allows hard-linking root-owned executables; set it to 1",
	}
	want := "fs.protected_hardlinks=0 allows hard-linking root-owned executables; set it to 1"

	if got := stripStyling(buildDashboardPersonalScriptLine("  Configuration", diagnostic)); !strings.Contains(got, want) {
		t.Fatalf("dashboard line missing the advisory: %q", got)
	}
	if got := captureDaemonDiagnosticLine(t, diagnostic); !strings.Contains(got, want) {
		t.Fatalf("CLI line missing the advisory: %q", got)
	}
}

// The advisory carries two opposite messages and used to be logged at WARNING for
// both. "fs.protected_hardlinks=1 blocks hard-linking root-owned executables" says
// the protection IS in force: raising it as a warning tells the operator to act on
// something that is already right, in the middle of a block where the other WARNING
// lines mean the opposite.
func TestTheAdvisoryLevelFollowsWhetherTheProtectionIsInForce(t *testing.T) {
	base := personalScriptDiagnostic{
		Key:    "PERSONAL_SCRIPT_PRE_RUN",
		State:  personalScriptReadyWithWarning,
		Path:   "/home/me/dd/hook.sh",
		Reason: "/home/me, /home/me/dd: UID 1000-owned; owner can replace descendants run as UID 0",
	}
	cases := map[string]struct {
		advisory  string
		inForce   bool
		wantLevel string
	}{
		"disabled": {
			advisory:  "fs.protected_hardlinks=0 allows hard-linking root-owned executables; set it to 1",
			wantLevel: "WARNING",
		},
		"unreadable": {
			advisory:  "fs.protected_hardlinks unreadable: permission denied",
			wantLevel: "WARNING",
		},
		"enforced": {
			advisory:  "fs.protected_hardlinks=1 blocks hard-linking root-owned executables",
			inForce:   true,
			wantLevel: "INFO",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			diagnostic := base
			diagnostic.HardlinkAdvisory = tc.advisory
			diagnostic.HardlinkProtectionInForce = tc.inForce

			var advisoryLine string
			for line := range strings.SplitSeq(captureDaemonDiagnosticLine(t, diagnostic), "\n") {
				if strings.Contains(line, "fs.protected_hardlinks") {
					advisoryLine = line
				}
			}
			if advisoryLine == "" {
				t.Fatal("the advisory never reached the log")
			}
			if !strings.Contains(advisoryLine, tc.wantLevel) {
				t.Fatalf("advisory logged at the wrong level, want %s: %q", tc.wantLevel, advisoryLine)
			}
		})
	}
}

// The text and the flag are two halves of one reading and must never disagree: a
// renderer that trusts the flag while the text says the opposite is worse than
// either being wrong alone.
func TestTheAdvisoryTextAndItsFlagAgree(t *testing.T) {
	orig := personalScriptHardlinkProtection
	t.Cleanup(func() { personalScriptHardlinkProtection = orig })

	cases := []struct {
		name    string
		value   int
		err     error
		inForce bool
	}{
		{"disabled", 0, nil, false},
		{"enforced", 1, nil, true},
		{"unexpected value", 2, nil, true},
		{"unreadable", 0, errors.New("permission denied"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			personalScriptHardlinkProtection = func() (int, error) { return tc.value, tc.err }
			text, inForce := personalScriptHardlinkAdvisory()
			if inForce != tc.inForce {
				t.Fatalf("inForce = %v, want %v for %q", inForce, tc.inForce, text)
			}
			saysBlocks := strings.Contains(text, "blocks hard-linking")
			if saysBlocks != tc.inForce {
				t.Fatalf("the text and the flag disagree: inForce=%v text=%q", inForce, text)
			}
		})
	}
}
