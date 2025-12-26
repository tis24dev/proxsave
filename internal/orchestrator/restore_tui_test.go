package orchestrator

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/tui"
	"github.com/tis24dev/proxsave/internal/tui/components"
)

func TestFilterAndSortCategoriesForSystem(t *testing.T) {
	categories := []Category{
		{Name: "Common", Type: CategoryTypeCommon},
		{Name: "PBS", Type: CategoryTypePBS},
		{Name: "Alpha", Type: CategoryTypePVE},
		{Name: "Beta", Type: CategoryTypePVE},
	}

	for _, tc := range []struct {
		name       string
		systemType SystemType
		wantNames  []string
	}{
		{name: "pve", systemType: SystemTypePVE, wantNames: []string{"Alpha", "Beta", "Common"}},
		{name: "pbs", systemType: SystemTypePBS, wantNames: []string{"PBS", "Common"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := filterAndSortCategoriesForSystem(categories, tc.systemType)
			if len(got) != len(tc.wantNames) {
				t.Fatalf("unexpected count: %d", len(got))
			}
			for i, want := range tc.wantNames {
				if got[i].Name != want {
					t.Fatalf("position %d: got %q, want %q", i, got[i].Name, want)
				}
			}
		})
	}
}

func TestBuildRestorePlanText(t *testing.T) {
	config := &SelectiveRestoreConfig{
		Mode:       RestoreModeCustom,
		SystemType: SystemTypePVE,
		SelectedCategories: []Category{
			{Name: "Alpha", Description: "First", Paths: []string{"./etc/alpha"}},
			{Name: "Beta", Description: "Second", Paths: []string{"./var/beta"}},
		},
	}

	text := buildRestorePlanText(config)

	if !strings.Contains(text, "CUSTOM selection (2 categories)") {
		t.Fatalf("missing mode line: %s", text)
	}
	if !strings.Contains(text, "System type:  Proxmox Virtual Environment (PVE)") {
		t.Fatalf("missing system type line: %s", text)
	}
	if !strings.Contains(text, "1. Alpha") || !strings.Contains(text, "2. Beta") {
		t.Fatalf("missing category entries: %s", text)
	}
	alphaIndex := strings.Index(text, "/etc/alpha")
	betaIndex := strings.Index(text, "/var/beta")
	if alphaIndex == -1 || betaIndex == -1 {
		t.Fatalf("missing paths: %s", text)
	}
	if alphaIndex > betaIndex {
		t.Fatalf("paths not sorted: %d vs %d", alphaIndex, betaIndex)
	}
	if !strings.Contains(text, "Existing files at these locations will be OVERWRITTEN") {
		t.Fatalf("missing warning text")
	}
}

func TestBuildRestoreWizardPageReturnsFlex(t *testing.T) {
	content := tview.NewBox()
	page := buildRestoreWizardPage("Title", "/etc/proxsave/backup.env", "sig", content)
	if page == nil {
		t.Fatalf("expected non-nil page")
	}
	if _, ok := page.(*tview.Flex); !ok {
		t.Fatalf("expected *tview.Flex, got %T", page)
	}
}

func TestPromptCompatibilityTUIUsesWarningText(t *testing.T) {
	restore := stubPromptYesNo(func(title, configPath, buildSig, message, yesLabel, noLabel string) (bool, error) {
		if title != "Compatibility warning" {
			t.Fatalf("unexpected title %q", title)
		}
		if !strings.Contains(message, "boom") {
			t.Fatalf("missing error text: %s", message)
		}
		if yesLabel != "Continue anyway" || noLabel != "Abort restore" {
			t.Fatalf("unexpected button labels %q/%q", yesLabel, noLabel)
		}
		return true, nil
	})
	defer restore()

	ok, err := promptCompatibilityTUI("cfg", "sig", errors.New("boom"))
	if err != nil || !ok {
		t.Fatalf("promptCompatibilityTUI returned %v, %v", ok, err)
	}
}

func TestPromptContinueWithoutSafetyBackupTUI(t *testing.T) {
	restore := stubPromptYesNo(func(title, configPath, buildSig, message, yesLabel, noLabel string) (bool, error) {
		if title != "Safety backup failed" {
			t.Fatalf("unexpected title %q", title)
		}
		if !strings.Contains(message, "failure") {
			t.Fatalf("missing cause text: %s", message)
		}
		return false, nil
	})
	defer restore()

	ok, err := promptContinueWithoutSafetyBackupTUI("cfg", "sig", errors.New("failure"))
	if err != nil {
		t.Fatalf("promptContinueWithoutSafetyBackupTUI error: %v", err)
	}
	if ok {
		t.Fatalf("expected false decision")
	}
}

func TestPromptContinueWithPBSServicesTUI(t *testing.T) {
	restore := stubPromptYesNo(func(title, configPath, buildSig, message, yesLabel, noLabel string) (bool, error) {
		if title != "PBS services running" {
			t.Fatalf("unexpected title %q", title)
		}
		if yesLabel != "Continue restore" || noLabel != "Abort restore" {
			t.Fatalf("unexpected button labels %q/%q", yesLabel, noLabel)
		}
		return true, nil
	})
	defer restore()

	ok, err := promptContinueWithPBSServicesTUI("cfg", "sig")
	if err != nil || !ok {
		t.Fatalf("promptContinueWithPBSServicesTUI returned %v, %v", ok, err)
	}
}

func TestConfirmOverwriteTUI(t *testing.T) {
	restore := stubPromptYesNo(func(title, configPath, buildSig, message, yesLabel, noLabel string) (bool, error) {
		if title != "Confirm overwrite" {
			t.Fatalf("unexpected title %q", title)
		}
		if yesLabel != "Overwrite and restore" || noLabel != "Cancel" {
			t.Fatalf("unexpected labels %q/%q", yesLabel, noLabel)
		}
		if !strings.Contains(message, "overwrite existing configuration files") {
			t.Fatalf("missing warning text: %s", message)
		}
		return true, nil
	})
	defer restore()

	ok, err := confirmOverwriteTUI("cfg", "sig")
	if err != nil || !ok {
		t.Fatalf("confirmOverwriteTUI returned %v, %v", ok, err)
	}
}

func TestShowRestoreErrorModalAddsWizardPage(t *testing.T) {
	app := tui.NewApp()
	pages := tview.NewPages()

	showRestoreErrorModal(app, pages, "cfg", "sig", "boom", nil)

	if !pages.HasPage(restoreErrorModalPage) {
		t.Fatalf("expected %q page to be present", restoreErrorModalPage)
	}
	page := pages.GetPage(restoreErrorModalPage)
	flex, ok := page.(*tview.Flex)
	if !ok {
		t.Fatalf("expected *tview.Flex, got %T", page)
	}
	content := flex.GetItem(3)
	modal, ok := content.(*tview.Modal)
	if !ok {
		t.Fatalf("expected *tview.Modal content, got %T", content)
	}
	if modal.GetTitle() != " Restore Error " {
		t.Fatalf("modal title=%q; want %q", modal.GetTitle(), " Restore Error ")
	}
}

func TestShowRestoreCandidatePageAddsCandidatesPageWithItems(t *testing.T) {
	app := tui.NewApp()
	pages := tview.NewPages()

	now := time.Unix(1700000000, 0)
	candidates := []*decryptCandidate{
		{
			Manifest: &backup.Manifest{
				CreatedAt:       now,
				EncryptionMode:  "age",
				ProxmoxTargets:  []string{"pve"},
				ProxmoxVersion:  "8.1",
				CompressionType: "zstd",
				ClusterMode:     "standalone",
				ScriptVersion:   "1.0.0",
			},
		},
		{
			Manifest: &backup.Manifest{
				CreatedAt:       now.Add(-time.Hour),
				EncryptionMode:  "age",
				ProxmoxTargets:  []string{"pbs"},
				CompressionType: "xz",
				ScriptVersion:   "1.0.0",
			},
		},
	}

	showRestoreCandidatePage(app, pages, candidates, "cfg", "sig", func(*decryptCandidate) {}, func() {})

	if !pages.HasPage("candidates") {
		t.Fatalf("expected candidates page to be present")
	}
	page := pages.GetPage("candidates")
	flex, ok := page.(*tview.Flex)
	if !ok {
		t.Fatalf("expected *tview.Flex, got %T", page)
	}
	content := flex.GetItem(3)
	form, ok := content.(*tview.Form)
	if !ok {
		t.Fatalf("expected *tview.Form content, got %T", content)
	}
	if form.GetFormItemCount() != 1 {
		t.Fatalf("form items=%d; want 1", form.GetFormItemCount())
	}
	listItem, ok := form.GetFormItem(0).(*components.ListFormItem)
	if !ok {
		t.Fatalf("expected *components.ListFormItem, got %T", form.GetFormItem(0))
	}
	if got := listItem.GetItemCount(); got != len(candidates) {
		t.Fatalf("list items=%d; want %d", got, len(candidates))
	}
}

func stubPromptYesNo(fn func(title, configPath, buildSig, message, yesLabel, noLabel string) (bool, error)) func() {
	orig := promptYesNoTUIFunc
	promptYesNoTUIFunc = fn
	return func() { promptYesNoTUIFunc = orig }
}
