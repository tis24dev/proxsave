package components

import (
	"errors"
	"strings"
	"testing"
)

// gateGrid builds a form with a gating toggle in front of a valid text field, so the
// only thing that can block submit is the toggle.
func gateGrid() (*FormGrid, *FormField) {
	gate := &FormField{
		Label: "I accept",
		Kind:  FieldToggle,
		ValidateBool: func(v bool) error {
			if !v {
				return errors.New("acknowledgement required")
			}
			return nil
		},
	}
	nick := &FormField{Label: "Nick", Kind: FieldText, Text: "alice"}
	return NewFormGrid("Consent", []*FormField{gate, nick}), gate
}

// TestFormGridToggleGateBlocksSubmit: a toggle whose ValidateBool rejects the current
// value must block Continue on BOTH ways of pressing it — Enter on the buttons row and
// a left click on the Continue band — with the same label-prefixed inline message and
// cursor jump the text validators produce. The click path force-sets the cursor and
// calls submit directly, 70 lines away from the key handler, so it is the one a gate
// added in the wrong place would miss.
func TestFormGridToggleGateBlocksSubmit(t *testing.T) {
	g, gate := gateGrid()
	cap := bindGrid(g)

	press(t, g, "down")  // gate -> Nick
	press(t, g, "down")  // Nick -> buttons (Continue focused)
	press(t, g, "enter") // Continue
	if cap.resolved {
		t.Fatal("an unmet toggle gate must block submit")
	}
	view := g.View(100, 20)
	if !strings.Contains(view, "I accept: acknowledgement required") {
		t.Fatalf("the gate message must be shown inline, prefixed with the row label:\n%s", view)
	}
	if g.cursor != 0 {
		t.Fatalf("the cursor must park on the row that has to change, cursor=%d", g.cursor)
	}

	// Same grid, same unmet gate, pressed with the mouse.
	g.Update(click(g.contX0+1, g.lastButtonsY)) //nolint:errcheck
	if cap.resolved {
		t.Fatal("an unmet toggle gate must block a Continue CLICK too")
	}

	// The acknowledgement given (the cursor is parked on the gate row), the same
	// Continue now resolves.
	press(t, g, "y")
	if !gate.Bool {
		t.Fatalf("test setup: y must set the gate row to Yes")
	}
	press(t, g, "down")
	press(t, g, "down")
	press(t, g, "enter")
	if !cap.resolved || cap.err != nil {
		t.Fatalf("submit must be accepted once the gate is satisfied, got %+v", cap)
	}
}

// TestFormGridTallNoteNeverHidesEveryFieldRow: the note is fixed — it never scrolls — so
// on a short terminal it used to absorb the whole height budget and render a form with
// NO field rows at all, just Continue/Cancel. On a settings form that is merely ugly; on
// a GATING form it is a dead end, because submit is then refused by a row the operator
// cannot reach, and at those same heights the refusal message is dropped from the footer
// too, so pressing Continue does nothing visible. The note must truncate first — which it
// announces — and it must never disappear in silence either: a vanished consent note
// would leave the acknowledgement rows on screen with nothing stating what is accepted.
func TestFormGridTallNoteNeverHidesEveryFieldRow(t *testing.T) {
	note := []string{"note one", "note two", "note three", "note four", "note five", "note six"}
	const truncated = "note truncated"

	for height := 7; height <= 16; height++ {
		g, _ := gateGrid()
		g = NewFormGrid("Consent", g.fields, WithFormGridNote(note...))
		view := g.View(100, height)
		if !strings.Contains(view, "I accept") && !strings.Contains(view, "Nick") {
			t.Errorf("height %d renders no field row, so a gating form cannot be completed:\n%s", height, view)
		}
		if strings.Contains(view, note[len(note)-1]) {
			continue // the whole note fits, nothing to announce
		}
		if !strings.Contains(view, truncated) {
			t.Errorf("height %d drops note lines without saying so:\n%s", height, view)
		}
	}

	// Below that the note is reduced to the indicator alone and the field rows go with
	// it. That is the safe way to fail: nothing can be acknowledged, and the line on
	// screen says what to do about it.
	for height := 3; height <= 6; height++ {
		g, _ := gateGrid()
		g = NewFormGrid("Consent", g.fields, WithFormGridNote(note...))
		if view := g.View(100, height); !strings.Contains(view, truncated) {
			t.Errorf("height %d must still say the note was truncated:\n%s", height, view)
		}
	}
}

// TestFormGridToggleWithoutValidateBoolSubmits: ValidateBool is opt-in. A plain toggle
// (the installer's six settings rows) leaves it nil and must submit at either value —
// adding the hook may not turn existing settings into gates.
func TestFormGridToggleWithoutValidateBoolSubmits(t *testing.T) {
	for _, on := range []bool{false, true} {
		toggle := &FormField{Label: "Cloud backups (rclone)", Kind: FieldToggle, Bool: on}
		g := NewFormGrid("Configuration", []*FormField{toggle})
		cap := bindGrid(g)
		press(t, g, "down")  // toggle -> buttons
		press(t, g, "enter") // Continue
		if !cap.resolved || cap.err != nil {
			t.Fatalf("a toggle with a nil ValidateBool must not block submit (Bool=%v), got %+v", on, cap)
		}
	}
}
