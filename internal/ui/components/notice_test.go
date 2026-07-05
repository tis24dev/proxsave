package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// A multi-sentence notice renders one sentence per line (never wrapping a word
// from the next sentence onto the previous line, which reads as a broken phrase).
func TestNoticeWrapsAtSentenceBoundaries(t *testing.T) {
	n := NewNotice(NoticeSuccess, "Daemon disabled",
		"Reverted to the cron scheduler and removed the daemon service. Future upgrades will not reinstall it.")
	lines := strings.Split(ansi.Strip(n.View(80, 20)), "\n")

	firstLineWith := func(sub string) int {
		for i, ln := range lines {
			if strings.Contains(ln, sub) {
				return i
			}
		}
		return -1
	}
	// The second sentence starts on its own line: "Future" and "upgrades" are
	// together, and NOT on the same line as "daemon service.".
	fut := firstLineWith("Future upgrades will not reinstall it.")
	svc := firstLineWith("removed the daemon service.")
	if fut < 0 || svc < 0 {
		t.Fatalf("both sentences must render intact:\n%s", ansi.Strip(n.View(80, 20)))
	}
	if fut == svc {
		t.Fatalf("the second sentence must not share a line with the first:\n%s", ansi.Strip(n.View(80, 20)))
	}
}

// A single-sentence notice (no ". Uppercase" boundary) renders as one block.
func TestNoticeSingleSentence(t *testing.T) {
	n := NewNotice(NoticeInfo, "T", "The cron entry was removed.")
	if !strings.Contains(ansi.Strip(n.View(80, 20)), "The cron entry was removed.") {
		t.Fatal("single-sentence message must render intact")
	}
}
