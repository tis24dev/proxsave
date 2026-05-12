package wizard

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
)

func TestRunPostInstallAuditWizard_PassesContextToRunner(t *testing.T) {
	origRunner := postInstallAuditWizardRunner
	t.Cleanup(func() {
		postInstallAuditWizardRunner = origRunner
	})

	ctx := t.Context()
	postInstallAuditWizardRunner = func(gotCtx context.Context, app *tui.App, root, focus tview.Primitive) error {
		if gotCtx != ctx {
			t.Fatalf("got context %p, want %p", gotCtx, ctx)
		}
		return nil
	}

	result, err := RunPostInstallAuditWizard(ctx, "/tmp/proxsave", "/tmp/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunPostInstallAuditWizard error: %v", err)
	}
	if result.Ran {
		t.Fatalf("expected Ran=false when runner exits without selecting an action")
	}
}

func TestShowAuditReviewDisableAllAppliesAllSuggestions(t *testing.T) {
	configPath := writePostInstallAuditConfig(t,
		"BACKUP_CLUSTER_CONFIG=true\n"+
			"BACKUP_PBS_DATASTORE=true\n"+
			"BACKUP_FIREWALL_RULES=true\n")
	suggestions := []PostInstallAuditSuggestion{
		{Key: "BACKUP_PBS_DATASTORE", Messages: []string{"pbs missing; set BACKUP_PBS_DATASTORE=false to disable"}},
		{Key: "BACKUP_CLUSTER_CONFIG", Messages: []string{"cluster missing; set BACKUP_CLUSTER_CONFIG=false to disable"}},
	}

	_, pages, _, buttons, applied := buildPostInstallAuditReview(t, configPath, suggestions)
	pressFormButton(t, buttons, "Disable all")

	values := readPostInstallAuditConfigValues(t, configPath)
	if got := values["BACKUP_CLUSTER_CONFIG"]; got != "false" {
		t.Fatalf("BACKUP_CLUSTER_CONFIG=%q, want false", got)
	}
	if got := values["BACKUP_PBS_DATASTORE"]; got != "false" {
		t.Fatalf("BACKUP_PBS_DATASTORE=%q, want false", got)
	}
	if got := values["BACKUP_FIREWALL_RULES"]; got != "true" {
		t.Fatalf("BACKUP_FIREWALL_RULES=%q, want true", got)
	}

	wantApplied := []string{"BACKUP_CLUSTER_CONFIG", "BACKUP_PBS_DATASTORE"}
	if !reflect.DeepEqual(*applied, wantApplied) {
		t.Fatalf("applied=%v, want %v", *applied, wantApplied)
	}
	assertPostInstallAuditDonePage(t, pages)
}

func TestShowAuditReviewDisableSelectedAppliesSelectedSuggestion(t *testing.T) {
	configPath := writePostInstallAuditConfig(t,
		"BACKUP_CLUSTER_CONFIG=true\n"+
			"BACKUP_PBS_DATASTORE=true\n")
	suggestions := []PostInstallAuditSuggestion{
		{Key: "BACKUP_CLUSTER_CONFIG", Messages: []string{"cluster missing; set BACKUP_CLUSTER_CONFIG=false to disable"}},
		{Key: "BACKUP_PBS_DATASTORE", Messages: []string{"pbs missing; set BACKUP_PBS_DATASTORE=false to disable"}},
	}

	_, pages, list, buttons, applied := buildPostInstallAuditReview(t, configPath, suggestions)
	list.InputHandler()(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone), func(tview.Primitive) {})
	main, _ := list.GetItemText(0)
	if !strings.Contains(main, "[x] BACKUP_CLUSTER_CONFIG") {
		t.Fatalf("selected list item text=%q, want checked BACKUP_CLUSTER_CONFIG", main)
	}

	pressFormButton(t, buttons, "Disable selected")

	values := readPostInstallAuditConfigValues(t, configPath)
	if got := values["BACKUP_CLUSTER_CONFIG"]; got != "false" {
		t.Fatalf("BACKUP_CLUSTER_CONFIG=%q, want false", got)
	}
	if got := values["BACKUP_PBS_DATASTORE"]; got != "true" {
		t.Fatalf("BACKUP_PBS_DATASTORE=%q, want true", got)
	}

	wantApplied := []string{"BACKUP_CLUSTER_CONFIG"}
	if !reflect.DeepEqual(*applied, wantApplied) {
		t.Fatalf("applied=%v, want %v", *applied, wantApplied)
	}
	assertPostInstallAuditDonePage(t, pages)
}

func TestShowAuditReviewNavigationMovesBetweenListAndActions(t *testing.T) {
	configPath := writePostInstallAuditConfig(t, "BACKUP_CLUSTER_CONFIG=true\nBACKUP_PBS_DATASTORE=true\n")
	suggestions := []PostInstallAuditSuggestion{
		{Key: "BACKUP_CLUSTER_CONFIG", Messages: []string{"cluster missing"}},
		{Key: "BACKUP_PBS_DATASTORE", Messages: []string{"pbs missing"}},
	}

	app, _, list, buttons, _ := buildPostInstallAuditReview(t, configPath, suggestions)
	setFocus := func(p tview.Primitive) { app.SetFocus(p) }

	if !list.HasFocus() {
		t.Fatalf("expected suggestions list to start focused")
	}

	list.InputHandler()(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), setFocus)
	_, buttonIndex := buttons.GetFocusedItemIndex()
	if buttonIndex != 0 {
		t.Fatalf("after list TAB, focused button=%d, want 0", buttonIndex)
	}

	buttons.InputHandler()(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone), setFocus)
	_, buttonIndex = buttons.GetFocusedItemIndex()
	if buttonIndex != 1 {
		t.Fatalf("after button RIGHT, focused button=%d, want 1", buttonIndex)
	}

	buttons.InputHandler()(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone), setFocus)
	if !list.HasFocus() {
		t.Fatalf("expected button UP to return focus to suggestions list")
	}

	list.SetCurrentItem(len(suggestions) - 1)
	list.InputHandler()(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone), setFocus)
	_, buttonIndex = buttons.GetFocusedItemIndex()
	if buttonIndex < 0 {
		t.Fatalf("expected list DOWN at the last item to focus an action button")
	}

	buttons.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), setFocus)
	if !list.HasFocus() {
		t.Fatalf("expected button ESC to return focus to suggestions list")
	}
}

func buildPostInstallAuditReview(t *testing.T, configPath string, suggestions []PostInstallAuditSuggestion) (*tui.App, *tview.Pages, *tview.List, *tview.Form, *[]string) {
	t.Helper()

	app := tui.NewApp()
	pages := tview.NewPages()
	applied := []string{}
	showAuditReview(app, pages, configPath, suggestions, &applied)
	list, buttons := extractPostInstallAuditReviewControls(t, pages)
	return app, pages, list, buttons, &applied
}

func extractPostInstallAuditReviewControls(t *testing.T, pages *tview.Pages) (*tview.List, *tview.Form) {
	t.Helper()

	page := pages.GetPage("review")
	if page == nil {
		t.Fatalf("review page not found")
	}
	review, ok := page.(*tview.Flex)
	if !ok {
		t.Fatalf("review page type=%T, want *tview.Flex", page)
	}
	if review.GetItemCount() < 3 {
		t.Fatalf("review item count=%d, want at least 3", review.GetItemCount())
	}
	mid, ok := review.GetItem(1).(*tview.Flex)
	if !ok {
		t.Fatalf("review middle item type=%T, want *tview.Flex", review.GetItem(1))
	}
	list, ok := mid.GetItem(0).(*tview.List)
	if !ok {
		t.Fatalf("suggestions item type=%T, want *tview.List", mid.GetItem(0))
	}
	buttons, ok := review.GetItem(2).(*tview.Form)
	if !ok {
		t.Fatalf("actions item type=%T, want *tview.Form", review.GetItem(2))
	}
	return list, buttons
}

func writePostInstallAuditConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func readPostInstallAuditConfigValues(t *testing.T, configPath string) map[string]string {
	t.Helper()

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return parseEnvTemplate(string(content))
}

func assertPostInstallAuditDonePage(t *testing.T, pages *tview.Pages) {
	t.Helper()

	name, primitive := pages.GetFrontPage()
	if name != "done" {
		t.Fatalf("front page=%q, want done", name)
	}
	if _, ok := primitive.(*tview.Modal); !ok {
		t.Fatalf("done page type=%T, want *tview.Modal", primitive)
	}
}
