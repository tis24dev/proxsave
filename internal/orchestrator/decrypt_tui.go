package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/tui"
)

const (
	decryptWizardSubtitle = "Decrypt Backup Workflow"
	decryptNavText        = "[yellow]Navigation:[white] TAB/↑↓ to move | ENTER to select | ESC to exit screens | Mouse clicks enabled"
)

// RunDecryptWorkflowTUI runs the decrypt workflow using a TUI flow.
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

	ui := newTUIWorkflowUI(configPath, buildSig, logger)
	if err := runDecryptWorkflowWithUI(ctx, cfg, logger, version, ui); err != nil {
		if errors.Is(err, ErrDecryptAborted) {
			return ErrDecryptAborted
		}
		return err
	}
	return nil
}

func buildTargetInfo(manifest *backup.Manifest) string {
	targets := formatTargets(manifest)
	if targets == "" {
		targets = "unknown"
	} else {
		targets = strings.ToUpper(targets)
	}

	version := normalizeProxmoxVersion(manifest.ProxmoxVersion)
	if version != "" {
		targets = fmt.Sprintf("%s %s", targets, version)
	}

	if cluster := formatClusterMode(manifest.ClusterMode); cluster != "" {
		targets = fmt.Sprintf("%s (%s)", targets, cluster)
	}

	return fmt.Sprintf("Targets: %s", targets)
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

func filterEncryptedCandidates(candidates []*decryptCandidate) []*decryptCandidate {
	if len(candidates) == 0 {
		return candidates
	}
	filtered := make([]*decryptCandidate, 0, len(candidates))
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

func buildWizardPage(title, configPath, buildSig string, content tview.Primitive) tview.Primitive {
	return tui.BuildScreen(tui.ScreenSpec{
		Title:           title,
		HeaderText:      fmt.Sprintf("ProxSave - By TIS24DEV\n%s\n", decryptWizardSubtitle),
		NavText:         decryptNavText,
		ConfigPath:      configPath,
		BuildSig:        buildSig,
		TitleColor:      tui.ProxmoxOrange,
		BorderColor:     tui.ProxmoxOrange,
		BackgroundColor: tcell.ColorBlack,
	}, content)
}
