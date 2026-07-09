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
)

// dashboardRunSupportForm is the seam so the dispatch can be tested without driving the
// full graphical form. Production points it at runDashboardSupportForm.
var dashboardRunSupportForm = runDashboardSupportForm

// runDashboardSupportForm shows the SAME single-screen grid form as the installer's
// configuration screen (components.FormGrid). A consent note sits ABOVE the two fields
// (always visible, one clause per line): the full run log is captured in DEBUG detail and
// emailed to the maintainer, and it may contain personal data such as this server's MAC.
// Below it are the GitHub nickname and the GitHub issue (#1234), each with a concise
// focused hint, plus the shared Continue / Cancel buttons. It returns (meta, true) only on
// Continue; esc / Cancel returns (_, false) so the caller loops back to the menu. The
// maintainer email address is never shown.
func runDashboardSupportForm(ctx context.Context, session *shell.Session) (support.Meta, bool) {
	errBack := errors.New("support: back")

	nickname := &components.FormField{
		Label:       "GitHub nickname",
		Description: "Your GitHub nickname for the support request.",
		Kind:        components.FieldText,
		Validate: func(v string) error {
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("nickname cannot be empty")
			}
			return nil
		},
	}
	issue := &components.FormField{
		Label:       "GitHub issue",
		Description: "The issue number, e.g. #1234 — it must already be open on GitHub.",
		Kind:        components.FieldText,
		Validate:    validateSupportIssue,
	}

	fields := []*components.FormField{nickname, issue}
	if _, err := shell.Ask(ctx, session, components.NewFormGrid(
		"Support", fields,
		components.WithFormGridNote(
			"The full run log is captured in DEBUG detail and emailed to the maintainer for support.",
			"It may contain personal data — for example this server's MAC address, hostnames or paths.",
			"Continue only if you consent to sharing it.",
		),
		components.WithFormGridBack(errBack),
	)); err != nil {
		return support.Meta{}, false // esc / Cancel / abort
	}
	return support.Meta{
		GitHubUser: strings.TrimSpace(nickname.Text),
		IssueID:    strings.TrimSpace(issue.Text),
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
