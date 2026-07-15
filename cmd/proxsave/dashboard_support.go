package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/tis24dev/proxsave/internal/support"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// dashboardRunSupportForm is the seam so the dispatch can be tested without driving the
// full graphical form. Production points it at runDashboardSupportForm.
var dashboardRunSupportForm = runDashboardSupportForm

// supportConsentAction / supportStartAction are the yes/no choices on the two selector
// steps of the support form.
type supportConsentAction int

const (
	supportConsentContinue supportConsentAction = iota
	supportConsentCancel
)

type supportStartAction int

const (
	supportStartGo supportStartAction = iota
	supportStartCancel
)

// runDashboardSupportForm collects, GRAPHICALLY, the same consent + GitHub metadata the CLI
// support intro (support.RunIntro) reads from stdin: a consent step (the DEBUG log — which
// may contain sensitive data, including this server's MAC address — is emailed to the
// maintainer, and a GitHub issue must already be open), the GitHub nickname, the issue id
// (#1234), and a final confirmation. It returns (meta, true) only when the user confirms;
// esc / Cancel at any step returns (_, false) so the caller loops back to the menu.
func runDashboardSupportForm(ctx context.Context, session *shell.Session) (support.Meta, bool) {
	errBack := errors.New("support: back")

	consentPrompt := theme.Text.Render(
		"Support mode runs a backup in DEBUG and, at the end, emails the full log to " +
			"github-support@tis24.it.\n\nThe log may contain personal or sensitive data (including this " +
			"server's MAC address); by continuing you agree to share it.\n\nYou must already have an open " +
			"GitHub issue for this problem — emails without one are not analyzed.")
	consent, err := shell.Ask(ctx, session, components.NewSelector(
		"Support",
		[]components.SelectorItem[supportConsentAction]{
			{Label: "Continue", Description: "accept and enter the issue details", Value: supportConsentContinue},
			{Label: "Cancel", Description: "return to the dashboard menu", Value: supportConsentCancel},
		},
		components.WithSelectorPromptStyled[supportConsentAction](consentPrompt),
		components.WithSelectorBack[supportConsentAction](errBack),
	))
	if err != nil || consent != supportConsentContinue {
		return support.Meta{}, false
	}

	nickname, err := shell.Ask(ctx, session, components.NewInput(
		"Support", "Enter your GitHub nickname.",
		components.WithValidate(func(v string) error {
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("nickname cannot be empty")
			}
			return nil
		}),
		components.WithInputBack(errBack),
	))
	if err != nil {
		return support.Meta{}, false
	}

	issue, err := shell.Ask(ctx, session, components.NewInput(
		"Support", "Enter the GitHub issue number in the format #1234.",
		components.WithPlaceholder("#1234"),
		components.WithValidate(validateSupportIssue),
		components.WithInputBack(errBack),
	))
	if err != nil {
		return support.Meta{}, false
	}

	meta := support.Meta{
		GitHubUser: strings.TrimSpace(nickname),
		IssueID:    strings.TrimSpace(issue),
	}

	summary := theme.Text.Render(fmt.Sprintf(
		"Start the support run?\n\nGitHub: %s\nIssue: %s\n\nThe run executes in DEBUG and the log (including "+
			"sensitive data / MAC) is emailed to github-support@tis24.it at the end.",
		meta.GitHubUser, meta.IssueID))
	start, err := shell.Ask(ctx, session, components.NewSelector(
		"Support",
		[]components.SelectorItem[supportStartAction]{
			{Label: "Start support run", Description: "run the backup in support mode now", Value: supportStartGo},
			{Label: "Cancel", Description: "return to the dashboard menu", Value: supportStartCancel},
		},
		components.WithSelectorPromptStyled[supportStartAction](summary),
		components.WithSelectorBack[supportStartAction](errBack),
	))
	if err != nil || start != supportStartGo {
		return support.Meta{}, false
	}
	return meta, true
}

// validateSupportIssue enforces the #<number> issue format (mirrors support.RunIntro).
func validateSupportIssue(v string) error {
	issue := strings.TrimSpace(v)
	if issue == "" {
		return fmt.Errorf("issue cannot be empty")
	}
	if !strings.HasPrefix(issue, "#") || len(issue) < 2 {
		return fmt.Errorf("issue must start with '#' and a numeric id, e.g. #1234")
	}
	if _, err := strconv.Atoi(issue[1:]); err != nil {
		return fmt.Errorf("issue must be #<number>, e.g. #1234")
	}
	return nil
}
