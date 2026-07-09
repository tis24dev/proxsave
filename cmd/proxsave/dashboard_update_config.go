package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// Seams for testing without touching a real config file.
var (
	updateConfigPlan  = config.PlanUpgradeConfigFile
	updateConfigApply = config.UpgradeConfigFileWithBaseDir
)

// runDashboardUpdateConfig merges new template keys into the config file from the
// dashboard using the shared two-step check/apply brick: a read-only CHECK
// (--upgrade-config-dry-run) classifies "Up to date" (green) vs "Update available"
// (yellow), and Apply runs the real merge (--upgrade-config: backup + rollback).
func runDashboardUpdateConfig(ctx context.Context, session *shell.Session, configPath string) {
	runDashboardCheckApply(ctx, session, "Upgrade config",
		func() (dashboardCheckResult, error) {
			result, err := updateConfigPlan(configPath)
			if err != nil {
				return dashboardCheckResult{}, err
			}
			if result == nil || !result.Changed {
				return dashboardCheckResult{Found: false, Level: orchestrator.HealthcheckSetupLevelOk, Keyword: "Up to date",
					Explanation: "The configuration already has every key from the template. Nothing to update."}, nil
			}
			return dashboardCheckResult{Found: true, Level: orchestrator.HealthcheckSetupLevelWarn, Keyword: "Update available",
				Explanation: describeConfigPlan(result)}, nil
		},
		func() (dashboardApplyResult, error) {
			baseDir, _ := detectedBaseDirOrFallback()
			result, err := updateConfigApply(configPath, baseDir)
			if err != nil {
				return dashboardApplyResult{}, err
			}
			keyword := "Updated"
			if result == nil || !result.Changed {
				keyword = "Up to date"
			}
			return dashboardApplyResult{Level: orchestrator.HealthcheckSetupLevelOk, Keyword: keyword, Explanation: describeConfigApply(result)}, nil
		},
		"Apply", "update the configuration file now")
}

// maxConfigKeysShown caps how many keys the check screen lists so a large template bump
// cannot push the menu items off-screen; the remainder is summarized ("… and N more").
const maxConfigKeysShown = 12

// describeConfigPlan renders the CHECK explanation for a pending config upgrade, using
// the same "(s)" wording as the CLI --upgrade-config output, and lists the keys that
// would be added (one per line, capped).
func describeConfigPlan(r *config.UpgradeResult) string {
	var parts []string
	if n := len(r.MissingKeys); n > 0 {
		parts = append(parts, fmt.Sprintf("%d missing key(s) to add", n))
	}
	if n := len(r.ExtraKeys); n > 0 {
		parts = append(parts, fmt.Sprintf("%d custom key(s) to keep", n))
	}
	if n := len(r.CaseConflictKeys); n > 0 {
		parts = append(parts, fmt.Sprintf("%d case-only key(s) to keep", n))
	}
	summary := "the file would be rewritten from the template"
	if len(parts) > 0 {
		summary = strings.Join(parts, ", ")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %s.", summary)
	if len(r.MissingKeys) > 0 {
		b.WriteString("\nKeys to add:")
		for i, k := range r.MissingKeys {
			if i >= maxConfigKeysShown {
				fmt.Fprintf(&b, "\n  … and %d more", len(r.MissingKeys)-maxConfigKeysShown)
				break
			}
			b.WriteString("\n  " + k)
		}
	}
	b.WriteString("\nApply updates the config file (a backup is saved first).")
	return b.String()
}

// describeConfigApply renders the real-run outcome explanation.
func describeConfigApply(r *config.UpgradeResult) string {
	if r == nil || !r.Changed {
		return "The configuration already has every key from the template."
	}
	var parts []string
	if n := len(r.MissingKeys); n > 0 {
		parts = append(parts, fmt.Sprintf("added %d key(s)", n))
	}
	if r.PreservedValues > 0 {
		parts = append(parts, fmt.Sprintf("preserved %d value(s)", r.PreservedValues))
	}
	if n := len(r.ExtraKeys); n > 0 {
		parts = append(parts, fmt.Sprintf("kept %d custom key(s)", n))
	}
	msg := "Updated the configuration"
	if len(parts) > 0 {
		msg += ": " + strings.Join(parts, ", ")
	}
	msg += "."
	if r.BackupPath != "" {
		msg += "\nBackup saved to " + r.BackupPath + "."
	}
	if len(r.Warnings) > 0 {
		msg += "\n" + strings.Join(r.Warnings, "\n")
	}
	return msg
}
