package support

import "errors"

// Consent copy and gates for support mode, single-sourced for EVERY front-end.
//
// Support mode attaches the operator's full debug log to an email addressed to the
// maintainer (SendEmail builds its EmailConfig with AttachLogFile: true). That is one
// privacy-relevant act, so the stdin flow (RunIntro) and the dashboard form must
// disclose the same thing and demand the same acknowledgements. They previously did
// not: the dashboard armed support mode from a metadata form with a passive note, with
// no affirmative consent and no issue-already-open question at all. Keeping the copy
// and the gates here — rather than as literals inside each renderer — is what stops
// the two from drifting apart again; a front-end that stops rendering these values is
// caught by the tests that walk ConsentDisclosure.Lines() and ConsentGates().
//
// Renderers own presentation only (color, layout, toggle vs. y/n prompt). The values
// below carry no ANSI on purpose: the dashboard feeds them to components.FormGrid,
// which sanitizes its note lines and every field label/hint, so an escape sequence
// written here would be stripped there rather than rendered.

// Disclosure is what the operator must be shown BEFORE being asked anything: what
// support mode does (Summary) and what it costs them in privacy terms (Warnings).
// The split exists so a renderer can emphasise the privacy lines — the CLI prints
// them in yellow — without owning the words.
type Disclosure struct {
	Summary  string
	Warnings []string
}

// Lines returns the disclosure in the order every front-end must show it: the
// summary first, then each warning. The slice is freshly allocated so a caller can
// append its own lines (the dashboard appends the gate detail) without writing
// through to the package value.
//
// The stdin flow does NOT render through this method -- it prints Summary and Warnings
// separately because it emphasises the warnings in yellow, which needs the split -- so
// it is held to this order by TestRunIntroFollowsDisclosureLineOrder instead. Without
// that test the two front-ends could disclose the same lines in different orders.
func (d Disclosure) Lines() []string {
	lines := make([]string, 0, len(d.Warnings)+1)
	if d.Summary != "" {
		lines = append(lines, d.Summary)
	}
	return append(lines, d.Warnings...)
}

// ConsentDisclosure is the shared disclosure, built STRICTLY ADDITIVELY over what the
// two front-ends carried on their own: the stdin flow's two lines are kept verbatim and
// the dashboard's two contributions (the maintainer as the recipient, the MAC address as
// a concrete example of what leaks) become warnings of their own. Sharing the copy does
// not license rewriting it -- a reworded summary would change CLI stdout for nothing,
// and the additive form was verified to satisfy every test the reworded one did.
//
// Line length is load-bearing: FormGrid renders the note at min(width, 100) columns and
// wraps past that, which no test can then match with a Contains. Keep each line short.
var ConsentDisclosure = Disclosure{
	Summary: "This mode will send the ProxSave log to the developer for debugging.",
	Warnings: []string{
		"If your log contains personal or sensitive information, it will be shared.",
		"The full log is emailed to the maintainer at the end of the run.",
		"The log may contain personal data such as this server's MAC address.",
	},
}

// ConsentGate is ONE acknowledgement the operator has to give before support mode may
// be armed. Both front-ends render the same gate values, so neither can end up asking
// for less than the other.
type ConsentGate struct {
	// Question is the acknowledgement itself, phrased as a yes/no question and
	// WITHOUT the "[y/N]: " suffix Prompt appends. The stdin flow asks it verbatim;
	// the dashboard shows it as the hint of the row that carries the toggle.
	Question string
	// Ack is the short affirmative form for a renderer that labels a control rather
	// than asking a question — the dashboard's toggle row. It says what a Yes means,
	// so the label and the Question can never disagree about which answer consents.
	Ack string
	// Detail are the supporting lines that must be shown together with the question
	// (they explain why the acknowledgement is being asked for). Both front-ends
	// display them before/alongside the gate; may be empty when the disclosure
	// already carries the reason.
	Detail []string
	// DeclineWarning is the exact bootstrap.Warning text emitted when the gate is
	// refused on the stdin path.
	DeclineWarning string
	// Unmet is the message a form renderer shows inline while the acknowledgement is
	// still missing. It is phrased as "what you must do", not as DeclineWarning's
	// past-tense abort, because at that point nothing has been aborted: the form
	// simply refuses to submit.
	Unmet string
}

// Prompt is the stdin prompt for this gate. The capital N advertises the default that
// promptYesNoSupport implements (an empty answer returns false), so silence at the
// prompt is a refusal.
func (g ConsentGate) Prompt() string { return g.Question + " [y/N]: " }

// Require reports whether the acknowledgement was given, as an error a form validator
// can surface inline. It is the toggle-side counterpart of the stdin default-to-No:
// the zero value of a bool control is false, so an untouched control never consents.
func (g ConsentGate) Require(given bool) error {
	if given {
		return nil
	}
	return errors.New(g.Unmet)
}

// ConsentGateAccept is the consent gate proper: permission to send the log at all.
var ConsentGateAccept = ConsentGate{
	Question:       "Do you accept and continue?",
	Ack:            "I accept and continue",
	DeclineWarning: "Support mode aborted by user (consent not granted)",
	Unmet:          "consent is required before the log can be emailed",
}

// ConsentGateIssueOpen is the second gate: the operator states that the GitHub issue
// the report belongs to already exists. Typing an issue number is not that statement —
// nothing checks the number against GitHub — which is why it is asked separately.
var ConsentGateIssueOpen = ConsentGate{
	Question: "Do you confirm that you have already opened a GitHub issue?",
	Ack:      "Issue already open on GitHub",
	Detail: []string{
		"Before proceeding, you must have an open GitHub issue for this problem.",
		"Emails without a corresponding GitHub issue will not be analyzed.",
	},
	DeclineWarning: "Support mode aborted: please open a GitHub issue first",
	Unmet:          "open the issue on GitHub first, then confirm here",
}

// ConsentGates returns every gate, in the order they must be presented. Front-ends
// iterate this instead of naming the gates one by one, so a gate added here shows up
// in both of them. The slice is freshly allocated per call.
func ConsentGates() []ConsentGate {
	return []ConsentGate{ConsentGateAccept, ConsentGateIssueOpen}
}
