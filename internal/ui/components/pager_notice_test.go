package components

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/ui/shell"
)

func TestPagerEnterContinues(t *testing.T) {
	p := NewPager("Restore plan", "line1\nline2")
	resolved := false
	var gotErr error
	p.Bind(func(_ struct{}, err error) {
		resolved = true
		gotErr = err
	})
	press(t, p, "enter")
	if !resolved || gotErr != nil {
		t.Fatalf("expected clean continue, got resolved=%v err=%v", resolved, gotErr)
	}
}

func TestPagerEscWithAbort(t *testing.T) {
	abort := errors.New("declined")
	p := NewPager("Restore plan", "content", WithPagerAbort(abort))
	var gotErr error
	p.Bind(func(_ struct{}, err error) { gotErr = err })
	press(t, p, "esc")
	if !errors.Is(gotErr, abort) {
		t.Fatalf("expected abort error, got %v", gotErr)
	}
}

// TestPagerEscDefaultAborts: a reflex Esc on a plan must never count as
// acceptance; without an explicit sentinel it resolves shell.ErrAborted.
func TestPagerEscDefaultAborts(t *testing.T) {
	for _, key := range []string{"esc", "q"} {
		p := NewPager("Restore plan", "content")
		resolved := false
		var gotErr error
		p.Bind(func(_ struct{}, err error) {
			resolved = true
			gotErr = err
		})
		press(t, p, key)
		if !resolved || !errors.Is(gotErr, shell.ErrAborted) {
			t.Fatalf("%s must abort by default, got resolved=%v err=%v", key, resolved, gotErr)
		}
	}
}

func TestPagerViewRendersContent(t *testing.T) {
	long := strings.Repeat("row\n", 50)
	p := NewPager("Restore plan", long)
	view := p.View(60, 10)
	if !strings.Contains(view, "Restore plan") || !strings.Contains(view, "row") {
		t.Errorf("pager view missing content: %q", view)
	}
	if !strings.Contains(view, "%") {
		t.Error("long content must show a scroll percentage")
	}
}

func planWrapSample() string {
	return "Restore mode: FULL restore (all categories)\n" +
		"System type:  Proxmox VE + PBS (both)\n\n" +
		"Categories to restore:\n" +
		"  1. PBS Host\n" +
		"     Node settings, ACME configuration, proxy, external metric servers and traffic control rules\n" +
		"\nFiles/directories that will be restored:\n" +
		"  • /var/lib/proxmox-backup/very/deeply/nested/datastore/path/that/is/quite/long/replication.cfg.backup\n"
}

// TestWrapPlanNoRowExceedsWidth: the whole point of #3 - no wrapped row may spill
// past the width (which the SoftWrap-off viewport would silently clip). Includes
// absurdly narrow widths that force the indent-drop guard.
func TestWrapPlanNoRowExceedsWidth(t *testing.T) {
	deep := "                 deeply indented and very long line that must never overflow\n" + planWrapSample()
	for _, w := range []int{2, 4, 6, 10, 20, 40, 60, 76, 80, 120} {
		for _, row := range wrapPlan(deep, w) {
			if got := ansi.StringWidth(row); got > w {
				t.Fatalf("width %d: row exceeds width (%d): %q", w, got, row)
			}
		}
	}
}

// TestWrapPlanPreservesIndent: a wrapped indented line hangs under its parent
// (continuation rows keep the leading indent), not reflowed to column 0.
func TestWrapPlanPreservesIndent(t *testing.T) {
	line := "     Node settings, ACME configuration, proxy, external metric servers and traffic control rules"
	rows := wrapPlan(line, 40)
	if len(rows) < 2 {
		t.Fatalf("expected the long indented line to wrap, got %d rows", len(rows))
	}
	for i, row := range rows {
		if !strings.HasPrefix(row, "     ") {
			t.Fatalf("row %d lost the hanging indent: %q", i, row)
		}
	}
}

// TestWrapPlanLongTokenPreservesPath: a path longer than the row is hard-split
// grapheme-safe, and reassembling the chunks round-trips the path (no glyph lost).
func TestWrapPlanLongTokenPreservesPath(t *testing.T) {
	path := "/var/lib/proxmox-backup/very/deeply/nested/datastore/path/that/is/quite/long/replication.cfg.backup"
	rows := wrapPlan("  • "+path, 40)
	var got strings.Builder
	for _, row := range rows {
		trimmed := strings.TrimLeft(row, " ")
		if trimmed == "•" {
			continue
		}
		got.WriteString(trimmed)
		if got := ansi.StringWidth(row); got > 40 {
			t.Fatalf("row over width (%d): %q", got, row)
		}
	}
	if got.String() != path {
		t.Fatalf("path corrupted by wrap:\n want %q\n got  %q", path, got.String())
	}
}

// TestPagerWrapsLongLineInView: a long line's tail is WRAPPED into view, not
// clipped off-screen, and no rendered row exceeds the width.
func TestPagerWrapsLongLineInView(t *testing.T) {
	tail := "END_OF_LONG_LINE"
	long := "     " + strings.Repeat("word ", 30) + tail
	p := NewPager("Restore plan", long)
	view := p.View(50, 40)
	if !strings.Contains(view, tail) {
		t.Fatalf("long line tail was clipped, not wrapped:\n%s", view)
	}
	for _, row := range strings.Split(view, "\n") {
		if got := ansi.StringWidth(row); got > 50 {
			t.Fatalf("rendered row exceeds width (%d): %q", got, row)
		}
	}
}

func TestNoticeResolvesOnAck(t *testing.T) {
	for _, key := range []string{"enter", "esc", "space"} {
		n := NewNotice(NoticeError, "Failure", "something broke")
		resolved := false
		n.Bind(func(_ struct{}, err error) { resolved = true })
		press(t, n, key)
		if !resolved {
			t.Fatalf("notice did not resolve on %q", key)
		}
	}
}

func TestNoticeViewShowsSeverity(t *testing.T) {
	n := NewNotice(NoticeWarning, "Careful", "check this")
	view := n.View(80, 20)
	if !strings.Contains(view, "Careful") || !strings.Contains(view, "check this") {
		t.Errorf("notice view incomplete: %q", view)
	}
	if !strings.Contains(view, "⚠") {
		t.Error("warning notice must show the warning symbol")
	}
}
