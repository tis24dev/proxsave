package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func runEnvMigration(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger) int {
	bootstrap.Println("Starting environment migration from legacy Bash backup.env")

	resolvedPath, err := resolveInstallConfigPath(args.ConfigPath)
	if err != nil {
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int()
	}

	legacyPath, err := resolveLegacyEnvPath(ctx, args, bootstrap)
	if err != nil {
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int()
	}

	summary, err := config.MigrateLegacyEnv(legacyPath, resolvedPath)
	if err != nil {
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int()
	}

	bootstrap.Println("")
	bootstrap.Println("✅ Environment migration completed.")
	bootstrap.Printf("New configuration file: %s", summary.OutputPath)
	if summary.BackupPath != "" {
		bootstrap.Printf("Previous configuration backup: %s", summary.BackupPath)
	}
	if len(summary.UnmappedLegacyKeys) > 0 {
		bootstrap.Printf("Legacy keys requiring manual review (%d): %s",
			len(summary.UnmappedLegacyKeys), strings.Join(summary.UnmappedLegacyKeys, ", "))
	}
	if summary.AutoDisabledCeph {
		bootstrap.Warning("Ceph configuration collection was disabled automatically (no Ceph configuration detected).")
		bootstrap.Warning("Edit BACKUP_CEPH_CONFIG in the generated file if you need to re-enable it.")
	}
	bootstrap.Println("")
	bootstrap.Println("IMPORTANT:")
	bootstrap.Println("- Review the generated configuration manually before any production run.")
	bootstrap.Println("- Run one or more dry-run tests to validate behavior:")
	bootstrap.Println("    ./build/proxsave --dry-run")
	bootstrap.Println("- Verify storage paths, retention policies, and notification settings.")
	return types.ExitSuccess.Int()
}

func runEnvMigrationDry(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger) int {
	bootstrap.Println("Planning environment migration from legacy Bash backup.env (dry run)")

	resolvedPath, err := resolveInstallConfigPath(args.ConfigPath)
	if err != nil {
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int()
	}

	legacyPath, err := resolveLegacyEnvPath(ctx, args, bootstrap)
	if err != nil {
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int()
	}

	summary, _, err := config.PlanLegacyEnvMigration(legacyPath, resolvedPath)
	if err != nil {
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int()
	}

	bootstrap.Printf("Target configuration file: %s", summary.OutputPath)
	printMigratedKeys(summary, bootstrap)
	printUnmappedKeys(summary, bootstrap)
	if summary.AutoDisabledCeph {
		bootstrap.Warning("Ceph configuration collection would be disabled automatically (no Ceph configuration detected).")
		bootstrap.Warning("Edit BACKUP_CEPH_CONFIG manually if you need to keep it enabled.")
	}
	bootstrap.Println("")
	bootstrap.Println("No files were modified. Run --env-migration to apply these changes after reviewing the plan.")
	return types.ExitSuccess.Int()
}

func resolveLegacyEnvPath(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger) (string, error) {
	legacyPath := strings.TrimSpace(args.LegacyEnvPath)
	if legacyPath != "" {
		if err := ensureLegacyFile(legacyPath); err != nil {
			return "", err
		}
		return legacyPath, nil
	}

	if err := ensureInteractiveStdin(); err != nil {
		return "", err
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Println()
	fmt.Println("Legacy configuration import")
	defaultPromptPath := pickLegacyEnvPath()
	question := fmt.Sprintf("Enter the path to the legacy Bash backup.env [%s]: ", defaultPromptPath)
	for {
		fmt.Print(question)
		input, err := readLineWithContext(ctx, reader)
		if err != nil {
			return "", err
		}
		input = strings.TrimSpace(input)
		if input == "" {
			legacyPath = defaultPromptPath
		} else {
			legacyPath = input
		}
		if legacyPath == "" {
			continue
		}
		if err := ensureLegacyFile(legacyPath); err != nil {
			bootstrap.Warning("Invalid legacy configuration path: %v", err)
			continue
		}
		return legacyPath, nil
	}
}

func printMigratedKeys(summary *config.EnvMigrationSummary, bootstrap *logging.BootstrapLogger) {
	if len(summary.MigratedKeys) == 0 {
		bootstrap.Println("No legacy keys matched; template defaults will be used.")
		return
	}
	bootstrap.Println("Mapped legacy keys:")
	lines := make([]string, 0, len(summary.MigratedKeys))
	for newKey, legacyKey := range summary.MigratedKeys {
		lines = append(lines, fmt.Sprintf("%s <- %s", newKey, legacyKey))
	}
	sort.Strings(lines)
	for _, line := range lines {
		bootstrap.Printf("  %s", line)
	}
}

func printUnmappedKeys(summary *config.EnvMigrationSummary, bootstrap *logging.BootstrapLogger) {
	if len(summary.UnmappedLegacyKeys) == 0 {
		return
	}
	keys := append([]string(nil), summary.UnmappedLegacyKeys...)
	sort.Strings(keys)
	bootstrap.Printf("Legacy keys requiring manual review (%d):", len(keys))
	for _, key := range keys {
		bootstrap.Printf("  %s", key)
	}
}

func ensureLegacyFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot stat legacy config %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("legacy config %s is a directory", path)
	}
	return nil
}

func pickLegacyEnvPath() string {
	candidates := []string{
		defaultLegacyEnvPath,
		legacyEnvFallbackPath,
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return defaultLegacyEnvPath
}
