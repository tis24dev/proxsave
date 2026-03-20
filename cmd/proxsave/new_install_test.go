package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
)

func testNewInstallPreservedEntries() []string {
	return []string{"build", "env", "identity"}
}

func registerNewInstallBuildSignature(t *testing.T, fn func() string) {
	t.Helper()
	original := newInstallBuildSignature
	newInstallBuildSignature = fn
	t.Cleanup(func() {
		newInstallBuildSignature = original
	})
}

func registerTestStdin(t *testing.T, content string) {
	t.Helper()
	original := os.Stdin
	file, err := os.CreateTemp(t.TempDir(), "stdin-*")
	if err != nil {
		t.Fatalf("create temp stdin: %v", err)
	}
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		t.Fatalf("write temp stdin: %v", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = file.Close()
		t.Fatalf("seek temp stdin: %v", err)
	}
	os.Stdin = file
	t.Cleanup(func() {
		os.Stdin = original
		_ = file.Close()
	})
}

func TestNewInstallPreservedEntries(t *testing.T) {
	got := newInstallPreservedEntries()
	want := testNewInstallPreservedEntries()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("newInstallPreservedEntries() = %#v, want %#v", got, want)
	}
}

func TestBuildNewInstallPlan(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "env", "backup.env")

	plan, err := buildNewInstallPlan(configPath)
	if err != nil {
		t.Fatalf("buildNewInstallPlan error: %v", err)
	}
	if plan.ResolvedConfigPath != configPath {
		t.Fatalf("resolved config path = %q, want %q", plan.ResolvedConfigPath, configPath)
	}
	if plan.BaseDir != baseDir {
		t.Fatalf("base dir = %q, want %q", plan.BaseDir, baseDir)
	}
	if strings.TrimSpace(plan.BuildSignature) == "" {
		t.Fatalf("build signature should not be empty")
	}
	if !reflect.DeepEqual(plan.PreservedEntries, newInstallPreservedEntries()) {
		t.Fatalf("preserved entries = %#v, want %#v", plan.PreservedEntries, newInstallPreservedEntries())
	}
}

func TestBuildNewInstallPlanUsesNAWhenBuildSignatureBlank(t *testing.T) {
	registerNewInstallBuildSignature(t, func() string { return "   " })

	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "env", "backup.env")

	plan, err := buildNewInstallPlan(configPath)
	if err != nil {
		t.Fatalf("buildNewInstallPlan error: %v", err)
	}
	if plan.BuildSignature != "n/a" {
		t.Fatalf("build signature = %q, want %q", plan.BuildSignature, "n/a")
	}
}

func TestBuildNewInstallPlanRejectsEmptyConfigPath(t *testing.T) {
	if _, err := buildNewInstallPlan("  "); err == nil {
		t.Fatalf("expected error for empty config path")
	}
}

func TestFormatNewInstallPreservedEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		want    string
	}{
		{
			name:    "formats trimmed entries",
			entries: []string{" build ", "env", " identity"},
			want:    "build/ env/ identity/",
		},
		{
			name:    "returns none for nil input",
			entries: nil,
			want:    "(none)",
		},
		{
			name:    "returns none for blank input",
			entries: []string{"", " ", "\t"},
			want:    "(none)",
		},
		{
			name:    "normalizes trailing slashes",
			entries: []string{"env/", "build//", " identity/// ", "/"},
			want:    "env/ build/ identity/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatNewInstallPreservedEntries(tt.entries); got != tt.want {
				t.Fatalf("formatNewInstallPreservedEntries(%v) = %q, want %q", tt.entries, got, tt.want)
			}
		})
	}
}

func TestConfirmNewInstallCLIContinue(t *testing.T) {
	plan := newInstallPlan{
		BaseDir:          "/opt/proxsave",
		BuildSignature:   "sig-123",
		PreservedEntries: testNewInstallPreservedEntries(),
	}

	reader := bufio.NewReader(strings.NewReader("y\n"))
	var confirmed bool
	var err error
	output := captureStdout(t, func() {
		confirmed, err = confirmNewInstallCLI(context.Background(), reader, plan)
	})
	if err != nil {
		t.Fatalf("confirmNewInstallCLI error: %v", err)
	}
	if !confirmed {
		t.Fatalf("expected confirmation=true")
	}
	if !strings.Contains(output, "Preserved entries: build/ env/ identity/") {
		t.Fatalf("expected preserved entries output, got %q", output)
	}
}

func TestConfirmNewInstallCLIContextCancelled(t *testing.T) {
	plan := newInstallPlan{
		BaseDir:          "/opt/proxsave",
		BuildSignature:   "sig-123",
		PreservedEntries: testNewInstallPreservedEntries(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := confirmNewInstallCLI(ctx, bufio.NewReader(strings.NewReader("y\n")), plan)
	if !errors.Is(err, errInteractiveAborted) {
		t.Fatalf("expected errInteractiveAborted, got %v", err)
	}
}

func TestConfirmNewInstallCLIUsesStdinWhenReaderNil(t *testing.T) {
	registerTestStdin(t, "y\n")

	plan := newInstallPlan{
		BaseDir:          "/opt/proxsave",
		BuildSignature:   "sig-123",
		PreservedEntries: testNewInstallPreservedEntries(),
	}

	var confirmed bool
	var err error
	output := captureStdout(t, func() {
		confirmed, err = confirmNewInstallCLI(context.Background(), nil, plan)
	})
	if err != nil {
		t.Fatalf("confirmNewInstallCLI error: %v", err)
	}
	if !confirmed {
		t.Fatalf("expected confirmation=true")
	}
	if !strings.Contains(output, "Continue? [y/N]: ") {
		t.Fatalf("expected prompt in output, got %q", output)
	}
}

func TestRunNewInstallCLIUsesCLIConfirmOnly(t *testing.T) {
	originalEnsure := newInstallEnsureInteractiveStdin
	originalConfirmCLI := newInstallConfirmCLI
	originalConfirmTUI := newInstallConfirmTUI
	originalRunInstall := newInstallRunInstall
	originalRunInstallTUI := newInstallRunInstallTUI
	defer func() {
		newInstallEnsureInteractiveStdin = originalEnsure
		newInstallConfirmCLI = originalConfirmCLI
		newInstallConfirmTUI = originalConfirmTUI
		newInstallRunInstall = originalRunInstall
		newInstallRunInstallTUI = originalRunInstallTUI
	}()

	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "env", "backup.env")
	stalePath := filepath.Join(baseDir, "stale.txt")
	if err := os.WriteFile(stalePath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	newInstallEnsureInteractiveStdin = func() error { return nil }

	cliConfirmCalled := false
	newInstallConfirmCLI = func(ctx context.Context, reader *bufio.Reader, plan newInstallPlan) (bool, error) {
		cliConfirmCalled = true
		if plan.BaseDir != baseDir {
			t.Fatalf("plan base dir = %q, want %q", plan.BaseDir, baseDir)
		}
		return true, nil
	}

	newInstallConfirmTUI = func(ctx context.Context, baseDirArg, buildSig string, preservedEntries []string) (bool, error) {
		t.Fatalf("TUI confirmation must not be called in --cli mode")
		return false, nil
	}

	runInstallCalled := false
	newInstallRunInstall = func(ctx context.Context, cfg string, bootstrap *logging.BootstrapLogger) error {
		runInstallCalled = true
		if cfg != configPath {
			t.Fatalf("runInstall config path = %q, want %q", cfg, configPath)
		}
		return nil
	}
	newInstallRunInstallTUI = func(ctx context.Context, cfg string, bootstrap *logging.BootstrapLogger) error {
		t.Fatalf("runInstallTUI must not be called in --cli mode")
		return nil
	}

	if err := runNewInstall(context.Background(), configPath, logging.NewBootstrapLogger(), true); err != nil {
		t.Fatalf("runNewInstall error: %v", err)
	}
	if !cliConfirmCalled {
		t.Fatalf("expected CLI confirmation to be called")
	}
	if !runInstallCalled {
		t.Fatalf("expected runInstall to be called")
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("expected stale marker to be removed by reset, got err=%v", err)
	}
}

func TestRunNewInstallCancelSkipsReset(t *testing.T) {
	originalEnsure := newInstallEnsureInteractiveStdin
	originalConfirmCLI := newInstallConfirmCLI
	originalRunInstall := newInstallRunInstall
	originalRunInstallTUI := newInstallRunInstallTUI
	defer func() {
		newInstallEnsureInteractiveStdin = originalEnsure
		newInstallConfirmCLI = originalConfirmCLI
		newInstallRunInstall = originalRunInstall
		newInstallRunInstallTUI = originalRunInstallTUI
	}()

	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "env", "backup.env")
	markerPath := filepath.Join(baseDir, "marker.txt")
	if err := os.WriteFile(markerPath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	newInstallEnsureInteractiveStdin = func() error { return nil }
	newInstallConfirmCLI = func(ctx context.Context, reader *bufio.Reader, plan newInstallPlan) (bool, error) {
		return false, nil
	}
	newInstallRunInstall = func(ctx context.Context, cfg string, bootstrap *logging.BootstrapLogger) error {
		t.Fatalf("runInstall must not be called on cancel")
		return nil
	}
	newInstallRunInstallTUI = func(ctx context.Context, cfg string, bootstrap *logging.BootstrapLogger) error {
		t.Fatalf("runInstallTUI must not be called on cancel")
		return nil
	}

	err := runNewInstall(context.Background(), configPath, logging.NewBootstrapLogger(), true)
	if !errors.Is(err, errInteractiveAborted) {
		t.Fatalf("expected interactive abort, got %v", err)
	}
	if _, statErr := os.Stat(markerPath); statErr != nil {
		t.Fatalf("expected marker to remain after cancel, got %v", statErr)
	}
}

func TestRunNewInstallTUIPassesContextToConfirm(t *testing.T) {
	originalEnsure := newInstallEnsureInteractiveStdin
	originalConfirmCLI := newInstallConfirmCLI
	originalConfirmTUI := newInstallConfirmTUI
	originalRunInstall := newInstallRunInstall
	originalRunInstallTUI := newInstallRunInstallTUI
	defer func() {
		newInstallEnsureInteractiveStdin = originalEnsure
		newInstallConfirmCLI = originalConfirmCLI
		newInstallConfirmTUI = originalConfirmTUI
		newInstallRunInstall = originalRunInstall
		newInstallRunInstallTUI = originalRunInstallTUI
	}()

	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "env", "backup.env")
	ctx := t.Context()

	newInstallEnsureInteractiveStdin = func() error { return nil }
	newInstallConfirmCLI = func(ctx context.Context, reader *bufio.Reader, plan newInstallPlan) (bool, error) {
		t.Fatalf("CLI confirmation must not be called in TUI mode")
		return false, nil
	}
	newInstallConfirmTUI = func(gotCtx context.Context, baseDirArg, buildSig string, preservedEntries []string) (bool, error) {
		if gotCtx != ctx {
			t.Fatalf("got context %p, want %p", gotCtx, ctx)
		}
		if baseDirArg != baseDir {
			t.Fatalf("baseDir=%q, want %q", baseDirArg, baseDir)
		}
		return false, nil
	}
	newInstallRunInstall = func(ctx context.Context, cfg string, bootstrap *logging.BootstrapLogger) error {
		t.Fatalf("runInstall must not be called in TUI mode")
		return nil
	}
	newInstallRunInstallTUI = func(ctx context.Context, cfg string, bootstrap *logging.BootstrapLogger) error {
		t.Fatalf("runInstallTUI must not be called when confirmation is declined")
		return nil
	}

	err := runNewInstall(ctx, configPath, logging.NewBootstrapLogger(), false)
	if !errors.Is(err, errInteractiveAborted) {
		t.Fatalf("expected interactive abort, got %v", err)
	}
}

func TestRunNewInstallTUIUsesTUIConfirmAndRunInstallTUI(t *testing.T) {
	originalEnsure := newInstallEnsureInteractiveStdin
	originalConfirmCLI := newInstallConfirmCLI
	originalConfirmTUI := newInstallConfirmTUI
	originalRunInstall := newInstallRunInstall
	originalRunInstallTUI := newInstallRunInstallTUI
	defer func() {
		newInstallEnsureInteractiveStdin = originalEnsure
		newInstallConfirmCLI = originalConfirmCLI
		newInstallConfirmTUI = originalConfirmTUI
		newInstallRunInstall = originalRunInstall
		newInstallRunInstallTUI = originalRunInstallTUI
	}()

	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "env", "backup.env")
	stalePath := filepath.Join(baseDir, "stale.txt")
	if err := os.WriteFile(stalePath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	ctx := t.Context()
	newInstallEnsureInteractiveStdin = func() error { return nil }
	newInstallConfirmCLI = func(ctx context.Context, reader *bufio.Reader, plan newInstallPlan) (bool, error) {
		t.Fatalf("CLI confirmation must not be called in TUI mode")
		return false, nil
	}

	tuiConfirmCalled := false
	newInstallConfirmTUI = func(gotCtx context.Context, baseDirArg, buildSig string, preservedEntries []string) (bool, error) {
		tuiConfirmCalled = true
		if gotCtx != ctx {
			t.Fatalf("got context %p, want %p", gotCtx, ctx)
		}
		if baseDirArg != baseDir {
			t.Fatalf("baseDir=%q, want %q", baseDirArg, baseDir)
		}
		if strings.TrimSpace(buildSig) == "" {
			t.Fatalf("expected non-empty build signature")
		}
		if !reflect.DeepEqual(preservedEntries, testNewInstallPreservedEntries()) {
			t.Fatalf("preservedEntries=%#v, want %#v", preservedEntries, testNewInstallPreservedEntries())
		}
		return true, nil
	}

	newInstallRunInstall = func(ctx context.Context, cfg string, bootstrap *logging.BootstrapLogger) error {
		t.Fatalf("runInstall must not be called in TUI mode")
		return nil
	}

	runInstallTuiCalled := false
	newInstallRunInstallTUI = func(gotCtx context.Context, cfg string, bootstrap *logging.BootstrapLogger) error {
		runInstallTuiCalled = true
		if gotCtx != ctx {
			t.Fatalf("got context %p, want %p", gotCtx, ctx)
		}
		if cfg != configPath {
			t.Fatalf("runInstallTUI config path = %q, want %q", cfg, configPath)
		}
		return nil
	}

	if err := runNewInstall(ctx, configPath, logging.NewBootstrapLogger(), false); err != nil {
		t.Fatalf("runNewInstall error: %v", err)
	}
	if !tuiConfirmCalled {
		t.Fatalf("expected TUI confirmation to be called")
	}
	if !runInstallTuiCalled {
		t.Fatalf("expected runInstallTUI to be called")
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("expected stale marker to be removed by reset, got err=%v", err)
	}
}
