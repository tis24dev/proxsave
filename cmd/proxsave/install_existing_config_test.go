package main

import (
	"bufio"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/installer"
)

func TestPromptExistingConfigModeCLIMissingFileDefaultsToOverwrite(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.env")
	mode, err := promptExistingConfigModeCLI(context.Background(), bufio.NewReader(strings.NewReader("")), missing)
	if err != nil {
		t.Fatalf("promptExistingConfigModeCLI error: %v", err)
	}
	if mode != installer.ExistingConfigOverwrite {
		t.Fatalf("expected overwrite mode, got %v", mode)
	}
}

func TestPromptExistingConfigModeCLIMissingFileRespectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	missing := filepath.Join(t.TempDir(), "missing.env")
	mode, err := promptExistingConfigModeCLI(ctx, bufio.NewReader(strings.NewReader("")), missing)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}
	if mode != installer.ExistingConfigCancel {
		t.Fatalf("expected cancel mode, got %v", mode)
	}
}

func TestPromptExistingConfigModeCLIOptions(t *testing.T) {
	cfgFile := createTempFile(t, "EXISTING=1\n")
	tests := []struct {
		name  string
		input string
		want  installer.ExistingConfigAction
	}{
		{name: "default keep continue", input: "\n", want: installer.ExistingConfigKeepContinue},
		{name: "overwrite", input: "1\n", want: installer.ExistingConfigOverwrite},
		{name: "edit", input: "2\n", want: installer.ExistingConfigEdit},
		{name: "keep continue", input: "3\n", want: installer.ExistingConfigKeepContinue},
		{name: "cancel", input: "0\n", want: installer.ExistingConfigCancel},
		{name: "invalid then overwrite", input: "x\n1\n", want: installer.ExistingConfigOverwrite},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(tc.input))
			var mode installer.ExistingConfigAction
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

func TestPromptExistingConfigModeCLIStatError(t *testing.T) {
	pathWithNul := string([]byte{0})
	_, err := promptExistingConfigModeCLI(context.Background(), bufio.NewReader(strings.NewReader("1\n")), pathWithNul)
	if err == nil {
		t.Fatalf("expected stat error")
	}
}
