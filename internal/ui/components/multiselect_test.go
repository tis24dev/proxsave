package components

import (
	"errors"
	"strings"
	"testing"
)

func categoryItems() []MultiSelectItem[string] {
	return []MultiSelectItem[string]{
		{Label: "network", Description: "interfaces, sdn", Value: "network", Selected: true},
		{Label: "storage", Description: "storage.cfg", Value: "storage"},
		{Label: "firewall", Description: "cluster.fw", Value: "firewall"},
	}
}

func bindMulti(m *MultiSelect[string]) *struct {
	resolved bool
	values   []string
	err      error
} {
	cap := &struct {
		resolved bool
		values   []string
		err      error
	}{}
	m.Bind(func(v []string, err error) {
		cap.resolved = true
		cap.values = v
		cap.err = err
	})
	return cap
}

func TestMultiSelectToggleAndConfirm(t *testing.T) {
	m := NewMultiSelect("Categories", categoryItems())
	cap := bindMulti(m)
	press(t, m, "down")
	press(t, m, "space") // select storage
	press(t, m, "enter")
	if !cap.resolved || cap.err != nil {
		t.Fatalf("expected resolution, got %+v", cap)
	}
	want := []string{"network", "storage"}
	if len(cap.values) != 2 || cap.values[0] != want[0] || cap.values[1] != want[1] {
		t.Fatalf("values = %v, want %v", cap.values, want)
	}
}

func TestMultiSelectMinimumBlocksConfirm(t *testing.T) {
	items := categoryItems()
	items[0].Selected = false
	m := NewMultiSelect("Categories", items, WithMinSelected[string](1))
	cap := bindMulti(m)
	press(t, m, "enter")
	if cap.resolved {
		t.Fatal("confirm below the minimum must not resolve")
	}
	if !strings.Contains(m.View(80, 20), "at least 1") {
		t.Error("validation message must be shown")
	}
	press(t, m, "space")
	press(t, m, "enter")
	if !cap.resolved || len(cap.values) != 1 {
		t.Fatalf("expected resolution after selecting, got %+v", cap)
	}
}

func TestMultiSelectAllAndInvert(t *testing.T) {
	m := NewMultiSelect("Categories", categoryItems())
	cap := bindMulti(m)
	press(t, m, "a")
	press(t, m, "enter")
	if len(cap.values) != 3 {
		t.Fatalf("select-all should resolve 3 values, got %v", cap.values)
	}

	m2 := NewMultiSelect("Categories", categoryItems())
	cap2 := bindMulti(m2)
	press(t, m2, "i") // invert: network off, storage+firewall on
	press(t, m2, "enter")
	want := []string{"storage", "firewall"}
	if len(cap2.values) != 2 || cap2.values[0] != want[0] || cap2.values[1] != want[1] {
		t.Fatalf("invert resolved %v, want %v", cap2.values, want)
	}
}

func TestMultiSelectEscBackSentinel(t *testing.T) {
	back := errors.New("back to mode selection")
	m := NewMultiSelect("Categories", categoryItems(), WithMultiSelectBack[string](back))
	cap := bindMulti(m)
	press(t, m, "esc")
	if !cap.resolved || !errors.Is(cap.err, back) {
		t.Fatalf("expected back sentinel, got %+v", cap)
	}

	m2 := NewMultiSelect("Categories", categoryItems())
	cap2 := bindMulti(m2)
	press(t, m2, "esc")
	if cap2.resolved {
		t.Fatal("esc without back sentinel must be ignored")
	}
}

func TestMultiSelectViewShowsState(t *testing.T) {
	m := NewMultiSelect("Categories", categoryItems())
	view := m.View(80, 20)
	if !strings.Contains(view, "(1/3 selected)") {
		t.Errorf("selection count missing: %q", view)
	}
	if !strings.Contains(view, "☑") || !strings.Contains(view, "☐") {
		t.Errorf("checkbox symbols missing: %q", view)
	}
}
