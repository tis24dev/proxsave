package components

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/viewport"
	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/uitest"
)

// feedLines appends raw lines through the same batched Update path production uses.
func feedLines(scr *StreamTask, token uint64, lines ...string) *StreamTask {
	updated, _ := scr.Update(StreamLinesMsg{Token: token, Lines: lines})
	return updated.(*StreamTask)
}

// equalRows reports exact slice content equality.
func equalRows(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestWrapFidelityAgainstViewport is the LOAD-BEARING invariant: feeding the
// viewport our pre-wrapped lines with SoftWrap OFF must render IDENTICALLY to
// letting the viewport SoftWrap the raw lines itself. If bubbles ever changes
// its wrap algorithm this fails loudly instead of silently garbling.
func TestWrapFidelityAgainstViewport(t *testing.T) {
	raws := []string{
		"a short line",
		"",
		strings.Repeat("y", 173),
		"\x1b[31m" + strings.Repeat("red ", 50) + "\x1b[0m",
		"[a] INFO " + strings.Repeat("mixed \x1b[36mcyan\x1b[0m ", 12),
	}
	for _, w := range []int{12, 24, 40, 80} {
		ours := viewport.New()
		ours.SoftWrap = false
		ours.SetWidth(w)
		ours.SetHeight(500)
		ours.SetContent(strings.Join(wrapLines(raws, w), "\n"))
		ours.GotoTop()

		ref := viewport.New()
		ref.SoftWrap = true
		ref.SetWidth(w)
		ref.SetHeight(500)
		ref.SetContent(strings.Join(raws, "\n"))
		ref.GotoTop()

		if ours.TotalLineCount() != ref.TotalLineCount() {
			t.Fatalf("width %d: wrapped row count ours=%d ref=%d", w, ours.TotalLineCount(), ref.TotalLineCount())
		}
		if ours.View() != ref.View() {
			t.Fatalf("width %d: rendered output differs\nOURS:\n%q\nREF:\n%q", w, ours.View(), ref.View())
		}
	}
}

// TestColoredLineWrapKeepsColor proves a colored line keeps its SGR across every
// wrap boundary (ansi.Cut re-emits the active SGR at each chunk start).
func TestColoredLineWrapKeepsColor(t *testing.T) {
	raw := "\x1b[32m" + strings.Repeat("g", 60) + "\x1b[0m"
	rows := wrapLine(raw, 20) // 60 wide -> 3 chunks at width 20
	if len(rows) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(rows))
	}
	for i, row := range rows {
		if !strings.Contains(row, "\x1b[32m") {
			t.Fatalf("chunk %d dropped the green SGR: %q", i, row)
		}
	}
}

// TestWrapLineWideGraphemeSafe proves wide graphemes (CJK/emoji) that straddle a
// column boundary never produce an over-width chunk (which the SoftWrap-off
// viewport would truncate) and never drop a glyph: every row is <= width and the
// stripped rows reassemble to the stripped original. Covers odd widths where a
// 2-cell grapheme lands across the boundary.
func TestWrapLineWideGraphemeSafe(t *testing.T) {
	cases := []string{"世世世世世世", "a世b世c世de", "🎉🎉🎉🎉🎉", "x世y世z世w世"}
	// width >= 2: rows must be <= width AND lose nothing.
	for _, w := range []int{2, 5, 6, 7, 9} {
		for _, c := range cases {
			rows := wrapLine(c, w)
			for i, r := range rows {
				if ansi.StringWidth(r) > w {
					t.Fatalf("wrapLine(%q,%d) row %d is over width: %q (w=%d)", c, w, i, r, ansi.StringWidth(r))
				}
			}
			if got, want := ansi.Strip(strings.Join(rows, "")), ansi.Strip(c); got != want {
				t.Fatalf("wrapLine(%q,%d) dropped/reordered content: reassembled %q want %q", c, w, got, want)
			}
		}
	}
	// width 1 is degenerate + unreachable in the real layout (router min width),
	// but wrapLine must still NOT DROP a glyph (rows may be over-width there).
	for _, c := range cases {
		rows := wrapLine(c, 1)
		if got, want := ansi.Strip(strings.Join(rows, "")), ansi.Strip(c); got != want {
			t.Fatalf("wrapLine(%q,1) dropped content: reassembled %q want %q", c, got, want)
		}
	}
}

// TestBlankLineWrapsToOneRow proves a blank section spacer wraps to exactly one
// empty row (never dropped, never expanded).
func TestBlankLineWrapsToOneRow(t *testing.T) {
	rows := wrapLine("", 40)
	if len(rows) != 1 || rows[0] != "" {
		t.Fatalf("blank line must wrap to one empty row, got %q", rows)
	}
}

// F04-03: a single pathologically long line must be capped before wrapLine so the
// O(L^2/width) loop cannot block the event loop; the cap carries a marker.
func TestWrapLineCapsPathologicalLine(t *testing.T) {
	const width = 80
	rows := wrapLine(strings.Repeat("z", 200_000), width)
	maxRows := streamLineWidthCap/width + 2
	if len(rows) > maxRows {
		t.Fatalf("pathological line not capped: got %d rows, want <= %d", len(rows), maxRows)
	}
	if !strings.Contains(strings.Join(rows, ""), "(truncated)") {
		t.Fatal("capped line must carry the truncation marker")
	}
}

// TestIncrementalWrapIsBounded proves an append does NOT re-wrap the whole
// buffer: after N lines are present, one more append adds only that line's
// wrapped rows and leaves every earlier wrapped row byte-identical. This is the
// O(new)-not-O(N) contract.
func TestIncrementalWrapIsBounded(t *testing.T) {
	scr := newStreamScreen("Running backup", 1, func() {})
	scr.View(40, 20) // establish a width so appends wrap incrementally
	const n = 3000
	for i := 0; i < n; i++ {
		scr = feedLines(scr, 1, fmt.Sprintf("line %d", i))
	}
	before := append([]string(nil), scr.wrapped...)
	beforeLen := len(scr.wrapped)

	scr = feedLines(scr, 1, "the newest short line")
	if len(scr.wrapped) != beforeLen+1 {
		t.Fatalf("short append should add exactly 1 wrapped row, added %d", len(scr.wrapped)-beforeLen)
	}
	for i := range before {
		if scr.wrapped[i] != before[i] {
			t.Fatalf("append re-wrapped prior row %d: %q -> %q", i, before[i], scr.wrapped[i])
		}
	}
	// A long line adds several wrapped rows without disturbing earlier ones.
	mark := len(scr.wrapped)
	scr = feedLines(scr, 1, strings.Repeat("z", 200))
	if len(scr.wrapped) <= mark+1 {
		t.Fatalf("long line should add several wrapped rows, added %d", len(scr.wrapped)-mark)
	}
	for i := 0; i < mark && i < len(before); i++ {
		if scr.wrapped[i] != before[i] {
			t.Fatalf("long append disturbed earlier row %d", i)
		}
	}
}

// TestWidthChangeRewraps proves a width change re-wraps the whole buffer to the
// new width, while a same-width View does not re-wrap.
func TestWidthChangeRewraps(t *testing.T) {
	scr := newStreamScreen("Running backup", 1, func() {})
	long := strings.Repeat("w", 100)
	scr.View(80, 20)
	scr = feedLines(scr, 1, long)
	if len(scr.wrapped) != 2 { // ceil(100/80)=2
		t.Fatalf("100-wide line at width 80 should wrap to 2 rows, got %d", len(scr.wrapped))
	}
	// Same width again: no re-wrap (slice backing unchanged).
	prev := scr.wrapped
	scr.View(80, 20)
	if len(scr.wrapped) > 0 && len(prev) > 0 && &scr.wrapped[0] != &prev[0] {
		t.Fatal("same-width View must not re-wrap")
	}
	// Narrower width: whole buffer re-wraps.
	scr.View(20, 20)
	if scr.wrapWidth != 20 {
		t.Fatalf("wrapWidth should track the new width, got %d", scr.wrapWidth)
	}
	if len(scr.wrapped) != 5 { // ceil(100/20)=5
		t.Fatalf("100-wide line at width 20 should wrap to 5 rows, got %d", len(scr.wrapped))
	}
}

// TestCopyUsesRawNotWrapped proves 'c' copies the RAW logical lines (ANSI
// stripped), NOT the cosmetic wrap boundaries.
func TestCopyUsesRawNotWrapped(t *testing.T) {
	scr := newStreamScreen("Running backup", 1, func() {})
	scr.View(10, 20) // narrow, so the line wraps into many display rows
	longWord := strings.Repeat("q", 55)
	scr = feedLines(scr, 1, "\x1b[33m"+longWord+"\x1b[0m")
	if len(scr.wrapped) <= 1 {
		t.Fatalf("precondition: line should have wrapped, wrapped=%d", len(scr.wrapped))
	}
	_, cmd := scr.Update(shell.KeyMsg("c"))
	if cmd == nil {
		t.Fatal("'c' should return a SetClipboard command")
	}
	got := fmt.Sprintf("%s", cmd())
	if got != longWord {
		t.Fatalf("clipboard must be the RAW logical line (ANSI-stripped, no wrap breaks); got %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("copy must not contain wrap-boundary newlines: %q", got)
	}
}

// TestRingDropKeepsRawAndWrappedConsistent proves the cap sheds the oldest RAW
// lines AND exactly their wrapped rows, so wrapped always equals a fresh
// re-wrap of the retained raw lines (the O(drop) ring-drop fix).
func TestRingDropKeepsRawAndWrappedConsistent(t *testing.T) {
	scr := newStreamScreen("Running backup", 1, func() {})
	scr.View(20, 20) // establish width so appends wrap incrementally
	for i := 0; i < streamLineCap+300; i++ {
		if i%3 == 0 {
			scr = feedLines(scr, 1, strings.Repeat("L", 50)) // 3 wrapped rows at width 20
		} else {
			scr = feedLines(scr, 1, fmt.Sprintf("s%d", i)) // 1 row
		}
	}
	if len(scr.lines) != streamLineCap {
		t.Fatalf("ring must be capped at %d, got %d", streamLineCap, len(scr.lines))
	}
	if !scr.dropped {
		t.Fatal("dropped flag must be set once the ring sheds lines")
	}
	want := wrapLines(scr.lines, scr.wrapWidth)
	if !equalRows(scr.wrapped, want) {
		t.Fatalf("wrapped mirror drifted from the raw ring after cap drops: ours=%d want=%d",
			len(scr.wrapped), len(want))
	}
}

// TestRingDropConsistentBigBatch stresses the multi-line drop path: a single
// batch that overflows the cap by many lines must drop the right number of
// wrapped rows too.
func TestRingDropConsistentBigBatch(t *testing.T) {
	scr := newStreamScreen("Running backup", 1, func() {})
	scr.View(20, 20)
	// Prime near the cap.
	for i := 0; i < streamLineCap-10; i++ {
		scr = feedLines(scr, 1, fmt.Sprintf("s%d", i))
	}
	// One big batch of long (multi-row) lines overflows the cap by ~90.
	big := make([]string, 100)
	for i := range big {
		big[i] = strings.Repeat("B", 45) // 3 rows at width 20
	}
	scr = feedLines(scr, 1, big...)
	if len(scr.lines) != streamLineCap {
		t.Fatalf("ring must cap at %d after the big batch, got %d", streamLineCap, len(scr.lines))
	}
	want := wrapLines(scr.lines, scr.wrapWidth)
	if !equalRows(scr.wrapped, want) {
		t.Fatalf("wrapped mirror drifted after a cap-overflowing batch: ours=%d want=%d", len(scr.wrapped), len(want))
	}
}

// TestGateSkipsUnchangedRenders proves the View gate re-feeds the viewport only
// when the content changed: repeated identical renders (spinner ticks) must not
// mark the screen dirty again.
func TestGateSkipsUnchangedRenders(t *testing.T) {
	scr := newStreamScreen("Running backup", 1, func() {})
	scr = feedLines(scr, 1, "one", "two")
	scr.View(80, 20) // first paint clears dirty
	if scr.dirty {
		t.Fatal("View should clear dirty after feeding the content")
	}
	// A no-change render leaves dirty false (nothing to push).
	scr.View(80, 20)
	if scr.dirty {
		t.Fatal("identical re-render must not set dirty")
	}
	// A new line sets dirty; the next View clears it again.
	scr = feedLines(scr, 1, "three")
	if !scr.dirty {
		t.Fatal("a new line must mark the content dirty")
	}
	scr.View(80, 20)
	if scr.dirty {
		t.Fatal("View after a new line must clear dirty")
	}
	// A width change re-wraps and marks dirty.
	scr.View(40, 20)
	// (dirty cleared inside View again)
	if scr.dirty {
		t.Fatal("View after a width change must clear dirty")
	}
	if scr.wrapWidth != 40 {
		t.Fatalf("width change must be recorded, got %d", scr.wrapWidth)
	}
}

// TestFollowAndScrollWithPreWrap proves follow/auto-scroll and manual scroll
// still work on the pre-wrapped (SoftWrap-off) content.
func TestFollowAndScrollWithPreWrap(t *testing.T) {
	scr := newStreamScreen("Running backup", 1, func() {})
	for i := 0; i < 200; i++ {
		scr = feedLines(scr, 1, fmt.Sprintf("line %d %s", i, strings.Repeat("x", 30)))
	}
	scr.View(20, 8) // small panel, wide lines -> many wrapped rows to scroll
	if !scr.follow {
		t.Fatal("should start following")
	}
	if !scr.vp.AtBottom() {
		t.Fatal("following should pin the viewport to the bottom")
	}
	updated, _ := scr.Update(shell.KeyMsg("up"))
	scr = updated.(*StreamTask)
	if scr.follow {
		t.Fatal("scrolling up must stop follow")
	}
	updated, _ = scr.Update(shell.KeyMsg("end"))
	scr = updated.(*StreamTask)
	if !scr.follow {
		t.Fatal("end must resume follow")
	}
	// A new line while following re-pins to the bottom on the next View.
	scr = feedLines(scr, 1, "brand new bottom line")
	scr.View(20, 8)
	if !scr.vp.AtBottom() {
		t.Fatal("a new line while following must keep the view pinned to the bottom")
	}
}

// TestCoalescedBatchAppends proves a StreamLinesMsg batch appends every line in
// order, and a foreign-token batch is ignored.
func TestCoalescedBatchAppends(t *testing.T) {
	scr := newStreamScreen("Running backup", 1, func() {})
	scr.View(80, 20)
	batch := []string{"one", "two", "three"}
	scr = feedLines(scr, 1, batch...)
	if len(scr.lines) != 3 {
		t.Fatalf("batch must append all 3 lines, got %d", len(scr.lines))
	}
	for i, want := range batch {
		if scr.lines[i] != want {
			t.Fatalf("line %d = %q, want %q", i, scr.lines[i], want)
		}
	}
	updated, _ := scr.Update(StreamLinesMsg{Token: 99, Lines: []string{"nope"}})
	scr = updated.(*StreamTask)
	if len(scr.lines) != 3 {
		t.Fatal("foreign-token batch must be ignored")
	}
}

// TestStreamLinesMsgKeepsANSIAndBlanks proves the batched path preserves ANSI
// SGR and blank spacers and strips control chars, like the single-line path.
func TestStreamLinesMsgKeepsANSIAndBlanks(t *testing.T) {
	scr := newStreamScreen("Running backup", 1, func() {})
	scr = feedLines(scr, 1, "\x1b[32mgreen\x1b[0m\x07 tail", "", "plain")
	if len(scr.lines) != 3 {
		t.Fatalf("batch must retain all 3 lines (incl blank), got %d: %q", len(scr.lines), scr.lines)
	}
	if !strings.Contains(scr.lines[0], "\x1b[32m") || strings.Contains(scr.lines[0], "\x07") {
		t.Fatalf("ANSI SGR must survive and control char be stripped, got %q", scr.lines[0])
	}
	if scr.lines[1] != "" {
		t.Fatalf("blank spacer must be kept, got %q", scr.lines[1])
	}
}

// 261-7: lipglossCount must count the PHYSICAL rows a block occupies at the given
// width (soft-wrapped), not just its literal newline count, or the header/outcome
// height is under-reserved and the final status row is clipped on narrow terminals.
func TestLipglossCountCountsWrappedRows(t *testing.T) {
	if got := lipglossCount("", 40); got != 0 {
		t.Fatalf("empty must be 0 rows, got %d", got)
	}
	if got := lipglossCount("short", 40); got != 1 {
		t.Fatalf("short line must be 1 row, got %d", got)
	}
	if got := lipglossCount(strings.Repeat("x", 100), 40); got != 3 { // ceil(100/40)
		t.Fatalf("100 cells at width 40 must be 3 rows, got %d", got)
	}
	if got := lipglossCount("a\n"+strings.Repeat("y", 81), 40); got != 4 { // 1 + ceil(81/40)=3
		t.Fatalf("mixed block must be 4 rows, got %d", got)
	}
}

// --- coalescing buffer (Part B) ----------------------------------------------

// TestEmitBufferNeverBlocks proves the producer's emit never blocks on the UI:
// emitting far more than streamFlushCount lines completes well under the
// deadline (the old s.Send-per-line path could stall on backpressure).
func TestEmitBufferNeverBlocks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buf := &shell.SyncBuffer{}
	s := shell.StartForTestWithOutput(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Backup"}, buf)
	t.Cleanup(func() { _ = s.Close() })

	ready := make(chan struct{})
	close(ready) // no push gate in this direct-flush unit test
	b := newStreamEmitBuffer(s, 1, ready)
	defer b.Close()

	const n = 50000
	emitted := make(chan struct{})
	go func() {
		for i := 0; i < n; i++ {
			b.emit(fmt.Sprintf("l%d", i))
		}
		close(emitted)
	}()
	select {
	case <-emitted:
	case <-time.After(uitest.Deadline(5 * time.Second)):
		t.Fatal("emit blocked the producer under UI backpressure")
	}
}

// TestEmitBufferCloseIdempotentNoLeak proves Close is idempotent and the flusher
// goroutine exits (no leak / no deadlock).
func TestEmitBufferCloseIdempotentNoLeak(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buf := &shell.SyncBuffer{}
	s := shell.StartForTestWithOutput(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Backup"}, buf)
	t.Cleanup(func() { _ = s.Close() })

	ready := make(chan struct{})
	close(ready) // no push gate in this direct-flush unit test
	b := newStreamEmitBuffer(s, 1, ready)
	b.emit("one")
	b.emit("two")

	returned := make(chan struct{})
	go func() {
		b.Close()
		b.Close() // idempotent second call must also return
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(uitest.Deadline(5 * time.Second)):
		t.Fatal("Close did not return (flusher leaked or deadlocked)")
	}
	b.emit("after-close") // no-op, must not panic
}

// TestRunStreamTaskFlushesEveryLine drives the whole batched pipeline: a run
// emits many lines fast plus a distinctive final line; the panel must show the
// final line (Close flushed it) AND the outcome (which arrives only after the
// flush), proving no line is lost and ordering holds.
func TestRunStreamTaskFlushesEveryLine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	buf := &shell.SyncBuffer{}
	s := shell.StartForTestWithOutput(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Backup"}, buf)
	t.Cleanup(func() {
		_ = s.Close()
		cancel()
	})

	done := make(chan error, 1)
	go func() {
		done <- RunStreamTask(ctx, s, "Running backup", func(_ context.Context, emit func(string)) (string, error) {
			for i := 0; i < 1000; i++ {
				emit(fmt.Sprintf("burst line %d", i))
			}
			emit("FINAL-LINE-SENTINEL")
			return "OUTCOME-SENTINEL", nil
		})
	}()

	waitForBuffer(t, buf, "FINAL-LINE-SENTINEL")
	waitForBuffer(t, buf, "OUTCOME-SENTINEL")
	waitForBuffer(t, buf, "enter continue")

	if err := pumpEnterUntilDone(s, done); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
