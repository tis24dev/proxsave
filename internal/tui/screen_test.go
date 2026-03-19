package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func primitiveContainsText(p tview.Primitive, want string) bool {
	return primitiveContainsTextWithVisited(p, want, map[uintptr]struct{}{})
}

func primitiveContainsTextWithVisited(p tview.Primitive, want string, visited map[uintptr]struct{}) bool {
	switch v := p.(type) {
	case nil:
		return false
	case *tview.TextView:
		return strings.Contains(v.GetTitle(), want) || strings.Contains(v.GetText(false), want)
	case *tview.Box:
		return strings.Contains(v.GetTitle(), want)
	case *tview.Button:
		return strings.Contains(v.GetTitle(), want) || strings.Contains(v.GetLabel(), want)
	case *tview.Flex:
		if strings.Contains(v.GetTitle(), want) {
			return true
		}
		for i := 0; i < v.GetItemCount(); i++ {
			if primitiveContainsTextWithVisited(v.GetItem(i), want, visited) {
				return true
			}
		}
		return false
	case *tview.Form:
		if strings.Contains(v.GetTitle(), want) {
			return true
		}
		for i := 0; i < v.GetFormItemCount(); i++ {
			if strings.Contains(v.GetFormItem(i).GetLabel(), want) {
				return true
			}
		}
		for i := 0; i < v.GetButtonCount(); i++ {
			if strings.Contains(v.GetButton(i).GetLabel(), want) {
				return true
			}
		}
	case *tview.List:
		if strings.Contains(v.GetTitle(), want) {
			return true
		}
		for i := 0; i < v.GetItemCount(); i++ {
			main, secondary := v.GetItemText(i)
			if strings.Contains(main, want) || strings.Contains(secondary, want) {
				return true
			}
		}
		return false
	case *tview.Pages:
		if strings.Contains(v.GetTitle(), want) {
			return true
		}
		for _, name := range v.GetPageNames(true) {
			if primitiveContainsTextWithVisited(v.GetPage(name), want, visited) {
				return true
			}
		}
		return false
	case *tview.Frame:
		if strings.Contains(v.GetTitle(), want) {
			return true
		}
		if primitiveContainsTextWithVisited(v.GetPrimitive(), want, visited) {
			return true
		}
	case *tview.Modal:
		if strings.Contains(v.GetTitle(), want) {
			return true
		}
	default:
	}

	return reflectedValueContainsText(reflect.ValueOf(p), want, visited)
}

func reflectedValueContainsText(v reflect.Value, want string, visited map[uintptr]struct{}) bool {
	if !v.IsValid() {
		return false
	}
	for v.Kind() == reflect.Interface {
		if v.IsNil() {
			return false
		}
		v = v.Elem()
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return false
		}
		ptr := v.Pointer()
		if ptr != 0 {
			if _, seen := visited[ptr]; seen {
				return false
			}
			visited[ptr] = struct{}{}
		}
		return reflectedValueContainsText(v.Elem(), want, visited)
	}

	switch v.Kind() {
	case reflect.String:
		return strings.Contains(v.String(), want)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if reflectedValueContainsText(v.Field(i), want, visited) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if reflectedValueContainsText(v.Index(i), want, visited) {
				return true
			}
		}
	}
	return false
}

func TestBuildScreenOmitsEmptyOptionalFooters(t *testing.T) {
	page := BuildScreen(ScreenSpec{
		Title:           "Title",
		HeaderText:      "Header",
		NavText:         "Navigation",
		ConfigPath:      "",
		BuildSig:        "sig",
		TitleColor:      ProxmoxOrange,
		BorderColor:     ProxmoxOrange,
		BackgroundColor: tcell.ColorBlack,
	}, tview.NewBox())

	if primitiveContainsText(page, "Configuration file:") {
		t.Fatalf("did not expect configuration footer when ConfigPath is empty")
	}
	if !primitiveContainsText(page, "Build Signature:") {
		t.Fatalf("expected build signature footer")
	}
}

func TestBuildScreenEscapesHeaderText(t *testing.T) {
	page := BuildScreen(ScreenSpec{
		Title:           "Title",
		HeaderText:      "Header[prod]",
		NavText:         "",
		ConfigPath:      "",
		BuildSig:        "",
		TitleColor:      ProxmoxOrange,
		BorderColor:     ProxmoxOrange,
		BackgroundColor: tcell.ColorBlack,
	}, tview.NewBox())

	if !primitiveContainsText(page, tview.Escape("Header[prod]")) {
		t.Fatalf("expected escaped header text")
	}
}

func TestPrimitiveContainsTextFindsBoxTitle(t *testing.T) {
	box := tview.NewBox().SetTitle("Box Title")

	if !primitiveContainsText(box, "Box Title") {
		t.Fatalf("expected box title to be discovered")
	}
}

func TestPrimitiveContainsTextFindsFormLabelsAndButtons(t *testing.T) {
	form := tview.NewForm().
		AddInputField("Token", "", 0, nil, nil).
		AddButton("Save", nil)

	if !primitiveContainsText(form, "Token") {
		t.Fatalf("expected form label to be discovered")
	}
	if !primitiveContainsText(form, "Save") {
		t.Fatalf("expected form button label to be discovered")
	}
}

func TestPrimitiveContainsTextFallsBackToModalText(t *testing.T) {
	modal := tview.NewModal().
		SetText("Danger zone").
		AddButtons([]string{"Continue"})

	if !primitiveContainsText(modal, "Danger zone") {
		t.Fatalf("expected modal text to be discovered")
	}
	if !primitiveContainsText(modal, "Continue") {
		t.Fatalf("expected modal button label to be discovered")
	}
}
