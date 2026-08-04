package orchestrator

import (
	"strings"
	"testing"
)

// TestTelegramSetupStatusMessageForLog pins the three things the persisted install log
// needs from an untrusted relay message, and which every front-end must apply the same
// way: control sequences stripped, length capped, and a stand-in when nothing is left.
// Only the first is about safety; the other two are why a shared helper exists at all,
// since a front-end that remembered to scrub but not to substitute would write a line
// ending on a bare status code.
func TestTelegramSetupStatusMessageForLog(t *testing.T) {
	t.Run("strips terminal escapes", func(t *testing.T) {
		got := TelegramSetupStatusMessageForLog(" \x1b[31mnot\tlinked\r\nyet\x1b[0m\x07 ")
		if got != "not linked yet" {
			t.Fatalf("got %q, want %q", got, "not linked yet")
		}
	})

	// The stand-in is for a relay that said NOTHING. A relay that sent only control
	// bytes said something, and the sanitizer already answers that with an
	// ASCII-quoted rendering — which is strictly more useful in a log than the
	// stand-in, because it preserves what arrived while making it safe to print.
	// Substituting there would destroy the only record of a malformed response.
	t.Run("stands in only for a silent relay", func(t *testing.T) {
		for _, raw := range []string{"", "   ", "\t\n"} {
			if got := TelegramSetupStatusMessageForLog(raw); got != TelegramSetupStatusUnknownMessage {
				t.Fatalf("TelegramSetupStatusMessageForLog(%q) = %q, want the stand-in %q",
					raw, got, TelegramSetupStatusUnknownMessage)
			}
		}
		for _, raw := range []string{"\x1b[2J\x1b[H", "\x00\x01\x02"} {
			got := TelegramSetupStatusMessageForLog(raw)
			if got == TelegramSetupStatusUnknownMessage {
				t.Fatalf("a control-only response must be quoted, not replaced by the stand-in: %q", raw)
			}
			if strings.ContainsAny(got, "\x1b\x00\x01\x02") {
				t.Fatalf("the quoted rendering must still be printable: %q", got)
			}
		}
	})

	t.Run("caps the length", func(t *testing.T) {
		got := TelegramSetupStatusMessageForLog(strings.Repeat("é", TelegramSetupStatusMessageMaxRunes*3))
		if n := len([]rune(got)); n > TelegramSetupStatusMessageMaxRunes {
			t.Fatalf("got %d runes, want at most %d", n, TelegramSetupStatusMessageMaxRunes)
		}
	})

	// Idempotence is load-bearing, not incidental: the TUI scrubs at the write site and
	// then routes the field through here for the stand-in, so a second pass must not
	// truncate an already-truncated message again or re-escape it.
	t.Run("is idempotent", func(t *testing.T) {
		for _, raw := range []string{
			"not linked yet",
			" \x1b[31mnot linked\x1b[0m ",
			strings.Repeat("x", TelegramSetupStatusMessageMaxRunes*2),
			"",
		} {
			once := TelegramSetupStatusMessageForLog(raw)
			if twice := TelegramSetupStatusMessageForLog(once); twice != once {
				t.Fatalf("second pass over %q changed %q into %q", raw, once, twice)
			}
		}
	})
}
