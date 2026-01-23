package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/tui"
	"github.com/tis24dev/proxsave/internal/tui/components"
)

const (
	decryptWizardSubtitle = "Decrypt Backup Workflow"
	decryptNavText        = "[yellow]Navigation:[white] TAB/↑↓ to move | ENTER to select | ESC to exit screens | Mouse clicks enabled"

	pathActionOverwrite = "overwrite"
	pathActionNew       = "new"
	pathActionCancel    = "cancel"
)

var (
	promptOverwriteActionFunc = promptOverwriteAction
	promptNewPathInputFunc    = promptNewPathInput
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

func ensureWritablePathTUI(path, description, configPath, buildSig string) (string, error) {
	current := filepath.Clean(path)
	if description == "" {
		description = "file"
	}
	var failureMessage string

	for {
		if _, err := restoreFS.Stat(current); errors.Is(err, os.ErrNotExist) {
			return current, nil
		} else if err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("stat %s: %w", current, err)
		}

		action, err := promptOverwriteActionFunc(current, description, failureMessage, configPath, buildSig)
		if err != nil {
			return "", err
		}
		failureMessage = ""

		switch action {
		case pathActionOverwrite:
			if err := restoreFS.Remove(current); err != nil && !errors.Is(err, os.ErrNotExist) {
				failureMessage = fmt.Sprintf("Failed to remove existing %s: %v", description, err)
				continue
			}
			return current, nil
		case pathActionNew:
			newPath, err := promptNewPathInputFunc(current, configPath, buildSig)
			if err != nil {
				if errors.Is(err, ErrDecryptAborted) {
					return "", ErrDecryptAborted
				}
				failureMessage = err.Error()
				continue
			}
			current = filepath.Clean(newPath)
		default:
			return "", ErrDecryptAborted
		}
	}
}

func promptOverwriteAction(path, description, failureMessage, configPath, buildSig string) (string, error) {
	app := newTUIApp()
	var choice string

	message := fmt.Sprintf("The %s [yellow]%s[white] already exists.\nSelect how you want to proceed.", description, path)
	if strings.TrimSpace(failureMessage) != "" {
		message = fmt.Sprintf("%s\n\n[red]%s[white]", message, failureMessage)
	}
	message += "\n\n[yellow]Use ←→ or TAB to switch buttons | ENTER to confirm[white]"

	modal := tview.NewModal().
		SetText(message).
		AddButtons([]string{"Overwrite", "Use different path", "Cancel"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			switch buttonLabel {
			case "Overwrite":
				choice = pathActionOverwrite
			case "Use different path":
				choice = pathActionNew
			default:
				choice = pathActionCancel
			}
			app.Stop()
		})

	modal.SetBorder(true).
		SetTitle(" Existing file ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.WarningYellow).
		SetBorderColor(tui.WarningYellow).
		SetBackgroundColor(tcell.ColorBlack)

	wrapped := buildWizardPage("Destination path", configPath, buildSig, modal)
	if err := app.SetRoot(wrapped, true).SetFocus(modal).Run(); err != nil {
		return "", err
	}
	return choice, nil
}

func promptNewPathInput(defaultPath, configPath, buildSig string) (string, error) {
	app := newTUIApp()
	var newPath string
	var cancelled bool

	form := components.NewForm(app)
	label := "New path"
	form.AddInputFieldWithValidation(label, defaultPath, 64, func(value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("path cannot be empty")
		}
		return nil
	})
	form.SetOnSubmit(func(values map[string]string) error {
		newPath = strings.TrimSpace(values[label])
		return nil
	})
	form.SetOnCancel(func() {
		cancelled = true
	})
	form.AddSubmitButton("Continue")
	form.AddCancelButton("Cancel")

	helper := tview.NewTextView().
		SetText("Provide a writable filesystem path for the decrypted files.").
		SetWrap(true).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true)

	content := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(helper, 3, 0, false).
		AddItem(form.Form, 0, 1, true)

	page := buildWizardPage("Choose destination path", configPath, buildSig, content)
	form.SetParentView(page)

	if err := app.SetRoot(page, true).SetFocus(form.Form).Run(); err != nil {
		return "", err
	}
	if cancelled {
		return "", ErrDecryptAborted
	}
	return filepath.Clean(newPath), nil
}

func preparePlainBundleTUI(ctx context.Context, cand *decryptCandidate, version string, logger *logging.Logger, configPath, buildSig string) (*preparedBundle, error) {
	if cand == nil || cand.Manifest == nil {
		return nil, fmt.Errorf("invalid backup candidate")
	}

	// If this is an rclone-backed bundle, download it first into the local temp area.
	var rcloneCleanup func()
	if cand.IsRclone && cand.Source == sourceBundle {
		logger.Debug("Detected rclone backup, downloading for TUI workflow...")
		localPath, cleanupFn, err := downloadRcloneBackup(ctx, cand.BundlePath, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to download rclone backup: %w", err)
		}
		rcloneCleanup = cleanupFn
		cand.BundlePath = localPath
	}

	tempRoot := filepath.Join("/tmp", "proxsave")
	if err := restoreFS.MkdirAll(tempRoot, 0o755); err != nil {
		if rcloneCleanup != nil {
			rcloneCleanup()
		}
		return nil, fmt.Errorf("create temp root: %w", err)
	}
	workDir, err := restoreFS.MkdirTemp(tempRoot, "proxmox-decrypt-*")
	if err != nil {
		if rcloneCleanup != nil {
			rcloneCleanup()
		}
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() {
		_ = restoreFS.RemoveAll(workDir)
		if rcloneCleanup != nil {
			rcloneCleanup()
		}
	}

	var staged stagedFiles
	switch cand.Source {
	case sourceBundle:
		logger.Debug("Extracting bundle %s", filepath.Base(cand.BundlePath))
		staged, err = extractBundleToWorkdirWithLogger(cand.BundlePath, workDir, logger)
	case sourceRaw:
		logger.Debug("Staging raw artifacts for %s", filepath.Base(cand.RawArchivePath))
		staged, err = copyRawArtifactsToWorkdirWithLogger(ctx, cand, workDir, logger)
	default:
		err = fmt.Errorf("unsupported candidate source")
	}
	if err != nil {
		cleanup()
		return nil, err
	}

	manifestCopy := *cand.Manifest
	currentEncryption := strings.ToLower(manifestCopy.EncryptionMode)

	logger.Debug("Preparing archive %s for decryption (mode: %s)", filepath.Base(manifestCopy.ArchivePath), statusFromManifest(&manifestCopy))

	plainArchiveName := strings.TrimSuffix(filepath.Base(staged.ArchivePath), ".age")
	plainArchivePath := filepath.Join(workDir, plainArchiveName)

	if currentEncryption == "age" {
		displayName := cand.DisplayBase
		if displayName == "" {
			displayName = filepath.Base(manifestCopy.ArchivePath)
		}
		if err := decryptArchiveWithTUIPrompts(ctx, staged.ArchivePath, plainArchivePath, displayName, configPath, buildSig, logger); err != nil {
			cleanup()
			return nil, err
		}
	} else if staged.ArchivePath != plainArchivePath {
		if err := copyFile(restoreFS, staged.ArchivePath, plainArchivePath); err != nil {
			cleanup()
			return nil, fmt.Errorf("copy archive: %w", err)
		}
	}

	archiveInfo, err := restoreFS.Stat(plainArchivePath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("stat decrypted archive: %w", err)
	}

	checksum, err := backup.GenerateChecksum(ctx, logger, plainArchivePath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("generate checksum: %w", err)
	}

	manifestCopy.ArchivePath = plainArchivePath
	manifestCopy.ArchiveSize = archiveInfo.Size()
	manifestCopy.SHA256 = checksum
	manifestCopy.EncryptionMode = "none"
	if version != "" {
		manifestCopy.ScriptVersion = version
	}

	return &preparedBundle{
		ArchivePath: plainArchivePath,
		Manifest:    manifestCopy,
		Checksum:    checksum,
		cleanup:     cleanup,
	}, nil
}

func decryptArchiveWithTUIPrompts(ctx context.Context, encryptedPath, outputPath, displayName, configPath, buildSig string, logger *logging.Logger) error {
	var promptError string
	for {
		identities, err := promptDecryptIdentity(displayName, configPath, buildSig, promptError)
		if err != nil {
			return err
		}

		if err := decryptWithIdentity(encryptedPath, outputPath, identities...); err != nil {
			var noMatch *age.NoIdentityMatchError
			if errors.Is(err, age.ErrIncorrectIdentity) || errors.As(err, &noMatch) {
				promptError = "Provided key or passphrase does not match this archive."
				logger.Warning("Incorrect key or passphrase for %s", filepath.Base(encryptedPath))
				continue
			}
			return err
		}
		return nil
	}
}

func promptDecryptIdentity(displayName, configPath, buildSig, errorMessage string) ([]age.Identity, error) {
	app := newTUIApp()
	var (
		chosenIdentity []age.Identity
		cancelled      bool
	)

	name := displayName
	if strings.TrimSpace(name) == "" {
		name = "selected backup"
	}
	infoMessage := fmt.Sprintf("Provide the AGE secret key or passphrase used for [yellow]%s[white].", name)
	if strings.TrimSpace(errorMessage) != "" {
		infoMessage = fmt.Sprintf("%s\n\n[red]%s[white]", infoMessage, errorMessage)
	}
	infoText := tview.NewTextView().
		SetText(infoMessage).
		SetWrap(true).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true)

	form := components.NewForm(app)
	label := "Key or passphrase:"
	form.AddPasswordField(label, 64)
	form.SetOnSubmit(func(values map[string]string) error {
		raw := strings.TrimSpace(values[label])
		if raw == "" {
			return fmt.Errorf("key or passphrase cannot be empty")
		}
		identity, err := parseIdentityInput(raw)
		resetString(&raw)
		if err != nil {
			return fmt.Errorf("invalid key or passphrase: %w", err)
		}
		chosenIdentity = identity
		return nil
	})
	form.SetOnCancel(func() {
		cancelled = true
	})
	// Buttons: Continue, Cancel
	form.AddSubmitButton("Continue")
	form.AddCancelButton("Cancel")

	content := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(infoText, 3, 0, false).
		AddItem(form.Form, 0, 1, true)

	page := buildWizardPage("Enter decryption secret", configPath, buildSig, content)
	form.SetParentView(page)

	if err := app.SetRoot(page, true).SetFocus(form.Form).Run(); err != nil {
		return nil, err
	}
	if cancelled {
		return nil, ErrDecryptAborted
	}
	if len(chosenIdentity) == 0 {
		return nil, fmt.Errorf("missing identity")
	}
	return chosenIdentity, nil
}

func enableFormNavigation(form *components.Form, dropdownOpen *bool) {
	if form == nil || form.Form == nil {
		return
	}
	form.Form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event == nil {
			return event
		}
		if dropdownOpen != nil && *dropdownOpen {
			return event
		}

		formItemIndex, buttonIndex := form.Form.GetFocusedItemIndex()
		isOnButton := formItemIndex < 0 && buttonIndex >= 0
		isOnField := formItemIndex >= 0

		if isOnButton {
			switch event.Key() {
			case tcell.KeyLeft, tcell.KeyUp:
				return tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone)
			case tcell.KeyRight, tcell.KeyDown:
				return tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
			}
		} else if isOnField {
			// If focused item is a ListFormItem, let it handle navigation internally
			if formItemIndex >= 0 {
				if _, ok := form.Form.GetFormItem(formItemIndex).(*components.ListFormItem); ok {
					return event
				}
			}
			// For other form fields, convert arrows to tab navigation
			switch event.Key() {
			case tcell.KeyUp:
				return tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone)
			case tcell.KeyDown:
				return tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
			}
		}
		return event
	})
}

func buildWizardPage(title, configPath, buildSig string, content tview.Primitive) tview.Primitive {
	welcomeText := tview.NewTextView().
		SetText(fmt.Sprintf("ProxSave - By TIS24DEV\n%s\n", decryptWizardSubtitle)).
		SetTextColor(tui.ProxmoxLight).
		SetDynamicColors(true)
	welcomeText.SetBorder(false)

	navInstructions := tview.NewTextView().
		SetText("\n" + decryptNavText).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	navInstructions.SetBorder(false)

	separator := tview.NewTextView().
		SetText(strings.Repeat("─", 80)).
		SetTextColor(tui.ProxmoxOrange)
	separator.SetBorder(false)

	configPathText := tview.NewTextView().
		SetText(fmt.Sprintf("[yellow]Configuration file:[white] %s", configPath)).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	configPathText.SetBorder(false)

	buildSigText := tview.NewTextView().
		SetText(fmt.Sprintf("[yellow]Build Signature:[white] %s", buildSig)).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	buildSigText.SetBorder(false)

	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(welcomeText, 5, 0, false).
		AddItem(navInstructions, 2, 0, false).
		AddItem(separator, 1, 0, false).
		AddItem(content, 0, 1, true).
		AddItem(configPathText, 1, 0, false).
		AddItem(buildSigText, 1, 0, false)

	flex.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s ", title)).
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	return flex
}
