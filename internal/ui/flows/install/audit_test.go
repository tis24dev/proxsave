package install

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// TestAuditResultPromptStripsInjectedEscapes proves auditResultPrompt scrubs free-form
// keyword/explanation before theme rendering, so raw ANSI/OSC/C0/C1 escapes from external tool
// output (e.g. the dry-run collect error string) can never reach the terminal via the verbatim
// WithSelectorPromptStyled path. The theme's own SGR (ESC[..m) still wraps the text, so we
// assert the INJECTED marker sequences are gone rather than "no 0x1b anywhere".
func TestAuditResultPromptStripsInjectedEscapes(t *testing.T) {
	keyword := "\x1b[2JBOOM"
	explanation := "failed: \x1b[31m\x1b]0;pwned\x07evil\x07"
	got := auditResultPrompt(orchestrator.HealthcheckSetupLevelError, keyword, explanation)

	// Injected escapes must be absent: the raw OSC/BEL/CSI-clear sequences and the C1 CSI byte.
	for _, bad := range []string{"\x1b]0;pwned", "\x1b[2J", "\x07"} {
		if strings.Contains(got, bad) {
			t.Errorf("injected escape %q survived in output %q", bad, got)
		}
	}
	if strings.ContainsRune(got, 0x9b) {
		t.Errorf("injected C1 CSI byte 0x9b survived in output %q", got)
	}

	// The legitimate text must survive the scrub.
	for _, want := range []string{"failed:", "evil", "BOOM"} {
		if !strings.Contains(got, want) {
			t.Errorf("legitimate text %q missing from output %q", want, got)
		}
	}
}

// TestAuditResultPromptPreservesNewline confirms a clean multi-line explanation keeps its
// newline (SanitizeText keeps \n/\t), so multi-line audit outcomes still render across lines.
func TestAuditResultPromptPreservesNewline(t *testing.T) {
	got := auditResultPrompt(orchestrator.HealthcheckSetupLevelOk, "updated", "line one\nline two")
	for _, want := range []string{"line one", "line two"} {
		if !strings.Contains(got, want) {
			t.Errorf("clean line %q missing from output %q", want, got)
		}
	}
	if !strings.Contains(got, "\n") {
		t.Errorf("newline not preserved in multi-line explanation %q", got)
	}
}

// TestRunPostInstallAuditApplyFailure proves an ApplyAuditDisables write failure is now
// recorded on the result (ApplyErr set, no keys applied), so the caller can distinguish it
// from the benign "nothing selected" no-op instead of logging both as a clean run. The
// failure is forced deterministically by pointing configPath at a nonexistent directory so
// ApplyAuditDisables' ReadFile fails. A second, benign pass (esc out of the multi-select)
// asserts ApplyErr stays nil when no write is attempted.
func TestRunPostInstallAuditApplyFailure(t *testing.T) {
	d := newDriver(t)

	origCollect := auditCollect
	auditCollect = func(ctx context.Context, execPath, cfgPath string) ([]installer.PostInstallAuditSuggestion, error) {
		return []installer.PostInstallAuditSuggestion{
			{Key: "BACKUP_X", Messages: []string{"unused collector X"}},
		}, nil
	}
	t.Cleanup(func() { auditCollect = origCollect })

	type result struct {
		res installer.PostInstallAuditResult
		err error
	}
	resCh := make(chan result, 1)

	// Failure path: configPath under a nonexistent dir makes ApplyAuditDisables' ReadFile fail.
	failPath := filepath.Join(t.TempDir(), "nope", "backup.env")
	go func() {
		res, err := RunPostInstallAudit(context.Background(), d.session, "/fake/proxsave", failPath, false)
		resCh <- result{res, err}
	}()

	d.waitScreen("Post-install check")
	d.keys("enter") // run the dry-run
	d.waitScreen("Unused components")
	// One item (row 0), Select ALL (row 1), Disable Selected (row 2): select the item,
	// move to Disable Selected, press it.
	d.keys("space down down enter")
	d.waitScreen("Post-install check") // the outcome screen (same title)
	d.waitText("✗ UPDATE FAILED")
	d.keys("enter") // dismiss via Continue

	res := <-resCh
	if res.err != nil {
		t.Fatalf("apply failure must stay non-blocking (nil returned err), got %v", res.err)
	}
	if res.res.ApplyErr == nil {
		t.Fatalf("apply failure must be recorded on ApplyErr, got %+v", res.res)
	}
	if len(res.res.AppliedKeys) != 0 {
		t.Fatalf("a failed write must apply no keys, got %v", res.res.AppliedKeys)
	}

	// Benign path: reach the multi-select, esc out. No write is attempted, so ApplyErr is nil,
	// distinct from the failure above.
	go func() {
		res, err := RunPostInstallAudit(context.Background(), d.session, "/fake/proxsave", failPath, false)
		resCh <- result{res, err}
	}()
	d.waitScreen("Post-install check")
	d.keys("enter") // run the dry-run
	d.waitScreen("Unused components")
	d.keys("esc") // leave without selecting anything
	res = <-resCh
	if res.err != nil {
		t.Fatalf("benign esc must return nil err, got %v", res.err)
	}
	if res.res.ApplyErr != nil {
		t.Fatalf("no write attempted, ApplyErr must stay nil, got %v", res.res.ApplyErr)
	}
	if len(res.res.AppliedKeys) != 0 {
		t.Fatalf("benign path must apply no keys, got %v", res.res.AppliedKeys)
	}
}
