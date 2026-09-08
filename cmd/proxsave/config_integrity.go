package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

// integrityFindingIndent aligns a finding under the block title. The verdict is
// deliberately NOT indented: it is the conclusion of the block, not one more
// finding inside it.
const integrityFindingIndent = "  "

var configIntegrityAuditor = config.AuditConfigFile

// auditRunConfigFile is the "Configuration integrity check" block, run once per run
// immediately after the configuration is loaded and BEFORE the effective settings are
// printed. The order is the point: a discarded value has to be reported before the
// recap that would otherwise show the surviving one as if nothing were lost.
//
// Levels carry the difference between the four findings, which are not the same fact:
//   - duplicated: a value the operator wrote is discarded by the loader's last-wins
//     rule, so it is a WARNING;
//   - absent: the binary carries the variable in its embedded template but the file
//     does not, which can only mean the configuration merge never ran, so it is a
//     WARNING too. An operator who has NOT upgraded runs an older binary with an older
//     template and sees nothing here;
//   - unknown: a variable the binary does not read. Nothing the operator wrote is lost
//     by the loader, it was never picked up in the first place, so it is INFO;
//   - legacy: an old name the loader still honours. WARNING when the canonical name is
//     assigned too, because then one of the two lines has no effect at all; INFO when
//     the legacy name is the only one, since it works and only wants renaming.
//
// The raw counters go to DEBUG BEFORE the operator-facing lines, because the audit
// knows the numbers before it renders them.
func auditRunConfigFile(rt *appRuntime) {
	if rt == nil || rt.bootstrap == nil {
		return
	}
	path := ""
	if rt.cfg != nil {
		path = strings.TrimSpace(rt.cfg.ConfigPath)
	}
	if path == "" && rt.args != nil {
		path = strings.TrimSpace(rt.args.ConfigPath)
	}
	if path == "" {
		return
	}

	started := time.Now()
	report, err := configIntegrityAuditor(path)
	if err != nil {
		// A file the loader has just read successfully cannot normally fail here, so
		// say so rather than staying silent: a silent block reads as "all clear".
		rt.bootstrap.Warning("Configuration integrity check could not run: %v", err)
		return
	}
	renderConfigIntegrityReport(rt.bootstrap, report, time.Since(started))
}

func renderConfigIntegrityReport(bootstrap *logging.BootstrapLogger, report *config.ConfigIntegrityReport, elapsed time.Duration) {
	if bootstrap == nil || report == nil {
		return
	}
	bootstrap.Info("Configuration integrity check:")
	bootstrap.Debug("Configuration integrity: file=%s lines=%d assignments=%d distinct=%d template=%d",
		report.Path, report.Lines, report.Assignments, report.Distinct, report.TemplateVariables)
	// Both halves of what counts as known, so the verdict can be reconstructed from the
	// block instead of from the source: the template ASSIGNS some variables and only
	// DOCUMENTS others on a commented line, and the audit reads both.
	bootstrap.Debug("Configuration integrity: template assigns %d variables and documents %d more as commented examples",
		report.TemplateVariables, report.TemplateDocumented)
	if len(report.SkippedMultiValue) > 0 {
		// These are the variables whose repetition CAN be legitimate, not the ones
		// this run left alone: the block form replaces instead of concatenating, so
		// one of them can be listed here and reported as duplicated below.
		bootstrap.Debug("Configuration integrity: variables that may repeat without discarding: %s",
			strings.Join(report.SkippedMultiValue, ", "))
	}
	for _, duplicated := range report.Duplicated {
		// Which FORM won matters more than the line number: an ordinary assignment is
		// last-wins, a block replaces everything before it and lets a later single line
		// concatenate onto it. The two resolve in opposite ways, so naming the line
		// without naming the form leaves the reader to guess which rule applied.
		form := "last assignment wins"
		if duplicated.ByBlock {
			form = "block form: it replaces everything before it"
		}
		bootstrap.Debug("Configuration integrity: %s assigned on lines %s; line %d wins (%s)",
			duplicated.Name, joinLineNumbers(duplicated.Lines), duplicated.WinningLine, form)
	}
	for _, known := range report.KnownOutsideTemplate {
		bootstrap.Debug("Configuration integrity: %s is read although the template does not assign it: %s",
			known.Name, known.Rule)
	}
	for _, name := range report.Absent {
		bootstrap.Debug("Configuration integrity: %s present in embedded template, absent from file", name)
	}
	for _, name := range report.Unknown {
		// Name every gate that was tried rather than only the outcome: "absent from the
		// template" was the OLD rule and, on its own, is exactly what made this category
		// report working configuration as ignored.
		bootstrap.Debug("Configuration integrity: %s is assigned in the file and matched no rule: %s",
			name, config.UnknownRuleTrace())
	}
	for _, legacy := range report.Legacy {
		consulted := legacy.Canonical
		if legacy.Wins {
			consulted = legacy.Name
		}
		bootstrap.Debug("Configuration integrity: %s is a legacy alias for %s; the loader consults %s first, canonical also assigned=%v",
			legacy.Name, legacy.Canonical, consulted, legacy.CanonicalAlsoSet)
	}

	for _, duplicated := range report.Duplicated {
		bootstrap.Warning("%s%s", integrityFindingIndent, duplicatedVariableSentence(duplicated))
	}
	for _, name := range report.Absent {
		bootstrap.Warning("%s%s is absent and falls back to its default", integrityFindingIndent, name)
	}
	nearMiss := make(map[string]string, len(report.NearMiss))
	for _, candidate := range report.NearMiss {
		nearMiss[candidate.Name] = candidate.Suggestion
	}
	for _, name := range report.Unknown {
		// A name one character away from a real variable is a typo, not a retired
		// setting, and the two have opposite consequences: the retired one changes
		// nothing, the typo silently disables what the operator asked for. Naming the
		// variable it was probably meant to be is what turns the line into a fix.
		if suggestion, ok := nearMiss[name]; ok {
			bootstrap.Warning("%s%s is not a known variable and is ignored; did you mean %s?",
				integrityFindingIndent, name, suggestion)
			continue
		}
		bootstrap.Info("%s%s is not a known variable and is ignored", integrityFindingIndent, name)
	}
	for _, legacy := range report.Legacy {
		// Two facts, two levels. Both names set means one of the two lines has no effect
		// at all, which is the duplicate's harm under another shape and earns a WARNING.
		// The legacy name on its own works, so it is an INFO naming where to move to.
		if !legacy.CanonicalAlsoSet {
			bootstrap.Info("%s%s is the legacy name for %s and is still read; rename it to %s when convenient",
				integrityFindingIndent, legacy.Name, legacy.Canonical, legacy.Canonical)
			continue
		}
		winner, ignored := legacy.Canonical, legacy.Name
		if legacy.Wins {
			winner, ignored = legacy.Name, legacy.Canonical
		}
		bootstrap.Warning("%s%s is the legacy name for %s and both are set; %s wins and %s has no effect",
			integrityFindingIndent, legacy.Name, legacy.Canonical, winner, ignored)
	}

	bootstrap.Debug("Configuration integrity: %d duplicated, %d absent, %d unknown, %d legacy (duration=%s)",
		len(report.Duplicated), len(report.Absent), len(report.Unknown), len(report.Legacy),
		elapsed.Round(100*time.Microsecond))
	if report.HasIssues() {
		bootstrap.Warning("⚠ Configuration file: %s", integrityVerdictCounts(report))
		return
	}
	if report.Clean() {
		bootstrap.Info("✓ Configuration file ok")
		return
	}
	// Green, but not silent: an unknown variable is reported without pretending the
	// file is untouched, because the operator wrote a line the binary never reads.
	bootstrap.Info("✓ Configuration file ok (%s)", integrityVerdictCounts(report))
}

// duplicatedVariableSentence names the winner and every discarded line, never the
// values: a duplicated TELEGRAM_BOT_TOKEN would otherwise print a secret.
func duplicatedVariableSentence(duplicated config.DuplicatedVariable) string {
	// How many times it is SET and how many values are LOST are two different
	// counts: a line that follows a block concatenates onto it, so it is one more
	// assignment and one fewer discarded value.
	times := "twice"
	if len(duplicated.Lines) > 2 {
		times = fmt.Sprintf("%d times", len(duplicated.Lines))
	}
	if len(duplicated.Discarded) == 1 {
		return fmt.Sprintf("%s is set %s; line %d wins and the value on line %d is discarded",
			duplicated.Name, times, duplicated.WinningLine, duplicated.Discarded[0])
	}
	return fmt.Sprintf("%s is set %s; line %d wins and the values on lines %s are discarded",
		duplicated.Name, times, duplicated.WinningLine, joinLineNumbers(duplicated.Discarded))
}

// integrityVerdictCounts lists only the categories that actually fired, so the verdict
// never spends a word on a category that found nothing.
func integrityVerdictCounts(report *config.ConfigIntegrityReport) string {
	parts := make([]string, 0, 4)
	if n := len(report.Duplicated); n > 0 {
		parts = append(parts, fmt.Sprintf("%d duplicated", n))
	}
	if n := len(report.Absent); n > 0 {
		parts = append(parts, fmt.Sprintf("%d absent", n))
	}
	if n := len(report.Unknown); n > 0 {
		parts = append(parts, fmt.Sprintf("%d unknown", n))
	}
	// Counted separately although every near miss is also counted as unknown: the
	// summary line is what an operator scanning a log stops on, and "7 unknown" alone
	// reads as housekeeping, while a possible typo is a setting that is not applied.
	if n := len(report.NearMiss); n > 0 {
		parts = append(parts, fmt.Sprintf("%d possible typo", n))
	}
	if n := len(report.Legacy); n > 0 {
		parts = append(parts, fmt.Sprintf("%d legacy", n))
	}
	if len(parts) == 0 {
		return "no duplicated, absent, unknown or legacy variable"
	}
	return strings.Join(parts, ", ")
}

func joinLineNumbers(lines []int) string {
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		parts = append(parts, strconv.Itoa(line))
	}
	return strings.Join(parts, ", ")
}

var personalScriptAuditor = config.AuditConfigFile

// annotatePersonalScriptAssignments explains a NOT CONFIGURED verdict with what the
// configuration file actually says about the variable, which is the one thing the
// verdict alone cannot tell apart: a variable that is not in the file at all, one
// assigned with an empty value, and one whose value is overwritten by a later line.
//
// Issue #306 is the third case seen from the outside: two of the reporter's machines
// showed NOT CONFIGURED on both sides while he was looking at a file that carries the
// path, with nothing anywhere to close the gap.
//
// Only the CURRENT side is annotated. The running daemon read its own copy of the file
// when it started, so describing today's file as if it explained the daemon's verdict
// would be a guess dressed as evidence.
func annotatePersonalScriptAssignments(scripts *personalScriptsDiagnostics, configPath string) {
	if scripts == nil || strings.TrimSpace(configPath) == "" {
		return
	}
	if scripts.Pre.State != personalScriptNotConfigured && scripts.Post.State != personalScriptNotConfigured {
		return
	}
	report, err := personalScriptAuditor(configPath)
	if err != nil {
		// The verdict stands on its own; it just stays unexplained. Reporting the read
		// failure here would put a second, louder error on a screen whose subject is
		// the script, not the file.
		return
	}
	annotatePersonalScriptAssignment(&scripts.Pre, report)
	annotatePersonalScriptAssignment(&scripts.Post, report)
}

func annotatePersonalScriptAssignment(diagnostic *personalScriptDiagnostic, report *config.ConfigIntegrityReport) {
	if diagnostic == nil || diagnostic.State != personalScriptNotConfigured {
		return
	}
	assignment, ok := report.Assignment(diagnostic.Key)
	if !ok {
		diagnostic.Assignment = fmt.Sprintf("%s is not in the file", diagnostic.Key)
		return
	}
	if !assignment.WinningEmpty {
		// Assigned and not empty, yet the verdict is NOT CONFIGURED: the file does not
		// explain this one, so say nothing rather than contradict the verdict.
		return
	}
	if len(assignment.Lines) == 1 {
		diagnostic.Assignment = fmt.Sprintf("%s is on line %d & is empty", diagnostic.Key, assignment.WinningLine)
		return
	}
	diagnostic.Assignment = fmt.Sprintf("%s is on lines %s & line %d wins and is empty",
		diagnostic.Key, joinLineNumbersWithAnd(assignment.Lines), assignment.WinningLine)
}

// joinLineNumbersWithAnd reads as a sentence, not as a list: "120 and 456", and
// "120, 300 and 456" once there are more than two.
func joinLineNumbersWithAnd(lines []int) string {
	switch len(lines) {
	case 0:
		return ""
	case 1:
		return strconv.Itoa(lines[0])
	}
	head := joinLineNumbers(lines[:len(lines)-1])
	return head + " and " + strconv.Itoa(lines[len(lines)-1])
}
