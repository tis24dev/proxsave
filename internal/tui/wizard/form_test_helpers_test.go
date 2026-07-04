package wizard

import (
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// pressFormButton drives a tview form button by label (previously lived in
// the deleted age_test.go; still used by the telegram and post-install-audit
// wizard tests until those flows migrate off tview).
func pressFormButton(t *testing.T, form *tview.Form, label string) {
	t.Helper()
	index := form.GetButtonIndex(label)
	if index < 0 {
		t.Fatalf("button %q not found", label)
	}
	button := form.GetButton(index)
	handler := button.InputHandler()
	if handler == nil {
		t.Fatalf("button %q has no input handler", label)
	}
	handler(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(p tview.Primitive) {})
}
