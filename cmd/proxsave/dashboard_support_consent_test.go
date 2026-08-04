package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/support"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/uitest"
)

// supportFormRun drives a real runDashboardSupportForm over an observed session, the
// way TestSupportFormIsOneScreen does, and exposes what the form returned so a test can
// assert BOTH that it resolved and that it did not.
type supportFormRun struct {
	driver *newkeyUIDriver
	meta   chan support.Meta
	done   chan struct{}
}

func startSupportForm(t *testing.T) *supportFormRun {
	t.Helper()
	driver := &newkeyUIDriver{t: t, buf: &shell.SyncBuffer{}, pushes: make(chan screenPush, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	run := &supportFormRun{driver: driver, meta: make(chan support.Meta, 1), done: make(chan struct{})}
	driver.session = shell.StartObservedForTest(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Dashboard"},
		driver.buf, func(title string) { driver.pushes <- screenPush{title: title, at: driver.buf.Len()} })
	go func() {
		defer close(run.done)
		if meta, ok := runDashboardSupportForm(ctx, driver.session); ok {
			run.meta <- meta
		}
	}()
	// Cancel and JOIN the form goroutine, so a form still waiting for input cannot
	// outlive the test and keep sending into the driver.
	t.Cleanup(func() {
		cancel()
		select {
		case <-run.done:
		case <-time.After(uitest.Deadline(60 * time.Second)):
			t.Error("support form did not return after cancel")
		}
	})
	driver.waitScreen("Support")
	return run
}

// stillOpen asserts the form has NOT resolved. It is paired with a waitOutput on the
// rejection message that follows it: the wait here catches an immediate resolve, and
// the rejection render then proves submit really ran and took the reject branch (so a
// gate that stopped blocking cannot slip through as a slow submit).
func (r *supportFormRun) stillOpen(t *testing.T, why string) {
	t.Helper()
	select {
	case <-r.done:
		t.Fatalf("%s\n%s", why, tailStr(ansi.Strip(r.driver.buf.String())))
	case <-time.After(uitest.Deadline(time.Second)):
	}
}

// TestSupportFormBlocksUntilBothConsentGatesAreSet is the D1 regression test.
//
// Support mode emails the operator's FULL debug log to the maintainer. The stdin flow
// demands two explicit affirmative answers first; the dashboard used to arm the very
// same run from a form whose only visible purpose was entering a nickname and an issue
// number, so pressing Continue there was never a statement of consent. Filling both
// text fields and pressing Continue must now be REFUSED while either acknowledgement is
// still No, and refused separately for each one — the run is armed only after both.
func TestSupportFormBlocksUntilBothConsentGatesAreSet(t *testing.T) {
	gates := support.ConsentGates()
	if len(gates) != 2 {
		t.Fatalf("this test drives the form by row index; it needs the two shared gates, got %d", len(gates))
	}
	run := startSupportForm(t)
	d := run.driver

	// Row order is: the two text fields, then the two gate toggles (untouched, i.e. No).
	// Navigation is "down" only, so no key can flip a toggle by accident.
	d.typeText("alice") // row 0, GitHub nickname, focused on entry
	d.keys("down")      // -> GitHub issue (row 1)
	d.typeText("#123")
	d.keys("down down down") // -> past both gate rows to the buttons row
	d.keys("enter")          // press Continue with both acknowledgements still No

	run.stillOpen(t, "the form resolved with both consent rows still No: the dashboard would arm support mode without consent")
	d.waitOutput(gates[0].Unmet)

	// Give only the FIRST acknowledgement (the rejection parks the cursor on it) and
	// press Continue again: the second gate must reject on its own.
	d.keys("y")
	d.keys("down down") // row 2 -> the buttons row
	d.keys("enter")     // Continue
	run.stillOpen(t, "the form resolved with only the first acknowledgement given")
	d.waitOutput(gates[1].Unmet)

	// Both acknowledgements: the form resolves and carries the metadata.
	d.keys("y")
	d.keys("down")  // row 3 -> the buttons row
	d.keys("enter") // Continue

	select {
	case meta := <-run.meta:
		if meta.GitHubUser != "alice" || meta.IssueID != "#123" {
			t.Fatalf("meta = %+v; want GitHubUser=alice IssueID=#123", meta)
		}
	case <-time.After(uitest.Deadline(30 * time.Second)):
		t.Fatalf("the form must resolve once both acknowledgements are given:\n%s", tailStr(ansi.Strip(d.buf.String())))
	}
}

// TestSupportConsentFieldsDefaultToNo pins the safety default: the rows the dashboard
// builds from the shared gates start unchecked and refuse submit in that state, so
// consent can only ever be the result of an explicit act. A default of Yes would put
// the form back to arming support mode on a bare Continue.
func TestSupportConsentFieldsDefaultToNo(t *testing.T) {
	gates := support.ConsentGates()
	fields := supportConsentFields(gates)
	if len(fields) != len(gates) {
		t.Fatalf("every shared gate must get a row: %d rows for %d gates", len(fields), len(gates))
	}
	for i, f := range fields {
		gate := gates[i]
		if f.Kind != components.FieldToggle {
			t.Errorf("row %q must be a toggle, got kind %v", f.Label, f.Kind)
		}
		if f.Bool {
			t.Errorf("row %q must default to No", f.Label)
		}
		// Label and hint come from the shared gate, so the dashboard cannot label a
		// row with something the stdin flow never asks.
		if f.Label != gate.Ack {
			t.Errorf("row %d label = %q; want the shared %q", i, f.Label, gate.Ack)
		}
		if f.Description != gate.Question {
			t.Errorf("row %d hint = %q; want the shared question %q", i, f.Description, gate.Question)
		}
		if f.ValidateBool == nil {
			t.Fatalf("row %q must gate submit (ValidateBool is nil)", f.Label)
		}
		if err := f.ValidateBool(false); err == nil {
			t.Errorf("row %q must refuse submit while it is No", f.Label)
		} else if !strings.Contains(err.Error(), gate.Unmet) {
			t.Errorf("row %q rejection = %q; want the shared %q", f.Label, err, gate.Unmet)
		}
		if err := f.ValidateBool(true); err != nil {
			t.Errorf("row %q must accept submit once it is Yes, got %v", f.Label, err)
		}
	}
}

// TestSupportFormRendersSharedConsentCopy is the dashboard half of the shared-copy
// check (its counterpart is TestRunIntroRendersSharedConsentCopy in internal/support).
// It walks the shared values rather than quoting them, so rewording consent.go keeps it
// green while this front-end silently dropping a line — the state D1 came from — fails
// it.
func TestSupportFormRendersSharedConsentCopy(t *testing.T) {
	run := startSupportForm(t)
	d := run.driver

	// The disclosure and every gate's supporting lines are the always-visible note.
	for _, line := range support.ConsentDisclosure.Lines() {
		d.waitOutput(line)
	}
	gates := support.ConsentGates()
	for _, gate := range gates {
		for _, detail := range gate.Detail {
			d.waitOutput(detail)
		}
		// Every gate gets its own row, labelled with the shared affirmative.
		d.waitOutput(gate.Ack)
	}
	// The question a gate asks is that row's hint, which renders only while the row is
	// focused -- and entry focus is the nickname field, not a gate (see the ordering
	// note on runDashboardSupportForm). Navigating there to assert it is not reliable
	// here: only the FIRST frame of a screen is a full render, and bubbletea's cell-diff
	// renderer then emits just the changed cells, which splits a re-rendered line in
	// this cumulative buffer (driving "down" produced a buffer holding
	// "...Yes  No  confirm that you have already opened a GitHub issue?", i.e. the hint
	// without its "Do you " prefix). The hints are pinned at the field level by
	// TestSupportConsentFieldsDefaultToNo instead.
}

// TestSupportFormEntryFocusKeepsTypingOffTheConsentRows pins the field ORDER as a
// consent property, not a cosmetic one.
//
// A focused toggle answers to a bare "y" (formgrid.go, FieldToggle key handling). With
// the gate rows first -- which is the order the stdin flow asks in -- the row focused on
// entry was a consent toggle, so an operator typing a GitHub nickname that begins with y
// granted consent with the first keystroke and never knew: "ydev" armed the run, while
// the stdin gate rejects that same answer and re-prompts. Typing is the natural first act
// on a form asking for a nickname, which is what made it reachable.
func TestSupportFormEntryFocusKeepsTypingOffTheConsentRows(t *testing.T) {
	gates := support.ConsentGates()
	run := startSupportForm(t)
	d := run.driver

	// Type a y-leading nickname as the very first act, exactly as an operator would.
	d.typeText("ydev")
	d.keys("down")
	d.typeText("#123")
	d.keys("down down down") // past both gate rows, touching neither
	d.keys("enter")          // Continue

	run.stillOpen(t, "the form resolved after only a nickname and an issue were typed: consent was granted by a keystroke meant for a text field")
	// The FIRST gate must be the one that rejects. If the typed "y" had reached it, the
	// rejection would be the second gate's instead, and this wait would time out.
	d.waitOutput(gates[0].Unmet)
}
