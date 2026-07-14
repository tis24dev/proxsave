package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

const restoreWizardSubtitle = "Restore Backup Workflow"

// errRestoreBackToMode is the back-navigation sentinel shared by the CLI
// menu (selective.go) and the UI category screen: it returns the flow to the
// restore mode selection.
var errRestoreBackToMode = errors.New("restore mode back")

// RunRestoreWorkflowTUI runs the restore workflow using the Charm UI: one
// long-lived shell.Session whose screens are driven by the same
// runRestoreWorkflowWithUI engine path the CLI uses.
func RunRestoreWorkflowTUI(ctx context.Context, cfg *config.Config, logger *logging.Logger, version, configPath, buildSig string) (err error) {
	if cfg == nil {
		return fmt.Errorf("configuration not available")
	}
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	if strings.TrimSpace(buildSig) == "" {
		buildSig = "n/a"
	}

	done := logging.DebugStart(logger, "restore workflow (tui)", "version=%s", version)
	defer func() { done(err) }()

	session := newUISession(ctx, shell.Config{
		AppName:    "ProxSave",
		Subtitle:   restoreWizardSubtitle,
		Version:    version,
		ConfigPath: configPath,
		BuildSig:   buildSig,
		UseColor:   cfg.UseColor,
	})
	// The engine keeps logging while the session owns the terminal: raw
	// stdout writes would corrupt the alternate screen (the diff renderer
	// never repaints cells it did not touch), so the console output is
	// silenced for the session lifetime. Log files are unaffected. Defers
	// run LIFO: the session closes (terminal restored) BEFORE the console
	// writer comes back.
	prevOut := logger.SwapOutput(io.Discard)
	defer logger.SetOutput(prevOut)
	// Deferred so a panicking engine cannot leave the terminal in
	// altscreen/raw mode; Close is idempotent for the normal path below.
	defer func() { _ = session.Close() }()

	ui := newCharmWorkflowUI(session, logger, ErrRestoreAborted)
	err = runRestoreWorkflowWithUI(ctx, cfg, logger, version, ui)
	closeErr := session.Close()
	switch {
	case err != nil:
		if errors.Is(err, ErrRestoreAborted) {
			return ErrRestoreAborted
		}
		if errors.Is(err, shell.ErrClosed) && closeErr == nil {
			// The program terminated out from under the workflow
			// (interrupt): treat it as a user abort.
			return ErrRestoreAborted
		}
		return err
	case closeErr != nil:
		return closeErr
	}
	return nil
}

func filterAndSortCategoriesForSystem(available []Category, systemType SystemType) []Category {
	relevant := make([]Category, 0, len(available))
	for _, cat := range available {
		if cat.Type == CategoryTypeCommon ||
			(systemType.SupportsPVE() && cat.Type == CategoryTypePVE) ||
			(systemType.SupportsPBS() && cat.Type == CategoryTypePBS) {
			relevant = append(relevant, cat)
		}
	}

	// Sort categories: PVE/PBS first, then common
	sort.Slice(relevant, func(i, j int) bool {
		if relevant[i].Type != relevant[j].Type {
			if relevant[i].Type == CategoryTypeCommon {
				return false
			}
			if relevant[j].Type == CategoryTypeCommon {
				return true
			}
		}
		return relevant[i].Name < relevant[j].Name
	})

	return relevant
}

func buildRestorePlanText(config *SelectiveRestoreConfig) string {
	if config == nil {
		return ""
	}

	var b strings.Builder

	b.WriteString("═══════════════════════════════════════════════════════════════\n")
	b.WriteString("RESTORE PLAN\n")
	b.WriteString("═══════════════════════════════════════════════════════════════\n\n")

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
		b.WriteString("  • TFA/WebAuthn: for best 1:1 compatibility keep the same UI origin (FQDN/hostname and port) and restore 'network' + 'ssl'\n\n")
	}

	return b.String()
}
