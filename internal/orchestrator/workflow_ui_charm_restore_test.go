package orchestrator

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

func newCharmRestoreUITestHarness(t *testing.T) (*charmUIDriver, *charmWorkflowUI) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := newCharmUIDriver(t)
	d.start(ctx, shell.Config{
		AppName:  "ProxSave",
		Subtitle: restoreWizardSubtitle,
	})
	t.Cleanup(func() {
		_ = d.session.Close()
		cancel()
	})
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)
	return d, newCharmWorkflowUI(d.session, logger, ErrRestoreAborted)
}

func testCategories() []Category {
	return []Category{
		{ID: "network", Name: "Network", Description: "interfaces", Type: CategoryTypeCommon},
		{ID: "pve_storage", Name: "Storage", Description: "storage.cfg", Type: CategoryTypePVE},
		{ID: "ssl", Name: "SSL", Description: "certificates", Type: CategoryTypeCommon},
	}
}

func TestCharmSelectRestoreMode(t *testing.T) {
	d, ui := newCharmRestoreUITestHarness(t)
	ui.selectedBackupSummary = "node1 2025-01-02"

	type result struct {
		mode RestoreMode
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		mode, err := ui.SelectRestoreMode(context.Background(), SystemTypePVE)
		resCh <- result{mode, err}
	}()
	d.waitScreen("Select restore mode")
	d.waitOutput("Selected backup: node1 2025-01-02")
	d.keys("down down enter")
	if res := <-resCh; res.err != nil || res.mode != RestoreModeBase {
		t.Fatalf("expected base mode, got %+v", res)
	}

	go func() {
		mode, err := ui.SelectRestoreMode(context.Background(), SystemTypePVE)
		resCh <- result{mode, err}
	}()
	d.waitScreen("Select restore mode")
	d.keys("esc")
	if res := <-resCh; !errors.Is(res.err, ErrRestoreAborted) {
		t.Fatalf("expected ErrRestoreAborted on esc, got %+v", res)
	}
}

func TestCharmSelectCategories(t *testing.T) {
	d, ui := newCharmRestoreUITestHarness(t)

	type result struct {
		cats []Category
		err  error
	}
	resCh := make(chan result, 1)
	ask := func() {
		go func() {
			cats, err := ui.SelectCategories(context.Background(), testCategories(), SystemTypePVE)
			resCh <- result{cats, err}
		}()
	}

	// Nothing selected: Enter is blocked with a validation message; then
	// toggle two categories and confirm.
	ask()
	d.waitScreen("Select restore categories")
	d.keys("enter")
	d.waitOutput("Select at least 1")
	d.keys("space down space enter")
	res := <-resCh
	if res.err != nil || len(res.cats) != 2 {
		t.Fatalf("expected 2 categories, got %+v", res)
	}
	// PVE categories sort before common ones.
	if res.cats[0].Name != "Storage" || res.cats[1].Name != "Network" {
		t.Fatalf("unexpected selection order: %+v", res.cats)
	}

	// Esc goes back to mode selection (the tview Back button contract).
	ask()
	d.waitScreen("Select restore categories")
	d.keys("esc")
	if res := <-resCh; !errors.Is(res.err, errRestoreBackToMode) {
		t.Fatalf("expected errRestoreBackToMode, got %+v", res)
	}

	// Ctrl+C aborts hard.
	ask()
	d.waitScreen("Select restore categories")
	d.keys("ctrl+c")
	if res := <-resCh; !errors.Is(res.err, ErrRestoreAborted) {
		t.Fatalf("expected ErrRestoreAborted on ctrl+c, got %+v", res)
	}

	// No relevant categories at all is an error, no screen involved.
	if _, err := ui.SelectCategories(context.Background(), nil, SystemTypePVE); err == nil {
		t.Fatal("expected error for empty category set")
	}
}

func TestCharmSelectPBSRestoreBehavior(t *testing.T) {
	d, ui := newCharmRestoreUITestHarness(t)

	type result struct {
		behavior PBSRestoreBehavior
		err      error
	}
	resCh := make(chan result, 1)
	go func() {
		behavior, err := ui.SelectPBSRestoreBehavior(context.Background())
		resCh <- result{behavior, err}
	}()
	d.waitScreen("PBS restore behavior")
	d.keys("down enter")
	if res := <-resCh; res.err != nil || res.behavior != PBSRestoreBehaviorClean {
		t.Fatalf("expected clean behavior, got %+v", res)
	}
}

func TestCharmShowRestorePlan(t *testing.T) {
	d, ui := newCharmRestoreUITestHarness(t)
	cfg := &SelectiveRestoreConfig{
		Mode:       RestoreModeCustom,
		SystemType: SystemTypePVE,
		SelectedCategories: []Category{
			{Name: "Alpha", Description: "First", Paths: []string{"./etc/alpha"}},
		},
	}

	errCh := make(chan error, 1)
	go func() { errCh <- ui.ShowRestorePlan(context.Background(), cfg) }()
	d.waitScreen("Restore plan")
	d.waitOutput("Restore mode:") // plan body (the styled title replaces the old ASCII banner)
	d.keys("enter")
	if err := <-errCh; err != nil {
		t.Fatalf("continue must return nil, got %v", err)
	}

	go func() { errCh <- ui.ShowRestorePlan(context.Background(), cfg) }()
	d.waitScreen("Restore plan")
	d.keys("esc")
	if err := <-errCh; !errors.Is(err, ErrRestoreAborted) {
		t.Fatalf("esc on the plan must abort, got %v", err)
	}

	if err := ui.ShowRestorePlan(context.Background(), nil); err == nil {
		t.Fatal("nil config must error")
	}
}

func TestCharmConfirmRestoreTwoStage(t *testing.T) {
	d, ui := newCharmRestoreUITestHarness(t)

	type result struct {
		ok  bool
		err error
	}
	resCh := make(chan result, 1)
	ask := func() {
		go func() {
			ok, err := ui.ConfirmRestore(context.Background())
			resCh <- result{ok, err}
		}()
	}

	// Full accept: RESTORE (default), then deliberate navigation to the
	// overwrite button (stage 2 default is Cancel).
	ask()
	d.waitScreen("Confirm restore")
	d.keys("enter")
	d.waitScreen("Confirm overwrite")
	d.keys("left enter")
	if res := <-resCh; res.err != nil || !res.ok {
		t.Fatalf("expected confirmed restore, got %+v", res)
	}

	// Declining stage 2 is a plain false, not an abort.
	ask()
	d.waitScreen("Confirm restore")
	d.keys("enter")
	d.waitScreen("Confirm overwrite")
	d.keys("enter") // bare Enter picks the safe Cancel default
	if res := <-resCh; res.err != nil || res.ok {
		t.Fatalf("stage-2 decline must be (false, nil), got %+v", res)
	}

	// Cancelling stage 1 aborts.
	ask()
	d.waitScreen("Confirm restore")
	d.keys("esc")
	if res := <-resCh; !errors.Is(res.err, ErrRestoreAborted) {
		t.Fatalf("stage-1 cancel must abort, got %+v", res)
	}
}

func TestCharmSelectClusterRestoreMode(t *testing.T) {
	d, ui := newCharmRestoreUITestHarness(t)

	type result struct {
		mode ClusterRestoreMode
		err  error
	}
	resCh := make(chan result, 1)
	ask := func() {
		go func() {
			mode, err := ui.SelectClusterRestoreMode(context.Background())
			resCh <- result{mode, err}
		}()
	}

	ask()
	d.waitScreen("Cluster restore mode")
	d.keys("enter")
	if res := <-resCh; res.err != nil || res.mode != ClusterRestoreSafe {
		t.Fatalf("expected SAFE, got %+v", res)
	}

	// Explicit Exit row aborts with a nil error (the engine decides).
	ask()
	d.waitScreen("Cluster restore mode")
	d.keys("down down enter")
	if res := <-resCh; res.err != nil || res.mode != ClusterRestoreAbort {
		t.Fatalf("expected abort choice, got %+v", res)
	}

	ask()
	d.waitScreen("Cluster restore mode")
	d.keys("esc")
	if res := <-resCh; !errors.Is(res.err, ErrRestoreAborted) {
		t.Fatalf("expected ErrRestoreAborted on esc, got %+v", res)
	}
}

func TestCharmSelectExportNode(t *testing.T) {
	d, ui := newCharmRestoreUITestHarness(t)
	exportRoot := t.TempDir()

	type result struct {
		node string
		err  error
	}
	resCh := make(chan result, 1)
	ask := func() {
		go func() {
			node, err := ui.SelectExportNode(context.Background(), exportRoot, "pve1", []string{"pve1", "pve2"})
			resCh <- result{node, err}
		}()
	}

	ask()
	d.waitScreen("Select export node")
	d.waitOutput("Current node: pve1")
	d.keys("down enter")
	if res := <-resCh; res.err != nil || res.node != "pve2" {
		t.Fatalf("expected pve2, got %+v", res)
	}

	// The trailing "Skip" row resolves the empty node.
	ask()
	d.waitScreen("Select export node")
	d.keys("down down enter")
	if res := <-resCh; res.err != nil || res.node != "" {
		t.Fatalf("expected skip, got %+v", res)
	}

	// Esc is also a skip, not an abort (tview Cancel parity).
	ask()
	d.waitScreen("Select export node")
	d.keys("esc")
	if res := <-resCh; res.err != nil || res.node != "" {
		t.Fatalf("expected skip on esc, got %+v", res)
	}
}

func TestCharmConfirmFstabMergeRecommendedText(t *testing.T) {
	d, ui := newCharmRestoreUITestHarness(t)

	type result struct {
		ok  bool
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		ok, err := ui.ConfirmFstabMerge(context.Background(), "Fstab merge", "3 new mounts detected.", 0, true)
		resCh <- result{ok, err}
	}()
	d.waitScreen("Fstab merge")
	d.waitOutput("Recommended action: Apply")
	d.waitOutput("3 new mounts detected.")
	d.keys("enter") // defaultYes=true: bare Enter applies
	if res := <-resCh; res.err != nil || !res.ok {
		t.Fatalf("expected apply via default, got %+v", res)
	}

	// Second, near-identical screen: the diff renderer may repaint only the
	// changed cells, so the full "Recommended action: Skip" phrase is not
	// guaranteed to appear contiguously in the output stream. The default
	// semantics (bare Enter skips) are the contract under test here.
	go func() {
		ok, err := ui.ConfirmFstabMerge(context.Background(), "Fstab merge", "", 0, false)
		resCh <- result{ok, err}
	}()
	d.waitScreen("Fstab merge")
	d.keys("enter") // defaultYes=false: bare Enter skips
	if res := <-resCh; res.err != nil || res.ok {
		t.Fatalf("expected skip via default, got %+v", res)
	}
}

func TestCharmConfirmApplyPrompts(t *testing.T) {
	d, ui := newCharmRestoreUITestHarness(t)

	type result struct {
		ok  bool
		err error
	}
	resCh := make(chan result, 1)

	go func() {
		ok, err := ui.ConfirmApplyVMConfigs(context.Background(), "pve9", "pve1", 4)
		resCh <- result{ok, err}
	}()
	d.waitScreen("Apply VM/CT configs")
	d.waitOutput("exported node pve9")
	d.keys("enter") // safe default: Skip
	if res := <-resCh; res.err != nil || res.ok {
		t.Fatalf("bare enter must skip, got %+v", res)
	}

	go func() {
		ok, err := ui.ConfirmApplyStorageCfg(context.Background(), "/tmp/storage.cfg")
		resCh <- result{ok, err}
	}()
	d.waitScreen("Apply storage.cfg")
	d.keys("left enter") // deliberate apply
	if res := <-resCh; res.err != nil || !res.ok {
		t.Fatalf("deliberate apply failed, got %+v", res)
	}

	go func() {
		ok, err := ui.ConfirmApplyDatacenterCfg(context.Background(), "/tmp/datacenter.cfg")
		resCh <- result{ok, err}
	}()
	d.waitScreen("Apply datacenter.cfg")
	d.keys("esc")
	if res := <-resCh; res.err != nil || res.ok {
		t.Fatalf("esc must decline, got %+v", res)
	}
}

// TestCharmAdapterCtxCancelIsNotAbort pins the error-identity contract:
// cancelling the context while a screen is active must surface
// context.Canceled, NOT the flow abort sentinel (the engine branches on the
// distinction). A mutation of mapAbort converting ctx errors would pass the
// rest of the suite.
func TestCharmAdapterCtxCancelIsNotAbort(t *testing.T) {
	d, ui := newCharmRestoreUITestHarness(t)

	type result struct {
		err error
	}
	resCh := make(chan result, 1)

	askCtx, cancel := context.WithCancel(context.Background())
	go func() {
		_, err := ui.ConfirmCompatibility(askCtx, errors.New("mismatch"))
		resCh <- result{err}
	}()
	d.waitScreen("Compatibility warning")
	cancel()
	res := <-resCh
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", res.err)
	}
	if errors.Is(res.err, ErrRestoreAborted) {
		t.Fatal("ctx cancellation must not be converted into ErrRestoreAborted")
	}

	askCtx2, cancel2 := context.WithCancel(context.Background())
	go func() {
		_, err := ui.PromptDecryptSecret(askCtx2, "backup", "")
		resCh <- result{err}
	}()
	d.waitScreen("Decrypt key")
	cancel2()
	res = <-resCh
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("expected context.Canceled from secret prompt, got %v", res.err)
	}
	if errors.Is(res.err, ErrDecryptAborted) {
		t.Fatal("ctx cancellation must not be converted into ErrDecryptAborted")
	}
}

// TestCharmConfirmCompatibilitySanitizesWarning: untrusted error text passes
// through the sanitize boundary; raw ESC sequences must never reach the
// terminal. The test session renders with the monochrome profile, so any SGR
// escape found in the output could only come from unsanitized data.
func TestCharmConfirmCompatibilitySanitizesWarning(t *testing.T) {
	d, ui := newCharmRestoreUITestHarness(t)

	type result struct {
		ok  bool
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		ok, err := ui.ConfirmCompatibility(context.Background(), errors.New("\x1b[31mevil-warning"))
		resCh <- result{ok, err}
	}()
	d.waitScreen("Compatibility warning")
	d.waitOutput("evil-warning")
	if strings.Contains(d.buf.String(), "\x1b[31m") {
		t.Fatal("raw ANSI from the warning text leaked into the terminal output")
	}
	d.keys("enter")
	if res := <-resCh; res.err != nil || res.ok {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestValidateDistinctNewPathInput(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		existing string
		want     string
		wantErr  bool
	}{
		{"empty rejected", "   ", "/tmp/out", "", true},
		{"identical rejected", "/tmp/out", "/tmp/out", "", true},
		{"normalized-equal rejected", "/tmp/out/", "/tmp/out", "", true},
		{"dot-segments-equal rejected", "/tmp/./out", "/tmp/out", "", true},
		{"distinct accepted trimmed", "  /tmp/other  ", "/tmp/out", "/tmp/other", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateDistinctNewPathInput(tc.value, tc.existing)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCharmConfirmDangerIgnoresShortcutKeys(t *testing.T) {
	d, ui := newCharmRestoreUITestHarness(t)

	type result struct {
		ok  bool
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		ok, err := ui.ConfirmCompatibility(context.Background(), errors.New("version mismatch"))
		resCh <- result{ok, err}
	}()
	d.waitScreen("Compatibility warning")
	d.waitOutput("version mismatch")
	// Single-key y must NOT accept a danger prompt.
	d.keys("y")
	d.keys("enter") // safe default: Abort restore
	if res := <-resCh; res.err != nil || res.ok {
		t.Fatalf("danger prompt accepted a shortcut or wrong default: %+v", res)
	}
}
