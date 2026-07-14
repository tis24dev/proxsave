package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// ErrInstallCancelled is returned when the user aborts the install wizard.
var ErrInstallCancelled = errors.New("installation aborted by user")

// ExistingConfigAction represents how to handle an already-present
// configuration file.
type ExistingConfigAction int

const (
	ExistingConfigOverwrite    ExistingConfigAction = iota // Start from embedded template (overwrite)
	ExistingConfigEdit                                     // Keep existing file as base and edit
	ExistingConfigKeepContinue                             // Leave file untouched and continue installation
	ExistingConfigCancel                                   // Abort installation
)

// PostInstallAuditResult reports the outcome of the optional post-install
// audit step.
type PostInstallAuditResult struct {
	// Ran indicates whether the user chose to run the post-install check.
	Ran bool
	// Suggestions contains the disable suggestions extracted from the dry-run output.
	Suggestions []PostInstallAuditSuggestion
	// AppliedKeys contains the keys written as KEY=false into backup.env.
	AppliedKeys []string
	// CollectErr is set when the dry-run/suggestion collection failed.
	CollectErr error
}

// TelegramSetupResult reports the outcome of the interactive Telegram
// pairing step.
type TelegramSetupResult struct {
	orchestrator.TelegramSetupBootstrap

	Shown bool

	CheckAttempts     int
	Verified          bool
	Partial           bool
	LastStatusFatal   bool
	LastStatusCode    int
	LastStatusMessage string
	LastStatusError   string

	SkippedVerification bool
}

// ApplyAuditDisables writes KEY=false for every selected key into the
// configuration file (atomic replace). Shared by the UI audit flow; keys are
// normalized to upper case and sorted.
func ApplyAuditDisables(configPath string, keys []string) error {
	if strings.TrimSpace(configPath) == "" {
		return fmt.Errorf("config path cannot be empty")
	}
	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.ToUpper(strings.TrimSpace(key))
		if key != "" {
			normalized = append(normalized, key)
		}
	}
	sort.Strings(normalized)
	if len(normalized) == 0 {
		return nil
	}

	contentBytes, err := os.ReadFile(configPath) // #nosec G304 -- admin-supplied config path
	if err != nil {
		return fmt.Errorf("read configuration: %w", err)
	}
	content := string(contentBytes)
	for _, key := range normalized {
		content = setEnvValue(content, key, "false")
	}
	return WriteConfigFileAtomic(configPath, configPath+".tmp.audit", content)
}

// WriteConfigFileAtomic writes content to tmpPath inside the config
// directory and renames it over configPath (same containment strategy as the
// installer's main writer).
func WriteConfigFileAtomic(configPath, tmpPath, content string) error {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create configuration directory: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("failed to open configuration directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	if err := root.WriteFile(filepath.Base(tmpPath), []byte(content), 0o600); err != nil {
		return fmt.Errorf("failed to write configuration file: %w", err)
	}
	defer func() {
		if _, statErr := os.Stat(tmpPath); statErr == nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := os.Rename(tmpPath, configPath); err != nil {
		return fmt.Errorf("failed to finalize configuration file: %w", err)
	}
	return nil
}
