// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

type postHeaderConfigModeHandler func(context.Context, *cli.Args, *logging.BootstrapLogger) (int, bool)

// applyConfigUpgrade is the single in-process entry point for the backup.env
// template merge. It decides the adoption of the live proxsave cron schedule on the
// file as it is BEFORE the merge, because the merge materializes the template
// defaults (daily at 02:00) and afterwards "absent" and "explicitly the default" are
// indistinguishable; on --upgrade the daemon auto-migration then deletes that cron
// line moments later, leaving the operator's real schedule nowhere on the system.
//
// It WRITES the adopted variables only AFTER the merge (A18). The merge then inserts
// every missing SCHEDULER_* variable at its template place, with its comment, and the
// summary reports it as an added key; the adoption only replaces the values. A merge
// that fails adopts nothing. A write that fails after a good merge is one Warnings
// item (A3), with the error as DEBUG evidence.
//
// The adoption block travels back in the UpgradeResult instead of being logged,
// because this also runs in --upgrade-config-json, whose stdout must stay pure JSON:
// its INFO and DEBUG lines as Notes, which the callers log before the warnings, and a
// finding the operator has to see as one Warnings item.
func applyConfigUpgrade(ctx context.Context, configPath, baseDir string) (*config.UpgradeResult, error) {
	seed := deriveSchedulerTimeFromCrontabFn(ctx, configPath)
	result, err := config.UpgradeConfigFileWithBaseDir(configPath, baseDir)
	if err != nil || result == nil {
		return result, err
	}
	seed = seed.written(strings.TrimSpace(configPath))
	result.Notes = append(result.Notes, legacyUpgradeNotes(result.Legacy)...)
	result.Notes = append(result.Notes, seed.upgradeNotes()...)
	if seed.Item != "" {
		result.Warnings = append(result.Warnings, seed.Item)
	}
	return result, nil
}

func runUpgradeConfigJSONMode(ctx context.Context, args *cli.Args) (int, bool) {
	if !args.UpgradeConfigJSON {
		return types.ExitSuccess.Int(), false
	}
	if _, err := os.Stat(args.ConfigPath); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: configuration file not found: %v\n", err)
		return types.ExitConfigError.Int(), true
	}

	baseDir, _ := detectedBaseDirOrFallback()
	result, err := applyConfigUpgrade(ctx, args.ConfigPath, baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to upgrade configuration: %v\n", err)
		return types.ExitConfigError.Int(), true
	}
	if result == nil {
		result = &config.UpgradeResult{}
	}

	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to encode JSON: %v\n", err)
		return types.ExitGenericError.Int(), true
	}
	return types.ExitSuccess.Int(), true
}

func dispatchPostHeaderConfigModes(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger) (int, bool) {
	for _, handler := range []postHeaderConfigModeHandler{
		runUpgradeConfigMode,
	} {
		if exitCode, handled := handler(ctx, args, bootstrap); handled {
			return exitCode, true
		}
	}
	return types.ExitSuccess.Int(), false
}

func runUpgradeConfigMode(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger) (int, bool) {
	if !args.UpgradeConfig {
		return types.ExitSuccess.Int(), false
	}
	logging.DebugStepBootstrap(bootstrap, "main run", "mode=upgrade-config")
	if err := ensureConfigExists(args.ConfigPath, bootstrap); err != nil {
		bootstrap.Error("%v", err)
		return types.ExitConfigError.Int(), true
	}

	bootstrap.Printf("Upgrading configuration file: %s", args.ConfigPath)
	baseDir, _ := detectedBaseDirOrFallback()
	result, err := applyConfigUpgrade(ctx, args.ConfigPath, baseDir)
	if err != nil {
		bootstrap.Error("Failed to upgrade configuration: %v", err)
		return types.ExitConfigError.Int(), true
	}
	logConfigUpgradeNotes(bootstrap, result.Notes)
	logConfigUpgradeWarnings(bootstrap, result.Warnings)
	if !result.Changed {
		bootstrap.Println("Configuration is already up to date with the embedded template; no changes were made.")
		reportConfigFindingsTheMergeLeaves(bootstrap, args.ConfigPath, result.Legacy)
		return types.ExitSuccess.Int(), true
	}

	printConfigUpgradeApplyResult(bootstrap, result)
	reportConfigFindingsTheMergeLeaves(bootstrap, args.ConfigPath, result.Legacy)
	return types.ExitSuccess.Int(), true
}

// reportConfigFindingsTheMergeLeaves names what the template merge does NOT fix, so
// the repair command stops being the one surface that says nothing about it.
//
// The merge only ADDS what the template has and the file lacks. A variable assigned
// twice and a variable whose name is one character off are both invisible to it: the
// key is present, so nothing is missing, and "already up to date with the embedded
// template" was printed over a file every backup run then warns about. Reported by an
// operator whose CUSTOM_BACKUP_PATHS was duplicated for months while --upgrade-config
// answered that there was nothing to do.
//
// This only reports. Removing the discarded lines is an edit to the operator's file
// that the merge has never made, and it is not made here either.
//
// legacy are the variables the merge renames or drops itself. A dry run audits the file
// before the merge, where they are still there, so they are left out of what it names.
func reportConfigFindingsTheMergeLeaves(bootstrap *logging.BootstrapLogger, configPath string, legacy []config.LegacyChange) {
	report, err := config.AuditConfigFile(configPath)
	if err != nil {
		logging.DebugStepBootstrap(bootstrap, "config upgrade", "audit after merge failed: %v", err)
		return
	}
	handled := make(map[string]bool, len(legacy))
	for _, change := range legacy {
		handled[change.Name] = true
	}
	for _, duplicated := range report.Duplicated {
		if handled[duplicated.Name] {
			continue
		}
		bootstrap.Warning("- %s. The merge does not remove it: delete the discarded line(s) by hand.",
			duplicatedVariableSentence(duplicated))
	}
	for _, candidate := range report.NearMiss {
		if handled[candidate.Name] {
			continue
		}
		bootstrap.Warning("- %s is not a variable ProxSave reads; did you mean %s? Nothing on that line is applied.",
			candidate.Name, candidate.Suggestion)
	}
}

func runUpgradeConfigDryMode(_ context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, _ string) (int, bool) {
	if !args.UpgradeConfigDry {
		return types.ExitSuccess.Int(), false
	}
	logging.DebugStepBootstrap(bootstrap, "main run", "mode=upgrade-config-dry")
	if err := ensureConfigExists(args.ConfigPath, bootstrap); err != nil {
		bootstrap.Error("%v", err)
		return types.ExitConfigError.Int(), true
	}

	bootstrap.Printf("Planning configuration upgrade using embedded template: %s", args.ConfigPath)
	result, err := config.PlanUpgradeConfigFile(args.ConfigPath)
	if err != nil {
		bootstrap.Error("Failed to plan configuration upgrade: %v", err)
		return types.ExitConfigError.Int(), true
	}
	logConfigUpgradeWarnings(bootstrap, result.Warnings)
	if !result.Changed {
		bootstrap.Println("Configuration is already up to date with the embedded template; no changes are required.")
		reportConfigFindingsTheMergeLeaves(bootstrap, args.ConfigPath, result.Legacy)
		return types.ExitSuccess.Int(), true
	}

	printConfigUpgradeDryRunResult(bootstrap, result)
	reportConfigFindingsTheMergeLeaves(bootstrap, args.ConfigPath, result.Legacy)
	return types.ExitSuccess.Int(), true
}

// logConfigUpgradeNotes logs an upgrade's Notes at their level, in order. The callers run it
// BEFORE the warnings list, so a block that ends in an outcome is read before the list.
// legacyUpgradeNotes are the lines of the variables the merge carried over from the
// names of the Bash version: the DEBUG evidence of each, then one INFO line for the
// renamed ones and one for the ones dropped because they had no effect.
func legacyUpgradeNotes(changes []config.LegacyChange) []config.UpgradeNote {
	var notes []config.UpgradeNote
	var renamed, removed []string
	for _, change := range changes {
		action := "removed"
		if change.Renamed {
			action = "renamed"
			renamed = append(renamed, change.Name+" to "+change.Current)
		} else {
			removed = append(removed, change.Name)
		}
		notes = append(notes, config.UpgradeNote{Level: config.UpgradeNoteDebug,
			Text: fmt.Sprintf("config upgrade: legacy %s current=%s lines=%s action=%s",
				change.Name, change.Current, joinLineNumbers(change.Lines), action)})
	}
	if len(renamed) > 0 {
		notes = append(notes, config.UpgradeNote{Level: config.UpgradeNoteInfo,
			Text: fmt.Sprintf("Renamed %d legacy variable(s): %s", len(renamed), strings.Join(renamed, ", "))})
	}
	if len(removed) > 0 {
		notes = append(notes, config.UpgradeNote{Level: config.UpgradeNoteInfo,
			Text: fmt.Sprintf("Removed %d legacy variable(s) with no effect: %s", len(removed), strings.Join(removed, ", "))})
	}
	return notes
}

// legacyPlanLines are what a dry run, or the dashboard check, says the merge would do
// with the variables written under a name of the Bash version.
func legacyPlanLines(changes []config.LegacyChange) []string {
	var renamed, removed []string
	for _, change := range changes {
		if change.Renamed {
			renamed = append(renamed, change.Name+" to "+change.Current)
		} else {
			removed = append(removed, change.Name)
		}
	}
	var lines []string
	if len(renamed) > 0 {
		lines = append(lines, fmt.Sprintf("Legacy variables that would be renamed (%d): %s", len(renamed), strings.Join(renamed, ", ")))
	}
	if len(removed) > 0 {
		lines = append(lines, fmt.Sprintf("Legacy variables that would be removed (%d): %s", len(removed), strings.Join(removed, ", ")))
	}
	return lines
}

func logConfigUpgradeNotes(bootstrap *logging.BootstrapLogger, notes []config.UpgradeNote) {
	for _, note := range notes {
		if note.Level == config.UpgradeNoteDebug {
			logBootstrapDebug(bootstrap, "%s", note.Text)
			continue
		}
		logBootstrapInfo(bootstrap, "%s", note.Text)
	}
}

func logConfigUpgradeWarnings(bootstrap *logging.BootstrapLogger, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	bootstrap.Warning("Config upgrade warnings (%d):", len(warnings))
	for _, warning := range warnings {
		bootstrap.Warning("  - %s", warning)
	}
}

func printConfigUpgradeDryRunResult(bootstrap *logging.BootstrapLogger, result *config.UpgradeResult) {
	for _, line := range legacyPlanLines(result.Legacy) {
		bootstrap.Println(line)
	}
	if len(result.MissingKeys) > 0 {
		bootstrap.Printf("Missing keys that would be added from the template (%d): %s",
			len(result.MissingKeys), strings.Join(result.MissingKeys, ", "))
	}
	if result.PreservedValues > 0 {
		bootstrap.Printf("Existing values that would be preserved: %d", result.PreservedValues)
	}
	if len(result.ExtraKeys) > 0 {
		bootstrap.Printf("Custom keys that would be preserved (not present in template) (%d): %s",
			len(result.ExtraKeys), strings.Join(result.ExtraKeys, ", "))
	}
	if len(result.CaseConflictKeys) > 0 {
		bootstrap.Printf("Keys that differ only by case from the template (%d): %s",
			len(result.CaseConflictKeys), strings.Join(result.CaseConflictKeys, ", "))
	}
	bootstrap.Println("Dry run only: no files were modified. Use --upgrade-config to apply these changes.")
}

func printConfigUpgradeApplyResult(bootstrap *logging.BootstrapLogger, result *config.UpgradeResult) {
	if len(result.MissingKeys) > 0 {
		bootstrap.Printf("- Added %d missing key(s): %s",
			len(result.MissingKeys), strings.Join(result.MissingKeys, ", "))
	} else {
		bootstrap.Println("- No new keys were required from the template")
	}
	if result.PreservedValues > 0 {
		bootstrap.Printf("- Preserved %d existing value(s) from current configuration", result.PreservedValues)
	}
	if len(result.ExtraKeys) > 0 {
		bootstrap.Printf("- Kept %d custom key(s) not present in the template: %s",
			len(result.ExtraKeys), strings.Join(result.ExtraKeys, ", "))
	}
	if len(result.CaseConflictKeys) > 0 {
		bootstrap.Printf("- Preserved %d key(s) that differ only by case: %s",
			len(result.CaseConflictKeys), strings.Join(result.CaseConflictKeys, ", "))
	}
	if result.BackupPath != "" {
		bootstrap.Printf("- Backup saved to: %s", result.BackupPath)
	}
	bootstrap.Println("✓ Configuration upgrade completed successfully.")
}
