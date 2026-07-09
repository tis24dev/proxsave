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

// supportStartAction is the yes/no choice on the final confirm step of the support form.
type supportStartAction int

const (
	supportStartGo supportStartAction = iota
	supportStartCancel
)

// runDashboardSupportForm collects, GRAPHICALLY, the GitHub metadata the CLI support intro
// (support.RunIntro) reads from stdin: the GitHub nickname, then the issue id (#1234) whose
// screen carries a concise consent note (the DEBUG log — which may contain sensitive data,
// e.g. this server's MAC — is sent to the maintainer, and the issue must already be open),
// then a final confirmation. It returns (meta, true) only when the user confirms; esc /
// Cancel at any step returns (_, false) so the caller loops back to the menu. The maintainer
// email address is never shown.
func runDashboardSupportForm(ctx context.Context, session *shell.Session) (support.Meta, bool) {
	errBack := errors.New("support: back")

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
		"Support", "Enter the GitHub issue number (#1234).",
		components.WithPlaceholder("#1234"),
		components.WithNote("Sends the DEBUG log (may contain sensitive data, e.g. this server's MAC) to the maintainer. The issue must already be open."),
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

	summary := theme.Text.Render(fmt.Sprintf("Start the support run in DEBUG?\n\nGitHub: %s\nIssue: %s", meta.GitHubUser, meta.IssueID))
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
