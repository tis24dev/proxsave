package install

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// errAuditSkip is the local sentinel for skipping the audit selection.
var errAuditSkip = errors.New("post-install audit: skip")

// auditCollect is an injection point for tests.
var auditCollect = installer.CollectPostInstallDisableSuggestions

// RunPostInstallAudit runs the optional post-install check: dry-run collect,
// multi-select of suggested disables, shared apply. Prompt aborts are
// non-blocking (the install continues), matching both legacy fronts.
func RunPostInstallAudit(ctx context.Context, session *shell.Session, execPath, configPath string) (installer.PostInstallAuditResult, error) {
	result := installer.PostInstallAuditResult{}

	run, err := shell.Ask(ctx, session, components.NewConfirm(
		"Post-install check",
		"Run a dry-run to detect unused components and reduce warnings?\nThis may take a minute.",
		components.WithLabels("Run check", "Skip"),
		components.WithDefaultYes(true),
	))
	if err != nil {
		if errors.Is(err, shell.ErrAborted) {
			return result, nil
		}
		return result, err
	}
	if !run.Answer {
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
		_, _ = shell.Ask(ctx, session, components.NewNotice(components.NoticeWarning,
			"Post-install check failed", fmt.Sprintf("Non-blocking: %v", collectErr)))
		return result, nil
	}
	result.Suggestions = suggestions

	if len(suggestions) == 0 {
		_, _ = shell.Ask(ctx, session, components.NewNotice(components.NoticeSuccess,
			"Post-install check", "No unused components detected. No changes required."))
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
		if errors.Is(err, errAuditSkip) || errors.Is(err, shell.ErrAborted) {
			return result, nil
		}
		return result, err
	}
	if len(keys) == 0 {
		_, _ = shell.Ask(ctx, session, components.NewNotice(components.NoticeInfo,
			"Post-install check", "No changes selected. Nothing was modified."))
		return result, nil
	}

	if err := installer.ApplyAuditDisables(configPath, keys); err != nil {
		_, _ = shell.Ask(ctx, session, components.NewNotice(components.NoticeError,
			"Configuration update failed", err.Error()))
		return result, nil
	}
	result.AppliedKeys = normalizeAuditKeys(keys)
	_, _ = shell.Ask(ctx, session, components.NewNotice(components.NoticeSuccess,
		"Configuration updated", fmt.Sprintf("Disabled %d component(s): %s",
			len(result.AppliedKeys), strings.Join(result.AppliedKeys, ", "))))
	return result, nil
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
