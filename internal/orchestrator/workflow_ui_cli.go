package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
)

type cliWorkflowUI struct {
	reader *bufio.Reader
	logger *logging.Logger
}

func newCLIWorkflowUI(reader *bufio.Reader, logger *logging.Logger) *cliWorkflowUI {
	if reader == nil {
		reader = bufio.NewReader(os.Stdin)
	}
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	return &cliWorkflowUI{reader: reader, logger: logger}
}

func (u *cliWorkflowUI) RunTask(ctx context.Context, title, initialMessage string, run func(ctx context.Context, report ProgressReporter) error) error {
	title = strings.TrimSpace(title)
	if title != "" {
		fmt.Fprintf(os.Stderr, "%s\n", title)
	}
	initialMessage = strings.TrimSpace(initialMessage)
	if initialMessage != "" {
		fmt.Fprintf(os.Stderr, "%s\n", initialMessage)
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
		fmt.Fprintf(os.Stderr, "%s\n", message)
	}

	return run(ctx, report)
}

func (u *cliWorkflowUI) ShowMessage(ctx context.Context, title, message string) error {
	if strings.TrimSpace(title) != "" {
		fmt.Printf("\n%s\n", title)
	}
	if strings.TrimSpace(message) != "" {
		fmt.Println(message)
	}
	return nil
}

func (u *cliWorkflowUI) ShowError(ctx context.Context, title, message string) error {
	if strings.TrimSpace(title) != "" {
		fmt.Fprintf(os.Stderr, "\n%s\n", title)
	}
	if strings.TrimSpace(message) != "" {
		fmt.Fprintln(os.Stderr, message)
	}
	return nil
}

func (u *cliWorkflowUI) SelectBackupSource(ctx context.Context, options []decryptPathOption) (decryptPathOption, error) {
	return promptPathSelection(ctx, u.reader, options)
}

func (u *cliWorkflowUI) SelectBackupCandidate(ctx context.Context, candidates []*decryptCandidate) (*decryptCandidate, error) {
	return promptCandidateSelection(ctx, u.reader, candidates)
}

func (u *cliWorkflowUI) PromptDestinationDir(ctx context.Context, defaultDir string) (string, error) {
	defaultDir = strings.TrimSpace(defaultDir)
	if defaultDir == "" {
		defaultDir = "./decrypt"
	}
	fmt.Printf("Enter destination directory (default: %s): ", defaultDir)
	line, err := input.ReadLineWithContext(ctx, u.reader)
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
		fmt.Fprintf(os.Stderr, "%s\n", strings.TrimSpace(failure))
	}

	current := filepath.Clean(path)
	desc := strings.TrimSpace(description)
	if desc == "" {
		desc = "file"
	}

	fmt.Printf("%s %s already exists.\n", titleCaser.String(desc), current)
	fmt.Println("  [1] Overwrite")
	fmt.Println("  [2] Enter a different path")
	fmt.Println("  [0] Exit")

	for {
		fmt.Print("Choice: ")
		inputLine, err := input.ReadLineWithContext(ctx, u.reader)
		if err != nil {
			return PathDecisionCancel, "", err
		}
		switch strings.TrimSpace(inputLine) {
		case "1":
			return PathDecisionOverwrite, "", nil
		case "2":
			fmt.Print("Enter new path: ")
			newPath, err := input.ReadLineWithContext(ctx, u.reader)
			if err != nil {
				return PathDecisionCancel, "", err
			}
			trimmed := strings.TrimSpace(newPath)
			if trimmed == "" {
				continue
			}
			return PathDecisionNewPath, filepath.Clean(trimmed), nil
		case "0":
			return PathDecisionCancel, "", ErrDecryptAborted
		default:
			fmt.Println("Please enter 1, 2 or 0.")
		}
	}
}

func (u *cliWorkflowUI) PromptDecryptSecret(ctx context.Context, displayName, previousError string) (string, error) {
	if strings.TrimSpace(previousError) != "" {
		fmt.Fprintln(os.Stderr, strings.TrimSpace(previousError))
	}

	displayName = strings.TrimSpace(displayName)
	if displayName != "" {
		fmt.Printf("Enter decryption key or passphrase for %s (0 = exit): ", displayName)
	} else {
		fmt.Print("Enter decryption key or passphrase (0 = exit): ")
	}

	inputBytes, err := input.ReadPasswordWithContext(ctx, readPassword, int(os.Stdin.Fd()))
	fmt.Println()
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

	fmt.Println()
	fmt.Print("This operation will overwrite existing configuration files on this system.\n\nProceed with overwrite? (yes/no): ")
	for {
		line, err := input.ReadLineWithContext(ctx, u.reader)
		if err != nil {
			return false, err
		}
		switch strings.TrimSpace(strings.ToLower(line)) {
		case "yes", "y":
			return true, nil
		case "no", "n", "":
			return false, nil
		default:
			fmt.Print("Please type 'yes' or 'no': ")
		}
	}
}

func (u *cliWorkflowUI) ConfirmCompatibility(ctx context.Context, warning error) (bool, error) {
	fmt.Println()
	fmt.Printf("⚠ %v\n\n", warning)
	fmt.Print("Do you want to continue anyway? This may cause system instability. (yes/no): ")

	line, err := input.ReadLineWithContext(ctx, u.reader)
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
	fmt.Println()
	fmt.Printf("Safety backup failed: %v\n", cause)
	fmt.Print("Continue without safety backup? (yes/no): ")
	line, err := input.ReadLineWithContext(ctx, u.reader)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(strings.ToLower(line)) == "yes", nil
}

func (u *cliWorkflowUI) ConfirmContinueWithPBSServicesRunning(ctx context.Context) (bool, error) {
	fmt.Println()
	fmt.Println("⚠ PBS services are still running. Continuing restore may lead to inconsistent state.")
	return promptYesNo(ctx, u.reader, "Continue restore with PBS services still running? (y/N): ")
}

func (u *cliWorkflowUI) ConfirmFstabMerge(ctx context.Context, title, message string, timeout time.Duration, defaultYes bool) (bool, error) {
	title = strings.TrimSpace(title)
	if title != "" {
		fmt.Printf("\n%s\n", title)
	}
	message = strings.TrimSpace(message)
	if message != "" {
		fmt.Println(message)
		fmt.Println()
	}
	return promptYesNoWithCountdown(ctx, u.reader, u.logger, "Apply fstab merge?", timeout, defaultYes)
}

func (u *cliWorkflowUI) SelectExportNode(ctx context.Context, exportRoot, currentNode string, exportNodes []string) (string, error) {
	return promptExportNodeSelection(ctx, u.reader, exportRoot, currentNode, exportNodes)
}

func (u *cliWorkflowUI) ConfirmApplyVMConfigs(ctx context.Context, sourceNode, currentNode string, count int) (bool, error) {
	fmt.Println()
	if strings.TrimSpace(sourceNode) == strings.TrimSpace(currentNode) {
		fmt.Printf("Found %d VM/CT configs for node %s\n", count, currentNode)
	} else {
		fmt.Printf("Found %d VM/CT configs for exported node %s (will apply to current node %s)\n", count, sourceNode, currentNode)
	}
	return promptYesNo(ctx, u.reader, "Apply all VM/CT configs via pvesh? (y/N): ")
}

func (u *cliWorkflowUI) ConfirmApplyStorageCfg(ctx context.Context, storageCfgPath string) (bool, error) {
	fmt.Println()
	fmt.Printf("Storage configuration found: %s\n", strings.TrimSpace(storageCfgPath))
	return promptYesNo(ctx, u.reader, "Apply storage.cfg via pvesh? (y/N): ")
}

func (u *cliWorkflowUI) ConfirmApplyDatacenterCfg(ctx context.Context, datacenterCfgPath string) (bool, error) {
	fmt.Println()
	fmt.Printf("Datacenter configuration found: %s\n", strings.TrimSpace(datacenterCfgPath))
	return promptYesNo(ctx, u.reader, "Apply datacenter.cfg via pvesh? (y/N): ")
}
