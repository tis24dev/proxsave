// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/input"
)

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
	return promptYesNoTUIFunc(ctx, title, u.configPath, u.buildSig, message, yesLabel, noLabel)
}

func (u *tuiWorkflowUI) RepairNICNames(ctx context.Context, archivePath string) (*nicRepairResult, error) {
	return maybeRepairNICNamesTUI(ctx, u.logger, archivePath, u.configPath, u.buildSig), nil
}

func (u *tuiWorkflowUI) PromptNetworkCommit(ctx context.Context, remaining time.Duration, health networkHealthReport, nicRepair *nicRepairResult, diagnosticsDir string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	committed, err := promptNetworkCommitTUI(ctx, remaining, health, nicRepair, diagnosticsDir, u.configPath, u.buildSig)
	if err != nil && errors.Is(err, input.ErrInputAborted) {
		return false, err
	}
	return committed, err
}
