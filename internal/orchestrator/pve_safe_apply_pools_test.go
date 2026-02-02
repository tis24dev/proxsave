package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPVEPoolsDefinitions_ExistingPoolNoComment_IsSuccess(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	pathDir := t.TempDir()
	pveumPath := filepath.Join(pathDir, "pveum")
	if err := os.WriteFile(pveumPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write pveum stub: %v", err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"pveum pool list":    []byte("poolid comment\ndev\n"),
			"pveum pool add dev": []byte("pool 'dev' already exists\n"),
		},
		Errors: map[string]error{
			"pveum pool add dev": fmt.Errorf("exit status 255"),
		},
	}
	restoreCmd = runner

	applied, failed, err := applyPVEPoolsDefinitions(context.Background(), newTestLogger(), []pvePoolSpec{{ID: "dev"}})
	if err != nil {
		t.Fatalf("applyPVEPoolsDefinitions error: %v", err)
	}
	if applied != 1 || failed != 0 {
		t.Fatalf("applyPVEPoolsDefinitions applied=%d failed=%d want applied=1 failed=0", applied, failed)
	}
}

func TestApplyPVEPoolsDefinitions_AddFailsNoCommentAndPoolMissing_IsFailure(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	pathDir := t.TempDir()
	pveumPath := filepath.Join(pathDir, "pveum")
	if err := os.WriteFile(pveumPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write pveum stub: %v", err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"pveum pool list": []byte("poolid comment\n"),
		},
		Errors: map[string]error{
			"pveum pool add dev": fmt.Errorf("boom"),
		},
	}
	restoreCmd = runner

	applied, failed, err := applyPVEPoolsDefinitions(context.Background(), newTestLogger(), []pvePoolSpec{{ID: "dev"}})
	if err == nil {
		t.Fatalf("expected error")
	}
	if applied != 0 || failed != 1 {
		t.Fatalf("applyPVEPoolsDefinitions applied=%d failed=%d want applied=0 failed=1", applied, failed)
	}
}
