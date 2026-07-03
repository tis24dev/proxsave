package components

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func bindSelector[T any](s *Selector[T]) *struct {
	resolved bool
	value    T
	err      error
} {
	cap := &struct {
		resolved bool
		value    T
		err      error
	}{}
	s.Bind(func(v T, err error) {
		cap.resolved = true
		cap.value = v
		cap.err = err
	})
	return cap
}

func threeItems() []SelectorItem[string] {
	return []SelectorItem[string]{
		{Label: "Full", Description: "restore everything", Value: "full"},
		{Label: "Storage", Description: "storage.cfg only", Value: "storage"},
		{Label: "Custom", Description: "pick categories", Value: "custom"},
	}
}

func TestSelectorNavigateAndSelect(t *testing.T) {
	s := NewSelector("Mode", threeItems())
	cap := bindSelector(s)
	press(t, s, "down")
	press(t, s, "down")
	press(t, s, "enter")
	if !cap.resolved || cap.err != nil || cap.value != "custom" {
		t.Fatalf("expected custom, got %q err=%v", cap.value, cap.err)
	}
}

func TestSelectorDigitShortcut(t *testing.T) {
	s := NewSelector("Mode", threeItems())
	cap := bindSelector(s)
	press(t, s, "2")
	if !cap.resolved || cap.value != "storage" {
		t.Fatalf("digit shortcut failed: %+v", cap)
	}
}

func TestSelectorEscWithoutBackIsIgnored(t *testing.T) {
	s := NewSelector("Mode", threeItems())
	cap := bindSelector(s)
	press(t, s, "esc")
	if cap.resolved {
		t.Fatal("esc without back option must not resolve")
	}
}

func TestSelectorEscResolvesBackError(t *testing.T) {
	back := errors.New("back to mode selection")
	s := NewSelector("Mode", threeItems(), WithSelectorBack[string](back))
	cap := bindSelector(s)
	press(t, s, "esc")
	if !cap.resolved || !errors.Is(cap.err, back) {
		t.Fatalf("expected back error, got %+v", cap)
	}
}

func TestSelectorInitialCursor(t *testing.T) {
	s := NewSelector("Mode", threeItems(), WithSelectorCursor[string](1))
	cap := bindSelector(s)
	press(t, s, "enter")
	if cap.value != "storage" {
		t.Fatalf("expected preselected storage, got %q", cap.value)
	}
}

func manyItems(n int) []SelectorItem[int] {
	items := make([]SelectorItem[int], n)
	for i := range items {
		items[i] = SelectorItem[int]{Label: fmt.Sprintf("backup-%02d", i), Value: i}
	}
	items[7].Label = "special-target"
	return items
}

func TestSelectorFilter(t *testing.T) {
	s := NewSelector("Backup", manyItems(12))
	cap := bindSelector(s)
	press(t, s, "/")
	if !s.filtering {
		t.Fatal("expected filtering mode after / on a long list")
	}
	for _, r := range "special" {
		press(t, s, string(r))
	}
	press(t, s, "enter") // leave filter entry, keep filter applied
	press(t, s, "enter") // select the single match
	if !cap.resolved || cap.value != 7 {
		t.Fatalf("expected filtered selection of 7, got %+v", cap)
	}
}

func TestSelectorFilterEscClearsFilter(t *testing.T) {
	s := NewSelector("Backup", manyItems(12))
	press(t, s, "/")
	press(t, s, "x")
	press(t, s, "esc")
	if s.filtering || s.filter != "" {
		t.Fatalf("esc must clear filter state, got filtering=%v filter=%q", s.filtering, s.filter)
	}
}

func TestSelectorEscClearsRetainedFilterBeforeBack(t *testing.T) {
	back := errors.New("back")
	s := NewSelector("Backup", manyItems(12), WithSelectorBack[int](back))
	cap := bindSelector(s)
	press(t, s, "/")
	press(t, s, "x")
	press(t, s, "enter") // retain filter, leave editing
	press(t, s, "esc")   // first esc clears the retained filter
	if cap.resolved {
		t.Fatal("first esc must clear the filter, not navigate back")
	}
	if s.filter != "" {
		t.Fatalf("filter not cleared: %q", s.filter)
	}
	press(t, s, "esc") // second esc navigates back
	if !cap.resolved || !errors.Is(cap.err, back) {
		t.Fatalf("second esc must resolve back sentinel, got %+v", cap)
	}
}

func TestSelectorSanitizesItems(t *testing.T) {
	s := NewSelector("Backup", []SelectorItem[string]{
		{Label: "evil\x1b[31mfile", Description: "line\nbreak", Value: "v"},
	})
	view := s.View(80, 10)
	if strings.Contains(view, "\x1b[31m") {
		t.Error("raw ANSI from item data must be stripped")
	}
	if !strings.Contains(view, "evilfile") {
		t.Errorf("sanitized label missing: %q", view)
	}
}

func TestSelectorShortListHasNoFilter(t *testing.T) {
	s := NewSelector("Mode", threeItems())
	press(t, s, "/")
	if s.filtering {
		t.Fatal("short lists must not enter filter mode")
	}
}

func TestSelectorViewShowsCursorAndScrolls(t *testing.T) {
	s := NewSelector("Backup", manyItems(12))
	view := s.View(60, 8)
	if !strings.Contains(view, "backup-00") {
		t.Errorf("first row missing: %q", view)
	}
	// Move beyond the window: the view must scroll to keep the cursor.
	for i := 0; i < 11; i++ {
		press(t, s, "down")
	}
	view = s.View(60, 8)
	if !strings.Contains(view, "backup-11") {
		t.Errorf("cursor row missing after scroll: %q", view)
	}
	if strings.Contains(view, "backup-00") {
		t.Errorf("scrolled view still shows first row: %q", view)
	}
}

func TestSelectorEmptyEnterIgnored(t *testing.T) {
	s := NewSelector("Empty", []SelectorItem[string]{})
	cap := bindSelector(s)
	press(t, s, "enter")
	if cap.resolved {
		t.Fatal("enter on empty selector must not resolve")
	}
}
