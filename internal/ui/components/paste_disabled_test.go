package components

import "testing"

// TestNewInputDisablesPasteBinding pins F01-01: the ctrl+v paste keybinding must
// be disabled so it never runs the external clipboard helper (atotto/clipboard,
// resolved by bare name via PATH = root code execution). Bracketed paste is a
// separate, terminal-native path and is unaffected.
func TestNewInputDisablesPasteBinding(t *testing.T) {
	in := NewInput("title", "prompt")
	if in.ti.KeyMap.Paste.Enabled() {
		t.Fatal("ctrl+v paste binding must be disabled on NewInput (F01-01)")
	}
}

func TestNewFormGridDisablesPasteBinding(t *testing.T) {
	g := NewFormGrid("title", []*FormField{{Label: "L", Kind: FieldText}})
	if g.ti.KeyMap.Paste.Enabled() {
		t.Fatal("ctrl+v paste binding must be disabled on NewFormGrid (F01-01)")
	}
}
