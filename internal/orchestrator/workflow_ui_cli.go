package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/components"
)

// cliIdleTimeout bounds each interactive restore/decrypt/age-setup read; on idle the
// read aborts gracefully (zero mutation - it fires at a pre-write confirmation gate).
// Var so tests can shrink it.
var cliIdleTimeout = input.DefaultIdleTimeout

type cliWorkflowUI struct {
	reader *bufio.Reader
	logger *logging.Logger
	out    io.Writer // human-facing prompts/menus; defaults to os.Stderr
}

func newCLIWorkflowUI(reader *bufio.Reader, logger *logging.Logger) *cliWorkflowUI {
	if reader == nil {
		reader = bufio.NewReader(os.Stdin)
	}
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	return &cliWorkflowUI{reader: reader, logger: logger, out: os.Stderr}
}

// w returns the human-facing output sink (stderr by default). Restore/decrypt emit
// no machine-readable stdout, so all prompts and menus go to stderr, keeping stdout clean.
func (u *cliWorkflowUI) w() io.Writer {
	if u.out == nil {
		return os.Stderr
	}
	return u.out
}

func (u *cliWorkflowUI) RunTask(ctx context.Context, title, initialMessage string, run func(ctx context.Context, report ProgressReporter) error) error {
	// Scrub every dynamic value before it reaches the terminal: progress
	// messages can embed remote/archive filenames (e.g. rclone lsf entries).
	title = strings.TrimSpace(title)
	if title != "" {
		fmt.Fprintf(u.w(), "%s\n", components.SanitizeLine(title))
	}
	initialMessage = strings.TrimSpace(initialMessage)
	if initialMessage != "" {
		fmt.Fprintf(u.w(), "%s\n", components.SanitizeLine(initialMessage))
	}

	var lastPrinted time.Time
	var lastMessage string
	report := func(message string) {
		message = strings.TrimSpace(message)
		if message == "" {
			return
		}
		now := time.Now()
		if message == lastMessage && now.Sub(lastPrinted) < 2*time.Second {
			return
		}
		lastPrinted = now
		lastMessage = message
		fmt.Fprintf(u.w(), "%s\n", components.SanitizeLine(message))
	}

	return run(ctx, report)
}

func (u *cliWorkflowUI) ShowMessage(ctx context.Context, title, message string) error {
	// title/message can carry free-form external text: scrub escape/control bytes
	// before printing to the terminal (same policy as the styled dashboard path).
	if t := strings.TrimSpace(components.SanitizeText(title)); t != "" {
		fmt.Fprintf(u.w(), "\n%s\n", t)
	}
	if m := strings.TrimSpace(components.SanitizeText(message)); m != "" {
		fmt.Fprintln(u.w(), m)
	}
	return nil
}

func (u *cliWorkflowUI) ShowStatusResult(ctx context.Context, screenTitle string, level HealthcheckSetupLevel, keyword, explanation string) error {
	// Non-fatal outcome (e.g. an empty-state): routed through u.w() like ShowMessage.
	// keyword/explanation can embed external text (e.g. rclone output in a scan
	// error), so scrub them before they reach the terminal.
	if t := strings.TrimSpace(components.SanitizeText(screenTitle)); t != "" {
		fmt.Fprintf(u.w(), "\n%s\n", t)
	}
	if kw := strings.TrimSpace(components.SanitizeText(keyword)); kw != "" {
		fmt.Fprintf(u.w(), "Status: %s\n", kw)
	}
	if exp := strings.TrimSpace(components.SanitizeText(explanation)); exp != "" {
		fmt.Fprintln(u.w(), exp)
	}
	return nil
}

func (u *cliWorkflowUI) ShowError(ctx context.Context, title, message string) error {
	if t := strings.TrimSpace(components.SanitizeText(title)); t != "" {
		fmt.Fprintf(u.w(), "\n%s\n", t)
	}
	if m := strings.TrimSpace(components.SanitizeText(message)); m != "" {
		fmt.Fprintln(u.w(), m)
	}
	return nil
}

func (u *cliWorkflowUI) SelectBackupSource(ctx context.Context, options []decryptPathOption) (decryptPathOption, error) {
	return promptPathSelection(ctx, u.reader, options)
}

func (u *cliWorkflowUI) SelectBackupCandidate(ctx context.Context, candidates []*backupCandidate) (*backupCandidate, error) {
	return promptCandidateSelection(ctx, u.reader, candidates)
}

func (u *cliWorkflowUI) PromptDestinationDir(ctx context.Context, defaultDir string) (string, error) {
	defaultDir = strings.TrimSpace(defaultDir)
	if defaultDir == "" {
		defaultDir = "./decrypt"
	}
	fmt.Fprintf(u.w(), "Enter destination directory (default: %s): ", components.SanitizeLine(defaultDir))
	line, err := input.ReadLineWithIdle(ctx, u.reader, cliIdleTimeout)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		trimmed = defaultDir
	}
	return filepath.Clean(trimmed), nil
}

func (u *cliWorkflowUI) ResolveExistingPath(ctx context.Context, path, description, failure string) (ExistingPathDecision, string, error) {
	if strings.TrimSpace(failure) != "" {
		fmt.Fprintf(u.w(), "%s\n", components.SanitizeText(strings.TrimSpace(failure)))
	}

	current := filepath.Clean(path)
	desc := strings.TrimSpace(description)
	if desc == "" {
		desc = "file"
	}

	fmt.Fprintf(u.w(), "%s %s already exists.\n", titleCaser.String(desc), components.SanitizeLine(current))
	fmt.Fprintln(u.w(), "  [1] Overwrite")
	fmt.Fprintln(u.w(), "  [2] Enter a different path")
	fmt.Fprintln(u.w(), "  [0] Exit")

	for {
		fmt.Fprint(u.w(), "Choice: ")
		inputLine, err := input.ReadLineWithIdle(ctx, u.reader, cliIdleTimeout)
		if err != nil {
			return PathDecisionCancel, "", err
		}
		switch strings.TrimSpace(inputLine) {
		case "1":
			return PathDecisionOverwrite, "", nil
		case "2":
			fmt.Fprint(u.w(), "Enter new path: ")
			newPath, err := input.ReadLineWithIdle(ctx, u.reader, cliIdleTimeout)
			if err != nil {
				return PathDecisionCancel, "", err
			}
			trimmed, err := validateDistinctNewPathInput(newPath, current)
			if err != nil {
				fmt.Fprintln(u.w(), components.SanitizeText(err.Error()))
				continue
			}
			return PathDecisionNewPath, filepath.Clean(trimmed), nil
		case "0":
			return PathDecisionCancel, "", ErrDecryptAborted
		default:
			fmt.Fprintln(u.w(), "Please enter 1, 2 or 0.")
		}
	}
}

func (u *cliWorkflowUI) PromptDecryptSecret(ctx context.Context, displayName, previousError string) (string, error) {
	if strings.TrimSpace(previousError) != "" {
		fmt.Fprintln(u.w(), components.SanitizeText(strings.TrimSpace(previousError)))
	}

	displayName = strings.TrimSpace(displayName)
	if displayName != "" {
		// displayName is the manifest archive filename (cand.DisplayBase): scrub it.
		fmt.Fprintf(u.w(), "Enter decryption key or passphrase for %s (0 = exit): ", components.SanitizeLine(displayName))
	} else {
		fmt.Fprint(u.w(), "Enter decryption key or passphrase (0 = exit): ")
	}

	inputBytes, err := input.ReadPasswordWithIdle(ctx, readPassword, int(os.Stdin.Fd()), cliIdleTimeout)
	fmt.Fprintln(u.w())
	if err != nil {
		return "", err
	}

	trimmed := strings.TrimSpace(string(inputBytes))
	zeroBytes(inputBytes)

	if trimmed == "" {
		return "", nil
	}
	if trimmed == "0" {
		return "", ErrDecryptAborted
	}
	return trimmed, nil
}

func (u *cliWorkflowUI) SelectRestoreMode(ctx context.Context, systemType SystemType) (RestoreMode, error) {
	return ShowRestoreModeMenuWithReader(ctx, u.reader, u.logger, systemType)
}

func (u *cliWorkflowUI) SelectCategories(ctx context.Context, available []Category, systemType SystemType) ([]Category, error) {
	return ShowCategorySelectionMenuWithReader(ctx, u.reader, u.logger, available, systemType)
}

func (u *cliWorkflowUI) SelectPBSRestoreBehavior(ctx context.Context) (PBSRestoreBehavior, error) {
	fmt.Fprintln(u.w())
	fmt.Fprintln(u.w(), "PBS restore reconciliation:")
	fmt.Fprintln(u.w(), "  [1] Merge (existing PBS) - Restore onto an already operational PBS (avoids API-side deletions of existing PBS objects not in the backup).")
	fmt.Fprintln(u.w(), "  [2] Clean 1:1 (fresh PBS install) - Restore onto a new, clean PBS and try to make configuration match the backup (may remove existing PBS objects not in the backup).")
	fmt.Fprintln(u.w(), "  [0] Exit")

	for {
		fmt.Fprint(u.w(), "Choice: ")
		line, err := input.ReadLineWithIdle(ctx, u.reader, cliIdleTimeout)
		if err != nil {
			return PBSRestoreBehaviorUnspecified, err
		}
		switch strings.TrimSpace(line) {
		case "1":
			return PBSRestoreBehaviorMerge, nil
		case "2":
			return PBSRestoreBehaviorClean, nil
		case "0":
			return PBSRestoreBehaviorUnspecified, ErrRestoreAborted
		default:
			fmt.Fprintln(u.w(), "Please enter 1, 2 or 0.")
		}
	}
}

func (u *cliWorkflowUI) ShowRestorePlan(ctx context.Context, config *SelectiveRestoreConfig) error {
	ShowRestorePlan(u.logger, config)
	return nil
}

func (u *cliWorkflowUI) ConfirmRestore(ctx context.Context) (bool, error) {
	confirmed, err := ConfirmRestoreOperationWithReader(ctx, u.reader, u.logger)
	if err != nil {
		return false, err
	}
	if !confirmed {
		return false, nil
	}

	fmt.Fprintln(u.w())
	fmt.Fprint(u.w(), "This operation will overwrite existing configuration files on this system.\n\nProceed with overwrite? (yes/no): ")
	for {
		line, err := input.ReadLineWithIdle(ctx, u.reader, cliIdleTimeout)
		if err != nil {
			return false, err
		}
		switch strings.TrimSpace(strings.ToLower(line)) {
		case "yes", "y":
			return true, nil
		case "no", "n", "":
			return false, nil
		default:
			fmt.Fprint(u.w(), "Please type 'yes' or 'no': ")
		}
	}
}

func (u *cliWorkflowUI) ConfirmCompatibility(ctx context.Context, warning error) (bool, error) {
	fmt.Fprintln(u.w())
	fmt.Fprintf(u.w(), "⚠ %s\n\n", components.SanitizeText(fmt.Sprint(warning)))
	fmt.Fprint(u.w(), "Do you want to continue anyway? This may cause system instability. (yes/no): ")

	line, err := input.ReadLineWithIdle(ctx, u.reader, cliIdleTimeout)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(strings.ToLower(line)) == "yes", nil
}

func (u *cliWorkflowUI) SelectClusterRestoreMode(ctx context.Context) (ClusterRestoreMode, error) {
	choice, err := promptClusterRestoreMode(ctx, u.reader)
	if err != nil {
		return ClusterRestoreAbort, err
	}
	switch choice {
	case 1:
		return ClusterRestoreSafe, nil
	case 2:
		return ClusterRestoreRecovery, nil
	default:
		return ClusterRestoreAbort, nil
	}
}

func (u *cliWorkflowUI) ConfirmContinueWithoutSafetyBackup(ctx context.Context, cause error) (bool, error) {
	fmt.Fprintln(u.w())
	fmt.Fprintf(u.w(), "Safety backup failed: %s\n", components.SanitizeText(fmt.Sprint(cause)))
	fmt.Fprint(u.w(), "Continue without safety backup? (yes/no): ")
	line, err := input.ReadLineWithIdle(ctx, u.reader, cliIdleTimeout)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(strings.ToLower(line)) == "yes", nil
}

func (u *cliWorkflowUI) ConfirmContinueWithPBSServicesRunning(ctx context.Context) (bool, error) {
	fmt.Fprintln(u.w())
	fmt.Fprintln(u.w(), "⚠ PBS services are still running. Continuing restore may lead to inconsistent state.")
	return promptYesNo(ctx, u.reader, "Continue restore with PBS services still running? (y/N): ")
}

func (u *cliWorkflowUI) ConfirmFstabMerge(ctx context.Context, title, message string, timeout time.Duration, defaultYes bool) (bool, error) {
	title = strings.TrimSpace(title)
	if title != "" {
		fmt.Fprintf(u.w(), "\n%s\n", components.SanitizeLine(title))
	}
	message = strings.TrimSpace(message)
	if message != "" {
		fmt.Fprintln(u.w(), components.SanitizeText(message))
		fmt.Fprintln(u.w())
	}
	return promptYesNoWithCountdown(ctx, u.reader, u.logger, "Apply fstab merge?", timeout, defaultYes)
}

func (u *cliWorkflowUI) SelectExportNode(ctx context.Context, exportRoot, currentNode string, exportNodes []string) (string, error) {
	return promptExportNodeSelection(ctx, u.reader, exportRoot, currentNode, exportNodes)
}

func (u *cliWorkflowUI) ConfirmApplyVMConfigs(ctx context.Context, sourceNode, currentNode string, count int) (bool, error) {
	fmt.Fprintln(u.w())
	// sourceNode is a node directory name read from inside the backup archive;
	// scrub it (and currentNode, for parity) before printing to the terminal.
	src := components.SanitizeLine(sourceNode)
	cur := components.SanitizeLine(currentNode)
	if strings.TrimSpace(sourceNode) == strings.TrimSpace(currentNode) {
		fmt.Fprintf(u.w(), "Found %d VM/CT configs for node %s\n", count, cur)
	} else {
		fmt.Fprintf(u.w(), "Found %d VM/CT configs for exported node %s (will apply to current node %s)\n", count, src, cur)
	}
	return promptYesNo(ctx, u.reader, "Apply all VM/CT configs via pvesh? (y/N): ")
}

func (u *cliWorkflowUI) ConfirmApplyStorageCfg(ctx context.Context, storageCfgPath string) (bool, error) {
	fmt.Fprintln(u.w())
	fmt.Fprintf(u.w(), "Storage configuration found: %s\n", components.SanitizeLine(strings.TrimSpace(storageCfgPath)))
	return promptYesNo(ctx, u.reader, "Apply storage.cfg via pvesh? (y/N): ")
}

func (u *cliWorkflowUI) ConfirmApplyDatacenterCfg(ctx context.Context, datacenterCfgPath string) (bool, error) {
	fmt.Fprintln(u.w())
	fmt.Fprintf(u.w(), "Datacenter configuration found: %s\n", components.SanitizeLine(strings.TrimSpace(datacenterCfgPath)))
	return promptYesNo(ctx, u.reader, "Apply datacenter.cfg via pvesh? (y/N): ")
}
