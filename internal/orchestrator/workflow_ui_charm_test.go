package orchestrator

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

func newCharmUITestHarness(t *testing.T) (*charmUIDriver, *charmWorkflowUI) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := newCharmUIDriver(t)
	d.start(ctx, shell.Config{
		AppName:  "ProxSave",
		Subtitle: "Decrypt Backup Workflow",
	})
	t.Cleanup(func() {
		_ = d.session.Close()
		cancel()
	})
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)
	return d, newCharmWorkflowUI(d.session, logger, ErrDecryptAborted)
}

func TestCharmWorkflowUISelectBackupSource(t *testing.T) {
	d, ui := newCharmUITestHarness(t)
	options := []decryptPathOption{
		{Label: "Primary backup path", Path: "/srv/backups"},
		{Label: "Cloud remote", Path: "remote:proxsave", IsRclone: true},
	}

	type result struct {
		opt decryptPathOption
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		opt, err := ui.SelectBackupSource(context.Background(), options)
		resCh <- result{opt, err}
	}()

	d.waitScreen("Select backup source")
	d.keys("down enter")
	res := <-resCh
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	if res.opt.Path != "remote:proxsave" || !res.opt.IsRclone {
		t.Fatalf("selected wrong option: %+v", res.opt)
	}

	// Esc aborts with the decrypt sentinel.
	go func() {
		_, err := ui.SelectBackupSource(context.Background(), options)
		resCh <- result{err: err}
	}()
	d.waitScreen("Select backup source")
	d.keys("esc")
	if res := <-resCh; !errors.Is(res.err, ErrDecryptAborted) {
		t.Fatalf("expected ErrDecryptAborted on esc, got %v", res.err)
	}
}

func TestBackupCandidateSelectorItemsAlignment(t *testing.T) {
	mkCand := func(host string) *backupCandidate {
		return &backupCandidate{Manifest: &backup.Manifest{
			CreatedAt:      time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
			Hostname:       host,
			EncryptionMode: "age",
			ProxmoxType:    "pve",
		}}
	}
	items := backupCandidateSelectorItems([]*backupCandidate{
		mkCand("node1"),
		mkCand("very-long-hostname"),
	})
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	for _, it := range items {
		if !strings.Contains(it.Label, "2025-01-02 03:04:05") {
			t.Errorf("row missing created timestamp: %q", it.Label)
		}
	}
	// Columns are padded to the widest hostname, so the field after the
	// hostname starts at the same offset in every row.
	col1 := strings.Index(items[0].Label, "node1")
	col2 := strings.Index(items[1].Label, "very-long-hostname")
	if col1 != col2 {
		t.Errorf("hostname column misaligned: %d vs %d\n%q\n%q", col1, col2, items[0].Label, items[1].Label)
	}
}

func TestCharmWorkflowUIPromptDestinationDir(t *testing.T) {
	d, ui := newCharmUITestHarness(t)

	type result struct {
		dir string
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		dir, err := ui.PromptDestinationDir(context.Background(), "./staging/../decrypt")
		resCh <- result{dir, err}
	}()
	d.waitScreen("Destination directory")
	d.keys("enter") // accept the prefilled default
	res := <-resCh
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	if res.dir != "decrypt" {
		t.Fatalf("expected cleaned default path %q, got %q", "decrypt", res.dir)
	}

	go func() {
		_, err := ui.PromptDestinationDir(context.Background(), "")
		resCh <- result{err: err}
	}()
	d.waitScreen("Destination directory")
	d.keys("esc")
	if res := <-resCh; !errors.Is(res.err, ErrDecryptAborted) {
		t.Fatalf("expected ErrDecryptAborted on esc, got %v", res.err)
	}

	// Empty default falls back to ./decrypt (prefilled, cleaned on submit).
	go func() {
		dir, err := ui.PromptDestinationDir(context.Background(), "")
		resCh <- result{dir, err}
	}()
	d.waitScreen("Destination directory")
	d.keys("enter")
	if res := <-resCh; res.err != nil || res.dir != "decrypt" {
		t.Fatalf("expected fallback default decrypt, got %+v", res)
	}
}

func TestCharmWorkflowUIResolveExistingPath(t *testing.T) {
	d, ui := newCharmUITestHarness(t)

	type result struct {
		decision ExistingPathDecision
		path     string
		err      error
	}
	resCh := make(chan result, 1)
	ask := func(failure string) {
		go func() {
			decision, path, err := ui.ResolveExistingPath(context.Background(), "/tmp/out.tar", "decrypted archive", failure)
			resCh <- result{decision, path, err}
		}()
	}

	// Overwrite.
	ask("")
	d.waitScreen("Existing file")
	d.keys("enter")
	if res := <-resCh; res.err != nil || res.decision != PathDecisionOverwrite {
		t.Fatalf("expected overwrite, got %+v", res)
	}

	// Use different path: same-path input is rejected, then a distinct one
	// is accepted and cleaned.
	ask("Failed to remove existing decrypted archive: permission denied")
	d.waitScreen("Existing file")
	d.waitOutput("permission denied") // failure from the previous round is shown
	d.keys("down enter")
	d.waitScreen("Choose destination path")
	d.keys("enter") // prefilled with the SAME path: validation must block
	d.waitOutput("must be different")
	d.typeText("2")
	d.keys("enter")
	if res := <-resCh; res.err != nil || res.decision != PathDecisionNewPath || res.path != "/tmp/out.tar2" {
		t.Fatalf("expected new path decision, got %+v", res)
	}

	// Esc at the new-path input falls back to cancel without a hard error.
	ask("")
	d.waitScreen("Existing file")
	d.keys("down enter")
	d.waitScreen("Choose destination path")
	d.keys("esc")
	if res := <-resCh; res.err != nil || res.decision != PathDecisionCancel {
		t.Fatalf("expected cancel fallback, got %+v", res)
	}

	// Esc at the decision picker aborts the flow.
	ask("")
	d.waitScreen("Existing file")
	d.keys("esc")
	if res := <-resCh; !errors.Is(res.err, ErrDecryptAborted) {
		t.Fatalf("expected ErrDecryptAborted, got %+v", res)
	}
}

func TestCharmWorkflowUIPromptDecryptSecret(t *testing.T) {
	d, ui := newCharmUITestHarness(t)

	type result struct {
		secret string
		err    error
	}
	resCh := make(chan result, 1)
	ask := func(previousError string) {
		go func() {
			secret, err := ui.PromptDecryptSecret(context.Background(), "backup.tar.xz.age", previousError)
			resCh <- result{secret, err}
		}()
	}

	// Surrounding spaces are preserved (parity with the tview prompt).
	ask("")
	d.waitScreen("Decrypt key")
	d.typeText("  s3cret ")
	d.keys("enter")
	if res := <-resCh; res.err != nil || res.secret != "  s3cret " {
		t.Fatalf("expected raw secret with spaces, got %+v", res)
	}

	// The previous failure is surfaced on the retry prompt.
	ask("Provided key or passphrase does not match this archive.")
	d.waitScreen("Decrypt key")
	d.waitOutput("does not match this archive")
	d.typeText("0")
	d.keys("enter")
	if res := <-resCh; !errors.Is(res.err, ErrDecryptAborted) {
		t.Fatalf("expected ErrDecryptAborted for 0, got %+v", res)
	}

	// Esc aborts.
	ask("")
	d.waitScreen("Decrypt key")
	d.keys("esc")
	if res := <-resCh; !errors.Is(res.err, ErrDecryptAborted) {
		t.Fatalf("expected ErrDecryptAborted on esc, got %+v", res)
	}
}

func TestCharmWorkflowUIRunTaskAndNotices(t *testing.T) {
	d, ui := newCharmUITestHarness(t)

	errCh := make(chan error, 1)
	go func() {
		errCh <- ui.RunTask(context.Background(), "Scanning backups", "Starting...", func(ctx context.Context, report ProgressReporter) error {
			report("Listing local path: /srv/backups")
			return nil
		})
	}()
	if err := <-errCh; err != nil {
		t.Fatalf("RunTask error: %v", err)
	}

	go func() {
		errCh <- ui.ShowError(context.Background(), "No backups found", "No backups found in /srv/backups.")
	}()
	d.waitScreen("No backups found")
	d.keys("enter")
	if err := <-errCh; err != nil {
		t.Fatalf("ShowError error: %v", err)
	}
}
