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
// template merge. It seeds SCHEDULER_TIME from the live proxsave cron line BEFORE
// the merge can materialize the template default (02:00): afterwards "absent" and
// "explicitly 02:00" are indistinguishable, and on --upgrade the daemon
// auto-migration deletes that cron line moments later, leaving the operator's real
// run time nowhere on the system. The merge preserves existing user values, so the
// seeded time survives and SCHEDULER_TIME is not even reported as an added key.
//
// The seeding note travels back as an UpgradeResult warning (the callers already
// render those) instead of being logged, because this also runs in
// --upgrade-config-json, whose stdout must stay pure JSON.
func applyConfigUpgrade(ctx context.Context, configPath, baseDir string) (*config.UpgradeResult, error) {
	seed := seedSchedulerTimeFromCrontabFn(ctx, configPath)
	result, err := config.UpgradeConfigFileWithBaseDir(configPath, baseDir)
	if result != nil && seed.Note != "" {
		result.Warnings = append(result.Warnings, seed.Note)
	}
	return result, err
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
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int(), true
	}

	bootstrap.Printf("Upgrading configuration file: %s", args.ConfigPath)
	baseDir, _ := detectedBaseDirOrFallback()
	result, err := applyConfigUpgrade(ctx, args.ConfigPath, baseDir)
	if err != nil {
		bootstrap.Error("ERROR: Failed to upgrade configuration: %v", err)
		return types.ExitConfigError.Int(), true
	}
	logConfigUpgradeWarnings(bootstrap, result.Warnings)
	if !result.Changed {
		bootstrap.Println("Configuration is already up to date with the embedded template; no changes were made.")
		return types.ExitSuccess.Int(), true
	}

	printConfigUpgradeApplyResult(bootstrap, result)
	return types.ExitSuccess.Int(), true
}

func runUpgradeConfigDryMode(_ context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, _ string) (int, bool) {
	if !args.UpgradeConfigDry {
		return types.ExitSuccess.Int(), false
	}
	logging.DebugStepBootstrap(bootstrap, "main run", "mode=upgrade-config-dry")
	if err := ensureConfigExists(args.ConfigPath, bootstrap); err != nil {
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int(), true
	}

	bootstrap.Printf("Planning configuration upgrade using embedded template: %s", args.ConfigPath)
	result, err := config.PlanUpgradeConfigFile(args.ConfigPath)
	if err != nil {
		bootstrap.Error("ERROR: Failed to plan configuration upgrade: %v", err)
		return types.ExitConfigError.Int(), true
	}
	logConfigUpgradeWarnings(bootstrap, result.Warnings)
	if !result.Changed {
		bootstrap.Println("Configuration is already up to date with the embedded template; no changes are required.")
		return types.ExitSuccess.Int(), true
	}

	printConfigUpgradeDryRunResult(bootstrap, result)
	return types.ExitSuccess.Int(), true
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
