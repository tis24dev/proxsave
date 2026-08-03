package support

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
)

// captureStdout points os.Stdout at a temp file for the rest of the test and returns a
// reader for everything written to it. RunIntro prints through fmt.Print*, which reads
// os.Stdout at call time, so swapping the variable captures it; the file is unbuffered,
// so the reader sees the writes without closing it first.
func captureStdout(t *testing.T) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdout.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create stdout capture: %v", err)
	}
	orig := os.Stdout
	os.Stdout = f
	t.Cleanup(func() {
		os.Stdout = orig
		_ = f.Close()
	})
	return func() string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read stdout capture: %v", err)
		}
		return string(data)
	}
}

// TestRunIntroRendersSharedConsentCopy: the stdin front-end must render the SHARED
// consent data (consent.go), not prose of its own, so it cannot drift away from the
// dashboard form — the defect this pins is exactly that the two front-ends demanded
// different consent for the same act (emailing the full debug log).
//
// The assertion follows the data instead of quoting it: rewording ConsentDisclosure or
// a gate keeps this test passing, while a front-end that stops rendering one of those
// values fails it. Its counterpart on the dashboard side is
// TestSupportFormRendersSharedConsentCopy in package main.
func TestRunIntroRendersSharedConsentCopy(t *testing.T) {
	withStdinFile(t, strings.Join([]string{"y", "y", "user", "#123", ""}, "\n"))
	stdout := captureStdout(t)

	_, ok, interrupted := RunIntro(context.Background(), logging.NewBootstrapLogger())
	got := stdout()
	if !ok || interrupted {
		t.Fatalf("ok=%v interrupted=%v; want true/false\n%s", ok, interrupted, got)
	}

	for _, line := range ConsentDisclosure.Lines() {
		if !strings.Contains(got, line) {
			t.Errorf("the stdin flow must show the shared disclosure line %q; output:\n%s", line, got)
		}
	}

	// Every gate is asked, with its supporting lines, in ConsentGates() order. The
	// order matters: the operator must consent to sharing the log before being asked
	// anything else about it.
	prev := -1
	for _, gate := range ConsentGates() {
		at := strings.Index(got, gate.Prompt())
		if at < 0 {
			t.Errorf("the stdin flow must ask the shared gate %q; output:\n%s", gate.Prompt(), got)
			continue
		}
		if at <= prev {
			t.Errorf("gate %q was asked out of ConsentGates() order (index %d, previous %d)", gate.Question, at, prev)
		}
		prev = at
		for _, detail := range gate.Detail {
			if !strings.Contains(got, detail) {
				t.Errorf("the stdin flow must show %q with gate %q; output:\n%s", detail, gate.Question, got)
			}
		}
	}
}

// TestConsentDisclosureKeepsTheLoadBearingWords anchors the shared disclosure to
// LITERALS, deliberately.
//
// Every other consent assertion walks ConsentDisclosure/ConsentGates, so it moves with
// the data: making Lines() return nothing keeps BOTH "renders the shared copy" tests
// green while the dashboard renders two consent toggles above an empty note -- an
// operator asked to accept, with nothing on screen saying what. These are the four facts
// they are accepting, and losing any of them is what this catches.
func TestConsentDisclosureKeepsTheLoadBearingWords(t *testing.T) {
	lines := ConsentDisclosure.Lines()
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"send the ProxSave log",     // what leaves the host
		"it will be shared",         // that someone else receives it
		"emailed to the maintainer", // who that someone is
		"MAC address",               // a concrete example of what it can carry
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the shared disclosure must still state %q; got:\n%s", want, joined)
		}
	}
	// The dashboard renders these lines through components.FormGrid, which lays the note
	// out at min(width, 100) columns and wraps past that. A wrapped line is no longer
	// findable as one string, so the dashboard-side copy test stops matching it and fails
	// only by timing out, 60s later, with a message that never mentions width.
	for _, line := range lines {
		if len(line) > 90 {
			t.Errorf("disclosure line is %d chars, too long to render unwrapped: %q", len(line), line)
		}
	}
}

// TestConsentGatePromptAdvertisesTheDefault: the capital N is the only thing telling the
// operator that silence at the prompt refuses, and the trailing space keeps their answer
// on the prompt's own line. Both are invisible to every data-driven assertion here --
// they all search for Prompt(), so they follow it wherever it goes.
func TestConsentGatePromptAdvertisesTheDefault(t *testing.T) {
	for _, gate := range ConsentGates() {
		if got := gate.Prompt(); got != gate.Question+" [y/N]: " {
			t.Errorf("gate prompt = %q; want the question followed by %q", got, " [y/N]: ")
		}
	}
}

// TestRunIntroFollowsDisclosureLineOrder holds the stdin flow to Lines() order without
// making it render through Lines(): it prints Summary and Warnings separately because it
// emphasises the warnings in yellow. Nothing else pins that, so the two front-ends could
// disclose the same lines in different orders with a green suite.
//
// It also pins that each gate's supporting lines are shown BEFORE that gate is asked.
// Moving them after the answer would have the operator confirm they opened a GitHub
// issue before being told why it is required.
func TestRunIntroFollowsDisclosureLineOrder(t *testing.T) {
	withStdinFile(t, strings.Join([]string{"y", "y", "user", "#123", ""}, "\n"))
	stdout := captureStdout(t)

	_, ok, interrupted := RunIntro(context.Background(), logging.NewBootstrapLogger())
	got := stdout()
	if !ok || interrupted {
		t.Fatalf("ok=%v interrupted=%v; want true/false\n%s", ok, interrupted, got)
	}

	prev := -1
	for _, line := range ConsentDisclosure.Lines() {
		at := strings.Index(got, line)
		if at < 0 {
			t.Fatalf("the stdin flow must show %q; output:\n%s", line, got)
		}
		if at <= prev {
			t.Errorf("disclosure line %q is out of Lines() order (index %d, previous %d)", line, at, prev)
		}
		prev = at
	}

	for _, gate := range ConsentGates() {
		asked := strings.Index(got, gate.Prompt())
		if asked < 0 {
			t.Fatalf("the stdin flow must ask %q; output:\n%s", gate.Prompt(), got)
		}
		for _, detail := range gate.Detail {
			at := strings.Index(got, detail)
			if at < 0 || at > asked {
				t.Errorf("%q must be shown BEFORE the gate asks %q (detail at %d, prompt at %d)", detail, gate.Question, at, asked)
			}
		}
	}
}

// TestPromptYesNoSupportEmptyAnswerIsNo pins the [y/N] default that the whole consent
// gate rests on: an operator who just presses Enter has NOT agreed to email their debug
// log. If an empty answer returned true, both gates would pass on silence.
func TestPromptYesNoSupportEmptyAnswerIsNo(t *testing.T) {
	for _, answer := range []string{"\n", "   \n"} {
		granted, err := promptYesNoSupport(context.Background(), bufio.NewReader(strings.NewReader(answer)), "prompt: ")
		if err != nil {
			t.Fatalf("promptYesNoSupport(%q) error: %v", answer, err)
		}
		if granted {
			t.Errorf("an empty answer (%q) must default to No, got granted=true", answer)
		}
	}
}

// TestConsentGatesOrder pins the sequence itself, which the data-driven assertions
// above deliberately cannot: they walk ConsentGates(), so reordering that slice moves
// the production render and the expectation together. Consent to sharing the log has
// to be asked FIRST — an operator must not be asked to state anything about a report
// they have not yet agreed to send.
func TestConsentGatesOrder(t *testing.T) {
	gates := ConsentGates()
	if len(gates) != 2 {
		t.Fatalf("expected the two consent gates, got %d", len(gates))
	}
	if gates[0].Question != ConsentGateAccept.Question {
		t.Errorf("the consent gate must come first, got %q", gates[0].Question)
	}
	if gates[1].Question != ConsentGateIssueOpen.Question {
		t.Errorf("the issue-already-open gate must come second, got %q", gates[1].Question)
	}
}

// TestConsentGateRequire: Require is the form-renderer side of that same default —
// a control left at its zero value (false) must produce a blocking error.
func TestConsentGateRequire(t *testing.T) {
	for _, gate := range ConsentGates() {
		if err := gate.Require(false); err == nil {
			t.Errorf("gate %q must reject a missing acknowledgement", gate.Question)
		} else if !strings.Contains(err.Error(), gate.Unmet) {
			t.Errorf("gate %q must explain what is missing, got %q; want %q", gate.Question, err, gate.Unmet)
		}
		if err := gate.Require(true); err != nil {
			t.Errorf("gate %q must accept an explicit acknowledgement, got %v", gate.Question, err)
		}
	}
}
