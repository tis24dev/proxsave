package orchestrator

import "strings"

const (
	etcPVEPrefix    = "./etc/pve"
	etcPVEDirPrefix = "./etc/pve/"
)

func sanitizeCategoriesForClusterRecovery(categories []Category) (sanitized []Category, removed map[string][]string) {
	removed = make(map[string][]string)
	sanitized = make([]Category, 0, len(categories))

	for _, category := range categories {
		if len(category.Paths) == 0 {
			sanitized = append(sanitized, category)
			continue
		}

		kept := make([]string, 0, len(category.Paths))
		for _, path := range category.Paths {
			if isEtcPVECategoryPath(path) {
				removed[category.ID] = append(removed[category.ID], path)
				continue
			}
			kept = append(kept, path)
		}

		if len(kept) == 0 && len(removed[category.ID]) > 0 {
			continue
		}

		category.Paths = kept
		sanitized = append(sanitized, category)
	}

	return sanitized, removed
}

func isEtcPVECategoryPath(path string) bool {
	normalized := strings.TrimSpace(path)
	if normalized == "" {
		return false
	}
	if !strings.HasPrefix(normalized, "./") && !strings.HasPrefix(normalized, "../") {
		normalized = "./" + strings.TrimPrefix(normalized, "/")
	}
	if normalized == etcPVEPrefix || normalized == etcPVEDirPrefix {
		return true
	}
	return strings.HasPrefix(normalized, etcPVEDirPrefix)
}
