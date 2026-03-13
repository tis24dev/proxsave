package wizard

import (
	"context"
	"errors"
	"testing"

	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/tui"
)

func TestAgeSetupUIAdapterCollectRecipientDraftCancelMapsAbort(t *testing.T) {
	originalRunner := ageWizardRunner
	defer func() { ageWizardRunner = originalRunner }()

	ageWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		form, ok := focus.(*tview.Form)
		if !ok {
			t.Fatalf("expected *tview.Form focus, got %T", focus)
		}
		pressFormButton(t, form, "Cancel")
		return nil
	}

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
	originalRunner := ageWizardRunner
	defer func() { ageWizardRunner = originalRunner }()

	expected := errors.New("boom")
	ageWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		return expected
	}

	ui := NewAgeSetupUI("/etc/proxsave/config.env", "sig-test")
	if _, err := ui.CollectRecipientDraft(context.Background(), "/tmp/recipient.age"); !errors.Is(err, expected) {
		t.Fatalf("err=%v; want %v", err, expected)
	}
}
