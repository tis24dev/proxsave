package cli

import (
	"bytes"
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestStringFlag(t *testing.T) {
	t.Run("default value", func(t *testing.T) {
		sf := newStringFlag("default")
		if sf.String() != "default" {
			t.Fatalf("String() = %q, want default", sf.String())
		}
		if sf.set {
			t.Fatal("flag should start unset")
		}
	})

	t.Run("set values", func(t *testing.T) {
		sf := newStringFlag("default")
		if err := sf.Set("first"); err != nil {
			t.Fatalf("Set returned error: %v", err)
		}
		if err := sf.Set("second"); err != nil {
			t.Fatalf("Set returned error: %v", err)
		}
		if sf.String() != "second" {
			t.Fatalf("String() = %q, want second", sf.String())
		}
		if !sf.set {
			t.Fatal("flag should be marked as set")
		}
	})
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected types.LogLevel
		ok       bool
	}{
		{"debug string", "debug", types.LogLevelDebug, true},
		{"debug number", "5", types.LogLevelDebug, true},
		{"info string", "info", types.LogLevelInfo, true},
		{"info number", "4", types.LogLevelInfo, true},
		{"warning string", "warning", types.LogLevelWarning, true},
		{"warning number", "3", types.LogLevelWarning, true},
		{"error string", "error", types.LogLevelError, true},
		{"error number", "2", types.LogLevelError, true},
		{"critical string", "critical", types.LogLevelCritical, true},
		{"critical number", "1", types.LogLevelCritical, true},
		{"uppercase", "DEBUG", types.LogLevelDebug, true},
		{"mixed case", "Warning", types.LogLevelWarning, true},
		{"none string refused", "none", types.LogLevelNone, false},
		{"none number refused", "0", types.LogLevelNone, false},
		{"out of range number refused", "6", types.LogLevelNone, false},
		{"short form refused", "warn", types.LogLevelNone, false},
		{"unknown refused", "invalid", types.LogLevelNone, false},
		{"leading whitespace refused", " debug", types.LogLevelNone, false},
		{"trailing whitespace refused", "debug ", types.LogLevelNone, false},
		{"empty string refused", "", types.LogLevelNone, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := parseLogLevel(tt.input)
			if result != tt.expected || ok != tt.ok {
				t.Errorf("parseLogLevel(%q) = (%v, %v); want (%v, %v)", tt.input, result, ok, tt.expected, tt.ok)
			}
		})
	}
}

func TestParseDefaults(t *testing.T) {
	args := parseWithArgs(t, nil)
	if args.ConfigPath == "" {
		t.Fatal("ConfigPath should default to non-empty path")
	}
	if args.ConfigPathSource != "default path" {
		t.Fatalf("ConfigPathSource = %q, want default path", args.ConfigPathSource)
	}
	if args.LogLevel != types.LogLevelNone {
		t.Fatalf("LogLevel = %v, want LogLevelNone", args.LogLevel)
	}
	if args.DryRun || args.ShowVersion || args.ShowHelp || args.ForceNewKey || args.Decrypt ||
		args.Restore || args.Install || args.NewInstall || args.UpgradeConfig ||
		args.UpgradeConfigDry || args.CleanupGuards {
		t.Fatal("all boolean flags should default to false")
	}
}

func TestParseCustomFlags(t *testing.T) {
	args := parseWithArgs(t, []string{
		"--config", "/custom/config.env",
		"--log-level", "debug",
		"--dry-run",
		"--support",
		"--version",
		"--help",
		"--newkey",
		"--decrypt",
		"--restore",
		"--install",
		"--new-install",
		"--cleanup-guards",
		"--upgrade-config",
		"--upgrade-config-dry-run",
	})

	if args.ConfigPath != "/custom/config.env" {
		t.Fatalf("ConfigPath = %q, want /custom/config.env", args.ConfigPath)
	}
	if args.ConfigPathSource != "specified via --config/-c flag" {
		t.Fatalf("ConfigPathSource = %q, want specified via flag", args.ConfigPathSource)
	}
	if args.LogLevel != types.LogLevelDebug {
		t.Fatalf("LogLevel = %v, want debug", args.LogLevel)
	}
	if !args.DryRun || !args.Support || !args.ShowVersion || !args.ShowHelp ||
		!args.ForceNewKey || !args.Decrypt || !args.Restore || !args.Install || !args.NewInstall ||
		!args.UpgradeConfig ||
		!args.UpgradeConfigDry || !args.CleanupGuards {
		t.Fatal("expected boolean flags to be set")
	}
}

func TestParseAliasFlags(t *testing.T) {
	args := parseWithArgs(t, []string{
		"-c", "/alias/config.env",
		"-l", "warning",
		"-n",
	})

	if args.ConfigPath != "/alias/config.env" {
		t.Fatalf("ConfigPath = %q, want /alias/config.env", args.ConfigPath)
	}
	if args.LogLevel != types.LogLevelWarning {
		t.Fatalf("LogLevel = %v, want warning", args.LogLevel)
	}
	if !args.DryRun {
		t.Fatal("DryRun should be true when -n is provided")
	}
}

func TestParseUpgradeAutoYes(t *testing.T) {
	args := parseWithArgs(t, []string{
		"--upgrade", "y",
		"--log-level", "debug",
	})

	if !args.Upgrade {
		t.Fatal("Upgrade should be true when --upgrade is provided")
	}
	if !args.UpgradeAutoYes {
		t.Fatal("UpgradeAutoYes should be true when --upgrade y is provided")
	}
	if args.LogLevel != types.LogLevelDebug {
		t.Fatalf("LogLevel = %v, want debug", args.LogLevel)
	}
}

// TestParseUpgradeAutoYesWithLocalfile pins the ordering contract upgrade-beta.sh depends on:
// `--upgrade y --localfile` must set BOTH UpgradeAutoYes (the `y` sits immediately after
// --upgrade, where extractUpgradeAutoYesArgs looks) AND LocalFile. If the token were placed
// after --localfile it would NOT be read as auto-yes, so this guards the script's finalize
// invocation against a silent regression that would re-introduce the pty stall.
func TestParseUpgradeAutoYesWithLocalfile(t *testing.T) {
	args := parseWithArgs(t, []string{"--upgrade", "y", "--localfile"})
	if !args.Upgrade {
		t.Fatal("Upgrade must be set")
	}
	if !args.UpgradeAutoYes {
		t.Fatal("`--upgrade y --localfile` must set UpgradeAutoYes (y must sit immediately after --upgrade)")
	}
	if !args.LocalFile {
		t.Fatal("`--upgrade y --localfile` must set LocalFile")
	}
}

func TestParseShowWhatsnew(t *testing.T) {
	if args := parseWithArgs(t, nil); args.ShowWhatsnew {
		t.Fatal("ShowWhatsnew must default to false")
	}
	if args := parseWithArgs(t, []string{"--show-whatsnew"}); !args.ShowWhatsnew {
		t.Fatal("ShowWhatsnew should be true when --show-whatsnew is provided")
	}
}

func parseWithArgs(t *testing.T, cliArgs []string) *Args {
	t.Helper()
	origCommandLine := flag.CommandLine
	origUsage := flag.Usage
	origArgs := os.Args

	flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
	flag.CommandLine.SetOutput(os.Stderr)
	flag.Usage = func() {}

	os.Args = append([]string{"test-binary"}, cliArgs...)

	t.Cleanup(func() {
		flag.CommandLine = origCommandLine
		flag.Usage = origUsage
		os.Args = origArgs
	})

	return Parse()
}

func TestPrintHelp(t *testing.T) {
	var buf bytes.Buffer
	flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
	flag.CommandLine.SetOutput(&buf)
	// register a couple of dummy flags so PrintDefaults emits content
	flag.CommandLine.String("config", "", "Path to configuration file")
	flag.CommandLine.Bool("dry-run", false, "Perform a dry run")

	printHelp(&buf, "proxsave")
	out := buf.String()
	if !strings.Contains(out, "Usage: proxsave [options]") {
		t.Fatalf("help missing usage line: %q", out)
	}
	if !strings.Contains(out, "-config") || !strings.Contains(out, "-dry-run") {
		t.Fatalf("help missing expected options: %q", out)
	}
}

func TestPrintVersion(t *testing.T) {
	var buf bytes.Buffer
	printVersion(&buf)
	out := buf.String()
	if !strings.Contains(out, "ProxSave") {
		t.Fatalf("version output missing header: %q", out)
	}
	if !strings.Contains(out, "Version: ") || !strings.Contains(out, "Author: tis24dev") {
		t.Fatalf("version output missing fields: %q", out)
	}
}

func TestShowHelpPrintsAndExitsZero(t *testing.T) {
	origExit := osExit
	origStdout := os.Stdout
	origStderr := os.Stderr
	origCommandLine := flag.CommandLine
	origArgs := os.Args

	var exitCode int
	osExit = func(code int) {
		exitCode = code
	}

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = outW
	os.Stderr = errW

	flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
	flag.CommandLine.SetOutput(errW)
	flag.CommandLine.Bool("dry-run", false, "Perform a dry run")
	os.Args = []string{"proxsave-test"}

	t.Cleanup(func() {
		_ = outW.Close()
		_ = outR.Close()
		_ = errW.Close()
		_ = errR.Close()
		osExit = origExit
		os.Stdout = origStdout
		os.Stderr = origStderr
		flag.CommandLine = origCommandLine
		os.Args = origArgs
	})

	ShowHelp()
	_ = outW.Close()
	_ = errW.Close()

	outBytes, err := io.ReadAll(outR)
	if err != nil {
		t.Fatalf("ReadAll stdout: %v", err)
	}
	errBytes, err := io.ReadAll(errR)
	if err != nil {
		t.Fatalf("ReadAll stderr: %v", err)
	}
	if out := string(outBytes); !strings.Contains(out, "Usage: proxsave-test [options]") || !strings.Contains(out, "-dry-run") {
		t.Fatalf("help on stdout missing usage line or options: %q", out)
	}
	if len(errBytes) != 0 {
		t.Fatalf("explicit help wrote to stderr: %q", errBytes)
	}
	if exitCode != 0 {
		t.Fatalf("exit code = %d; want 0", exitCode)
	}
}

// A parse error is not output the operator asked for: its usage stays on stderr.
func TestParseErrorUsageStaysOnStderr(t *testing.T) {
	origStdout, origStderr, origUsage := os.Stdout, os.Stderr, flag.Usage
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW
	t.Cleanup(func() {
		_ = outR.Close()
		_ = errR.Close()
		os.Stdout, os.Stderr, flag.Usage = origStdout, origStderr, origUsage
	})

	_ = parseWithArgsKeepingUsage(t, []string{"--bogus"})
	_ = outW.Close()
	_ = errW.Close()

	outBytes, _ := io.ReadAll(outR)
	errBytes, _ := io.ReadAll(errR)
	if len(outBytes) != 0 {
		t.Fatalf("parse error wrote to stdout: %q", outBytes)
	}
	if !strings.Contains(string(errBytes), "Usage: test-binary [options]") {
		t.Fatalf("parse error usage missing from stderr: %q", errBytes)
	}
}

// parseWithArgsKeepingUsage is parseWithArgs without blanking flag.Usage, so the
// usage Parse installs for a parse error is the one that runs. The real CommandLine
// routes its usage to flag.Usage; a fresh FlagSet has to be told to.
func parseWithArgsKeepingUsage(t *testing.T, cliArgs []string) *Args {
	t.Helper()
	origCommandLine, origArgs := flag.CommandLine, os.Args
	flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	flag.CommandLine.Usage = func() { flag.Usage() }
	os.Args = append([]string{"test-binary"}, cliArgs...)
	t.Cleanup(func() {
		flag.CommandLine, os.Args = origCommandLine, origArgs
	})
	return Parse()
}

func TestShowVersionPrintsAndExitsZero(t *testing.T) {
	origExit := osExit
	origStdout := os.Stdout
	origArgs := os.Args

	var exitCode int
	osExit = func(code int) {
		exitCode = code
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	os.Args = []string{"proxsave-test"}

	t.Cleanup(func() {
		_ = w.Close()
		_ = r.Close()
		osExit = origExit
		os.Stdout = origStdout
		os.Args = origArgs
	})

	ShowVersion()
	_ = w.Close()

	outBytes, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	out := string(outBytes)
	if !strings.Contains(out, "ProxSave") || !strings.Contains(out, "Version:") {
		t.Fatalf("version output missing expected fields: %q", out)
	}
	if exitCode != 0 {
		t.Fatalf("exit code = %d; want 0", exitCode)
	}
}

func TestParseDefaultConfigPath(t *testing.T) {
	args := parseWithArgs(t, nil)
	if args.ConfigPath != defaultConfigPath {
		t.Fatalf("ConfigPath = %q, want %q", args.ConfigPath, defaultConfigPath)
	}
	if args.ConfigPathSource != configSourceDefault {
		t.Fatalf("ConfigPathSource = %q, want %q", args.ConfigPathSource, configSourceDefault)
	}
}

func TestParseLogLevelOverrideOrder(t *testing.T) {
	args := parseWithArgs(t, []string{"--log-level", "debug", "-l", "warning"})
	if args.LogLevel != types.LogLevelWarning {
		t.Fatalf("LogLevel = %v, want warning (last flag wins)", args.LogLevel)
	}
}

func TestParseSupportDoesNotChangeLogLevel(t *testing.T) {
	args := parseWithArgs(t, []string{"--support"})
	if !args.Support {
		t.Fatal("Support should be true")
	}
	if args.LogLevel != types.LogLevelNone {
		t.Fatalf("LogLevel = %v, want LogLevelNone when not specified", args.LogLevel)
	}
}
