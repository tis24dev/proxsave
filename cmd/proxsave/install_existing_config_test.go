package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptExistingConfigModeCLIMissingFileDefaultsToOverwrite(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.env")
	mode, err := promptExistingConfigModeCLI(context.Background(), bufio.NewReader(strings.NewReader("")), missing)
	if err != nil {
		t.Fatalf("promptExistingConfigModeCLI error: %v", err)
	}
	if mode != existingConfigOverwrite {
		t.Fatalf("expected overwrite mode, got %v", mode)
	}
}

func TestPromptExistingConfigModeCLIOptions(t *testing.T) {
	cfgFile := createTempFile(t, "EXISTING=1\n")
	tests := []struct {
		name  string
		input string
		want  existingConfigMode
	}{
		{name: "default keep continue", input: "\n", want: existingConfigKeepContinue},
		{name: "overwrite", input: "1\n", want: existingConfigOverwrite},
		{name: "edit", input: "2\n", want: existingConfigEdit},
		{name: "keep continue", input: "3\n", want: existingConfigKeepContinue},
		{name: "cancel", input: "0\n", want: existingConfigCancel},
		{name: "invalid then overwrite", input: "x\n1\n", want: existingConfigOverwrite},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(tc.input))
			var mode existingConfigMode
			var err error
			captureStdout(t, func() {
				mode, err = promptExistingConfigModeCLI(context.Background(), reader, cfgFile)
			})
			if err != nil {
				t.Fatalf("promptExistingConfigModeCLI error: %v", err)
			}
			if mode != tc.want {
				t.Fatalf("mode = %v, want %v", mode, tc.want)
			}
		})
	}
}

func TestResolveExistingConfigDecision(t *testing.T) {
	cfgFile := createTempFile(t, "EXISTING=1\n")

	overwrite, err := resolveExistingConfigDecision(existingConfigOverwrite, cfgFile)
	if err != nil {
		t.Fatalf("overwrite decision error: %v", err)
	}
	if overwrite.SkipConfigWizard || overwrite.AbortInstall {
		t.Fatalf("overwrite decision flags are invalid: %+v", overwrite)
	}
	if strings.TrimSpace(overwrite.BaseTemplate) == "" {
		t.Fatalf("overwrite base template should not be empty")
	}

	edit, err := resolveExistingConfigDecision(existingConfigEdit, cfgFile)
	if err != nil {
		t.Fatalf("edit decision error: %v", err)
	}
	if edit.SkipConfigWizard || edit.AbortInstall {
		t.Fatalf("edit decision flags are invalid: %+v", edit)
	}
	if !strings.Contains(edit.BaseTemplate, "EXISTING=1") {
		t.Fatalf("expected existing content, got %q", edit.BaseTemplate)
	}

	keep, err := resolveExistingConfigDecision(existingConfigKeepContinue, cfgFile)
	if err != nil {
		t.Fatalf("keep decision error: %v", err)
	}
	if !keep.SkipConfigWizard || keep.AbortInstall {
		t.Fatalf("keep decision flags are invalid: %+v", keep)
	}

	cancel, err := resolveExistingConfigDecision(existingConfigCancel, cfgFile)
	if err != nil {
		t.Fatalf("cancel decision error: %v", err)
	}
	if cancel.SkipConfigWizard || !cancel.AbortInstall {
		t.Fatalf("cancel decision flags are invalid: %+v", cancel)
	}
}

func TestPrepareExistingConfigDecisionCLICancel(t *testing.T) {
	cfgFile := createTempFile(t, "EXISTING=1\n")
	reader := bufio.NewReader(strings.NewReader("0\n"))
	decision, err := prepareExistingConfigDecisionCLI(context.Background(), reader, cfgFile)
	if err != nil {
		t.Fatalf("prepareExistingConfigDecisionCLI error: %v", err)
	}
	if !decision.AbortInstall {
		t.Fatalf("expected abort decision, got %+v", decision)
	}
}

func TestResolveExistingConfigDecisionEditReadError(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "missing.env")
	_, err := resolveExistingConfigDecision(existingConfigEdit, cfgFile)
	if err == nil {
		t.Fatalf("expected read error for missing file")
	}
}

func TestPromptExistingConfigModeCLIPropagatesReadError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfgFile := createTempFile(t, "EXISTING=1\n")
	_, err := promptExistingConfigModeCLI(ctx, bufio.NewReader(strings.NewReader("1\n")), cfgFile)
	if !errors.Is(err, errInteractiveAborted) {
		t.Fatalf("expected interactive aborted error, got %v", err)
	}
}

func TestPromptExistingConfigModeCLINonRegularFile(t *testing.T) {
	dirPath := t.TempDir()
	_, err := promptExistingConfigModeCLI(context.Background(), bufio.NewReader(strings.NewReader("1\n")), dirPath)
	if err == nil {
		t.Fatalf("expected error for non-regular file")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestResolveExistingConfigDecisionUnsupportedMode(t *testing.T) {
	cfgFile := createTempFile(t, "EXISTING=1\n")
	_, err := resolveExistingConfigDecision(existingConfigMode(99), cfgFile)
	if err == nil {
		t.Fatalf("expected unsupported mode error")
	}
}

func TestPromptExistingConfigModeCLIStatError(t *testing.T) {
	pathWithNul := string([]byte{0})
	_, err := promptExistingConfigModeCLI(context.Background(), bufio.NewReader(strings.NewReader("1\n")), pathWithNul)
	if err == nil {
		t.Fatalf("expected stat error")
	}
}

func TestResolveExistingConfigDecisionEditExistingContentExact(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "backup.env")
	content := "KEY=VALUE\nANOTHER=1\n"
	if err := os.WriteFile(cfg, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	decision, err := resolveExistingConfigDecision(existingConfigEdit, cfg)
	if err != nil {
		t.Fatalf("resolveExistingConfigDecision error: %v", err)
	}
	if decision.BaseTemplate != content {
		t.Fatalf("expected exact content, got %q", decision.BaseTemplate)
	}
}
