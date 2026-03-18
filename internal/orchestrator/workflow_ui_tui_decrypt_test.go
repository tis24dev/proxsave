package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func stubTUIExistingPathDecisionPrompt(fn func(ctx context.Context, path, description, failure, configPath, buildSig string) (ExistingPathDecision, string, error)) func() {
	orig := tuiPromptExistingPathDecision
	tuiPromptExistingPathDecision = fn
	return func() { tuiPromptExistingPathDecision = orig }
}

func TestTUIWorkflowUIResolveExistingPath_Overwrite(t *testing.T) {
	restore := stubTUIExistingPathDecisionPrompt(func(ctx context.Context, path, description, failure, configPath, buildSig string) (ExistingPathDecision, string, error) {
		if path != "/tmp/archive.tar" {
			t.Fatalf("path=%q, want /tmp/archive.tar", path)
		}
		if description != "archive" {
			t.Fatalf("description=%q, want archive", description)
		}
		if configPath != "/tmp/config.env" {
			t.Fatalf("configPath=%q, want /tmp/config.env", configPath)
		}
		if buildSig != "sig" {
			t.Fatalf("buildSig=%q, want sig", buildSig)
		}
		return PathDecisionOverwrite, "", nil
	})
	defer restore()

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	decision, newPath, err := ui.ResolveExistingPath(context.Background(), "/tmp/archive.tar", "archive", "")
	if err != nil {
		t.Fatalf("ResolveExistingPath error: %v", err)
	}
	if decision != PathDecisionOverwrite {
		t.Fatalf("decision=%v, want %v", decision, PathDecisionOverwrite)
	}
	if newPath != "" {
		t.Fatalf("newPath=%q, want empty", newPath)
	}
}

func TestTUIWorkflowUIResolveExistingPath_NewPathIsCleaned(t *testing.T) {
	restore := stubTUIExistingPathDecisionPrompt(func(ctx context.Context, path, description, failure, configPath, buildSig string) (ExistingPathDecision, string, error) {
		return PathDecisionNewPath, "/tmp/out/../out/final.tar", nil
	})
	defer restore()

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	decision, newPath, err := ui.ResolveExistingPath(context.Background(), "/tmp/archive.tar", "archive", "")
	if err != nil {
		t.Fatalf("ResolveExistingPath error: %v", err)
	}
	if decision != PathDecisionNewPath {
		t.Fatalf("decision=%v, want %v", decision, PathDecisionNewPath)
	}
	if newPath != filepath.Clean("/tmp/out/../out/final.tar") {
		t.Fatalf("newPath=%q, want %q", newPath, filepath.Clean("/tmp/out/../out/final.tar"))
	}
}

func TestTUIWorkflowUIResolveExistingPath_WhitespaceNewPathStaysEmpty(t *testing.T) {
	restore := stubTUIExistingPathDecisionPrompt(func(ctx context.Context, path, description, failure, configPath, buildSig string) (ExistingPathDecision, string, error) {
		return PathDecisionNewPath, "   \t  ", nil
	})
	defer restore()

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	decision, newPath, err := ui.ResolveExistingPath(context.Background(), "/tmp/archive.tar", "archive", "")
	if err != nil {
		t.Fatalf("ResolveExistingPath error: %v", err)
	}
	if decision != PathDecisionNewPath {
		t.Fatalf("decision=%v, want %v", decision, PathDecisionNewPath)
	}
	if newPath != "" {
		t.Fatalf("newPath=%q, want empty", newPath)
	}
}

func TestTUIWorkflowUIResolveExistingPath_PropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	restore := stubTUIExistingPathDecisionPrompt(func(ctx context.Context, path, description, failure, configPath, buildSig string) (ExistingPathDecision, string, error) {
		return PathDecisionCancel, "", wantErr
	})
	defer restore()

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	if _, _, err := ui.ResolveExistingPath(context.Background(), "/tmp/archive.tar", "archive", ""); !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

func TestTUIWorkflowUIResolveExistingPath_PassesContext(t *testing.T) {
	called := false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restore := stubTUIExistingPathDecisionPrompt(func(gotCtx context.Context, path, description, failure, configPath, buildSig string) (ExistingPathDecision, string, error) {
		called = true
		if gotCtx != ctx {
			t.Fatalf("got context %p, want %p", gotCtx, ctx)
		}
		return PathDecisionOverwrite, "", nil
	})
	defer restore()

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	if _, _, err := ui.ResolveExistingPath(ctx, "/tmp/archive.tar", "archive", ""); err != nil {
		t.Fatalf("ResolveExistingPath error: %v", err)
	}
	if !called {
		t.Fatalf("expected prompt to be called")
	}
}

func stubTUIDecryptSecretPrompt(fn func(ctx context.Context, configPath, buildSig, displayName, previousError string) (string, error)) func() {
	orig := tuiPromptDecryptSecret
	tuiPromptDecryptSecret = fn
	return func() { tuiPromptDecryptSecret = orig }
}

func TestTUIWorkflowUIPromptDecryptSecret_PassesContext(t *testing.T) {
	called := false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restore := stubTUIDecryptSecretPrompt(func(gotCtx context.Context, configPath, buildSig, displayName, previousError string) (string, error) {
		called = true
		if gotCtx != ctx {
			t.Fatalf("got context %p, want %p", gotCtx, ctx)
		}
		return "secret", nil
	})
	defer restore()

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	got, err := ui.PromptDecryptSecret(ctx, "archive", "")
	if err != nil {
		t.Fatalf("PromptDecryptSecret error: %v", err)
	}
	if got != "secret" {
		t.Fatalf("secret=%q, want %q", got, "secret")
	}
	if !called {
		t.Fatalf("expected prompt to be called")
	}
}

func TestTUIWorkflowUIPromptDestinationDir_ContinueReturnsCleanPath(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyTab, tcell.KeyEnter})

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	got, err := ui.PromptDestinationDir(context.Background(), "/tmp/out/../out")
	if err != nil {
		t.Fatalf("PromptDestinationDir error: %v", err)
	}
	if got != "/tmp/out" {
		t.Fatalf("destination=%q, want %q", got, "/tmp/out")
	}
}

func TestTUIWorkflowUIPromptDestinationDir_CancelReturnsAborted(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyTab, tcell.KeyTab, tcell.KeyEnter})

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	_, err := ui.PromptDestinationDir(context.Background(), "/tmp/out")
	if !errors.Is(err, ErrDecryptAborted) {
		t.Fatalf("err=%v, want %v", err, ErrDecryptAborted)
	}
}

func TestValidateDistinctNewPathInputRejectsEquivalentNormalizedPath(t *testing.T) {
	_, err := validateDistinctNewPathInput("/tmp/out/", "/tmp/out")
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if err.Error() != "path must be different from existing path" {
		t.Fatalf("err=%q, want %q", err.Error(), "path must be different from existing path")
	}
}

func TestValidateDistinctNewPathInputAcceptsDifferentPath(t *testing.T) {
	got, err := validateDistinctNewPathInput(" /tmp/out/alt ", "/tmp/out")
	if err != nil {
		t.Fatalf("validateDistinctNewPathInput error: %v", err)
	}
	if got != "/tmp/out/alt" {
		t.Fatalf("path=%q, want %q", got, "/tmp/out/alt")
	}
}
