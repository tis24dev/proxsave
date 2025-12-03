package orchestrator

import (
	"errors"
	"strings"
	"testing"

	"github.com/rivo/tview"
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

func stubPromptYesNo(fn func(title, configPath, buildSig, message, yesLabel, noLabel string) (bool, error)) func() {
	orig := promptYesNoTUIFunc
	promptYesNoTUIFunc = fn
	return func() { promptYesNoTUIFunc = orig }
}
