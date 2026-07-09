package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"charm.land/huh/v2"

	"github.com/tis24dev/proxsave/internal/support"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// dashboardRunSupportForm is the seam so the dispatch can be tested without driving the
// full graphical form. Production points it at runDashboardSupportForm.
var dashboardRunSupportForm = runDashboardSupportForm

// runDashboardSupportForm shows a SINGLE screen (one huh form) with everything: a concise
// consent note, the GitHub nickname, the GitHub issue (#1234), and a Start button. It
// returns (meta, true) only when the user picks Start; esc / Cancel returns (_, false) so
// the caller loops back to the menu. The maintainer email address is never shown.
func runDashboardSupportForm(ctx context.Context, session *shell.Session) (support.Meta, bool) {
	errBack := errors.New("support: back")
	var (
		nickname string
		issue    string
		start    bool
	)
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().Description(
				"Runs a backup in DEBUG and sends its log to the maintainer for support.\n"+
					"The log may contain sensitive data (e.g. this server's MAC). The GitHub issue must already be open."),
			huh.NewInput().Title("GitHub nickname").Value(&nickname).Validate(func(v string) error {
				if strings.TrimSpace(v) == "" {
					return fmt.Errorf("nickname cannot be empty")
				}
				return nil
			}),
			huh.NewInput().Title("GitHub issue").Placeholder("#1234").Value(&issue).Validate(validateSupportIssue),
			huh.NewConfirm().Title("Start the support run in DEBUG?").Affirmative("Start").Negative("Cancel").Value(&start),
		),
	)
	if _, err := shell.Ask(ctx, session, components.NewFormScreen("Support", form, components.WithFormBack(errBack))); err != nil {
		return support.Meta{}, false // esc / back / abort
	}
	if !start {
		return support.Meta{}, false // chose Cancel on the Start confirm
	}
	return support.Meta{
		GitHubUser: strings.TrimSpace(nickname),
		IssueID:    strings.TrimSpace(issue),
	}, true
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
