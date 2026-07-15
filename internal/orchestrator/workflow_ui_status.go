package orchestrator

import (
	"strings"

	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// RenderStatusLevel is the SINGLE colored-keyword renderer for every "Status:" line:
// green ✓ (Ok), red ✗ (Error), yellow ⚠ (Warn), yellow with NO symbol (Neutral). The
// dashboard daemon screens (renderDaemonStatusLevel) and the install healthcheck/audit
// screens (renderHealthcheckLevel) delegate here, so the three can never drift (previously
// they were hand-copied byte-for-byte, guarded only by comments).
func RenderStatusLevel(level HealthcheckSetupLevel, text string) string {
	switch level {
	case HealthcheckSetupLevelOk:
		return theme.SuccessText.Render(theme.SymbolSuccess + " " + text)
	case HealthcheckSetupLevelError:
		return theme.ErrorText.Render(theme.SymbolError + " " + text)
	case HealthcheckSetupLevelNeutral:
		return theme.WarningText.Render(text)
	default: // HealthcheckSetupLevelWarn
		return theme.WarningText.Render(theme.SymbolWarning + " " + text)
	}
}

// buildWorkflowStatusPrompt renders the styled "Status:" block for a workflow outcome screen,
// identical to dashboard.go buildDaemonResultPrompt: a colored keyword line + a Subtle
// explanation on the next line (separated by a blank line). This is the single styled
// renderer for the decrypt-workflow outcomes, so they can never disagree visually with the
// daemon / check result screens. Keyword and explanation are free-form (may embed external
// tool output / error strings), so both are SanitizeText-scrubbed before theme rendering to
// keep raw ANSI/OSC/C0/C1 escapes out of the verbatim WithSelectorPromptStyled path. The
// scrub-then-render shape is BYTE-IDENTICAL to buildDaemonResultPrompt.
func buildWorkflowStatusPrompt(level HealthcheckSetupLevel, keyword, explanation string) string {
	var b strings.Builder
	b.WriteString(theme.Text.Render("Status: "))
	b.WriteString(RenderStatusLevel(level, components.SanitizeText(keyword)))
	if exp := components.SanitizeText(explanation); exp != "" {
		b.WriteString("\n\n")
		b.WriteString(theme.Subtle.Render(exp))
	}
	return b.String()
}
