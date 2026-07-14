package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

const decryptWizardSubtitle = "Decrypt Backup Workflow"

// RunDecryptWorkflowTUI runs the decrypt workflow using the Charm UI: one
// long-lived Session whose screens are driven by the same
// runDecryptWorkflowWithUI engine path the CLI uses.
func RunDecryptWorkflowTUI(ctx context.Context, cfg *config.Config, logger *logging.Logger, version, configPath, buildSig string) (err error) {
	if cfg == nil {
		return fmt.Errorf("configuration not available")
	}
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	if strings.TrimSpace(buildSig) == "" {
		buildSig = "n/a"
	}
	done := logging.DebugStart(logger, "decrypt workflow (tui)", "version=%s", version)
	defer func() { done(err) }()

	session := newUISession(ctx, shell.Config{
		AppName:    "ProxSave",
		Subtitle:   decryptWizardSubtitle,
		Version:    version,
		ConfigPath: configPath,
		BuildSig:   buildSig,
		UseColor:   cfg.UseColor,
	})
	// Silence the logger console while the session owns the terminal (raw
	// writes corrupt the alternate screen); log files are unaffected.
	// Defers run LIFO: the session closes before the writer comes back.
	prevOut := logger.SwapOutput(io.Discard)
	defer logger.SetOutput(prevOut)
	// Deferred so a panicking engine cannot leave the terminal in
	// altscreen/raw mode; Close is idempotent for the normal path below.
	defer func() { _ = session.Close() }()

	ui := newCharmWorkflowUI(session, logger, ErrDecryptAborted)
	var bundlePath string
	bundlePath, err = runDecryptWorkflowWithUI(ctx, cfg, logger, version, ui)
	if err == nil && strings.TrimSpace(bundlePath) != "" {
		// The logger lines land in the altscreen and vanish on Close:
		// show the result where the user can actually read it.
		_, _ = shell.Ask(ctx, session, components.NewNotice(components.NoticeSuccess,
			"Decrypt complete", fmt.Sprintf("Decrypted bundle created:\n%s", bundlePath)))
	}
	closeErr := session.Close()
	switch {
	case err != nil:
		if errors.Is(err, ErrDecryptAborted) {
			return ErrDecryptAborted
		}
		if errors.Is(err, shell.ErrClosed) && closeErr == nil {
			// The program terminated out from under the workflow
			// (interrupt): treat it as a user abort.
			return ErrDecryptAborted
		}
		return err
	case closeErr != nil:
		return closeErr
	}
	return nil
}

func buildTargetInfo(manifest *backup.Manifest) string {
	return fmt.Sprintf("Targets: %s", formatBackupCandidateTarget(manifest))
}

func normalizeProxmoxVersion(value string) string {
	version := strings.TrimSpace(value)
	if version == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(version), "v") {
		version = "v" + version
	}
	return version
}

func filterEncryptedCandidates(candidates []*backupCandidate) []*backupCandidate {
	if len(candidates) == 0 {
		return candidates
	}
	filtered := make([]*backupCandidate, 0, len(candidates))
	for _, c := range candidates {
		if c == nil || c.Manifest == nil {
			continue
		}
		if statusFromManifest(c.Manifest) == "encrypted" {
			filtered = append(filtered, c)
		}
	}
	return filtered
}
