package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func stubTUIExistingPathDecisionPrompt(fn func(path, description, failure, configPath, buildSig string) (ExistingPathDecision, string, error)) func() {
	orig := tuiPromptExistingPathDecision
	tuiPromptExistingPathDecision = fn
	return func() { tuiPromptExistingPathDecision = orig }
}

func TestTUIWorkflowUIResolveExistingPath_Overwrite(t *testing.T) {
	restore := stubTUIExistingPathDecisionPrompt(func(path, description, failure, configPath, buildSig string) (ExistingPathDecision, string, error) {
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
	restore := stubTUIExistingPathDecisionPrompt(func(path, description, failure, configPath, buildSig string) (ExistingPathDecision, string, error) {
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

func TestTUIWorkflowUIResolveExistingPath_PropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	restore := stubTUIExistingPathDecisionPrompt(func(path, description, failure, configPath, buildSig string) (ExistingPathDecision, string, error) {
		return PathDecisionCancel, "", wantErr
	})
	defer restore()

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	if _, _, err := ui.ResolveExistingPath(context.Background(), "/tmp/archive.tar", "archive", ""); !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
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
