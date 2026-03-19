package wizard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
)

func testPreservedEntries() []string {
	return []string{"build", "env", "identity"}
}

func registerConfirmNewInstallRunner(t *testing.T, runner func(context.Context, *tui.App, tview.Primitive, tview.Primitive) error) {
	t.Helper()
	originalRunner := confirmNewInstallRunner
	confirmNewInstallRunner = runner
	t.Cleanup(func() {
		confirmNewInstallRunner = originalRunner
	})
}

func wizardPrimitiveContainsText(p tview.Primitive, want string) bool {
	switch v := p.(type) {
	case *tview.TextView:
		return strings.Contains(v.GetText(false), want)
	case *tview.Flex:
		for i := 0; i < v.GetItemCount(); i++ {
			if wizardPrimitiveContainsText(v.GetItem(i), want) {
				return true
			}
		}
	}
	return false
}

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%q) failed: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restoring working directory to %q failed: %v", original, err)
		}
	})
}

func TestFormatPreservedEntries(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tempDir, "build"), 0o755); err != nil {
		t.Fatalf("Mkdir(build) failed: %v", err)
	}
	if err := os.Mkdir(filepath.Join(tempDir, "identity"), 0o755); err != nil {
		t.Fatalf("Mkdir(identity) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "backup.env"), []byte("TEST=1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(backup.env) failed: %v", err)
	}
	withWorkingDir(t, tempDir)

	tests := []struct {
		name    string
		entries []string
		want    string
	}{
		{
			name:    "adds slash only for directories",
			entries: []string{" build ", "backup.env", " missing ", " identity"},
			want:    "build/ backup.env missing identity/",
		},
		{
			name:    "returns none for nil input",
			entries: nil,
			want:    "(none)",
		},
		{
			name:    "returns none for blank entries",
			entries: []string{"", "   ", "\t"},
			want:    "(none)",
		},
		{
			name:    "preserves existing trailing slash without doubling",
			entries: []string{"build/", " identity/ ", "backup.env"},
			want:    "build/ identity/ backup.env",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatPreservedEntries(tt.entries); got != tt.want {
				t.Fatalf("formatPreservedEntries(%v) = %q, want %q", tt.entries, got, tt.want)
			}
		})
	}
}

func TestConfirmNewInstallContinue(t *testing.T) {
	registerConfirmNewInstallRunner(t, func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		done := extractModalDone(focus.(*tview.Modal))
		done(0, "Continue")
		return nil
	})

	proceed, err := ConfirmNewInstall(context.Background(), "/opt/proxmox", "sig-123", testPreservedEntries())
	if err != nil {
		t.Fatalf("ConfirmNewInstall error: %v", err)
	}
	if !proceed {
		t.Fatalf("expected proceed=true when Continue is selected")
	}
}

func TestConfirmNewInstallCancel(t *testing.T) {
	registerConfirmNewInstallRunner(t, func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		done := extractModalDone(focus.(*tview.Modal))
		done(1, "Cancel")
		return nil
	})

	proceed, err := ConfirmNewInstall(context.Background(), "/opt/proxmox", "sig-123", testPreservedEntries())
	if err != nil {
		t.Fatalf("ConfirmNewInstall error: %v", err)
	}
	if proceed {
		t.Fatalf("expected proceed=false when Cancel is selected")
	}
}

func TestConfirmNewInstallMessageIncludesBaseDir(t *testing.T) {
	var captured string
	registerConfirmNewInstallRunner(t, func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		captured = extractModalText(focus.(*tview.Modal))
		return nil
	})

	_, err := ConfirmNewInstall(context.Background(), "/var/lib/data", "build-sig", testPreservedEntries())
	if err != nil {
		t.Fatalf("ConfirmNewInstall error: %v", err)
	}
	if !strings.Contains(captured, "/var/lib/data") {
		t.Fatalf("expected modal text to mention base dir, got %q", captured)
	}
}

func TestConfirmNewInstallMessageIncludesPreservedEntries(t *testing.T) {
	tempDir := t.TempDir()
	for _, dir := range []string{"build", "env", "identity"} {
		if err := os.Mkdir(filepath.Join(tempDir, dir), 0o755); err != nil {
			t.Fatalf("Mkdir(%s) failed: %v", dir, err)
		}
	}
	withWorkingDir(t, tempDir)

	var captured string
	registerConfirmNewInstallRunner(t, func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		captured = extractModalText(focus.(*tview.Modal))
		return nil
	})

	_, err := ConfirmNewInstall(context.Background(), "/var/lib/data", "build-sig", testPreservedEntries())
	if err != nil {
		t.Fatalf("ConfirmNewInstall error: %v", err)
	}
	if !strings.Contains(captured, "build/ env/ identity/") {
		t.Fatalf("expected modal text to mention preserved entries, got %q", captured)
	}
}

func TestConfirmNewInstallMessageUsesNoneWhenEntriesAreBlank(t *testing.T) {
	var captured string
	registerConfirmNewInstallRunner(t, func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		captured = extractModalText(focus.(*tview.Modal))
		return nil
	})

	_, err := ConfirmNewInstall(context.Background(), "/var/lib/data", "build-sig", []string{"", " ", "\t"})
	if err != nil {
		t.Fatalf("ConfirmNewInstall error: %v", err)
	}
	if !strings.Contains(captured, "(none)") {
		t.Fatalf("expected modal text to mention (none), got %q", captured)
	}
}

func TestConfirmNewInstallMessageEscapesDynamicColorMarkup(t *testing.T) {
	baseDir := "/var/lib/[prod]"
	preservedEntries := []string{" build[0] ", " identity] "}

	var captured string
	registerConfirmNewInstallRunner(t, func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		captured = extractModalText(focus.(*tview.Modal))
		return nil
	})

	_, err := ConfirmNewInstall(context.Background(), baseDir, "build-sig", preservedEntries)
	if err != nil {
		t.Fatalf("ConfirmNewInstall error: %v", err)
	}
	if !strings.Contains(captured, tview.Escape(baseDir)) {
		t.Fatalf("expected escaped base dir in modal text, got %q", captured)
	}

	wantPreserved := tview.Escape(formatPreservedEntries(preservedEntries))
	if !strings.Contains(captured, wantPreserved) {
		t.Fatalf("expected escaped preserved entries %q in modal text, got %q", wantPreserved, captured)
	}
}

func TestConfirmNewInstallPropagatesRunnerError(t *testing.T) {
	expectedErr := errors.New("runner failed")
	registerConfirmNewInstallRunner(t, func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		return expectedErr
	})

	_, err := ConfirmNewInstall(context.Background(), "/opt/proxmox", "sig-123", testPreservedEntries())
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
}

func TestConfirmNewInstallPassesContextToRunner(t *testing.T) {
	ctx := t.Context()
	registerConfirmNewInstallRunner(t, func(gotCtx context.Context, app *tui.App, root, focus tview.Primitive) error {
		if gotCtx != ctx {
			t.Fatalf("got context %p, want %p", gotCtx, ctx)
		}
		done := extractModalDone(focus.(*tview.Modal))
		done(0, "Continue")
		return nil
	})

	proceed, err := ConfirmNewInstall(ctx, "/opt/proxmox", "sig-123", testPreservedEntries())
	if err != nil {
		t.Fatalf("ConfirmNewInstall error: %v", err)
	}
	if !proceed {
		t.Fatalf("expected proceed=true when Continue is selected")
	}
}

func TestConfirmNewInstallBuildsWizardScreenWithEscapedBuildSignature(t *testing.T) {
	buildSig := "sig-[123]"

	var root tview.Primitive
	var focus tview.Primitive
	registerConfirmNewInstallRunner(t, func(ctx context.Context, app *tui.App, gotRoot, gotFocus tview.Primitive) error {
		root = gotRoot
		focus = gotFocus
		return nil
	})

	_, err := ConfirmNewInstall(context.Background(), "/opt/proxmox", buildSig, testPreservedEntries())
	if err != nil {
		t.Fatalf("ConfirmNewInstall error: %v", err)
	}
	if root == nil {
		t.Fatalf("expected wizard root to be passed to runner")
	}
	if _, ok := focus.(*tview.Modal); !ok {
		t.Fatalf("expected modal focus, got %T", focus)
	}
	if !wizardPrimitiveContainsText(root, tview.Escape(buildSig)) {
		t.Fatalf("expected root screen to include escaped build signature %q", tview.Escape(buildSig))
	}
}
