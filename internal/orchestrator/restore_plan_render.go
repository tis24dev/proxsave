package orchestrator

import (
	"fmt"
	"sort"
	"strings"
)

// buildRestorePlanText renders the restore plan *body* shared by both
// front-ends: the Charm UI feeds it to a Pager, and the CLI prints it under its
// own ASCII banner. Chrome (banners, titles, framing) belongs to the UIs, not
// here, so the two never drift on the content itself.
//
// Every line must fit 80 columns: the Pager does not wrap, so an over-width line
// is truncated and only reachable via horizontal scroll.
// TestBuildRestorePlanTextLinesFit80Columns pins this.
func buildRestorePlanText(config *SelectiveRestoreConfig) string {
	if config == nil {
		return ""
	}

	var b strings.Builder

	// No ASCII banner: the Pager renders the styled "Restore plan" title, and the
	// legacy box rule was cosmetically inconsistent with the Charm screens.
	modeName := ""
	switch config.Mode {
	case RestoreModeFull:
		modeName = "FULL restore (all categories)"
	case RestoreModeStorage:
		if config.SystemType.SupportsPVE() && !config.SystemType.SupportsPBS() {
			modeName = "STORAGE only (cluster + storage + jobs + mounts)"
		} else if config.SystemType.SupportsPBS() && !config.SystemType.SupportsPVE() {
			modeName = "DATASTORE only (datastores + jobs + mounts)"
		} else {
			modeName = "STORAGE/DATASTORE only (PVE + PBS storage/jobs + mounts)"
		}
	case RestoreModeBase:
		modeName = "SYSTEM BASE only (network + SSL + SSH + services + filesystem)"
	case RestoreModeCustom:
		modeName = fmt.Sprintf("CUSTOM selection (%d categories)", len(config.SelectedCategories))
	default:
		modeName = "Unknown mode"
	}

	fmt.Fprintf(&b, "Restore mode: %s\n", modeName)
	fmt.Fprintf(&b, "System type:  %s\n\n", GetSystemTypeString(config.SystemType))

	b.WriteString("Categories to restore:\n")
	for i, cat := range config.SelectedCategories {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, cat.Name)
		fmt.Fprintf(&b, "     %s\n", cat.Description)
	}

	b.WriteString("\nFiles/directories that will be restored:\n")
	allPaths := GetSelectedPaths(config.SelectedCategories)
	sort.Strings(allPaths)
	for _, path := range allPaths {
		fsPath := strings.TrimPrefix(path, "./")
		fmt.Fprintf(&b, "  • /%s\n", fsPath)
	}

	b.WriteString("\n⚠ WARNING:\n")
	b.WriteString("  • Existing files at these locations will be OVERWRITTEN\n")
	b.WriteString("  • A safety backup will be created before restoration\n")
	b.WriteString("  • Services may need to be restarted after restoration\n\n")
	if (hasCategoryID(config.SelectedCategories, "pve_access_control") || hasCategoryID(config.SelectedCategories, "pbs_access_control")) &&
		(!hasCategoryID(config.SelectedCategories, "network") || !hasCategoryID(config.SelectedCategories, "ssl")) {
		b.WriteString("  • TFA/WebAuthn: keep the same UI origin (FQDN/hostname and port) for 1:1\n")
		b.WriteString("    compatibility, and restore 'network' + 'ssl'\n\n")
	}

	return b.String()
}
