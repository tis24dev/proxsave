package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/support"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// dashboardRunSupportForm is the seam so the dispatch can be tested without driving the
// full graphical form. Production points it at runDashboardSupportForm.
var dashboardRunSupportForm = runDashboardSupportForm

// runDashboardSupportForm shows the SAME single-screen grid form as the installer's
// configuration screen (components.FormGrid), carrying the SAME consent as the stdin
// flow (support.RunIntro): the shared disclosure and gate detail sit above the fields
// as an always-visible note, and each shared gate gets its own toggle row that must be
// set to Yes before Continue is accepted. The GitHub nickname and the GitHub issue
// (#1234) come first, each with a concise focused hint, then the gate rows, then the
// shared Continue / Cancel buttons.
//
// The gate rows sit AFTER the text fields even though the stdin flow asks them first,
// because the grid focuses its first row on entry and a focused toggle absorbs a bare
// "y" (formgrid.go, FieldToggle key handling). With the gates first, an operator whose
// GitHub nickname begins with y -- typing being the natural first act on a form that
// asks for a nickname -- silently granted consent, while the stdin gate rejects that
// same input and re-prompts. Order here is presentation only: submit() validates every
// row whatever the order, so neither acknowledgement can be skipped.
//
// It returns (meta, true) only on Continue with both acknowledgements given; esc /
// Cancel returns (_, false) so the caller loops back to the menu. The maintainer email
// address is never shown.
func runDashboardSupportForm(ctx context.Context, session *shell.Session) (support.Meta, bool) {
	errBack := errors.New("support: back")

	gates := support.ConsentGates()

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
		Description: "Must be an issue already open on GitHub, e.g. #1234.",
		Kind:        components.FieldText,
		Validate:    validateSupportIssue,
	}
	fields := append([]*components.FormField{nickname, issue}, supportConsentFields(gates)...)

	// The note is the shared disclosure plus every gate's supporting lines, so the
	// operator reads the same words the stdin flow prints before its first prompt —
	// here they stay on screen while the acknowledgements are given.
	note := support.ConsentDisclosure.Lines()
	for _, gate := range gates {
		note = append(note, gate.Detail...)
	}

	if _, err := shell.Ask(ctx, session, components.NewFormGrid(
		"Support", fields,
		components.WithFormGridNote(note...),
		components.WithFormGridBack(errBack),
	)); err != nil {
		return support.Meta{}, false // esc / Cancel / abort
	}
	return support.Meta{
		GitHubUser: strings.TrimSpace(nickname.Text),
		IssueID:    strings.TrimSpace(issue.Text),
	}, true
}

// supportConsentFields turns the shared consent gates into toggle rows. Bool is left at
// its zero value: an untouched row reads "No", mirroring the stdin prompt's [y/N]
// default. Require blocks submit while the row is still No, which is what stops the
// dashboard from arming support mode on a form whose visible purpose is entering
// metadata.
//
// The default alone does not make consent deliberate -- a focused toggle also answers to
// a bare "y", so where these rows sit in the field order decides whether stray typing can
// reach them. See the ordering note on runDashboardSupportForm.
func supportConsentFields(gates []support.ConsentGate) []*components.FormField {
	fields := make([]*components.FormField, 0, len(gates))
	for _, gate := range gates {
		fields = append(fields, &components.FormField{
			Label:        gate.Ack,
			Description:  gate.Question,
			Kind:         components.FieldToggle,
			ValidateBool: gate.Require,
		})
	}
	return fields
}

// validateSupportIssue enforces the #<number> issue format via the shared helper
// (mirrors support.RunIntro).
func validateSupportIssue(v string) error {
	return support.ValidateIssueID(v)
}
