package components

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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

func actionsMulti() *MultiSelect[string] {
	return NewMultiSelect("Categories", categoryItems(),
		WithMultiSelectActions[string]("Select ALL", "Disable Selected"))
}

// With action buttons, a plain Enter on a toggle row toggles that row and never
// confirms; only the confirm button resolves.
func TestMultiSelectActionsEnterOnItemDoesNotConfirm(t *testing.T) {
	m := actionsMulti() // 3 items (rows 0-2), Select ALL (3), Disable Selected (4)
	cap := bindMulti(m)
	// Cursor on item 0 (network, pre-selected): Enter toggles it OFF, no resolve.
	press(t, m, "enter")
	if cap.resolved {
		t.Fatal("enter on a toggle row must not confirm the screen")
	}
	if m.selectedCount() != 0 {
		t.Fatalf("enter on network should have deselected it, selected=%d", m.selectedCount())
	}
	// Walk to the confirm button and press it.
	for i := 0; i < 4; i++ {
		press(t, m, "down")
	}
	press(t, m, "enter")
	if !cap.resolved || cap.err != nil {
		t.Fatalf("enter on Disable Selected must confirm, got %+v", cap)
	}
	if len(cap.values) != 0 {
		t.Fatalf("nothing selected -> 0 values, got %v", cap.values)
	}
}

// The Select ALL button toggles every item (select all, then deselect all), and
// never confirms; you still have to press Disable Selected.
func TestMultiSelectActionsSelectAllButtonToggles(t *testing.T) {
	m := actionsMulti()
	cap := bindMulti(m)
	for i := 0; i < 3; i++ { // to Select ALL (row 3)
		press(t, m, "down")
	}
	press(t, m, "enter") // 1/3 -> all selected
	if cap.resolved {
		t.Fatal("Select ALL must not confirm")
	}
	if m.selectedCount() != 3 {
		t.Fatalf("Select ALL should select all, got %d", m.selectedCount())
	}
	press(t, m, "enter") // all selected -> deselect all
	if m.selectedCount() != 0 {
		t.Fatalf("Select ALL again should deselect all, got %d", m.selectedCount())
	}
	press(t, m, "enter") // select all again -> 3
	press(t, m, "down")  // to Disable Selected
	press(t, m, "enter") // confirm
	if !cap.resolved || len(cap.values) != 3 {
		t.Fatalf("confirm after Select ALL -> 3 values, got %+v", cap)
	}
}

// space on a button row is a no-op (only toggle rows toggle).
func TestMultiSelectActionsSpaceOnButtonIsNoop(t *testing.T) {
	m := actionsMulti()
	before := m.selectedCount()
	for i := 0; i < 4; i++ { // 3 items + skipped spacer -> Disable Selected
		press(t, m, "down")
	}
	press(t, m, "space")
	if m.selectedCount() != before {
		t.Fatalf("space on a button must not change selection: %d -> %d", before, m.selectedCount())
	}
}

// A blank spacer row separates the list from the buttons: it renders as an empty
// line and the cursor skips over it in both directions.
func TestMultiSelectActionsSpacerRow(t *testing.T) {
	m := actionsMulti() // 3 items -> spacer=3, Select ALL=4, Disable Selected=5
	if m.spacerRow() != 3 || m.selectAllRow() != 4 || m.confirmRow() != 5 {
		t.Fatalf("layout: spacer=%d selectAll=%d confirm=%d", m.spacerRow(), m.selectAllRow(), m.confirmRow())
	}
	for i := 0; i < 3; i++ { // 0->1->2->(skip 3)->4
		press(t, m, "down")
	}
	if m.cursor != 4 {
		t.Fatalf("down from the last item must skip the spacer to Select ALL (4), got %d", m.cursor)
	}
	press(t, m, "up") // 4->(skip 3)->2
	if m.cursor != 2 {
		t.Fatalf("up from Select ALL must skip the spacer to the last item (2), got %d", m.cursor)
	}

	// The rendered view has a blank line immediately before the Select ALL button.
	lines := strings.Split(ansi.Strip(m.View(80, 20)), "\n")
	idx := -1
	for i, ln := range lines {
		if strings.Contains(ln, "Select ALL") {
			idx = i
			break
		}
	}
	if idx <= 0 {
		t.Fatalf("Select ALL button not found in view: %q", lines)
	}
	if strings.TrimSpace(lines[idx-1]) != "" {
		t.Fatalf("expected a blank spacer line before the buttons, got %q", lines[idx-1])
	}
}

func TestMultiSelectActionsViewShowsButtons(t *testing.T) {
	view := actionsMulti().View(80, 20)
	if !strings.Contains(view, "Select ALL") || !strings.Contains(view, "Disable Selected") {
		t.Fatalf("action buttons missing from view: %q", view)
	}
}

func detailItems() []MultiSelectItem[string] {
	return []MultiSelectItem[string]{
		{Label: "BACKUP_ZFS_CONFIG", Value: "zfs", Detail: "ZFS configuration pool cache description."},
		{Label: "BACKUP_CEPH_CONFIG", Value: "ceph", Detail: "Ceph cluster configuration description."},
	}
}

// The detail pane renders the highlighted item's Detail on the right, follows the
// cursor, and keeps the list column clean (no inline Description).
func TestMultiSelectDetailPane(t *testing.T) {
	m := NewMultiSelect("Unused components", detailItems(),
		WithMultiSelectDetailPane[string]("Details"),
		WithMultiSelectActions[string]("Select ALL", "Disable Selected"))

	view := ansi.Strip(m.View(100, 24))
	if !strings.Contains(view, "Details") {
		t.Fatalf("detail pane title missing: %q", view)
	}
	if !strings.Contains(view, "│") {
		t.Fatalf("two-pane separator missing: %q", view)
	}
	if !strings.Contains(view, "ZFS configuration pool cache") {
		t.Fatalf("highlighted item's detail missing: %q", view)
	}
	if strings.Contains(view, "Ceph cluster configuration") {
		t.Fatalf("only the highlighted item's detail should render: %q", view)
	}

	press(t, m, "down") // highlight the second item
	view2 := ansi.Strip(m.View(100, 24))
	if !strings.Contains(view2, "Ceph cluster configuration") {
		t.Fatalf("detail pane must follow the cursor: %q", view2)
	}
}

// A terminal too narrow for two columns falls back to a single column.
func TestMultiSelectDetailPaneNarrowFallback(t *testing.T) {
	m := NewMultiSelect("T", detailItems(), WithMultiSelectDetailPane[string]("Details"))
	if strings.Contains(ansi.Strip(m.View(30, 10)), "│") {
		t.Fatal("narrow width must not render the two-pane separator")
	}
}

// A click above the first list row (the title or the blank separator) must be a
// no-op even when scrolled, and must not toggle a hidden off-screen item.
func TestMultiSelectMouseClickAboveListIsNoop(t *testing.T) {
	items := make([]MultiSelectItem[string], 30)
	for i := range items {
		items[i] = MultiSelectItem[string]{Label: fmt.Sprintf("item-%d", i), Value: fmt.Sprintf("item-%d", i)}
	}
	m := NewMultiSelect("Categories", items)
	bindMulti(m)
	for i := 0; i < 20; i++ { // scroll the window down
		press(t, m, "down")
	}
	m.View(80, 12) // establish lastRowsTop and offset>0
	if m.offset == 0 {
		t.Fatalf("test setup: expected the list to be scrolled, offset=%d", m.offset)
	}

	before := make([]bool, len(m.items))
	for i, it := range m.items {
		before[i] = it.Selected
	}

	// Click the blank separator row just above the first list row.
	m.Update(click(4, m.lastRowsTop-1)) //nolint:errcheck

	for i, it := range m.items {
		if it.Selected != before[i] {
			t.Fatalf("click above the list toggled item %d (Selected %v -> %v)", i, before[i], it.Selected)
		}
	}
}

// lipgloss.Place top-pads the body, so a scrolled window leaves a blank line
// below it. A click there must not toggle an off-screen item.
func TestMultiSelectMouseClickBelowWindowIsNoop(t *testing.T) {
	items := make([]MultiSelectItem[string], 30)
	for i := range items {
		items[i] = MultiSelectItem[string]{Label: fmt.Sprintf("item-%d", i), Value: fmt.Sprintf("item-%d", i)}
	}
	m := NewMultiSelect("Categories", items)
	bindMulti(m)
	for i := 0; i < 20; i++ {
		press(t, m, "down")
	}
	m.View(80, 12)
	if m.offset == 0 {
		t.Fatalf("test setup: expected the list to be scrolled, offset=%d", m.offset)
	}
	if row := m.lastWindowEnd - m.lastRowsTop + m.offset; row >= m.totalRows() {
		t.Fatalf("test setup: window reaches the end (row=%d); below-blank rejected anyway", row)
	}

	before := make([]bool, len(m.items))
	for i, it := range m.items {
		before[i] = it.Selected
	}
	m.Update(click(4, m.lastWindowEnd)) //nolint:errcheck
	for i, it := range m.items {
		if it.Selected != before[i] {
			t.Fatalf("click below the window toggled item %d (Selected %v -> %v)", i, before[i], it.Selected)
		}
	}
}

// With action buttons, a below-window blank click could (pre-fix) land on the
// off-screen confirm button and silently resolve the whole screen.
func TestMultiSelectMouseClickBelowWindowDoesNotHitOffscreenConfirm(t *testing.T) {
	items := make([]MultiSelectItem[string], 8)
	for i := range items {
		items[i] = MultiSelectItem[string]{Label: fmt.Sprintf("item-%d", i), Value: fmt.Sprintf("item-%d", i)}
	}
	m := NewMultiSelect("Categories", items, WithMultiSelectActions[string]("Select all", "Apply"))
	cap := bindMulti(m)
	for i := 0; i < 8; i++ { // land on the select-all button, forcing offset>0
		press(t, m, "down")
	}
	m.View(80, 8)
	if m.offset == 0 {
		t.Fatalf("test setup: expected a scrolled list, offset=%d", m.offset)
	}
	// Precondition: the below-window blank maps exactly to the off-screen confirm row.
	if row := m.lastWindowEnd - m.lastRowsTop + m.offset; row != m.confirmRow() {
		t.Fatalf("test setup: below-blank maps to row %d, want confirmRow %d", row, m.confirmRow())
	}
	m.Update(click(4, m.lastWindowEnd)) //nolint:errcheck
	if cap.resolved {
		t.Fatalf("click on the blank below the window must not trigger the off-screen confirm button")
	}
}

// The band guard must not over-reject: a click on the FIRST visible row while
// scrolled toggles that row (offset), not row 0 and not a no-op.
func TestMultiSelectMouseClickFirstVisibleRowToggles(t *testing.T) {
	items := make([]MultiSelectItem[string], 30)
	for i := range items {
		items[i] = MultiSelectItem[string]{Label: fmt.Sprintf("item-%d", i), Value: fmt.Sprintf("item-%d", i)}
	}
	m := NewMultiSelect("Categories", items)
	bindMulti(m)
	for i := 0; i < 20; i++ {
		press(t, m, "down")
	}
	m.View(80, 12)
	if m.offset == 0 {
		t.Fatalf("test setup: expected the list to be scrolled, offset=%d", m.offset)
	}
	first := m.offset
	m.Update(click(4, m.lastRowsTop)) //nolint:errcheck
	if !m.items[first].Selected {
		t.Fatalf("click on the first visible row must toggle item %d", first)
	}
	for i, it := range m.items {
		if i != first && it.Selected {
			t.Fatalf("click on the first visible row also toggled item %d", i)
		}
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
