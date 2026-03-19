package wizard

import (
	"context"
	"errors"
	"testing"

	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/tui"
)

func registerAgeWizardRunner(t *testing.T, runner func(app *tui.App, root, focus tview.Primitive) error) {
	t.Helper()

	originalRunner := ageWizardRunner
	ageWizardRunner = runner
	t.Cleanup(func() {
		ageWizardRunner = originalRunner
	})
}

func TestAgeSetupUIAdapterCollectRecipientDraftCancelMapsAbort(t *testing.T) {
	registerAgeWizardRunner(t, func(app *tui.App, root, focus tview.Primitive) error {
		form, ok := focus.(*tview.Form)
		if !ok {
			t.Fatalf("expected *tview.Form focus, got %T", focus)
		}
		pressFormButton(t, form, "Cancel")
		return nil
	})

	ui := NewAgeSetupUI("/etc/proxsave/config.env", "sig-test")
	draft, err := ui.CollectRecipientDraft(context.Background(), "/tmp/recipient.age")
	if !errors.Is(err, orchestrator.ErrAgeRecipientSetupAborted) {
		t.Fatalf("err=%v; want %v", err, orchestrator.ErrAgeRecipientSetupAborted)
	}
	if draft != nil {
		t.Fatalf("draft=%+v; want nil", draft)
	}
}

func TestAgeSetupUIAdapterCollectRecipientDraftRunnerError(t *testing.T) {
	expected := errors.New("boom")
	registerAgeWizardRunner(t, func(app *tui.App, root, focus tview.Primitive) error {
		return expected
	})

	ui := NewAgeSetupUI("/etc/proxsave/config.env", "sig-test")
	if _, err := ui.CollectRecipientDraft(context.Background(), "/tmp/recipient.age"); !errors.Is(err, expected) {
		t.Fatalf("err=%v; want %v", err, expected)
	}
}

func TestAgeSetupUIAdapterConfirmOverwriteExistingRecipientCanceledContext(t *testing.T) {
	registerAgeWizardRunner(t, func(app *tui.App, root, focus tview.Primitive) error {
		t.Fatal("ageWizardRunner should not be called when context is already canceled")
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ui := NewAgeSetupUI("/etc/proxsave/config.env", "sig-test")
	confirmed, err := ui.ConfirmOverwriteExistingRecipient(ctx, "/tmp/recipient.age")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v; want %v", err, context.Canceled)
	}
	if confirmed {
		t.Fatalf("confirmed=%t; want false", confirmed)
	}
}

func TestAgeSetupUIAdapterConfirmAddAnotherRecipientCanceledContext(t *testing.T) {
	registerAgeWizardRunner(t, func(app *tui.App, root, focus tview.Primitive) error {
		t.Fatal("ageWizardRunner should not be called when context is already canceled")
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ui := NewAgeSetupUI("/etc/proxsave/config.env", "sig-test")
	confirmed, err := ui.ConfirmAddAnotherRecipient(ctx, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v; want %v", err, context.Canceled)
	}
	if confirmed {
		t.Fatalf("confirmed=%t; want false", confirmed)
	}
}
