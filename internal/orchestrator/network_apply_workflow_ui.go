package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
)

func maybeApplyNetworkConfigWithUI(ctx context.Context, ui RestoreWorkflowUI, logger *logging.Logger, plan *RestorePlan, safetyBackup, networkRollbackBackup *SafetyBackupResult, stageRoot, archivePath string, dryRun bool) (err error) {
	if !shouldAttemptNetworkApply(plan) {
		if logger != nil {
			logger.Debug("Network safe apply (UI): skipped (network category not selected)")
		}
		return nil
	}
	done := logging.DebugStart(logger, "network safe apply (ui)", "dryRun=%v euid=%d stage=%s archive=%s", dryRun, os.Geteuid(), strings.TrimSpace(stageRoot), strings.TrimSpace(archivePath))
	defer func() { done(err) }()

	if ui == nil {
		return fmt.Errorf("restore UI not available")
	}
	if !isRealRestoreFS(restoreFS) {
		logger.Debug("Skipping live network apply: non-system filesystem in use")
		return nil
	}
	if dryRun {
		logger.Info("Dry run enabled: skipping live network apply")
		return nil
	}
	if os.Geteuid() != 0 {
		logger.Warning("Skipping live network apply: requires root privileges")
		return nil
	}

	logging.DebugStep(logger, "network safe apply (ui)", "Resolve rollback backup paths")
	networkRollbackPath := ""
	if networkRollbackBackup != nil {
		networkRollbackPath = strings.TrimSpace(networkRollbackBackup.BackupPath)
	}
	fullRollbackPath := ""
	if safetyBackup != nil {
		fullRollbackPath = strings.TrimSpace(safetyBackup.BackupPath)
	}
	logging.DebugStep(logger, "network safe apply (ui)", "Rollback backup resolved: network=%q full=%q", networkRollbackPath, fullRollbackPath)

	if networkRollbackPath == "" && fullRollbackPath == "" {
		logger.Warning("Skipping live network apply: rollback backup not available")
		if strings.TrimSpace(stageRoot) != "" {
			logger.Info("Network configuration is staged; skipping NIC repair/apply due to missing rollback backup.")
			return nil
		}

		repairNow, err := ui.ConfirmAction(
			ctx,
			"NIC name repair (recommended)",
			"Attempt NIC name repair in restored network config files now (no reload)?\n\nThis will only rewrite /etc/network/interfaces and /etc/network/interfaces.d/* when safe mappings are found.",
			"Repair now",
			"Skip repair",
			0,
			false,
		)
		if err != nil {
			return err
		}
		logging.DebugStep(logger, "network safe apply (ui)", "User choice: repairNow=%v", repairNow)
		if repairNow {
			if repair, err := ui.RepairNICNames(ctx, archivePath); err != nil {
				return err
			} else if repair != nil && strings.TrimSpace(repair.Summary()) != "" {
				_ = ui.ShowMessage(ctx, "NIC repair result", repair.Summary())
			}
		}

		logger.Info("Skipping live network apply (you can reboot or apply manually later).")
		return nil
	}

	logging.DebugStep(logger, "network safe apply (ui)", "Prompt: apply network now with rollback timer")
	sourceLine := "Source: /etc/network (will be applied)"
	if strings.TrimSpace(stageRoot) != "" {
		sourceLine = fmt.Sprintf("Source: %s (will be copied to /etc and applied)", strings.TrimSpace(stageRoot))
	}
	message := fmt.Sprintf(
		"Network restore: a restored network configuration is ready to apply.\n%s\n\nThis will reload networking immediately (no reboot).\n\nWARNING: This may change the active IP and disconnect SSH/Web sessions.\n\nAfter applying, type COMMIT within %ds or ProxSave will roll back automatically.\n\nRecommendation: run this step from the local console/IPMI, not over SSH.\n\nApply network configuration now?",
		sourceLine,
		int(defaultNetworkRollbackTimeout.Seconds()),
	)
	applyNow, err := ui.ConfirmAction(ctx, "Apply network configuration", message, "Apply now", "Skip apply", 90*time.Second, false)
	if err != nil {
		return err
	}
	logging.DebugStep(logger, "network safe apply (ui)", "User choice: applyNow=%v", applyNow)
	if !applyNow {
		if strings.TrimSpace(stageRoot) == "" {
			repairNow, err := ui.ConfirmAction(
				ctx,
				"NIC name repair (recommended)",
				"Attempt NIC name repair in restored network config files now (no reload)?\n\nThis will only rewrite /etc/network/interfaces and /etc/network/interfaces.d/* when safe mappings are found.",
				"Repair now",
				"Skip repair",
				0,
				false,
			)
			if err != nil {
				return err
			}
			logging.DebugStep(logger, "network safe apply (ui)", "User choice: repairNow=%v", repairNow)
			if repairNow {
				if repair, err := ui.RepairNICNames(ctx, archivePath); err != nil {
					return err
				} else if repair != nil && strings.TrimSpace(repair.Summary()) != "" {
					_ = ui.ShowMessage(ctx, "NIC repair result", repair.Summary())
				}
			}
		} else {
			logger.Info("Network configuration is staged (not yet written to /etc); skipping NIC repair prompt.")
		}
		logger.Info("Skipping live network apply (you can apply later).")
		return nil
	}

	rollbackPath := networkRollbackPath
	if rollbackPath == "" {
		logging.DebugStep(logger, "network safe apply (ui)", "Prompt: network-only rollback missing; allow full rollback backup fallback")
		ok, err := ui.ConfirmAction(
			ctx,
			"Network-only rollback not available",
			"Network-only rollback backup is not available.\n\nIf you proceed, the rollback timer will use the full safety backup, which may revert other restored categories.\n\nProceed anyway?",
			"Proceed with full rollback",
			"Skip apply",
			0,
			false,
		)
		if err != nil {
			return err
		}
		logging.DebugStep(logger, "network safe apply (ui)", "User choice: allowFullRollback=%v", ok)
		if !ok {
			if strings.TrimSpace(stageRoot) == "" {
				repairNow, err := ui.ConfirmAction(
					ctx,
					"NIC name repair (recommended)",
					"Attempt NIC name repair in restored network config files now (no reload)?\n\nThis will only rewrite /etc/network/interfaces and /etc/network/interfaces.d/* when safe mappings are found.",
					"Repair now",
					"Skip repair",
					0,
					false,
				)
				if err != nil {
					return err
				}
				logging.DebugStep(logger, "network safe apply (ui)", "User choice: repairNow=%v", repairNow)
				if repairNow {
					if repair, err := ui.RepairNICNames(ctx, archivePath); err != nil {
						return err
					} else if repair != nil && strings.TrimSpace(repair.Summary()) != "" {
						_ = ui.ShowMessage(ctx, "NIC repair result", repair.Summary())
					}
				}
			}
			logger.Info("Skipping live network apply (you can reboot or apply manually later).")
			return nil
		}
		rollbackPath = fullRollbackPath
	}
	logging.DebugStep(logger, "network safe apply (ui)", "Selected rollback backup: %s", rollbackPath)

	systemType := SystemTypeUnknown
	if plan != nil {
		systemType = plan.SystemType
	}
	return applyNetworkWithRollbackWithUI(ctx, ui, logger, rollbackPath, networkRollbackPath, stageRoot, archivePath, defaultNetworkRollbackTimeout, systemType)
}

func applyNetworkWithRollbackWithUI(ctx context.Context, ui RestoreWorkflowUI, logger *logging.Logger, rollbackBackupPath, networkRollbackPath, stageRoot, archivePath string, timeout time.Duration, systemType SystemType) (err error) {
	done := logging.DebugStart(
		logger,
		"network safe apply (ui)",
		"rollbackBackup=%s networkRollback=%s timeout=%s systemType=%s stage=%s",
		strings.TrimSpace(rollbackBackupPath),
		strings.TrimSpace(networkRollbackPath),
		timeout,
		systemType,
		strings.TrimSpace(stageRoot),
	)
	defer func() { done(err) }()

	if ui == nil {
		return fmt.Errorf("restore UI not available")
	}

	logging.DebugStep(logger, "network safe apply (ui)", "Create diagnostics directory")
	diagnosticsDir, err := createNetworkDiagnosticsDir()
	if err != nil {
		logger.Warning("Network diagnostics disabled: %v", err)
		diagnosticsDir = ""
	} else {
		logger.Info("Network diagnostics directory: %s", diagnosticsDir)
	}

	logging.DebugStep(logger, "network safe apply (ui)", "Detect management interface (SSH/default route)")
	iface, source := detectManagementInterface(ctx, logger)
	if iface != "" {
		logger.Info("Detected management interface: %s (%s)", iface, source)
	}

	if diagnosticsDir != "" {
		logging.DebugStep(logger, "network safe apply (ui)", "Capture network snapshot (before)")
		if snap, err := writeNetworkSnapshot(ctx, logger, diagnosticsDir, "before", 3*time.Second); err != nil {
			logger.Debug("Network snapshot before apply failed: %v", err)
		} else {
			logger.Debug("Network snapshot (before): %s", snap)
		}

		logging.DebugStep(logger, "network safe apply (ui)", "Run baseline health checks (before)")
		healthBefore := runNetworkHealthChecks(ctx, networkHealthOptions{
			SystemType:         systemType,
			Logger:             logger,
			CommandTimeout:     3 * time.Second,
			EnableGatewayPing:  false,
			ForceSSHRouteCheck: false,
			EnableDNSResolve:   false,
		})
		if path, err := writeNetworkHealthReportFileNamed(diagnosticsDir, "health_before.txt", healthBefore); err != nil {
			logger.Debug("Failed to write network health (before) report: %v", err)
		} else {
			logger.Debug("Network health (before) report: %s", path)
		}
	}

	if strings.TrimSpace(stageRoot) != "" {
		logging.DebugStep(logger, "network safe apply (ui)", "Apply staged network files to system paths (before NIC repair)")
		applied, err := applyNetworkFilesFromStage(logger, stageRoot)
		if err != nil {
			return err
		}
		if len(applied) > 0 {
			logging.DebugStep(logger, "network safe apply (ui)", "Staged network files written: %d", len(applied))
		}
	}

	logging.DebugStep(logger, "network safe apply (ui)", "NIC name repair (optional)")
	var nicRepair *nicRepairResult
	if repair, err := ui.RepairNICNames(ctx, archivePath); err != nil {
		logger.Warning("NIC repair failed: %v", err)
	} else {
		nicRepair = repair
		if nicRepair != nil {
			if nicRepair.Applied() || nicRepair.SkippedReason != "" {
				logger.Info("%s", nicRepair.Summary())
			} else {
				logger.Debug("%s", nicRepair.Summary())
			}
		}
	}

	if strings.TrimSpace(iface) != "" {
		if cur, err := currentNetworkEndpoint(ctx, iface, 2*time.Second); err == nil {
			if tgt, err := targetNetworkEndpointFromConfig(logger, iface); err == nil {
				logger.Info("Network plan: %s -> %s", cur.summary(), tgt.summary())
			}
		}
	}

	if diagnosticsDir != "" {
		logging.DebugStep(logger, "network safe apply (ui)", "Write network plan (current -> target)")
		if planText, err := buildNetworkPlanReport(ctx, logger, iface, source, 2*time.Second); err != nil {
			logger.Debug("Network plan build failed: %v", err)
		} else if strings.TrimSpace(planText) != "" {
			if path, err := writeNetworkTextReportFile(diagnosticsDir, "plan.txt", planText+"\n"); err != nil {
				logger.Debug("Network plan write failed: %v", err)
			} else {
				logger.Debug("Network plan: %s", path)
			}
		}

		logging.DebugStep(logger, "network safe apply (ui)", "Run ifquery diagnostic (pre-apply)")
		ifqueryPre := runNetworkIfqueryDiagnostic(ctx, 5*time.Second, logger)
		if !ifqueryPre.Skipped {
			if path, err := writeNetworkIfqueryDiagnosticReportFile(diagnosticsDir, "ifquery_pre_apply.txt", ifqueryPre); err != nil {
				logger.Debug("Failed to write ifquery (pre-apply) report: %v", err)
			} else {
				logger.Debug("ifquery (pre-apply) report: %s", path)
			}
		}
	}

	logging.DebugStep(logger, "network safe apply (ui)", "Network preflight validation (ifupdown/ifupdown2)")
	preflight := runNetworkPreflightValidation(ctx, 5*time.Second, logger)
	if diagnosticsDir != "" {
		if path, err := writeNetworkPreflightReportFile(diagnosticsDir, preflight); err != nil {
			logger.Debug("Failed to write network preflight report: %v", err)
		} else {
			logger.Debug("Network preflight report: %s", path)
		}
	}
	if !preflight.Ok() {
		message := preflight.Summary()
		if diagnosticsDir != "" {
			message += "\n\nDiagnostics saved under:\n" + diagnosticsDir
		}
		if out := strings.TrimSpace(preflight.Output); out != "" {
			message += "\n\nOutput:\n" + out
		}

		if strings.TrimSpace(stageRoot) != "" && strings.TrimSpace(networkRollbackPath) != "" {
			logging.DebugStep(logger, "network safe apply (ui)", "Preflight failed in staged mode: rolling back network files automatically")
			rollbackLog, rbErr := rollbackNetworkFilesNow(ctx, logger, networkRollbackPath, diagnosticsDir)
			if strings.TrimSpace(rollbackLog) != "" {
				logger.Info("Network rollback log: %s", rollbackLog)
			}
			if rbErr != nil {
				logger.Error("Network apply aborted: preflight validation failed (%s) and rollback failed: %v", preflight.CommandLine(), rbErr)
				return fmt.Errorf("network preflight validation failed; rollback attempt failed: %w", rbErr)
			}
			if diagnosticsDir != "" {
				logging.DebugStep(logger, "network safe apply (ui)", "Capture network snapshot (after rollback)")
				if snap, err := writeNetworkSnapshot(ctx, logger, diagnosticsDir, "after_rollback", 3*time.Second); err != nil {
					logger.Debug("Network snapshot after rollback failed: %v", err)
				} else {
					logger.Debug("Network snapshot (after rollback): %s", snap)
				}
				logging.DebugStep(logger, "network safe apply (ui)", "Run ifquery diagnostic (after rollback)")
				ifqueryAfterRollback := runNetworkIfqueryDiagnostic(ctx, 5*time.Second, logger)
				if !ifqueryAfterRollback.Skipped {
					if path, err := writeNetworkIfqueryDiagnosticReportFile(diagnosticsDir, "ifquery_after_rollback.txt", ifqueryAfterRollback); err != nil {
						logger.Debug("Failed to write ifquery (after rollback) report: %v", err)
					} else {
						logger.Debug("ifquery (after rollback) report: %s", path)
					}
				}
			}
			logger.Warning(
				"Network apply aborted: preflight validation failed (%s). Rolled back /etc/network/*, /etc/hosts, /etc/hostname, /etc/resolv.conf to the pre-restore state (rollback=%s).",
				preflight.CommandLine(),
				strings.TrimSpace(networkRollbackPath),
			)
			_ = ui.ShowError(ctx, "Network preflight failed", "Network configuration failed preflight and was rolled back automatically.")
			return fmt.Errorf("network preflight validation failed; network files rolled back")
		}

		if !preflight.Skipped && preflight.ExitError != nil && strings.TrimSpace(networkRollbackPath) != "" {
			message += "\n\nRollback restored network config files to the pre-restore configuration now? (recommended)"
			rollbackNow, err := ui.ConfirmAction(ctx, "Network preflight failed", message, "Rollback now", "Keep restored files", 0, true)
			if err != nil {
				return err
			}
			logging.DebugStep(logger, "network safe apply (ui)", "User choice: rollbackNow=%v", rollbackNow)
			if rollbackNow {
				logging.DebugStep(logger, "network safe apply (ui)", "Rollback network files now (backup=%s)", strings.TrimSpace(networkRollbackPath))
				rollbackLog, rbErr := rollbackNetworkFilesNow(ctx, logger, networkRollbackPath, diagnosticsDir)
				if strings.TrimSpace(rollbackLog) != "" {
					logger.Info("Network rollback log: %s", rollbackLog)
				}
				if rbErr != nil {
					logger.Warning("Network rollback failed: %v", rbErr)
					return fmt.Errorf("network preflight validation failed; rollback attempt failed: %w", rbErr)
				}
				logger.Warning("Network files rolled back to pre-restore configuration due to preflight failure")
				return fmt.Errorf("network preflight validation failed; network files rolled back")
			}
		}
		return fmt.Errorf("network preflight validation failed; aborting live network apply")
	}

	logging.DebugStep(logger, "network safe apply (ui)", "Arm rollback timer BEFORE applying changes")
	handle, err := armNetworkRollback(ctx, logger, rollbackBackupPath, timeout, diagnosticsDir)
	if err != nil {
		return err
	}

	logging.DebugStep(logger, "network safe apply (ui)", "Apply network configuration now")
	if err := applyNetworkConfig(ctx, logger); err != nil {
		logger.Warning("Network apply failed: %v", err)
		return err
	}

	if diagnosticsDir != "" {
		logging.DebugStep(logger, "network safe apply (ui)", "Capture network snapshot (after)")
		if snap, err := writeNetworkSnapshot(ctx, logger, diagnosticsDir, "after", 3*time.Second); err != nil {
			logger.Debug("Network snapshot after apply failed: %v", err)
		} else {
			logger.Debug("Network snapshot (after): %s", snap)
		}

		logging.DebugStep(logger, "network safe apply (ui)", "Run ifquery diagnostic (post-apply)")
		ifqueryPost := runNetworkIfqueryDiagnostic(ctx, 5*time.Second, logger)
		if !ifqueryPost.Skipped {
			if path, err := writeNetworkIfqueryDiagnosticReportFile(diagnosticsDir, "ifquery_post_apply.txt", ifqueryPost); err != nil {
				logger.Debug("Failed to write ifquery (post-apply) report: %v", err)
			} else {
				logger.Debug("ifquery (post-apply) report: %s", path)
			}
		}
	}

	logging.DebugStep(logger, "network safe apply (ui)", "Run post-apply health checks")
	health := runNetworkHealthChecks(ctx, networkHealthOptions{
		SystemType:         systemType,
		Logger:             logger,
		CommandTimeout:     3 * time.Second,
		EnableGatewayPing:  true,
		ForceSSHRouteCheck: false,
		EnableDNSResolve:   true,
		LocalPortChecks:    defaultNetworkPortChecks(systemType),
	})
	logNetworkHealthReport(logger, health)
	if diagnosticsDir != "" {
		if path, err := writeNetworkHealthReportFile(diagnosticsDir, health); err != nil {
			logger.Debug("Failed to write network health report: %v", err)
		} else {
			logger.Debug("Network health report: %s", path)
		}
	}

	remaining := handle.remaining(time.Now())
	if remaining <= 0 {
		logger.Warning("Rollback window already expired; leaving rollback armed")
		return nil
	}

	logging.DebugStep(logger, "network safe apply (ui)", "Wait for COMMIT (rollback in %ds)", int(remaining.Seconds()))
	committed, commitErr := ui.PromptNetworkCommit(ctx, remaining, health, nicRepair, diagnosticsDir)
	if commitErr != nil {
		logger.Warning("Commit prompt error: %v", commitErr)
		return buildNetworkApplyNotCommittedError(ctx, logger, iface, handle)
	}
	logging.DebugStep(logger, "network safe apply (ui)", "User commit result: committed=%v", committed)
	if committed {
		if rollbackAlreadyRunning(ctx, logger, handle) {
			logger.Warning("Commit received too late: rollback already running. Network configuration NOT committed.")
			return buildNetworkApplyNotCommittedError(ctx, logger, iface, handle)
		}
		disarmNetworkRollback(ctx, logger, handle)
		logger.Info("Network configuration committed successfully.")
		return nil
	}

	// Not committed: keep rollback ARMED.
	if strings.TrimSpace(diagnosticsDir) != "" {
		_ = ui.ShowMessage(ctx, "Network rollback armed", fmt.Sprintf("Network configuration not committed.\n\nRollback will run automatically.\n\nDiagnostics saved under:\n%s", diagnosticsDir))
	}
	return buildNetworkApplyNotCommittedError(ctx, logger, iface, handle)
}

func (c *cliWorkflowUI) ConfirmAction(ctx context.Context, title, message, yesLabel, noLabel string, timeout time.Duration, defaultYes bool) (bool, error) {
	_ = yesLabel
	_ = noLabel

	title = strings.TrimSpace(title)
	if title != "" {
		fmt.Printf("\n%s\n", title)
	}
	message = strings.TrimSpace(message)
	if message != "" {
		fmt.Println(message)
		fmt.Println()
	}
	question := title
	if question == "" {
		question = "Proceed?"
	}
	return promptYesNoWithCountdown(ctx, c.reader, c.logger, question, timeout, defaultYes)
}

func (c *cliWorkflowUI) RepairNICNames(ctx context.Context, archivePath string) (*nicRepairResult, error) {
	return maybeRepairNICNamesCLI(ctx, c.reader, c.logger, archivePath), nil
}

func (c *cliWorkflowUI) PromptNetworkCommit(ctx context.Context, remaining time.Duration, health networkHealthReport, nicRepair *nicRepairResult, diagnosticsDir string) (bool, error) {
	if strings.TrimSpace(diagnosticsDir) != "" {
		fmt.Printf("Network diagnostics saved under: %s\n", strings.TrimSpace(diagnosticsDir))
	}
	fmt.Println(health.Details())
	if health.Severity == networkHealthCritical {
		fmt.Println("CRITICAL: Connectivity checks failed. Recommended action: do NOT commit and let rollback run.")
	}
	if nicRepair != nil && strings.TrimSpace(nicRepair.Summary()) != "" {
		fmt.Printf("\nNIC repair: %s\n", nicRepair.Summary())
	}
	return promptNetworkCommitWithCountdown(ctx, c.reader, c.logger, remaining)
}

func (u *tuiWorkflowUI) ConfirmAction(ctx context.Context, title, message, yesLabel, noLabel string, timeout time.Duration, defaultYes bool) (bool, error) {
	_ = defaultYes

	title = strings.TrimSpace(title)
	if title == "" {
		title = "Confirm"
	}
	message = strings.TrimSpace(message)
	if timeout > 0 {
		return promptYesNoTUIWithCountdown(ctx, u.logger, title, u.configPath, u.buildSig, message, yesLabel, noLabel, timeout)
	}
	return promptYesNoTUIFunc(title, u.configPath, u.buildSig, message, yesLabel, noLabel)
}

func (u *tuiWorkflowUI) RepairNICNames(ctx context.Context, archivePath string) (*nicRepairResult, error) {
	return maybeRepairNICNamesTUI(ctx, u.logger, archivePath, u.configPath, u.buildSig), nil
}

func (u *tuiWorkflowUI) PromptNetworkCommit(ctx context.Context, remaining time.Duration, health networkHealthReport, nicRepair *nicRepairResult, diagnosticsDir string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	committed, err := promptNetworkCommitTUI(remaining, health, nicRepair, diagnosticsDir, u.configPath, u.buildSig)
	if err != nil && errors.Is(err, input.ErrInputAborted) {
		return false, err
	}
	return committed, err
}

