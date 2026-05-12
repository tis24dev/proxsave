// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

func maybeAddRecommendedCategoriesForTFA(ctx context.Context, ui RestoreWorkflowUI, logger *logging.Logger, selected []Category, available []Category) ([]Category, error) {
	if !shouldPromptForTFARecommendations(ui, logger, selected) {
		return selected, nil
	}
	addCategories, addNames := tfaRecommendedCategories(selected, available)
	if len(addCategories) == 0 {
		return selected, nil
	}

	addNow, err := confirmTFARecommendedCategories(ctx, ui, addNames)
	if err != nil {
		return nil, err
	}
	if !addNow {
		logger.Warning("Access control selected without %s; WebAuthn users may require re-enrollment if the UI origin changes", strings.Join(addNames, ", "))
		return selected, nil
	}
	return dedupeCategoriesByID(append(selected, addCategories...)), nil
}

func shouldPromptForTFARecommendations(ui RestoreWorkflowUI, logger *logging.Logger, selected []Category) bool {
	return ui != nil &&
		logger != nil &&
		(hasCategoryID(selected, "pve_access_control") || hasCategoryID(selected, "pbs_access_control"))
}

func tfaRecommendedCategories(selected, available []Category) ([]Category, []string) {
	var categories []Category
	var names []string
	for _, id := range missingTFARecommendedCategoryIDs(selected) {
		cat := GetCategoryByID(id, available)
		if cat == nil || !cat.IsAvailable || cat.ExportOnly {
			continue
		}
		categories = append(categories, *cat)
		names = append(names, cat.Name)
	}
	return categories, names
}

func missingTFARecommendedCategoryIDs(selected []Category) []string {
	var missing []string
	if !hasCategoryID(selected, "network") {
		missing = append(missing, "network")
	}
	if !hasCategoryID(selected, "ssl") {
		missing = append(missing, "ssl")
	}
	return missing
}

func confirmTFARecommendedCategories(ctx context.Context, ui RestoreWorkflowUI, addNames []string) (bool, error) {
	message := fmt.Sprintf(
		"You selected Access Control without restoring: %s\n\n"+
			"If TFA includes WebAuthn/FIDO2, changing the UI origin (FQDN/hostname or port) may require re-enrollment.\n\n"+
			"For maximum 1:1 compatibility, ProxSave recommends restoring these categories too.\n\n"+
			"Add recommended categories now?",
		strings.Join(addNames, ", "),
	)
	return ui.ConfirmAction(ctx, "TFA/WebAuthn compatibility", message, "Add recommended", "Keep current", 0, true)
}

func dedupeCategoriesByID(categories []Category) []Category {
	if len(categories) == 0 {
		return categories
	}
	seen := make(map[string]struct{}, len(categories))
	out := make([]Category, 0, len(categories))
	for _, cat := range categories {
		if dedupeCategorySeen(seen, cat.ID) {
			continue
		}
		out = append(out, cat)
	}
	return out
}

func dedupeCategorySeen(seen map[string]struct{}, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if _, ok := seen[id]; ok {
		return true
	}
	seen[id] = struct{}{}
	return false
}
