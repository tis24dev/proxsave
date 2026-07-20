package components

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func click(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

func wheel(down bool) tea.MouseWheelMsg {
	b := tea.MouseWheelUp
	if down {
		b = tea.MouseWheelDown
	}
	return tea.MouseWheelMsg{Button: b}
}

func TestSelectorMouseClickActivatesRow(t *testing.T) {
	s := NewSelector("Mode", threeItems())
	cap := bindSelector(s)
	s.View(80, 20) // establish layout
	// Rows start after title + blank: row 1 is the second item.
	s.Update(click(4, s.lastRowsTop+1)) //nolint:errcheck
	if !cap.resolved || cap.value != "storage" {
		t.Fatalf("click on row 1 must select storage, got %+v", cap)
	}

	// Clicks outside the rows are ignored.
	s2 := NewSelector("Mode", threeItems())
	cap2 := bindSelector(s2)
	s2.View(80, 20)
	s2.Update(click(4, s2.lastRowsTop+10)) //nolint:errcheck
	if cap2.resolved {
		t.Fatal("click outside rows must be ignored")
	}
}

func TestSelectorMouseWheelMovesCursor(t *testing.T) {
	s := NewSelector("Mode", threeItems())
	bindSelector(s)
	s.View(80, 20)
	s.Update(wheel(true)) //nolint:errcheck
	s.Update(wheel(true)) //nolint:errcheck
	if s.cursor != 2 {
		t.Fatalf("wheel down twice must reach row 2, cursor=%d", s.cursor)
	}
	s.Update(wheel(false)) //nolint:errcheck
	if s.cursor != 1 {
		t.Fatalf("wheel up must move back, cursor=%d", s.cursor)
	}
}

func TestMultiSelectMouseClickToggles(t *testing.T) {
	m := NewMultiSelect("Categories", categoryItems())
	bindMulti(m)
	m.View(80, 20)
	m.Update(click(4, m.lastRowsTop+1)) //nolint:errcheck
	if !m.items[1].Selected {
		t.Fatal("click must toggle the row on")
	}
	m.Update(click(4, m.lastRowsTop+1)) //nolint:errcheck
	if m.items[1].Selected {
		t.Fatal("second click must toggle the row off")
	}
}

func TestConfirmMouseClickButtons(t *testing.T) {
	c := NewConfirm("Confirm", "Proceed?", WithLabels("Apply", "Skip"))
	cap := bindConfirm(c)
	c.View(80, 12)                           // establish layout
	c.Update(click(c.yesX0, c.lastButtonsY)) //nolint:errcheck
	if !cap.resolved || !cap.result.Answer {
		t.Fatalf("click on the yes button must resolve Yes, got %+v", cap)
	}

	c2 := NewConfirm("Confirm", "Proceed?")
	cap2 := bindConfirm(c2)
	c2.View(80, 12)
	c2.Update(click(c2.noX0+1, c2.lastButtonsY)) //nolint:errcheck
	if !cap2.resolved || cap2.result.Answer {
		t.Fatalf("click on the no button must resolve No, got %+v", cap2)
	}

	// A click off the buttons row does nothing.
	c3 := NewConfirm("Confirm", "Proceed?")
	cap3 := bindConfirm(c3)
	c3.View(80, 12)
	c3.Update(click(0, 0)) //nolint:errcheck
	if cap3.resolved {
		t.Fatal("click outside buttons must be ignored")
	}
}

func TestFormGridMouse(t *testing.T) {
	toggle, path, cron := gridFields()
	g := NewFormGrid("Configuration", []*FormField{toggle, path, cron})
	cap := bindGrid(g)
	g.View(100, 20)

	// Click the toggle row: focuses and flips it.
	g.Update(click(4, g.lastRowsTop)) //nolint:errcheck
	if !toggle.Bool {
		t.Fatal("click must toggle the row")
	}

	// Wheel moves between active rows.
	g.View(100, 20)
	g.Update(wheel(true)) //nolint:errcheck
	if g.cursor != 1 {
		t.Fatalf("wheel down must move to the path row, cursor=%d", g.cursor)
	}

	// Click Continue: submit fails on the empty path (validation) and the
	// error is shown.
	g.View(100, 20)
	g.Update(click(g.contX0+1, g.lastButtonsY)) //nolint:errcheck
	if cap.resolved {
		t.Fatal("invalid form must not submit on Continue click")
	}
	if !strings.Contains(g.View(100, 20), "must be absolute") {
		t.Fatal("validation error must be shown after Continue click")
	}

	// Click Cancel resolves the back error.
	g2fields := []*FormField{{Label: "Cron", Kind: FieldText, Text: "02:00"}}
	g2 := NewFormGrid("Configuration", g2fields)
	cap2 := bindGrid(g2)
	g2.View(100, 20)
	g2.Update(click(g2.cancX0+1, g2.lastButtonsY)) //nolint:errcheck
	if !cap2.resolved || cap2.err == nil {
		t.Fatalf("Cancel click must resolve the back error, got %+v", cap2)
	}
}
