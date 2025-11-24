package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tis24dev/proxmox-backup/internal/backup"
	"github.com/tis24dev/proxmox-backup/internal/config"
	"github.com/tis24dev/proxmox-backup/internal/logging"
)

// FakePrompterCancel cancels selection/confirmation.
type FakePrompterCancel struct{}

func (FakePrompterCancel) SelectRestoreMode(logger *logging.Logger, systemType SystemType) (RestoreMode, error) {
	return RestoreModeCustom, errors.New("user cancelled")
}

func (FakePrompterCancel) SelectCategories(logger *logging.Logger, available []Category, systemType SystemType) ([]Category, error) {
	return nil, errors.New("user cancelled")
}

func (FakePrompterCancel) ConfirmRestore(logger *logging.Logger) (bool, error) {
	return false, errors.New("user cancelled")
}

// FakePrompterConfirm always confirms with provided mode/categories.
type FakePrompterConfirm struct {
	Mode       RestoreMode
	Categories []Category
}

func (f FakePrompterConfirm) SelectRestoreMode(logger *logging.Logger, systemType SystemType) (RestoreMode, error) {
	return f.Mode, nil
}

func (f FakePrompterConfirm) SelectCategories(logger *logging.Logger, available []Category, systemType SystemType) ([]Category, error) {
	return f.Categories, nil
}

func (f FakePrompterConfirm) ConfirmRestore(logger *logging.Logger) (bool, error) {
	return true, nil
}

func TestRunRestoreWorkflow_UserCancels(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &config.Config{BaseDir: t.TempDir()}

	deps := defaultDeps(logger, t.TempDir(), true)
	deps.Config = cfg
	deps.Prompter = FakePrompterCancel{}
	restorePrompter = deps.Prompter
	restoreFS = osFS{}
	restoreCmd = osCommandRunner{}
	restoreSystem = FakeSystemDetector{Type: SystemTypePVE}
	t.Cleanup(func() {
		restorePrompter = consolePrompter{}
		restoreFS = osFS{}
		restoreCmd = osCommandRunner{}
		restoreSystem = realSystemDetector{}
	})

	// Minimal manifest
	manifest := &backup.Manifest{
		ArchivePath: "missing.tar",
		CreatedAt:   time.Now(),
	}
	candidate := &decryptCandidate{
		Manifest:       manifest,
		Source:         sourceRaw,
		RawArchivePath: "missing.tar",
	}
	// Bypass prepareDecryptedBackup by calling lower-level function
	reader := bufio.NewReader(strings.NewReader(""))
	err := runFullRestore(context.Background(), reader, candidate, &preparedBundle{ArchivePath: "missing.tar"}, "/", logger)
	if err == nil {
		t.Fatalf("expected error due to missing archive")
	}
}
