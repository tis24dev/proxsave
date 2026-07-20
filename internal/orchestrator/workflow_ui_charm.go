package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// charmWorkflowUI implements the workflow UI interfaces on top of the Charm
// shell. It contains no rendering code: every method maps engine data to
// component screens and blocking shell.Ask calls against one long-lived
// Session per mode. Behavioral parity notes reference the tview
// implementation it replaces (workflow_ui_tui_*.go).
type charmWorkflowUI struct {
	session  *shell.Session
	logger   *logging.Logger
	abortErr error // flow-specific abort sentinel (e.g. ErrDecryptAborted)

	selectedBackupSummary string
}

func newCharmWorkflowUI(session *shell.Session, logger *logging.Logger, abortErr error) *charmWorkflowUI {
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	return &charmWorkflowUI{session: session, logger: logger, abortErr: abortErr}
}

// mapAbort converts the shell-level abort sentinel (Ctrl+C, Esc) into the
// flow's canonical abort error, leaving everything else untouched.
func (u *charmWorkflowUI) mapAbort(err error) error {
	if shell.IsAbort(err) {
		return u.abortErr
	}
	return err
}

func (u *charmWorkflowUI) RunTask(ctx context.Context, title, initialMessage string, run func(ctx context.Context, report ProgressReporter) error) error {
	return components.RunTask(ctx, u.session, title, initialMessage, func(taskCtx context.Context, report func(string)) error {
		return run(taskCtx, ProgressReporter(report))
	})
}

func (u *charmWorkflowUI) ShowMessage(ctx context.Context, title, message string) error {
	_, err := shell.Ask(ctx, u.session, components.NewNotice(components.NoticeInfo, title, message))
	return u.mapAbort(err)
}

// workflowStatusResultAction is the single choice on a workflow outcome screen: continue
// past the result (mirrors daemonResultAction in cmd/proxsave/dashboard.go).
type workflowStatusResultAction int

const workflowStatusResultActionContinue workflowStatusResultAction = iota

// ShowStatusResult presents a workflow outcome with the SAME look as the daemon / check
// result screens: a styled "Status:" line (a colored keyword + a Subtle explanation) above
// a single Continue item. It mirrors showDaemonResultScreen (single item + WithSelectorBack +
// non-blocking loop): it loops until Continue/esc, returning nil on esc and u.mapAbort(err)
// on a real UI error. This is a Selector, NOT a components.Notice.
func (u *charmWorkflowUI) ShowStatusResult(ctx context.Context, screenTitle string, level HealthcheckSetupLevel, keyword, explanation string) error {
	errWorkflowStatusEsc := errors.New("workflow status: esc")
	prompt := BuildStatusPrompt(level, keyword, explanation)
	items := []components.SelectorItem[workflowStatusResultAction]{
		{Label: "Continue", Description: "continue the workflow", Value: workflowStatusResultActionContinue},
	}
	for {
		action, err := shell.Ask(ctx, u.session, components.NewSelector(
			screenTitle, items,
			components.WithSelectorPromptStyled[workflowStatusResultAction](prompt),
			components.WithSelectorBack[workflowStatusResultAction](errWorkflowStatusEsc),
		))
		if err != nil {
			if errors.Is(err, errWorkflowStatusEsc) {
				return nil
			}
			return u.mapAbort(err)
		}
		switch action {
		case workflowStatusResultActionContinue:
			return nil
		}
	}
}

// ShowError renders a workflow failure with the SAME styled "Status:" selector as
// ShowStatusResult, at Error level, so a failure (e.g. "Network preflight failed")
// reads "Status: ✗ <keyword>" instead of a components.Notice. The keyword is the
// uppercased title, matching the failure look of the other Status screens.
func (u *charmWorkflowUI) ShowError(ctx context.Context, title, message string) error {
	return u.ShowStatusResult(ctx, title, HealthcheckSetupLevelError, strings.ToUpper(title), message)
}

func (u *charmWorkflowUI) SelectBackupSource(ctx context.Context, options []decryptPathOption) (decryptPathOption, error) {
	items := make([]components.SelectorItem[decryptPathOption], 0, len(options))
	for _, opt := range options {
		items = append(items, components.SelectorItem[decryptPathOption]{
			Label:       opt.Label,
			Description: fmt.Sprintf("(%s)", opt.Path),
			Value:       opt,
		})
	}
	selected, err := shell.Ask(ctx, u.session, components.NewSelector(
		"Select backup source", items,
		components.WithSelectorPrompt[decryptPathOption]("Available backup sources"),
		components.WithSelectorBack[decryptPathOption](u.abortErr),
	))
	if err != nil {
		return decryptPathOption{}, u.mapAbort(err)
	}
	return selected, nil
}

// backupCandidateSelectorItems renders candidates as aligned columns, the
// same table layout the tview list used (created, hostname, mode, tool,
// target, compression).
func backupCandidateSelectorItems(candidates []*backupCandidate) []components.SelectorItem[*backupCandidate] {
	displays := make([]backupCandidateDisplay, len(candidates))
	var maxHost, maxMode, maxTool, maxTarget, maxComp int
	for idx, cand := range candidates {
		d := describeBackupCandidate(cand)
		displays[idx] = d
		maxHost = max(maxHost, len(d.Hostname))
		maxMode = max(maxMode, len(d.Mode))
		maxTool = max(maxTool, len(d.Tool))
		maxTarget = max(maxTarget, len(d.Target))
		maxComp = max(maxComp, len(d.Compression))
	}
	items := make([]components.SelectorItem[*backupCandidate], 0, len(candidates))
	for idx, d := range displays {
		line := fmt.Sprintf(
			"%s  %-*s  %-*s  %-*s  %-*s",
			d.Created,
			maxHost, d.Hostname,
			maxMode, d.Mode,
			maxTool, d.Tool,
			maxTarget, d.Target,
		)
		if maxComp > 0 {
			line = fmt.Sprintf("%s  %-*s", line, maxComp, d.Compression)
		}
		items = append(items, components.SelectorItem[*backupCandidate]{
			Label: line,
			Value: candidates[idx],
		})
	}
	return items
}

func (u *charmWorkflowUI) SelectBackupCandidate(ctx context.Context, candidates []*backupCandidate) (*backupCandidate, error) {
	selected, err := shell.Ask(ctx, u.session, components.NewSelector(
		"Select backup", backupCandidateSelectorItems(candidates),
		components.WithSelectorPrompt[*backupCandidate]("Available backups"),
		components.WithSelectorBack[*backupCandidate](u.abortErr),
	))
	if err != nil {
		return nil, u.mapAbort(err)
	}
	u.selectedBackupSummary = backupSummaryForUI(selected)
	return selected, nil
}

func (u *charmWorkflowUI) PromptDestinationDir(ctx context.Context, defaultDir string) (string, error) {
	defaultDir = strings.TrimSpace(defaultDir)
	if defaultDir == "" {
		defaultDir = "./decrypt"
	}
	dest, err := shell.Ask(ctx, u.session, components.NewInput(
		"Destination directory",
		"Directory where the decrypted files will be written.",
		components.WithInitialValue(defaultDir),
		components.WithValidate(func(value string) error {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("destination directory cannot be empty")
			}
			return nil
		}),
	))
	if err != nil {
		return "", u.mapAbort(err)
	}
	return filepath.Clean(strings.TrimSpace(dest)), nil
}

func (u *charmWorkflowUI) ResolveExistingPath(ctx context.Context, path, description, failure string) (ExistingPathDecision, string, error) {
	desc := strings.TrimSpace(description)
	if desc == "" {
		desc = "file"
	}
	prompt := fmt.Sprintf("The %s %s already exists.\nSelect how you want to proceed.", desc, path)
	if strings.TrimSpace(failure) != "" {
		prompt += "\n\n" + strings.TrimSpace(failure)
	}

	items := []components.SelectorItem[ExistingPathDecision]{
		{Label: "Overwrite", Description: "replace the existing " + desc, Value: PathDecisionOverwrite},
		{Label: "Use different path", Description: "enter a new destination path", Value: PathDecisionNewPath},
		{Label: "Cancel", Description: "abort the workflow", Value: PathDecisionCancel},
	}
	decision, err := shell.Ask(ctx, u.session, components.NewSelector(
		"Existing file", items,
		components.WithSelectorPrompt[ExistingPathDecision](prompt),
		components.WithSelectorBack[ExistingPathDecision](u.abortErr),
	))
	if err != nil {
		return PathDecisionCancel, "", u.mapAbort(err)
	}
	if decision != PathDecisionNewPath {
		return decision, "", nil
	}

	newPath, err := shell.Ask(ctx, u.session, components.NewInput(
		"Choose destination path",
		"Provide a writable filesystem path for the decrypted files.",
		components.WithInitialValue(path),
		components.WithValidate(func(value string) error {
			_, err := validateDistinctNewPathInput(value, path)
			return err
		}),
	))
	if err != nil {
		if shell.IsAbort(err) {
			// Parity with tview: cancelling the new-path input falls back
			// to the cancel decision without a hard error; the caller
			// decides how to proceed.
			return PathDecisionCancel, "", nil
		}
		return PathDecisionCancel, "", err
	}
	trimmed := strings.TrimSpace(newPath)
	if trimmed == "" {
		return PathDecisionNewPath, "", nil
	}
	return PathDecisionNewPath, filepath.Clean(trimmed), nil
}

func backupSummaryForUI(cand *backupCandidate) string {
	return describeBackupCandidate(cand).Summary
}

// validateDistinctNewPathInput rejects empty paths and paths equal to the
// existing one (shared by the CLI and Charm new-path prompts).
func validateDistinctNewPathInput(value, defaultPath string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("path cannot be empty")
	}

	trimmedDefault := strings.TrimSpace(defaultPath)
	if trimmedDefault != "" && filepath.Clean(trimmed) == filepath.Clean(trimmedDefault) {
		return "", fmt.Errorf("path must be different from existing path")
	}

	return trimmed, nil
}

func (u *charmWorkflowUI) PromptDecryptSecret(ctx context.Context, displayName, previousError string) (string, error) {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = "selected backup"
	}
	opts := []components.InputOption{
		components.WithSecret(),
		components.WithNote("Enter 0 to exit."),
		components.WithValidate(func(value string) error {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("key or passphrase cannot be empty")
			}
			return nil
		}),
	}
	if strings.TrimSpace(previousError) != "" {
		opts = append(opts, components.WithErrorText(strings.TrimSpace(previousError)))
	}
	secret, err := shell.Ask(ctx, u.session, components.NewInput(
		"Decrypt key",
		fmt.Sprintf("Provide the AGE secret key or passphrase used for %s.", name),
		opts...,
	))
	if err != nil {
		// Parity with tview: the shared secret prompt reports decrypt
		// abort semantics in every flow.
		if shell.IsAbort(err) {
			return "", ErrDecryptAborted
		}
		return "", err
	}
	if strings.TrimSpace(secret) == "0" {
		return "", ErrDecryptAborted
	}
	return secret, nil
}
