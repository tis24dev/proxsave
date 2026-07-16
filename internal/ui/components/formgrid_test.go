package components

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// TestFormGridTabReachesCancel: Tab/Down on the buttons row toggles Continue/Cancel
// so Cancel is reachable via Tab (it used to snap back to Continue).
func TestFormGridTabReachesCancel(t *testing.T) {
	g := NewFormGrid("Config", []*FormField{{Label: "A", Kind: FieldToggle}})
	cap := bindGrid(g)
	press(t, g, "down") // A -> buttons (Continue focused)
	if g.cursor != len(g.fields) || g.onCancel {
		t.Fatalf("expected the buttons row with Continue focused (cursor=%d onCancel=%v)", g.cursor, g.onCancel)
	}
	press(t, g, "tab") // must reach Cancel, not snap back to Continue
	if !g.onCancel {
		t.Fatalf("tab on the buttons row must reach Cancel, got onCancel=%v", g.onCancel)
	}
	press(t, g, "enter") // Cancel resolves the back error
	if !cap.resolved || cap.err == nil {
		t.Fatalf("enter on Cancel must resolve an abort error, got %+v", cap)
	}
}

// TestFormGridDownStaysOnContinue: down on the buttons row keeps Continue focused
// (unlike tab, it does not toggle), so a "mash down then Enter" submit is reliable.
func TestFormGridDownStaysOnContinue(t *testing.T) {
	g := NewFormGrid("Config", []*FormField{{Label: "A", Kind: FieldToggle}})
	for i := 0; i < 4; i++ {
		press(t, g, "down") // 1st down -> buttons (Continue); the rest stay on Continue
	}
	if g.onCancel {
		t.Fatalf("extra down on the buttons row must not toggle to Cancel, got onCancel=%v", g.onCancel)
	}
}

// TestFormGridBlinkCmdOnFieldChange: moving to a text field returns the caret-blink
// Cmd instead of dropping it (the caret stopped blinking after the first field).
func TestFormGridBlinkCmdOnFieldChange(t *testing.T) {
	g := NewFormGrid("Config", []*FormField{
		{Label: "One", Kind: FieldText},
		{Label: "Two", Kind: FieldText},
	})
	bindGrid(g)
	_, cmd := g.Update(shell.KeyMsg("enter")) // field 0 -> field 1 (both text)
	if cmd == nil {
		t.Fatal("moving to a text field must return a blink Cmd")
	}
}

// TestFormGridReservesButtonsAtSmallHeight: the buttons block is reserved first, so
// at a tight height (that used to crop it from below) the buttons still render and
// the body fits the budget.
func TestFormGridReservesButtonsAtSmallHeight(t *testing.T) {
	fields := make([]*FormField, 6)
	for i := range fields {
		fields[i] = &FormField{Label: fmt.Sprintf("Field %d", i), Description: "a hint", Kind: FieldToggle}
	}
	g := NewFormGrid("Config", fields)
	bindGrid(g)
	view := g.View(80, 4) // cursor on field 0 (a Description -> a footer line)
	if n := len(strings.Split(view, "\n")); n > 4 {
		t.Fatalf("body must fit the height budget (got %d lines, want <= 4):\n%s", n, view)
	}
	if !strings.Contains(view, "Continue") || !strings.Contains(view, "Cancel") {
		t.Fatalf("buttons must always render at small heights:\n%s", view)
	}
}

func gridFields() (toggle, path, cron *FormField) {
	toggle = &FormField{Label: "Secondary storage", Kind: FieldToggle}
	path = &FormField{
		Label:  "Secondary backup path",
		Kind:   FieldText,
		Active: func() bool { return toggle.Bool },
		Validate: func(v string) error {
			if !strings.HasPrefix(strings.TrimSpace(v), "/") {
				return fmt.Errorf("must be absolute")
			}
			return nil
		},
	}
	cron = &FormField{Label: "Cron time", Kind: FieldText, Text: "02:00"}
	return
}

func bindGrid(g *FormGrid) *struct {
	resolved bool
	err      error
} {
	cap := &struct {
		resolved bool
		err      error
	}{}
	g.Bind(func(_ struct{}, err error) {
		cap.resolved = true
		cap.err = err
	})
	return cap
}

func TestFormGridSkipsInactiveAndSubmits(t *testing.T) {
	toggle, path, cron := gridFields()
	g := NewFormGrid("Configuration", []*FormField{toggle, path, cron})
	cap := bindGrid(g)

	// Toggle off: path is inactive, Enter goes toggle -> cron -> Continue.
	press(t, g, "enter") // toggle -> cron (path skipped)
	press(t, g, "enter") // cron -> buttons
	press(t, g, "enter") // Continue: submit (path not validated: inactive)
	if !cap.resolved || cap.err != nil {
		t.Fatalf("expected clean submit, got %+v", cap)
	}
}

func TestFormGridToggleAndValidation(t *testing.T) {
	toggle, path, cron := gridFields()
	g := NewFormGrid("Configuration", []*FormField{toggle, path, cron})
	cap := bindGrid(g)

	press(t, g, "space") // enable secondary
	if !toggle.Bool {
		t.Fatal("space must toggle")
	}
	press(t, g, "enter") // -> path (now active)
	for _, r := range "relative" {
		press(t, g, string(r))
	}
	press(t, g, "enter") // -> cron
	press(t, g, "enter") // -> buttons
	press(t, g, "enter") // Continue: validation must fail on path
	if cap.resolved {
		t.Fatal("invalid path must block submit")
	}
	view := g.View(100, 20)
	if !strings.Contains(view, "must be absolute") {
		t.Errorf("validation error not shown: %q", view)
	}

	// Cursor moved back to the failing field: fix it and resubmit.
	for range "relative" {
		press(t, g, "backspace")
	}
	for _, r := range "/mnt/nas" {
		press(t, g, string(r))
	}
	press(t, g, "enter") // -> cron
	press(t, g, "enter") // -> buttons
	press(t, g, "enter") // Continue
	if !cap.resolved || cap.err != nil {
		t.Fatalf("expected submit after fix, got %+v", cap)
	}
	if path.Text != "/mnt/nas" {
		t.Fatalf("path value = %q", path.Text)
	}
}

func TestFormGridSelectCyclesAndAlignment(t *testing.T) {
	sel := &FormField{Label: "Email delivery method", Kind: FieldSelect,
		Options: []string{"relay", "sendmail", "pmf"}}
	short := &FormField{Label: "Cron", Kind: FieldText, Text: "02:00"}
	g := NewFormGrid("Configuration", []*FormField{sel, short})
	bindGrid(g)

	press(t, g, "right")
	press(t, g, "right")
	if sel.OptionIndex != 2 {
		t.Fatalf("right must cycle, index=%d", sel.OptionIndex)
	}
	press(t, g, "right")
	if sel.OptionIndex != 0 {
		t.Fatalf("cycle must wrap, index=%d", sel.OptionIndex)
	}
	press(t, g, "left")
	if sel.OptionIndex != 2 {
		t.Fatalf("left must cycle back, index=%d", sel.OptionIndex)
	}

	// Aligned controls: both rows place the control at the same column.
	view := g.View(100, 20)
	lines := strings.Split(view, "\n")
	var rows []string
	for _, l := range lines {
		if strings.Contains(l, "Email delivery method") || strings.Contains(l, "Cron") {
			rows = append(rows, l)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 field rows, got %d", len(rows))
	}

	// The hint line must be separated from the buttons by a blank line,
	// mirroring the blank line above them.
	all := strings.Split(view, "\n")
	for i, l := range all {
		if strings.Contains(l, "Continue") {
			if i == 0 || strings.TrimSpace(all[i-1]) != "" {
				t.Fatalf("missing blank line above buttons: %q", all[i-1])
			}
			if i+1 < len(all) && strings.TrimSpace(all[i+1]) != "" {
				t.Fatalf("missing blank line below buttons: %q", all[i+1])
			}
		}
	}
}

// TestFormGridHintWrapsAtReadableWidth: a long field description must wrap
// instead of running across the whole (wide) terminal.
func TestFormGridHintWrapsAtReadableWidth(t *testing.T) {
	long := &FormField{
		Label:       "Secondary storage",
		Description: "Additional local path for redundant copies; must be filesystem-mounted (e.g. /mnt/nas-backup). For direct network access use cloud storage (rclone).",
		Kind:        FieldToggle,
	}
	g := NewFormGrid("Configuration", []*FormField{long})
	bindGrid(g)
	view := g.View(180, 30) // very wide terminal
	var first, second string
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, "redundant copies") {
			first = l
		}
		if strings.Contains(l, "direct network access") {
			second = l
		}
		if w := lipgloss.Width(l); w > 110 {
			t.Fatalf("hint line too wide (%d cols): %q", w, l)
		}
	}
	if first == "" || second == "" {
		t.Fatalf("hint must span two lines, got first=%q second=%q", first, second)
	}
	// Sentence integrity: the split happens at the sentence boundary, so
	// the first line ends its sentence and the second starts with "For".
	if strings.Contains(first, "For direct") {
		t.Fatalf("second sentence must not start on the first line: %q", first)
	}
	if !strings.Contains(second, "For direct network access") {
		t.Fatalf("second line must carry the whole second sentence: %q", second)
	}
}

func TestFormGridNoteAboveFields(t *testing.T) {
	field := &FormField{Label: "GitHub nickname", Kind: FieldText}
	g := NewFormGrid("Support", []*FormField{field},
		WithFormGridNote(
			"The full run log is emailed to the maintainer for support.",
			"It may contain personal data such as this server's MAC address.",
			"Continue only if you consent to sharing it.",
		))
	bindGrid(g)
	view := g.View(100, 20)
	lines := strings.Split(view, "\n")

	idx := func(substr string) int {
		for i, l := range lines {
			if strings.Contains(l, substr) {
				return i
			}
		}
		return -1
	}
	noteTop := idx("emailed to the maintainer")
	noteMid := idx("MAC address")
	noteBot := idx("consent to sharing")
	label := idx("GitHub nickname")
	if noteTop < 0 || noteMid < 0 || noteBot < 0 {
		t.Fatalf("all consent note lines must render:\n%s", view)
	}
	if label < 0 {
		t.Fatalf("the field label must render:\n%s", view)
	}
	// The note sits ABOVE the fields, in order, one clause per line (never merged).
	if noteTop >= noteMid || noteMid >= noteBot || noteBot >= label {
		t.Fatalf("note must be above the fields, in order: top=%d mid=%d bot=%d label=%d\n%s",
			noteTop, noteMid, noteBot, label, view)
	}
}

// TestFormGridMouseClickBandRejectsOffscreen guards the two-sided hit-test band:
// when the field window is scrolled, a click above lastRowsTop (title/intro/blank)
// or on the blank separator at lastWindowEnd must NOT map to an off-screen field
// (which would otherwise be silently focused and toggled/cycled).
func TestFormGridMouseClickBandRejectsOffscreen(t *testing.T) {
	fields := make([]*FormField, 12)
	for i := range fields {
		fields[i] = &FormField{Label: fmt.Sprintf("Toggle %d", i), Kind: FieldToggle}
	}
	g := NewFormGrid("Configuration", fields)
	bindGrid(g)

	// Scroll: move the cursor down so offset > 0 and fields stay hidden both
	// ABOVE and BELOW the window.
	for i := 0; i < 8; i++ {
		press(t, g, "down")
	}
	g.View(80, 12)
	if g.offset == 0 {
		t.Fatalf("expected the window to scroll (offset>0), got offset=%d", g.offset)
	}
	if g.lastWindowEnd >= len(fields)+g.lastRowsTop {
		t.Fatalf("test setup: window must not reach the last field (end=%d)", g.lastWindowEnd)
	}

	// Off-screen field ABOVE the window (index 0), and the field the blank at
	// lastWindowEnd would (pre-fix) map to BELOW the window.
	above := fields[0]
	belowIdx := g.offset + (g.lastWindowEnd - g.lastRowsTop)
	if belowIdx >= len(fields) {
		t.Fatalf("test setup: expected an off-screen field below the window, belowIdx=%d", belowIdx)
	}
	below := fields[belowIdx]
	if above.Bool || below.Bool {
		t.Fatalf("test setup: off-screen toggles must start false")
	}
	cursor0, editing0 := g.cursor, g.editing

	// CASE 1: click above lastRowsTop (the title/blank). Nothing may change.
	g.Update(click(4, 1)) //nolint:errcheck
	if above.Bool || below.Bool {
		t.Fatalf("click above the window must not toggle an off-screen field (above=%v below=%v)", above.Bool, below.Bool)
	}
	if g.cursor != cursor0 || g.editing != editing0 {
		t.Fatalf("click above the window must not move focus (cursor %d->%d editing %d->%d)", cursor0, g.cursor, editing0, g.editing)
	}

	// CASE 2: click on the blank separator at lastWindowEnd (below the window). The
	// field this Y maps to (belowIdx) must stay untouched and focus must not move.
	g.Update(click(4, g.lastWindowEnd)) //nolint:errcheck
	if above.Bool || below.Bool {
		t.Fatalf("click on the blank below the window must not toggle an off-screen field (above=%v below=%v)", above.Bool, below.Bool)
	}
	if g.cursor != cursor0 || g.editing != editing0 {
		t.Fatalf("click below the window must not move focus (cursor %d->%d editing %d->%d)", cursor0, g.cursor, editing0, g.editing)
	}
}

// TestFormGridMouseClickBelowWindowLeavesOffscreenSelectUnchanged mirrors CASE 2
// of TestFormGridMouseClickBandRejectsOffscreen but for the FieldSelect branch:
// a blank click at lastWindowEnd (below the scrolled window) must NOT cycle an
// off-screen select's OptionIndex, and must not move focus.
func TestFormGridMouseClickBelowWindowLeavesOffscreenSelectUnchanged(t *testing.T) {
	fields := make([]*FormField, 12)
	for i := range fields {
		fields[i] = &FormField{
			Label:   fmt.Sprintf("Select %d", i),
			Kind:    FieldSelect,
			Options: []string{"a", "b"}, // 2 options so a cycle is observable
		}
	}
	g := NewFormGrid("Configuration", fields)
	bindGrid(g)

	// Scroll: move the cursor down so offset > 0 and fields stay hidden BELOW
	// the window (same layout math as the FieldToggle band test).
	for i := 0; i < 8; i++ {
		press(t, g, "down")
	}
	g.View(80, 12)
	if g.offset == 0 {
		t.Fatalf("expected the window to scroll (offset>0), got offset=%d", g.offset)
	}
	if g.lastWindowEnd >= len(fields)+g.lastRowsTop {
		t.Fatalf("test setup: window must not reach the last field (end=%d)", g.lastWindowEnd)
	}

	// The field the blank at lastWindowEnd would (pre-fix) map to BELOW the window.
	belowIdx := g.offset + (g.lastWindowEnd - g.lastRowsTop)
	if belowIdx >= len(fields) {
		t.Fatalf("test setup: expected an off-screen field below the window, belowIdx=%d", belowIdx)
	}
	below := fields[belowIdx]
	if below.OptionIndex != 0 {
		t.Fatalf("test setup: off-screen select must start at index 0, got %d", below.OptionIndex)
	}
	cursor0, editing0 := g.cursor, g.editing

	// Click on the blank separator at lastWindowEnd (below the window). The
	// off-screen select this Y maps to must not cycle and focus must not move.
	g.Update(click(4, g.lastWindowEnd)) //nolint:errcheck
	if below.OptionIndex != 0 {
		t.Fatalf("click on the blank below the window must not cycle an off-screen select (index=%d)", below.OptionIndex)
	}
	if g.cursor != cursor0 || g.editing != editing0 {
		t.Fatalf("click below the window must not move focus (cursor %d->%d editing %d->%d)", cursor0, g.cursor, editing0, g.editing)
	}
}

func TestSplitSentences(t *testing.T) {
	got := splitSentences("Must be filesystem-mounted (e.g. /mnt/nas-backup). For direct network access use rclone.")
	if len(got) != 2 {
		t.Fatalf("expected 2 sentences (e.g. must not split), got %d: %q", len(got), got)
	}
	if !strings.HasPrefix(got[1], "For direct") {
		t.Fatalf("second sentence wrong: %q", got[1])
	}
	if one := splitSentences("Single sentence without split."); len(one) != 1 {
		t.Fatalf("single sentence must stay whole, got %q", one)
	}
}

func TestFormGridEscAndCancelButton(t *testing.T) {
	back := errors.New("cancelled")
	toggle, path, cron := gridFields()
	g := NewFormGrid("Configuration", []*FormField{toggle, path, cron},
		WithFormGridBack(back))
	cap := bindGrid(g)
	press(t, g, "esc")
	if !cap.resolved || !errors.Is(cap.err, back) {
		t.Fatalf("esc must resolve back sentinel, got %+v", cap)
	}

	toggle2, path2, cron2 := gridFields()
	g2 := NewFormGrid("Configuration", []*FormField{toggle2, path2, cron2},
		WithFormGridBack(back))
	cap2 := bindGrid(g2)
	press(t, g2, "enter") // -> cron
	press(t, g2, "enter") // -> buttons (Continue)
	press(t, g2, "right") // -> Cancel
	press(t, g2, "enter")
	if !cap2.resolved || !errors.Is(cap2.err, back) {
		t.Fatalf("Cancel button must resolve back sentinel, got %+v", cap2)
	}
}

// A FieldSelect with no Options must never divide by zero in the left/right/space
// or mouse-click handlers (the modulo guard mirrors renderControl). Shipped
// callers pass non-empty Options, but FormGrid/FieldSelect are reusable.
func TestFormGridSelectEmptyOptionsNoPanic(t *testing.T) {
	sel := &FormField{Label: "Empty select", Kind: FieldSelect, Options: nil}
	g := NewFormGrid("Configuration", []*FormField{sel})
	bindGrid(g)
	g.View(100, 20) // populate lastRowsTop for the click below

	// Cursor starts on the (only, active) select; none of these may panic.
	press(t, g, "right")
	press(t, g, "left")
	press(t, g, "space")
	if sel.OptionIndex != 0 {
		t.Fatalf("empty-options index must stay 0, got %d", sel.OptionIndex)
	}

	// A mouse click on the select row must not panic either.
	g.Update(click(4, g.lastRowsTop)) //nolint:errcheck
	if sel.OptionIndex != 0 {
		t.Fatalf("empty-options index must stay 0 after click, got %d", sel.OptionIndex)
	}
}

// F04-02: NewFormGrid must sanitize the FormField.Text prefill (config-on-disk /
// restored backup) exactly like Label/Description/Options, so raw ESC/OSC/C0 in a
// prefill cannot reach the terminal (display-spoofing to the root operator).
func TestNewFormGridSanitizesTextPrefill(t *testing.T) {
	fields := []*FormField{{Label: "Host", Kind: FieldText, Text: "foo\x1b[31mbar\x1b]0;evil\x07baz"}}
	_ = NewFormGrid("T", fields)
	got := fields[0].Text
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("FormField.Text still contains ESC: %q", got)
	}
	if strings.ContainsRune(got, 0x07) {
		t.Fatalf("FormField.Text still contains BEL: %q", got)
	}
	if got != "foobarbaz" {
		t.Fatalf("sanitized Text = %q, want %q", got, "foobarbaz")
	}
}
