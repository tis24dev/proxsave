package components

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

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
