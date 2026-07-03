package components

import (
	"errors"
	"strings"
	"testing"

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
