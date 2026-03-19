package orchestrator

import (
	"context"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestPromptYesNoTUI_YesReturnsTrue(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyEnter})

	ok, err := promptYesNoTUI(context.Background(), "Title", "/tmp/config.env", "sig", "Message", "Yes", "No")
	if err != nil {
		t.Fatalf("promptYesNoTUI error: %v", err)
	}
	if !ok {
		t.Fatalf("ok=%v; want true", ok)
	}
}

func TestPromptYesNoTUI_NoReturnsFalse(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyTab, tcell.KeyEnter})

	ok, err := promptYesNoTUI(context.Background(), "Title", "/tmp/config.env", "sig", "Message", "Yes", "No")
	if err != nil {
		t.Fatalf("promptYesNoTUI error: %v", err)
	}
	if ok {
		t.Fatalf("ok=%v; want false", ok)
	}
}

func TestShowRestorePlanTUI_ContinueReturnsNil(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyEnter})

	cfg := &SelectiveRestoreConfig{
		Mode:       RestoreModeBase,
		SystemType: SystemTypePVE,
		SelectedCategories: []Category{
			{Name: "Alpha", Type: CategoryTypePVE, Description: "First", Paths: []string{"./etc/alpha"}},
		},
	}
	if err := showRestorePlanTUI(context.Background(), cfg, "/tmp/config.env", "sig"); err != nil {
		t.Fatalf("showRestorePlanTUI error: %v", err)
	}
}

func TestShowRestorePlanTUI_CancelReturnsAborted(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyTab, tcell.KeyEnter})

	cfg := &SelectiveRestoreConfig{
		Mode:       RestoreModeBase,
		SystemType: SystemTypePVE,
		SelectedCategories: []Category{
			{Name: "Alpha", Type: CategoryTypePVE, Description: "First", Paths: []string{"./etc/alpha"}},
		},
	}
	err := showRestorePlanTUI(context.Background(), cfg, "/tmp/config.env", "sig")
	if err != ErrRestoreAborted {
		t.Fatalf("err=%v; want %v", err, ErrRestoreAborted)
	}
}

func TestConfirmRestoreTUI_ConfirmedAndOverwriteReturnsTrue(t *testing.T) {
	expectedCtx := context.WithValue(context.Background(), struct{}{}, "confirm-restore")
	restore := stubPromptYesNo(func(ctx context.Context, title, configPath, buildSig, message, yesLabel, noLabel string) (bool, error) {
		if ctx != expectedCtx {
			t.Fatalf("stub received unexpected context: got %v want %v", ctx, expectedCtx)
		}
		return true, nil
	})
	defer restore()

	withSimApp(t, []tcell.Key{tcell.KeyEnter})

	ok, err := confirmRestoreTUI(expectedCtx, "/tmp/config.env", "sig")
	if err != nil {
		t.Fatalf("confirmRestoreTUI error: %v", err)
	}
	if !ok {
		t.Fatalf("ok=%v; want true", ok)
	}
}

func TestConfirmRestoreTUI_OverwriteDeclinedReturnsFalse(t *testing.T) {
	expectedCtx := context.WithValue(context.Background(), struct{}{}, "overwrite-declined")
	restore := stubPromptYesNo(func(ctx context.Context, title, configPath, buildSig, message, yesLabel, noLabel string) (bool, error) {
		if ctx != expectedCtx {
			t.Fatalf("stub received unexpected context: got %v want %v", ctx, expectedCtx)
		}
		return false, nil
	})
	defer restore()

	withSimApp(t, []tcell.Key{tcell.KeyEnter})

	ok, err := confirmRestoreTUI(expectedCtx, "/tmp/config.env", "sig")
	if err != nil {
		t.Fatalf("confirmRestoreTUI error: %v", err)
	}
	if ok {
		t.Fatalf("ok=%v; want false", ok)
	}
}

func TestSelectCategoriesTUI_SelectsAtLeastOne(t *testing.T) {
	available := []Category{
		{Name: "Alpha", Type: CategoryTypePVE},
	}
	withSimApp(t, []tcell.Key{
		tcell.KeyEnter, // open dropdown
		tcell.KeyDown,  // select "Yes"
		tcell.KeyEnter, // close dropdown with selection
		tcell.KeyTab,   // Back
		tcell.KeyTab,   // Continue
		tcell.KeyEnter, // submit
	})

	got, err := selectCategoriesTUI(context.Background(), available, SystemTypePVE, "/tmp/config.env", "sig")
	if err != nil {
		t.Fatalf("selectCategoriesTUI error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Alpha" {
		t.Fatalf("got=%v; want [Alpha]", got)
	}
}

func TestSelectCategoriesTUI_BackReturnsErrRestoreBackToMode(t *testing.T) {
	available := []Category{
		{Name: "Alpha", Type: CategoryTypePVE},
	}
	withSimApp(t, []tcell.Key{tcell.KeyTab, tcell.KeyEnter})

	_, err := selectCategoriesTUI(context.Background(), available, SystemTypePVE, "/tmp/config.env", "sig")
	if err != errRestoreBackToMode {
		t.Fatalf("err=%v; want %v", err, errRestoreBackToMode)
	}
}

func TestSelectCategoriesTUI_CancelReturnsAborted(t *testing.T) {
	available := []Category{
		{Name: "Alpha", Type: CategoryTypePVE},
	}
	withSimApp(t, []tcell.Key{
		tcell.KeyTab, // Back
		tcell.KeyTab, // Continue
		tcell.KeyTab, // Cancel
		tcell.KeyEnter,
	})

	_, err := selectCategoriesTUI(context.Background(), available, SystemTypePVE, "/tmp/config.env", "sig")
	if err != ErrRestoreAborted {
		t.Fatalf("err=%v; want %v", err, ErrRestoreAborted)
	}
}
