package install

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// errAuditSkip is the local sentinel for skipping the audit selection.
var errAuditSkip = errors.New("post-install audit: skip")

// auditCollect is an injection point for tests.
var auditCollect = installer.CollectPostInstallDisableSuggestions

// auditAction is the choice on the post-install check's initial screen: run the
// dry-run or leave (mirrors the Telegram/Healthchecks check screens' Check + leave).
type auditAction int

const (
	auditActionCheck auditAction = iota
	auditActionLeave
)

// buildAuditPrompt renders the styled prompt shown above the Check/leave selector,
// matching the pre-check "Status: NOT CHECKED" look of the Telegram/Healthchecks checks.
func buildAuditPrompt() string {
	var b strings.Builder
	b.WriteString(theme.Text.Render("Detect unused backup components."))
	b.WriteString("\n\n")
	b.WriteString(theme.Text.Render("Status: "))
	b.WriteString(renderHealthcheckLevel(orchestrator.HealthcheckSetupLevelNeutral, "NOT CHECKED"))
	return b.String()
}

// RunPostInstallAudit runs the optional post-install check: dry-run collect,
// multi-select of suggested disables, shared apply. Prompt aborts are
// non-blocking (the install continues), matching both legacy fronts.
func RunPostInstallAudit(ctx context.Context, session *shell.Session, execPath, configPath string, backToMenu bool) (installer.PostInstallAuditResult, error) {
	result := installer.PostInstallAuditResult{}

	// Mirror the Telegram/Healthchecks check screens: a styled "Status: NOT CHECKED"
	// prompt above a "Check" + leave selector, instead of a bespoke Yes/No confirm.
	leaveLabel, leaveDesc := "Skip", "skip the check"
	if backToMenu {
		leaveLabel, leaveDesc = "Back", "return to the dashboard menu"
	}
	initItems := []components.SelectorItem[auditAction]{
		{Label: "Check", Description: "run the dry-run to detect unused components", Value: auditActionCheck},
		{Label: leaveLabel, Description: leaveDesc, Value: auditActionLeave},
	}
	action, err := shell.Ask(ctx, session, components.NewSelector(
		"Post-install check", initItems,
		components.WithSelectorPromptStyled[auditAction](buildAuditPrompt()),
		components.WithSelectorBack[auditAction](errAuditSkip),
	))
	if err != nil {
		if errors.Is(err, errAuditSkip) || shell.IsAbort(err) {
			return result, nil
		}
		return result, err
	}
	if action == auditActionLeave {
		return result, nil
	}
	result.Ran = true

	var suggestions []installer.PostInstallAuditSuggestion
	collectErr := components.RunTask(ctx, session, "Post-install check", "Running dry-run...", func(taskCtx context.Context, report func(string)) error {
		report("Running proxsave --dry-run (this may take a minute)...")
		found, err := auditCollect(taskCtx, execPath, configPath)
		if err != nil {
			return err
		}
		suggestions = found
		return nil
	})
	if collectErr != nil {
		result.CollectErr = collectErr
		showAuditResult(ctx, session, "Post-install check", orchestrator.HealthcheckSetupLevelWarn,
			"CHECK FAILED", fmt.Sprintf("Non-blocking: %v", collectErr), backToMenu)
		return result, nil
	}
	result.Suggestions = suggestions

	if len(suggestions) == 0 {
		showAuditResult(ctx, session, "Post-install check", orchestrator.HealthcheckSetupLevelOk,
			"NO UNUSED COMPONENTS", "", backToMenu)
		return result, nil
	}

	items := make([]components.MultiSelectItem[string], 0, len(suggestions))
	for _, s := range suggestions {
		items = append(items, components.MultiSelectItem[string]{
			Label:  s.Key,
			Value:  s.Key,
			Detail: auditComponentDetail(s),
		})
	}
	keys, err := shell.Ask(ctx, session, components.NewMultiSelect(
		"Unused components", items,
		components.WithMultiSelectPrompt[string](
			fmt.Sprintf("Detected %d unused/optional component(s) that may cause warnings.\nSelected components are written as KEY=false into backup.env.", len(suggestions))),
		components.WithMultiSelectDetailPane[string]("Details"),
		components.WithMultiSelectActions[string]("Select ALL", "Disable Selected"),
		components.WithMultiSelectBack[string](errAuditSkip),
	))
	if err != nil {
		if errors.Is(err, errAuditSkip) || shell.IsAbort(err) {
			return result, nil
		}
		return result, err
	}
	if len(keys) == 0 {
		showAuditResult(ctx, session, "Post-install check", orchestrator.HealthcheckSetupLevelNeutral,
			"NO CHANGES", "No components were selected; nothing was modified.", backToMenu)
		return result, nil
	}

	if err := installer.ApplyAuditDisables(configPath, keys); err != nil {
		result.ApplyErr = err
		showAuditResult(ctx, session, "Post-install check", orchestrator.HealthcheckSetupLevelError,
			"UPDATE FAILED", err.Error(), backToMenu)
		return result, nil
	}
	result.AppliedKeys = normalizeAuditKeys(keys)
	showAuditResult(ctx, session, "Post-install check", orchestrator.HealthcheckSetupLevelOk,
		"UPDATED", disabledComponentsSummary(result.AppliedKeys), backToMenu)
	return result, nil
}

// disabledComponentsSummary renders the "UPDATED" explanation as a header line followed by
// ONE component key per line (a "- " bulleted column), so a long list stays readable instead
// of collapsing into a single truncated line. The result screen (auditResultPrompt ->
// SanitizeText -> theme.Subtle) preserves newlines, so each key gets its own row.
func disabledComponentsSummary(keys []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Disabled %d component(s):", len(keys))
	for _, k := range keys {
		b.WriteString("\n- " + k)
	}
	return b.String()
}

// auditResultAction is the single choice on a post-install audit outcome screen:
// dismiss it and return to the caller (mirrors daemonResultAction on the dashboard).
type auditResultAction int

const auditResultActionBack auditResultAction = iota

// auditResultPrompt builds the styled "Status:" block for an audit outcome, mirroring
// orchestrator.BuildStatusPrompt: a colored keyword line + an optional Subtle explanation. It is a
// pure string builder (extracted for testability). Keyword and explanation are free-form (may
// embed external tool output / error strings), so both are SanitizeText-scrubbed before theme
// rendering to keep raw ANSI/OSC/C0/C1 escapes out of the verbatim WithSelectorPromptStyled path.
func auditResultPrompt(level orchestrator.HealthcheckSetupLevel, keyword, explanation string) string {
	prompt := theme.Text.Render("Status: ") + renderHealthcheckLevel(level, components.SanitizeText(keyword))
	if exp := components.SanitizeText(explanation); exp != "" {
		prompt += "\n\n" + theme.Subtle.Render(exp)
	}
	return prompt
}

// showAuditResult presents a post-install audit outcome with the SAME styled look as the
// healthcheck/daemon result screens: a "Status:" line with a colored keyword (green ✓ Ok,
// red ✗ Error, yellow ⚠ Warn, yellow with no symbol Neutral, via renderHealthcheckLevel) and,
// only when the explanation is non-empty, a blank line then a Subtle explanation, above a
// single Back item. These are non-blocking informational outcomes (exactly like the Notices
// they replaced): the result and any esc/abort are swallowed, never propagated as an error.
func showAuditResult(ctx context.Context, session *shell.Session, title string, level orchestrator.HealthcheckSetupLevel, keyword, explanation string, backToMenu bool) {
	errAuditResultEsc := errors.New("post-install audit result: esc")
	prompt := auditResultPrompt(level, keyword, explanation)
	// Mirror the Telegram/healthcheck check screens' leave item: the install flow
	// continues forward ("Continue"); the dashboard returns to its menu ("Back").
	leaveLabel, leaveDesc := "Continue", "continue the install"
	if backToMenu {
		leaveLabel, leaveDesc = "Back", "return to the dashboard menu"
	}
	items := []components.SelectorItem[auditResultAction]{
		{Label: leaveLabel, Description: leaveDesc, Value: auditResultActionBack},
	}
	_, _ = shell.Ask(ctx, session, components.NewSelector(
		title, items,
		components.WithSelectorPromptStyled[auditResultAction](prompt),
		components.WithSelectorBack[auditResultAction](errAuditResultEsc),
	))
}

// auditComponentDetail is the side-pane text for a suggestion: the curated
// component description, or the raw dry-run warnings when no description is
// catalogued for that key (so the pane is never empty).
func auditComponentDetail(s installer.PostInstallAuditSuggestion) string {
	if desc := installer.PostInstallComponentDescription(s.Key); desc != "" {
		return desc
	}
	if len(s.Messages) > 0 {
		return "Detected during the dry-run:\n" + strings.Join(s.Messages, "\n")
	}
	return ""
}

func normalizeAuditKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.ToUpper(strings.TrimSpace(key))
		if key != "" {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}
