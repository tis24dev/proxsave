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

type decryptSelection struct {
	Candidate *decryptCandidate
	DestDir   string
}

const (
	decryptWizardSubtitle = "Decrypt Backup Workflow"
	decryptNavText        = "[yellow]Navigation:[white] TAB/↑↓ to move | ENTER to select | ESC to exit screens | Mouse clicks enabled"
	errorModalPage        = "decrypt-error-modal"

	pathActionOverwrite = "overwrite"
	pathActionNew       = "new"
	pathActionCancel    = "cancel"
)

// RunDecryptWorkflowTUI runs the decrypt workflow using a TUI flow.
func RunDecryptWorkflowTUI(ctx context.Context, cfg *config.Config, logger *logging.Logger, version, configPath, buildSig string) error {
	if cfg == nil {
		return fmt.Errorf("configuration not available")
	}
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	if strings.TrimSpace(buildSig) == "" {
		buildSig = "n/a"
	}

	selection, err := runDecryptSelectionWizard(cfg, configPath, buildSig)
	if err != nil {
		if errors.Is(err, ErrDecryptAborted) {
			return ErrDecryptAborted
		}
		return err
	}

	prepared, err := preparePlainBundleTUI(ctx, selection.Candidate, version, logger, configPath, buildSig)
	if err != nil {
		return err
	}
	defer prepared.Cleanup()

	destDir := selection.DestDir
	if err := restoreFS.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	// Determine the logical decrypted archive path for naming purposes.
	// This keeps the same defaults and prompts as before, but the archive
	// itself stays in the temporary working directory.
	destArchivePath := filepath.Join(destDir, filepath.Base(prepared.ArchivePath))
	destArchivePath, err = ensureWritablePathTUI(destArchivePath, "decrypted archive", configPath, buildSig)
	if err != nil {
		return err
	}

	// Work exclusively inside the temporary directory created by preparePlainBundleTUI.
	workDir := filepath.Dir(prepared.ArchivePath)
	archiveBase := filepath.Base(destArchivePath)
	tempArchivePath := filepath.Join(workDir, archiveBase)

	// Ensure the staged archive in the temp dir has the desired basename.
	if tempArchivePath != prepared.ArchivePath {
		if err := moveFileSafe(prepared.ArchivePath, tempArchivePath); err != nil {
			return fmt.Errorf("move decrypted archive within temp dir: %w", err)
		}
	}

	manifestCopy := prepared.Manifest
	// As in the CLI workflow, keep the manifest's ArchivePath pointing to the
	// destination archive location while the actual archive continues to live
	// only inside the temporary work directory.
	manifestCopy.ArchivePath = destArchivePath

	metadataPath := tempArchivePath + ".metadata"
	if err := backup.CreateManifest(ctx, logger, &manifestCopy, metadataPath); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}

	checksumPath := tempArchivePath + ".sha256"
	if err := restoreFS.WriteFile(checksumPath, []byte(fmt.Sprintf("%s  %s\n", prepared.Checksum, filepath.Base(tempArchivePath))), 0o640); err != nil {
		return fmt.Errorf("write checksum file: %w", err)
	}

	logger.Debug("Creating decrypted bundle...")
	bundlePath, err := createBundle(ctx, logger, tempArchivePath)
	if err != nil {
		return err
	}

	// Only the final decrypted bundle is moved into the destination directory.
	logicalBundlePath := destArchivePath + ".bundle.tar"
	targetBundlePath := strings.TrimSuffix(logicalBundlePath, ".bundle.tar") + ".decrypted.bundle.tar"
	targetBundlePath, err = ensureWritablePathTUI(targetBundlePath, "decrypted bundle", configPath, buildSig)
	if err != nil {
		return err
	}
	if err := restoreFS.Remove(targetBundlePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warning("Failed to remove existing bundle target: %v", err)
	}
	if err := moveFileSafe(bundlePath, targetBundlePath); err != nil {
		return fmt.Errorf("move decrypted bundle: %w", err)
	}

	logger.Info("Decrypted bundle created: %s", targetBundlePath)
	return nil
}

func runDecryptSelectionWizard(cfg *config.Config, configPath, buildSig string) (*decryptSelection, error) {
	options := buildDecryptPathOptions(cfg)
	if len(options) == 0 {
		return nil, fmt.Errorf("no backup paths configured in backup.env")
	}

	app := tui.NewApp()
	pages := tview.NewPages()

	selection := &decryptSelection{}
	var selectionErr error

	pathList := tview.NewList().ShowSecondaryText(false)
	pathList.SetMainTextColor(tcell.ColorWhite).
		SetSelectedTextColor(tcell.ColorWhite).
		SetSelectedBackgroundColor(tui.ProxmoxOrange)

	for _, opt := range options {
		// Use parentheses instead of square brackets (tview interprets [] as color tags)
		label := fmt.Sprintf("%s (%s)", opt.Label, opt.Path)
		pathList.AddItem(label, "", 0, nil)
	}

	pathList.SetSelectedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index < 0 || index >= len(options) {
			return
		}
		selectedOption := options[index]
		pages.SwitchToPage("loading")
		go func() {
			candidates, err := discoverBackupCandidates(logging.GetDefaultLogger(), selectedOption.Path)
			app.QueueUpdateDraw(func() {
				if err != nil {
					message := fmt.Sprintf("Failed to inspect %s: %v", selectedOption.Path, err)
					showErrorModal(app, pages, configPath, buildSig, message, func() {
						pages.SwitchToPage("paths")
					})
					return
				}

				encrypted := filterEncryptedCandidates(candidates)
				if len(encrypted) == 0 {
					message := "No encrypted backups found in selected path."
					showErrorModal(app, pages, configPath, buildSig, message, func() {
						pages.SwitchToPage("paths")
					})
					return
				}

				showCandidatePage(app, pages, encrypted, configPath, buildSig, func(c *decryptCandidate) {
					selection.Candidate = c
					showDestinationForm(app, pages, cfg, c, configPath, buildSig, func(dest string) {
						selection.DestDir = dest
						app.Stop()
					})
				}, func() {
					selectionErr = ErrDecryptAborted
					app.Stop()
				})
			})
		}()
	})
	pathList.SetDoneFunc(func() {
		selectionErr = ErrDecryptAborted
		app.Stop()
	})

	form := components.NewForm(app)
	listHeight := len(options)
	if listHeight < 8 {
		listHeight = 8
	}
	if listHeight > 14 {
		listHeight = 14
	}
	listItem := components.NewListFormItem(pathList).
		SetLabel("Available backup sources").
		SetFieldHeight(listHeight)
	form.Form.AddFormItem(listItem)
	form.Form.SetFocus(0)

	form.SetOnCancel(func() {
		selectionErr = ErrDecryptAborted
	})
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, nil)

	pathPage := buildWizardPage("Select backup source", configPath, buildSig, form.Form)
	pages.AddPage("paths", pathPage, true, true)

	loadingText := tview.NewTextView().
		SetText("Scanning backup path...").
		SetTextAlign(tview.AlignCenter)

	loadingForm := components.NewForm(app)
	loadingForm.SetOnCancel(func() {
		selectionErr = ErrDecryptAborted
	})
	loadingForm.AddCancelButton("Cancel")
	loadingContent := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(loadingText, 0, 1, false).
		AddItem(loadingForm.Form, 3, 0, false)
	loadingPage := buildWizardPage("Loading backups", configPath, buildSig, loadingContent)
	pages.AddPage("loading", loadingPage, true, false)

	app.SetRoot(pages, true).SetFocus(form.Form)
	if err := app.Run(); err != nil {
		return nil, err
	}
	if selectionErr != nil {
		return nil, selectionErr
	}
	if selection.Candidate == nil || selection.DestDir == "" {
		return nil, ErrDecryptAborted
	}
	return selection, nil
}

func showErrorModal(app *tui.App, pages *tview.Pages, configPath, buildSig, message string, onDismiss func()) {
	modal := tview.NewModal().
		SetText(fmt.Sprintf("%s %s\n\n[yellow]Press ENTER to continue[white]", tui.SymbolError, message)).
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			if pages.HasPage(errorModalPage) {
				pages.RemovePage(errorModalPage)
			}
			if onDismiss != nil {
				onDismiss()
			}
		})

	modal.SetBorder(true).
		SetTitle(" Decrypt Error ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ErrorRed).
		SetBorderColor(tui.ErrorRed).
		SetBackgroundColor(tcell.ColorBlack)

	page := buildWizardPage("Error", configPath, buildSig, modal)
	if pages.HasPage(errorModalPage) {
		pages.RemovePage(errorModalPage)
	}
	pages.AddPage(errorModalPage, page, true, true)
	app.SetFocus(modal)
}

func showCandidatePage(app *tui.App, pages *tview.Pages, candidates []*decryptCandidate, configPath, buildSig string, onSelect func(*decryptCandidate), onCancel func()) {
	list := tview.NewList().ShowSecondaryText(false)
	list.SetMainTextColor(tcell.ColorWhite).
		SetSelectedTextColor(tcell.ColorWhite).
		SetSelectedBackgroundColor(tui.ProxmoxOrange)

	type row struct {
		created     string
		mode        string
		tool        string
		targets     string
		compression string
	}

	rows := make([]row, len(candidates))
	var maxMode, maxTool, maxTargets, maxComp int

	for idx, cand := range candidates {
		created := cand.Manifest.CreatedAt.Format("2006-01-02 15:04:05")

		mode := strings.ToUpper(statusFromManifest(cand.Manifest))
		if mode == "" {
			mode = "UNKNOWN"
		}

		toolVersion := strings.TrimSpace(cand.Manifest.ScriptVersion)
		if toolVersion == "" {
			toolVersion = "unknown"
		}
		tool := "Tool " + toolVersion

		targets := buildTargetInfo(cand.Manifest)

		comp := ""
		if c := strings.TrimSpace(cand.Manifest.CompressionType); c != "" {
			comp = strings.ToUpper(c)
		}

		rows[idx] = row{
			created:     created,
			mode:        mode,
			tool:        tool,
			targets:     targets,
			compression: comp,
		}

		if len(mode) > maxMode {
			maxMode = len(mode)
		}
		if len(tool) > maxTool {
			maxTool = len(tool)
		}
		if len(targets) > maxTargets {
			maxTargets = len(targets)
		}
		if len(comp) > maxComp {
			maxComp = len(comp)
		}
	}

	for idx, r := range rows {
		line := fmt.Sprintf(
			"%2d) %s  %-*s  %-*s  %-*s",
			idx+1,
			r.created,
			maxMode, r.mode,
			maxTool, r.tool,
			maxTargets, r.targets,
		)
		if maxComp > 0 {
			line = fmt.Sprintf("%s  %-*s", line, maxComp, r.compression)
		}
		list.AddItem(line, "", 0, nil)
	}

	list.SetSelectedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index < 0 || index >= len(candidates) {
			return
		}
		onSelect(candidates[index])
	})
	list.SetDoneFunc(func() {
		pages.SwitchToPage("paths")
	})

	form := components.NewForm(app)
	listHeight := len(candidates)
	if listHeight < 8 {
		listHeight = 8
	}
	if listHeight > 14 {
		listHeight = 14
	}
	listItem := components.NewListFormItem(list).
		SetLabel("Available backups").
		SetFieldHeight(listHeight)
	form.Form.AddFormItem(listItem)
	form.Form.SetFocus(0)

	form.SetOnCancel(func() {
		if onCancel != nil {
			onCancel()
		}
	})

	// Back goes on the left, Cancel on the right (order of AddButton calls)
	form.Form.AddButton("Back", func() {
		pages.SwitchToPage("paths")
	})
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, nil)

	page := buildWizardPage("Select backup to decrypt", configPath, buildSig, form.Form)
	if pages.HasPage("candidates") {
		pages.RemovePage("candidates")
	}
	pages.AddPage("candidates", page, true, true)
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

func showDestinationForm(app *tui.App, pages *tview.Pages, cfg *config.Config, selected *decryptCandidate, configPath, buildSig string, onSubmit func(string)) {
	defaultDir := "./decrypt"
	if cfg != nil && strings.TrimSpace(cfg.BaseDir) != "" {
		defaultDir = filepath.Join(strings.TrimSpace(cfg.BaseDir), "decrypt")
	}
	form := components.NewForm(app)
	form.AddInputField("Destination directory", defaultDir, 48, nil, nil)

	form.SetOnSubmit(func(values map[string]string) error {
		dest := strings.TrimSpace(values["Destination directory"])
		if dest == "" {
			return fmt.Errorf("destination directory cannot be empty")
		}
		onSubmit(filepath.Clean(dest))
		return nil
	})
	form.SetOnCancel(func() {
		pages.SwitchToPage("candidates")
	})

	// Buttons: Back (left), Continue, Cancel (right)
	form.Form.AddButton("Back", func() {
		pages.SwitchToPage("candidates")
	})
	form.AddSubmitButton("Continue")
	form.AddCancelButton("Cancel")

	// Selected backup summary
	var summary string
	if selected != nil && selected.Manifest != nil {
		created := selected.Manifest.CreatedAt.Format("2006-01-02 15:04:05")
		mode := strings.ToUpper(statusFromManifest(selected.Manifest))
		if mode == "" {
			mode = "UNKNOWN"
		}
		targetInfo := buildTargetInfo(selected.Manifest)
		summary = fmt.Sprintf("Selected backup: %s • %s • %s", created, mode, targetInfo)
	}

	var content tview.Primitive
	if summary != "" {
		selText := tview.NewTextView().
			SetText(summary).
			SetTextColor(tcell.ColorWhite).
			SetDynamicColors(true)
		content = tview.NewFlex().
			SetDirection(tview.FlexRow).
			AddItem(selText, 2, 0, false).
			AddItem(form.Form, 0, 1, true)
	} else {
		content = form.Form
	}

	page := buildWizardPage("Destination directory", configPath, buildSig, content)
	if pages.HasPage("destination") {
		pages.RemovePage("destination")
	}
	pages.AddPage("destination", page, true, true)
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

		action, err := promptOverwriteAction(current, description, failureMessage, configPath, buildSig)
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
			newPath, err := promptNewPathInput(current, configPath, buildSig)
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
	app := tui.NewApp()
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
	app := tui.NewApp()
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

	tempRoot := filepath.Join("/tmp", "proxsave")
	if err := restoreFS.MkdirAll(tempRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create temp root: %w", err)
	}
	workDir, err := restoreFS.MkdirTemp(tempRoot, "proxmox-decrypt-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() {
		_ = restoreFS.RemoveAll(workDir)
	}

	var staged stagedFiles
	switch cand.Source {
	case sourceBundle:
		logger.Debug("Extracting bundle %s", filepath.Base(cand.BundlePath))
		staged, err = extractBundleToWorkdir(cand.BundlePath, workDir)
	case sourceRaw:
		logger.Debug("Staging raw artifacts for %s", filepath.Base(cand.RawArchivePath))
		staged, err = copyRawArtifactsToWorkdir(cand, workDir)
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
	app := tui.NewApp()
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
