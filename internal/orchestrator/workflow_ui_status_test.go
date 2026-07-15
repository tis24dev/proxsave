package orchestrator

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// TestRenderStatusLevel locks the single shared Status: renderer (the daemon, healthcheck,
// audit, and workflow screens all delegate here): green ✓ Ok, red ✗ Error, yellow ⚠ Warn,
// and yellow with NO symbol for Neutral.
func TestRenderStatusLevel(t *testing.T) {
	cases := []struct {
		level  HealthcheckSetupLevel
		symbol string
	}{
		{HealthcheckSetupLevelOk, theme.SymbolSuccess},
		{HealthcheckSetupLevelError, theme.SymbolError},
		{HealthcheckSetupLevelWarn, theme.SymbolWarning},
		{HealthcheckSetupLevelNeutral, ""},
	}
	for _, tc := range cases {
		plain := ansi.Strip(RenderStatusLevel(tc.level, "msg"))
		if !strings.Contains(plain, "msg") {
			t.Fatalf("level %v: missing text: %q", tc.level, plain)
		}
		if tc.symbol == "" {
			for _, sym := range []string{theme.SymbolSuccess, theme.SymbolError, theme.SymbolWarning} {
				if strings.Contains(plain, sym) {
					t.Fatalf("neutral must carry no symbol, got %q", plain)
				}
			}
			continue
		}
		if !strings.HasPrefix(plain, tc.symbol+" ") {
			t.Fatalf("level %v: want prefix %q, got %q", tc.level, tc.symbol, plain)
		}
	}
}

// TestBuildWorkflowStatusPromptStripsInjectedEscapes proves buildWorkflowStatusPrompt scrubs
// free-form keyword/explanation before theme rendering, so raw ANSI/OSC/C0/C1 escapes from
// external tool output (e.g. rclone lsf embedded in an error) can never reach the terminal via
// the verbatim WithSelectorPromptStyled path. The theme's own SGR (ESC[..m) still wraps the
// text, so we assert the INJECTED marker sequences are gone rather than "no 0x1b anywhere".
func TestBuildWorkflowStatusPromptStripsInjectedEscapes(t *testing.T) {
	keyword := "\x1b[2JBOOM"
	explanation := "failed: \x1b[31m\x1b]0;pwned\x07evil\x07"
	got := buildWorkflowStatusPrompt(HealthcheckSetupLevelError, keyword, explanation)

	// Injected escapes must be absent: the raw OSC/BEL/CSI-clear sequences and the C1 CSI byte.
	for _, bad := range []string{"\x1b]0;pwned", "\x1b[2J", "\x07"} {
		if strings.Contains(got, bad) {
			t.Errorf("injected escape %q survived in output %q", bad, got)
		}
	}
	if strings.ContainsRune(got, 0x9b) {
		t.Errorf("injected C1 CSI byte 0x9b survived in output %q", got)
	}

	// The legitimate text must survive the scrub.
	for _, want := range []string{"failed:", "evil", "BOOM"} {
		if !strings.Contains(got, want) {
			t.Errorf("legitimate text %q missing from output %q", want, got)
		}
	}
}

// TestBuildWorkflowStatusPromptPreservesNewline confirms a clean multi-line explanation keeps
// its newline (SanitizeText keeps \n/\t), so multi-line outcomes still render across lines.
func TestBuildWorkflowStatusPromptPreservesNewline(t *testing.T) {
	got := buildWorkflowStatusPrompt(HealthcheckSetupLevelOk, "ok", "line one\nline two")
	for _, want := range []string{"line one", "line two"} {
		if !strings.Contains(got, want) {
			t.Errorf("clean line %q missing from output %q", want, got)
		}
	}
	if !strings.Contains(got, "\n") {
		t.Errorf("newline not preserved in multi-line explanation %q", got)
	}
}
