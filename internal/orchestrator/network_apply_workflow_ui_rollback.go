// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

var errNetworkApplySkipped = fmt.Errorf("network apply skipped")

type networkRollbackUIApplyFlow struct {
	ctx                 context.Context
	ui                  RestoreWorkflowUI
	logger              *logging.Logger
	rollbackBackupPath  string
	networkRollbackPath string
	stageRoot           string
	archivePath         string
	timeout             time.Duration
	systemType          SystemType
	suppressPVEChecks   bool
	diagnosticsDir      string
	iface               string
	source              string
	nicRepair           *nicRepairResult
	handle              *networkRollbackHandle
	health              networkHealthReport
}

type networkRollbackUIApplyRequest struct {
	rollbackBackupPath  string
	networkRollbackPath string
	stageRoot           string
	archivePath         string
	timeout             time.Duration
	systemType          SystemType
	suppressPVEChecks   bool
}

func applyNetworkWithRollbackWithUI(ctx context.Context, ui RestoreWorkflowUI, logger *logging.Logger, req networkRollbackUIApplyRequest) (err error) {
	done := logging.DebugStart(
		logger,
		"network safe apply (ui)",
		"rollbackBackup=%s networkRollback=%s timeout=%s systemType=%s stage=%s suppressPVEChecks=%v",
		strings.TrimSpace(req.rollbackBackupPath),
		strings.TrimSpace(req.networkRollbackPath),
		req.timeout,
		req.systemType,
		strings.TrimSpace(req.stageRoot),
		req.suppressPVEChecks,
	)
	defer func() { done(err) }()

	flow := &networkRollbackUIApplyFlow{
		ctx:                 ctx,
		ui:                  ui,
		logger:              logger,
		rollbackBackupPath:  req.rollbackBackupPath,
		networkRollbackPath: req.networkRollbackPath,
		stageRoot:           req.stageRoot,
		archivePath:         req.archivePath,
		timeout:             req.timeout,
		systemType:          req.systemType,
		suppressPVEChecks:   req.suppressPVEChecks,
	}
	return flow.run()
}

func (f *networkRollbackUIApplyFlow) run() error {
	if f.ui == nil {
		return fmt.Errorf("restore UI not available")
	}
	f.createDiagnosticsDir()
	f.detectManagementInterface()
	f.captureBeforeDiagnostics()
	if err := f.applyStagedNetworkFiles(); err != nil {
		return err
	}
	f.repairNICNames()
	f.logNetworkPlan()
	f.writePreApplyDiagnostics()
	if err := f.validatePreflight(); err != nil {
		return err
	}
	if err := f.armRollbackAndApply(); err != nil {
		return err
	}
	f.writePostApplyDiagnostics()
	f.runPostApplyHealthChecks()
	return f.waitForCommit()
}

func (f *networkRollbackUIApplyFlow) createDiagnosticsDir() {
	logging.DebugStep(f.logger, "network safe apply (ui)", "Create diagnostics directory")
	dir, err := createNetworkDiagnosticsDir()
	if err != nil {
		f.warning("Network diagnostics disabled: %v", err)
		return
	}
	f.diagnosticsDir = dir
	f.info("Network diagnostics directory: %s", dir)
}

func (f *networkRollbackUIApplyFlow) detectManagementInterface() {
	logging.DebugStep(f.logger, "network safe apply (ui)", "Detect management interface (SSH/default route)")
	f.iface, f.source = detectManagementInterface(f.ctx, f.logger)
	if f.iface != "" {
		f.info("Detected management interface: %s (%s)", f.iface, f.source)
	}
}

func (f *networkRollbackUIApplyFlow) captureBeforeDiagnostics() {
	if f.diagnosticsDir == "" {
		return
	}
	logging.DebugStep(f.logger, "network safe apply (ui)", "Capture network snapshot (before)")
	if snap, err := writeNetworkSnapshot(f.ctx, f.logger, f.diagnosticsDir, "before", 3*time.Second); err != nil {
		f.debug("Network snapshot before apply failed: %v", err)
	} else {
		f.debug("Network snapshot (before): %s", snap)
	}

	logging.DebugStep(f.logger, "network safe apply (ui)", "Run baseline health checks (before)")
	healthBefore := runNetworkHealthChecks(f.ctx, networkHealthOptions{
		SystemType:         f.systemType,
		Logger:             f.logger,
		CommandTimeout:     3 * time.Second,
		EnableGatewayPing:  false,
		ForceSSHRouteCheck: false,
		EnableDNSResolve:   false,
	})
	if path, err := writeNetworkHealthReportFileNamed(f.diagnosticsDir, "health_before.txt", healthBefore); err != nil {
		f.debug("Failed to write network health (before) report: %v", err)
	} else {
		f.debug("Network health (before) report: %s", path)
	}
}

func (f *networkRollbackUIApplyFlow) applyStagedNetworkFiles() error {
	if strings.TrimSpace(f.stageRoot) == "" {
		return nil
	}
	logging.DebugStep(f.logger, "network safe apply (ui)", "Apply staged network files to system paths (before NIC repair)")
	applied, err := applyNetworkFilesFromStage(f.logger, f.stageRoot)
	if err != nil {
		return err
	}
	if len(applied) > 0 {
		logging.DebugStep(f.logger, "network safe apply (ui)", "Staged network files written: %d", len(applied))
	}
	return nil
}

func (f *networkRollbackUIApplyFlow) repairNICNames() {
	logging.DebugStep(f.logger, "network safe apply (ui)", "NIC name repair (optional)")
	repair, err := f.ui.RepairNICNames(f.ctx, f.archivePath)
	if err != nil {
		f.warning("NIC repair failed: %v", err)
		return
	}
	f.nicRepair = repair
	if repair == nil {
		return
	}
	if repair.Applied() || repair.SkippedReason != "" {
		f.info("%s", repair.Summary())
		return
	}
	f.debug("%s", repair.Summary())
}

func (f *networkRollbackUIApplyFlow) logNetworkPlan() {
	if strings.TrimSpace(f.iface) == "" {
		return
	}
	cur, curErr := currentNetworkEndpoint(f.ctx, f.iface, 2*time.Second)
	tgt, tgtErr := targetNetworkEndpointFromConfig(f.iface)
	if curErr == nil && tgtErr == nil {
		f.info("Network plan: %s -> %s", cur.summary(), tgt.summary())
	}
}

func (f *networkRollbackUIApplyFlow) writePreApplyDiagnostics() {
	if f.diagnosticsDir == "" {
		return
	}
	f.writeNetworkPlanReport()
	f.writeIfqueryDiagnostic("Run ifquery diagnostic (pre-apply)", "ifquery_pre_apply.txt", "pre-apply")
}

func (f *networkRollbackUIApplyFlow) writeNetworkPlanReport() {
	logging.DebugStep(f.logger, "network safe apply (ui)", "Write network plan (current -> target)")
	planText, err := buildNetworkPlanReport(f.ctx, f.iface, f.source, 2*time.Second)
	if err != nil {
		f.debug("Network plan build failed: %v", err)
		return
	}
	if strings.TrimSpace(planText) == "" {
		return
	}
	if path, err := writeNetworkTextReportFile(f.diagnosticsDir, "plan.txt", planText+"\n"); err != nil {
		f.debug("Network plan write failed: %v", err)
	} else {
		f.debug("Network plan: %s", path)
	}
}

func (f *networkRollbackUIApplyFlow) writeIfqueryDiagnostic(step, filename, label string) {
	logging.DebugStep(f.logger, "network safe apply (ui)", "%s", step)
	result := runNetworkIfqueryDiagnostic(f.ctx, 5*time.Second, f.logger)
	if result.Skipped {
		return
	}
	if path, err := writeNetworkIfqueryDiagnosticReportFile(f.diagnosticsDir, filename, result); err != nil {
		f.debug("Failed to write ifquery (%s) report: %v", label, err)
	} else {
		f.debug("ifquery (%s) report: %s", label, path)
	}
}

func (f *networkRollbackUIApplyFlow) validatePreflight() error {
	logging.DebugStep(f.logger, "network safe apply (ui)", "Network preflight validation (ifupdown/ifupdown2)")
	preflight := runNetworkPreflightValidation(f.ctx, 5*time.Second, f.logger)
	f.writePreflightReport(preflight)
	if preflight.Ok() {
		return nil
	}
	return f.handlePreflightFailure(preflight)
}

func (f *networkRollbackUIApplyFlow) writePreflightReport(preflight networkPreflightResult) {
	if f.diagnosticsDir == "" {
		return
	}
	if path, err := writeNetworkPreflightReportFile(f.diagnosticsDir, preflight); err != nil {
		f.debug("Failed to write network preflight report: %v", err)
	} else {
		f.debug("Network preflight report: %s", path)
	}
}

func (f *networkRollbackUIApplyFlow) handlePreflightFailure(preflight networkPreflightResult) error {
	message := f.preflightFailureMessage(preflight)
	if strings.TrimSpace(f.stageRoot) != "" && strings.TrimSpace(f.networkRollbackPath) != "" {
		return f.rollbackStagedPreflightFailure(preflight)
	}
	if f.canAskPreflightRollback(preflight) {
		return f.confirmPreflightRollback(message)
	}
	return fmt.Errorf("network preflight validation failed; aborting live network apply")
}

func (f *networkRollbackUIApplyFlow) preflightFailureMessage(preflight networkPreflightResult) string {
	message := preflight.Summary()
	if f.diagnosticsDir != "" {
		message += "\n\nDiagnostics saved under:\n" + f.diagnosticsDir
	}
	if out := strings.TrimSpace(preflight.Output); out != "" {
		message += "\n\nOutput:\n" + out
	}
	return message
}

func (f *networkRollbackUIApplyFlow) rollbackStagedPreflightFailure(preflight networkPreflightResult) error {
	logging.DebugStep(f.logger, "network safe apply (ui)", "Preflight failed in staged mode: rolling back network files automatically")
	rollbackLog, rbErr := rollbackNetworkFilesNow(f.ctx, f.logger, f.networkRollbackPath, f.diagnosticsDir)
	if strings.TrimSpace(rollbackLog) != "" {
		f.info("Network rollback log: %s", rollbackLog)
	}
	if rbErr != nil {
		f.error("Network apply aborted: preflight validation failed (%s) and rollback failed: %v", preflight.CommandLine(), rbErr)
		return fmt.Errorf("network preflight validation failed; rollback attempt failed: %w", rbErr)
	}
	f.captureAfterRollbackDiagnostics()
	f.warning(
		"Network apply aborted: preflight validation failed (%s). Rolled back /etc/network/*, /etc/hosts, /etc/hostname, /etc/resolv.conf to the pre-restore state (rollback=%s).",
		preflight.CommandLine(),
		strings.TrimSpace(f.networkRollbackPath),
	)
	_ = f.ui.ShowError(f.ctx, "Network preflight failed", "Network configuration failed preflight and was rolled back automatically.")
	return fmt.Errorf("network preflight validation failed; network files rolled back")
}

func (f *networkRollbackUIApplyFlow) captureAfterRollbackDiagnostics() {
	if f.diagnosticsDir == "" {
		return
	}
	logging.DebugStep(f.logger, "network safe apply (ui)", "Capture network snapshot (after rollback)")
	if snap, err := writeNetworkSnapshot(f.ctx, f.logger, f.diagnosticsDir, "after_rollback", 3*time.Second); err != nil {
		f.debug("Network snapshot after rollback failed: %v", err)
	} else {
		f.debug("Network snapshot (after rollback): %s", snap)
	}
	f.writeIfqueryDiagnostic("Run ifquery diagnostic (after rollback)", "ifquery_after_rollback.txt", "after rollback")
}

func (f *networkRollbackUIApplyFlow) canAskPreflightRollback(preflight networkPreflightResult) bool {
	return !preflight.Skipped && preflight.ExitError != nil && strings.TrimSpace(f.networkRollbackPath) != ""
}

func (f *networkRollbackUIApplyFlow) confirmPreflightRollback(message string) error {
	message += "\n\nRollback restored network config files to the pre-restore configuration now? (recommended)"
	rollbackNow, err := f.ui.ConfirmAction(f.ctx, "Network preflight failed", message, "Rollback now", "Keep restored files", 0, true)
	if err != nil {
		return err
	}
	logging.DebugStep(f.logger, "network safe apply (ui)", "User choice: rollbackNow=%v", rollbackNow)
	if !rollbackNow {
		return fmt.Errorf("network preflight validation failed; aborting live network apply")
	}
	return f.rollbackPreflightFailureNow()
}

func (f *networkRollbackUIApplyFlow) rollbackPreflightFailureNow() error {
	logging.DebugStep(f.logger, "network safe apply (ui)", "Rollback network files now (backup=%s)", strings.TrimSpace(f.networkRollbackPath))
	rollbackLog, rbErr := rollbackNetworkFilesNow(f.ctx, f.logger, f.networkRollbackPath, f.diagnosticsDir)
	if strings.TrimSpace(rollbackLog) != "" {
		f.info("Network rollback log: %s", rollbackLog)
	}
	if rbErr != nil {
		f.warning("Network rollback failed: %v", rbErr)
		return fmt.Errorf("network preflight validation failed; rollback attempt failed: %w", rbErr)
	}
	f.warning("Network files rolled back to pre-restore configuration due to preflight failure")
	return fmt.Errorf("network preflight validation failed; network files rolled back")
}

func (f *networkRollbackUIApplyFlow) armRollbackAndApply() error {
	logging.DebugStep(f.logger, "network safe apply (ui)", "Arm rollback timer BEFORE applying changes")
	handle, err := armNetworkRollback(f.ctx, f.logger, f.rollbackBackupPath, f.timeout, f.diagnosticsDir)
	if err != nil {
		return err
	}
	f.handle = handle

	logging.DebugStep(f.logger, "network safe apply (ui)", "Apply network configuration now")
	if err := applyNetworkConfig(f.ctx, f.logger); err != nil {
		f.warning("Network apply failed: %v", err)
		return err
	}
	return nil
}

func (f *networkRollbackUIApplyFlow) writePostApplyDiagnostics() {
	if f.diagnosticsDir == "" {
		return
	}
	logging.DebugStep(f.logger, "network safe apply (ui)", "Capture network snapshot (after)")
	if snap, err := writeNetworkSnapshot(f.ctx, f.logger, f.diagnosticsDir, "after", 3*time.Second); err != nil {
		f.debug("Network snapshot after apply failed: %v", err)
	} else {
		f.debug("Network snapshot (after): %s", snap)
	}
	f.writeIfqueryDiagnostic("Run ifquery diagnostic (post-apply)", "ifquery_post_apply.txt", "post-apply")
}

func (f *networkRollbackUIApplyFlow) runPostApplyHealthChecks() {
	logging.DebugStep(f.logger, "network safe apply (ui)", "Run post-apply health checks")
	healthOptions := networkHealthOptions{
		SystemType:         f.systemType,
		Logger:             f.logger,
		CommandTimeout:     3 * time.Second,
		EnableGatewayPing:  true,
		ForceSSHRouteCheck: false,
		EnableDNSResolve:   true,
		LocalPortChecks:    defaultNetworkPortChecks(f.systemType),
	}
	if f.suppressPVEChecks {
		healthOptions.SystemType = SystemTypeUnknown
		healthOptions.LocalPortChecks = nil
	}
	f.health = runNetworkHealthChecks(f.ctx, healthOptions)
	if f.suppressPVEChecks {
		f.health.add("PVE service checks", networkHealthOK, "skipped (cluster database restore in progress; services will be restarted after restore completes)")
	}
	logNetworkHealthReport(f.logger, f.health)
	if f.diagnosticsDir == "" {
		return
	}
	if path, err := writeNetworkHealthReportFile(f.diagnosticsDir, f.health); err != nil {
		f.debug("Failed to write network health report: %v", err)
	} else {
		f.debug("Network health report: %s", path)
	}
}

func (f *networkRollbackUIApplyFlow) waitForCommit() error {
	remaining := f.handle.remaining(time.Now())
	if remaining <= 0 {
		f.warning("Rollback window already expired; leaving rollback armed")
		return nil
	}

	logging.DebugStep(f.logger, "network safe apply (ui)", "Wait for COMMIT (rollback in %ds)", int(remaining.Seconds()))
	committed, commitErr := f.ui.PromptNetworkCommit(f.ctx, remaining, f.health, f.nicRepair, f.diagnosticsDir)
	if commitErr != nil {
		f.warning("Commit prompt error: %v", commitErr)
		return buildNetworkApplyNotCommittedError(f.ctx, f.logger, f.iface, f.handle)
	}
	logging.DebugStep(f.logger, "network safe apply (ui)", "User commit result: committed=%v", committed)
	if committed {
		return f.commitNetworkConfig()
	}
	return f.handleNetworkNotCommitted()
}

func (f *networkRollbackUIApplyFlow) commitNetworkConfig() error {
	if rollbackAlreadyRunning(f.ctx, f.logger, f.handle) {
		f.warning("Commit received too late: rollback already running. Network configuration NOT committed.")
		return buildNetworkApplyNotCommittedError(f.ctx, f.logger, f.iface, f.handle)
	}
	disarmNetworkRollback(f.ctx, f.logger, f.handle)
	f.info("Network configuration committed successfully.")
	return nil
}

func (f *networkRollbackUIApplyFlow) handleNetworkNotCommitted() error {
	notCommittedErr := buildNetworkApplyNotCommittedError(f.ctx, f.logger, f.iface, f.handle)
	f.showNetworkNotCommittedMessage(notCommittedErr)
	return notCommittedErr
}

func (f *networkRollbackUIApplyFlow) showNetworkNotCommittedMessage(notCommittedErr *NetworkApplyNotCommittedError) {
	if strings.TrimSpace(f.diagnosticsDir) == "" {
		return
	}
	message := networkNotCommittedMessage(f.diagnosticsDir, notCommittedErr)
	_ = f.ui.ShowMessage(f.ctx, "Network rollback", message)
}

func networkNotCommittedMessage(diagnosticsDir string, notCommittedErr *NetworkApplyNotCommittedError) string {
	rollbackState := "Rollback is ARMED and will run automatically."
	if notCommittedErr != nil && !notCommittedErr.RollbackArmed {
		rollbackState = "Rollback has executed (or marker cleared)."
	}
	observed, original := networkNotCommittedIPs(notCommittedErr)
	reconnectHost := reconnectHostFromOriginalIP(original)

	var b strings.Builder
	b.WriteString("Network configuration not committed.\n\n")
	b.WriteString(rollbackState + "\n\n")
	fmt.Fprintf(&b, "IP now (after apply): %s\n", observed)
	if original != "unknown" {
		fmt.Fprintf(&b, "Expected after rollback: %s\n", original)
	}
	if reconnectHost != "" && reconnectHost != "unknown" {
		fmt.Fprintf(&b, "Reconnect using: %s\n", reconnectHost)
	}
	b.WriteString("\nDiagnostics saved under:\n")
	b.WriteString(strings.TrimSpace(diagnosticsDir))
	return b.String()
}

func networkNotCommittedIPs(notCommittedErr *NetworkApplyNotCommittedError) (string, string) {
	observed := "unknown"
	original := "unknown"
	if notCommittedErr == nil {
		return observed, original
	}
	if v := strings.TrimSpace(notCommittedErr.RestoredIP); v != "" {
		observed = v
	}
	if v := strings.TrimSpace(notCommittedErr.OriginalIP); v != "" {
		original = v
	}
	return observed, original
}

func reconnectHostFromOriginalIP(original string) string {
	if original == "" || original == "unknown" {
		return ""
	}
	reconnectHost := original
	if i := strings.Index(reconnectHost, ","); i >= 0 {
		reconnectHost = reconnectHost[:i]
	}
	if i := strings.Index(reconnectHost, "/"); i >= 0 {
		reconnectHost = reconnectHost[:i]
	}
	return strings.TrimSpace(reconnectHost)
}

func (f *networkRollbackUIApplyFlow) debug(format string, args ...interface{}) {
	if f.logger != nil {
		f.logger.Debug(format, args...)
	}
}

func (f *networkRollbackUIApplyFlow) info(format string, args ...interface{}) {
	if f.logger != nil {
		f.logger.Info(format, args...)
	}
}

func (f *networkRollbackUIApplyFlow) warning(format string, args ...interface{}) {
	if f.logger != nil {
		f.logger.Warning(format, args...)
	}
}

func (f *networkRollbackUIApplyFlow) error(format string, args ...interface{}) {
	if f.logger != nil {
		f.logger.Error(format, args...)
	}
}
