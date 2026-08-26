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
//
// runHostname is the name this run resolves as its own; it is what the access
// control host check compares a restored bundle's hostname against, next to
// os.Hostname(). Pass "" if it is not known: the check is then strict, which warns
// more, never less.
func RunRestoreWorkflowTUI(ctx context.Context, cfg *config.Config, logger *logging.Logger, version, configPath, buildSig, runHostname string) (err error) {
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
	err = runRestoreWorkflowWithUI(ctx, cfg, logger, version, ui, runHostname)
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
