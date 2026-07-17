package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// screenTitleCapturingUI drives selectBackupCandidateWithUI to its empty-state
// branch and records the screenTitle passed to ShowStatusResult.
type screenTitleCapturingUI struct {
	statusTitles []string
}

func (f *screenTitleCapturingUI) RunTask(ctx context.Context, title, initialMessage string, run func(ctx context.Context, report ProgressReporter) error) error {
	return run(ctx, nil)
}
func (f *screenTitleCapturingUI) ShowMessage(ctx context.Context, title, message string) error {
	return nil
}
func (f *screenTitleCapturingUI) ShowStatusResult(ctx context.Context, screenTitle string, level HealthcheckSetupLevel, keyword, explanation string) error {
	f.statusTitles = append(f.statusTitles, screenTitle)
	return nil
}
func (f *screenTitleCapturingUI) ShowError(ctx context.Context, title, message string) error {
	return nil
}
func (f *screenTitleCapturingUI) SelectBackupSource(ctx context.Context, options []decryptPathOption) (decryptPathOption, error) {
	return options[0], nil
}
func (f *screenTitleCapturingUI) SelectBackupCandidate(ctx context.Context, candidates []*backupCandidate) (*backupCandidate, error) {
	return nil, fmt.Errorf("unexpected SelectBackupCandidate call")
}

// The empty-state "Status:" screen must carry the CALLER's screen title, so the
// shared helper reads "Restore" on the restore path and "Decrypt" on decrypt,
// instead of the previously hardcoded "Decrypt" on both.
func TestSelectBackupCandidateEmptyStateUsesCallerScreenTitle(t *testing.T) {
	for _, title := range []string{"Decrypt", "Restore"} {
		t.Run(title, func(t *testing.T) {
			cfg := &config.Config{BackupPath: t.TempDir()} // empty dir => no backups
			logger := logging.New(types.LogLevelError, false)
			ui := &screenTitleCapturingUI{}

			_, err := selectBackupCandidateWithUI(context.Background(), ui, cfg, logger, title, false)
			if !errors.Is(err, ErrDecryptNoBackups) {
				t.Fatalf("err=%v, want ErrDecryptNoBackups", err)
			}
			if len(ui.statusTitles) == 0 {
				t.Fatal("ShowStatusResult was never called")
			}
			for _, got := range ui.statusTitles {
				if got != title {
					t.Fatalf("screenTitle=%q, want %q", got, title)
				}
			}
		})
	}
}

// abortingSelectionUI drives selectBackupCandidateWithUI to its empty-state
// branch and aborts at the status screen (Ctrl+C), returning ErrDecryptAborted
// from ShowStatusResult.
type abortingSelectionUI struct{}

func (f *abortingSelectionUI) RunTask(ctx context.Context, title, initialMessage string, run func(ctx context.Context, report ProgressReporter) error) error {
	return run(ctx, nil)
}
func (f *abortingSelectionUI) ShowMessage(ctx context.Context, title, message string) error {
	return nil
}
func (f *abortingSelectionUI) ShowStatusResult(ctx context.Context, screenTitle string, level HealthcheckSetupLevel, keyword, explanation string) error {
	return ErrDecryptAborted
}
func (f *abortingSelectionUI) ShowError(ctx context.Context, title, message string) error {
	return nil
}
func (f *abortingSelectionUI) SelectBackupSource(ctx context.Context, options []decryptPathOption) (decryptPathOption, error) {
	return options[len(options)-1], nil
}
func (f *abortingSelectionUI) SelectBackupCandidate(ctx context.Context, candidates []*backupCandidate) (*backupCandidate, error) {
	return nil, fmt.Errorf("unexpected SelectBackupCandidate call")
}

// A user abort (Ctrl+C) at the empty-state status screen must surface as
// ErrDecryptAborted, not the misleading ErrDecryptNoBackups that makes an
// explicit --decrypt/--restore exit 1 instead of a clean user-abort exit 0.
func TestSelectBackupCandidateSurfacesAbortAtEmptyState(t *testing.T) {
	cfg := &config.Config{BackupPath: t.TempDir()} // empty dir => exactly one path option => no backups
	logger := logging.New(types.LogLevelError, false)
	ui := &abortingSelectionUI{}

	_, err := selectBackupCandidateWithUI(context.Background(), ui, cfg, logger, "Decrypt", false)
	if !errors.Is(err, ErrDecryptAborted) {
		t.Fatalf("err=%v, want ErrDecryptAborted", err)
	}
}
