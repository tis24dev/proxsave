package orchestrator

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SanitizeTelegramSetupStatusMessage strips terminal/control sequences from an
// untrusted upstream status message and truncates it, producing copy that is safe
// to render in BOTH the CLI and the TUI. The classifier is the single source of
// truth for setup copy, and the TUI only tview.Escapes the string (it does NOT
// strip control bytes), so a hostile relay response must be scrubbed HERE so both
// UIs consume the same safe message. Falls back to an ASCII-quoted form when
// stripping empties the string.
func SanitizeTelegramSetupStatusMessage(raw string) string {
	msg := strings.TrimSpace(raw)
	if msg == "" {
		return ""
	}

	sanitized := stripTelegramTerminalSequences(msg)
	sanitized = TruncateTelegramSetupStatusMessage(sanitized)
	if sanitized != "" {
		return sanitized
	}

	quoted := strconv.QuoteToASCII(msg)
	quoted = strings.TrimPrefix(quoted, `"`)
	quoted = strings.TrimSuffix(quoted, `"`)
	return TruncateTelegramSetupStatusMessage(quoted)
}

// TelegramSetupStatusUnknownMessage stands in for the relay's status message in the
// "not verified" log line when the server said nothing usable — either it sent no
// message at all, or everything it sent was stripped as a control sequence. Without
// it the line ends on a bare status code with nothing after it, which reads like a
// log that was cut short rather than a server that stayed silent.
const TelegramSetupStatusUnknownMessage = "Registration not active yet"

// TelegramSetupStatusMessageForLog is the untrusted relay status message as EVERY
// front-end must write it into the persisted install log: scrubbed, truncated, and
// replaced by the stand-in when nothing survives. That log is written to disk and
// read back later, so an escape sequence surviving into it would move the cursor of
// whoever cats the file — the CLI scrubbed here from the start, the TUI did not.
//
// Sanitizing is idempotent (a scrubbed string has no control bytes left to strip and
// is already within the cap), so a caller that already scrubbed at the write site can
// still route through here to pick up the stand-in.
func TelegramSetupStatusMessageForLog(raw string) string {
	if msg := SanitizeTelegramSetupStatusMessage(raw); msg != "" {
		return msg
	}
	return TelegramSetupStatusUnknownMessage
}

func stripTelegramTerminalSequences(msg string) string {
	var b strings.Builder
	b.Grow(len(msg))
	pendingSpace := false

	for i := 0; i < len(msg); {
		switch msg[i] {
		case 0x1b:
			i = skipTelegramEscapeSequence(msg, i)
			pendingSpace = true
			continue
		case 0x9b:
			i = skipTelegramCSI(msg, i+1)
			pendingSpace = true
			continue
		}

		r, size := utf8.DecodeRuneInString(msg[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			pendingSpace = true
			i += size
			continue
		}
		if !unicode.IsPrint(r) {
			i += size
			continue
		}
		if pendingSpace && b.Len() > 0 {
			b.WriteByte(' ')
		}
		pendingSpace = false
		b.WriteRune(r)
		i += size
	}

	return strings.TrimSpace(b.String())
}

func skipTelegramEscapeSequence(msg string, i int) int {
	if i >= len(msg) || msg[i] != 0x1b {
		return i + 1
	}
	i++
	if i >= len(msg) {
		return i
	}
	switch msg[i] {
	case '[':
		return skipTelegramCSI(msg, i+1)
	case ']':
		return skipTelegramOSC(msg, i+1)
	case 'P', 'X', '^', '_':
		return skipTelegramST(msg, i+1)
	default:
		return i + 1
	}
}

func skipTelegramCSI(msg string, i int) int {
	for i < len(msg) {
		b := msg[i]
		i++
		if b >= 0x40 && b <= 0x7e {
			return i
		}
	}
	return i
}

func skipTelegramOSC(msg string, i int) int {
	for i < len(msg) {
		switch msg[i] {
		case 0x07:
			return i + 1
		case 0x1b:
			if i+1 < len(msg) && msg[i+1] == '\\' {
				return i + 2
			}
		}
		i++
	}
	return i
}

func skipTelegramST(msg string, i int) int {
	for i < len(msg) {
		if msg[i] == 0x1b && i+1 < len(msg) && msg[i+1] == '\\' {
			return i + 2
		}
		i++
	}
	return i
}
