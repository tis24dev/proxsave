package orchestrator

import (
	"strings"

	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// renderWorkflowStatusLevel is the colored-keyword renderer for a workflow "Status:" line.
// It is BYTE-IDENTICAL to dashboard.go renderDaemonStatusLevel (same switch, same theme
// constants), so a decrypt-workflow outcome screen is visually identical to the daemon /
// check result screens: green ✓ (Ok), red ✗ (Error), yellow ⚠ (Warn), and yellow with NO
// symbol (Neutral). HealthcheckSetupLevel lives in this package, so no import is needed.
func renderWorkflowStatusLevel(level HealthcheckSetupLevel, text string) string {
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
// daemon / check result screens.
func buildWorkflowStatusPrompt(level HealthcheckSetupLevel, keyword, explanation string) string {
	var b strings.Builder
	b.WriteString(theme.Text.Render("Status: "))
	b.WriteString(renderWorkflowStatusLevel(level, keyword))
	if explanation != "" {
		b.WriteString("\n\n")
		b.WriteString(theme.Subtle.Render(explanation))
	}
	return b.String()
}
