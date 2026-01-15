package orchestrator

import (
	"archive/tar"
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
)

// SelectiveRestoreConfig holds the configuration for selective restore
type SelectiveRestoreConfig struct {
	Mode               RestoreMode
	SelectedCategories []Category
	SystemType         SystemType
	Metadata           *backup.Manifest
}

// AnalyzeBackupCategories detects which categories are available in the backup
func AnalyzeBackupCategories(archivePath string, logger *logging.Logger) (categories []Category, err error) {
	done := logging.DebugStart(logger, "analyze backup categories", "archive=%s", archivePath)
	defer func() { done(err) }()
	logger.Info("Analyzing backup categories...")

	// Open the archive and read all entry names
	file, err := restoreFS.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()

	// Create appropriate reader based on compression
	reader, err := createDecompressionReader(context.Background(), file, archivePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closer, ok := reader.(interface{ Close() error }); ok {
			closer.Close()
		}
	}()

	tarReader := tar.NewReader(reader)

	archivePaths := collectArchivePaths(tarReader)
	logger.Debug("Found %d entries in archive", len(archivePaths))

	availableCategories := AnalyzeArchivePaths(archivePaths, GetAllCategories())
	for _, cat := range availableCategories {
		logger.Debug("Category available: %s (%s)", cat.ID, cat.Name)
	}

	logger.Info("Detected %d available categories", len(availableCategories))
	return availableCategories, nil
}

// AnalyzeArchivePaths determines available categories from the provided archive entries.
func AnalyzeArchivePaths(archivePaths []string, allCategories []Category) []Category {
	if len(archivePaths) == 0 || len(allCategories) == 0 {
		return nil
	}
	var availableCategories []Category
	for _, cat := range allCategories {
		isAvailable := false

		// Check if any path in this category exists in the archive
		for _, catPath := range cat.Paths {
			for _, archivePath := range archivePaths {
				if pathMatchesPattern(archivePath, catPath) {
					isAvailable = true
					break
				}
			}
			if isAvailable {
				break
			}
		}

		if isAvailable {
			cat.IsAvailable = true
			availableCategories = append(availableCategories, cat)
		}
	}
	return availableCategories
}

func collectArchivePaths(tarReader *tar.Reader) []string {
	var archivePaths []string
	for {
		header, err := tarReader.Next()
		if err != nil {
			break // EOF or error
		}
		archivePaths = append(archivePaths, header.Name)
	}
	return archivePaths
}

// pathMatchesPattern checks if an archive path matches a category pattern
func pathMatchesPattern(archivePath, pattern string) bool {
	// Normalize paths
	normArchive := archivePath
	if !strings.HasPrefix(normArchive, "./") {
		normArchive = "./" + normArchive
	}

	normPattern := pattern
	if !strings.HasPrefix(normPattern, "./") {
		normPattern = "./" + normPattern
	}

	// Exact match
	if normArchive == normPattern {
		return true
	}

	// Directory prefix match
	if strings.HasSuffix(normPattern, "/") {
		if strings.HasPrefix(normArchive, normPattern) {
			return true
		}
	}

	// Parent directory match
	if strings.HasPrefix(normArchive, strings.TrimSuffix(normPattern, "/")+"/") {
		return true
	}

	return false
}

// ShowRestoreModeMenu displays the restore mode selection menu
func ShowRestoreModeMenu(logger *logging.Logger, systemType SystemType) (RestoreMode, error) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	fmt.Println("Select restore mode:")
	fmt.Println("  [1] FULL restore - Restore everything from backup")

	if systemType == SystemTypePVE {
		fmt.Println("  [2] STORAGE only - PVE cluster + storage configuration + VM configs + jobs")
	} else if systemType == SystemTypePBS {
		fmt.Println("  [2] DATASTORE only - PBS config + datastore definitions + sync/verify/prune jobs")
	} else {
		fmt.Println("  [2] STORAGE/DATASTORE only - Storage or datastore configuration")
	}

	fmt.Println("  [3] SYSTEM BASE only - Network + SSL + SSH + services")
	fmt.Println("  [4] CUSTOM selection - Choose specific categories")
	fmt.Println("  [0] Cancel")
	fmt.Print("Choice: ")

	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	choice := strings.TrimSpace(input)

	switch choice {
	case "1":
		return RestoreModeFull, nil
	case "2":
		return RestoreModeStorage, nil
	case "3":
		return RestoreModeBase, nil
	case "4":
		return RestoreModeCustom, nil
	case "0":
		return "", fmt.Errorf("user cancelled")
	default:
		fmt.Println("Invalid choice. Please try again.")
		return ShowRestoreModeMenu(logger, systemType)
	}
}

// ShowCategorySelectionMenu displays an interactive category selection menu
func ShowCategorySelectionMenu(logger *logging.Logger, availableCategories []Category, systemType SystemType) ([]Category, error) {
	reader := bufio.NewReader(os.Stdin)

	// Filter categories by system type
	relevantCategories := make([]Category, 0)
	for _, cat := range availableCategories {
		if cat.Type == CategoryTypeCommon ||
			(systemType == SystemTypePVE && cat.Type == CategoryTypePVE) ||
			(systemType == SystemTypePBS && cat.Type == CategoryTypePBS) {
			relevantCategories = append(relevantCategories, cat)
		}
	}

	// Sort categories: PVE/PBS first, then common
	sort.Slice(relevantCategories, func(i, j int) bool {
		if relevantCategories[i].Type != relevantCategories[j].Type {
			if relevantCategories[i].Type == CategoryTypeCommon {
				return false
			}
			if relevantCategories[j].Type == CategoryTypeCommon {
				return true
			}
		}
		return relevantCategories[i].Name < relevantCategories[j].Name
	})

	// Track selection state
	selected := make(map[int]bool)

	for {
		fmt.Println()
		fmt.Println("═══════════════════════════════════════════════════════════════")
		fmt.Println("CUSTOM CATEGORY SELECTION")
		fmt.Println("═══════════════════════════════════════════════════════════════")
		fmt.Println()

		// Display categories with checkboxes
		for i, cat := range relevantCategories {
			checkbox := "[ ]"
			if selected[i] {
				checkbox = "[X]"
			}

			fmt.Printf("  [%d] %s %s\n", i+1, checkbox, cat.Name)
			fmt.Printf("      %s\n", cat.Description)
		}

		fmt.Println()
		fmt.Println("Commands:")
		fmt.Println("  1-9    - Toggle category selection")
		fmt.Println("  a      - Select all")
		fmt.Println("  n      - Deselect all")
		fmt.Println("  c      - Continue with selected categories")
		fmt.Println("  0      - Cancel")
		fmt.Print("\nChoice: ")

		input, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}

		choice := strings.TrimSpace(strings.ToLower(input))

		switch choice {
		case "a":
			// Select all
			for i := range relevantCategories {
				selected[i] = true
			}
		case "n":
			// Deselect all
			selected = make(map[int]bool)
		case "c":
			// Continue - check if at least one category is selected
			selectedCount := 0
			for range selected {
				selectedCount++
			}

			if selectedCount == 0 {
				fmt.Println()
				fmt.Println("⚠ Warning: No categories selected. Please select at least one category.")
				continue
			}

			// Build list of selected categories
			var selectedCategories []Category
			for i, cat := range relevantCategories {
				if selected[i] {
					selectedCategories = append(selectedCategories, cat)
				}
			}

			return selectedCategories, nil

		case "0":
			return nil, fmt.Errorf("user cancelled")

		default:
			// Try to parse as a number
			num, err := strconv.Atoi(choice)
			if err != nil || num < 1 || num > len(relevantCategories) {
				fmt.Println("Invalid choice. Please try again.")
				continue
			}

			// Toggle selection
			index := num - 1
			selected[index] = !selected[index]
		}
	}
}

// GetCategoriesForMode returns categories based on the selected restore mode
func GetCategoriesForMode(mode RestoreMode, systemType SystemType, availableCategories []Category) []Category {
	switch mode {
	case RestoreModeFull:
		// Return all available categories, including export-only ones (e.g., /etc/pve)
		return append([]Category{}, availableCategories...)

	case RestoreModeStorage:
		// Return storage/datastore categories
		storageCats := GetStorageModeCategories(string(systemType))
		return filterOutExportOnly(filterAvailable(storageCats, availableCategories))

	case RestoreModeBase:
		// Return system base categories
		baseCats := GetBaseModeCategories()
		return filterOutExportOnly(filterAvailable(baseCats, availableCategories))

	default:
		// Custom mode - should not be called for this, but return empty
		return []Category{}
	}
}

// filterAvailable filters categories to only include those available in the backup
func filterAvailable(requested []Category, available []Category) []Category {
	var result []Category

	for _, req := range requested {
		for _, avail := range available {
			if req.ID == avail.ID && avail.IsAvailable {
				result = append(result, avail)
				break
			}
		}
	}

	return result
}

func filterOutExportOnly(categories []Category) []Category {
	if len(categories) == 0 {
		return categories
	}
	out := make([]Category, 0, len(categories))
	for _, cat := range categories {
		if cat.ExportOnly {
			continue
		}
		out = append(out, cat)
	}
	return out
}

// ShowRestorePlan displays a detailed plan of what will be restored
func ShowRestorePlan(logger *logging.Logger, config *SelectiveRestoreConfig) {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Println("RESTORE PLAN")
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Println()

	// Show mode
	modeName := ""
	switch config.Mode {
	case RestoreModeFull:
		modeName = "FULL restore (all categories)"
	case RestoreModeStorage:
		if config.SystemType == SystemTypePVE {
			modeName = "STORAGE only (PVE cluster + storage + jobs)"
		} else {
			modeName = "DATASTORE only (PBS config + datastores + jobs)"
		}
	case RestoreModeBase:
		modeName = "SYSTEM BASE only (network + SSL + SSH + services)"
	case RestoreModeCustom:
		modeName = fmt.Sprintf("CUSTOM selection (%d categories)", len(config.SelectedCategories))
	}

	fmt.Printf("Restore mode: %s\n", modeName)
	fmt.Printf("System type:  %s\n", GetSystemTypeString(config.SystemType))
	fmt.Println()

	// Show selected categories
	fmt.Println("Categories to restore:")
	for i, cat := range config.SelectedCategories {
		fmt.Printf("  %d. %s\n", i+1, cat.Name)
		fmt.Printf("     %s\n", cat.Description)
	}

	fmt.Println()
	fmt.Println("Files/directories that will be restored:")

	// Collect and display all paths
	allPaths := GetSelectedPaths(config.SelectedCategories)
	sort.Strings(allPaths)

	for _, path := range allPaths {
		// Convert to filesystem path for display
		fsPath := strings.TrimPrefix(path, "./")
		fmt.Printf("  • /%s\n", fsPath)
	}

	fmt.Println()
	fmt.Println("⚠ WARNING:")
	fmt.Println("  • Existing files at these locations will be OVERWRITTEN")
	fmt.Println("  • A safety backup will be created before restoration")
	fmt.Println("  • Services may need to be restarted after restoration")
	fmt.Println()
}

// ConfirmRestoreOperation asks for user confirmation before proceeding
func ConfirmRestoreOperation(logger *logging.Logger) (bool, error) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Println("═══════════════════════════════════════════════════════════════")
		fmt.Print("Type 'RESTORE' to proceed or 'cancel' to abort: ")

		input, err := reader.ReadString('\n')
		if err != nil {
			return false, err
		}

		response := strings.TrimSpace(input)

		if response == "RESTORE" {
			return true, nil
		}

		if strings.ToLower(response) == "cancel" || response == "0" {
			return false, nil
		}

		fmt.Println("Invalid input. Please type 'RESTORE' or 'cancel'.")
	}
}
