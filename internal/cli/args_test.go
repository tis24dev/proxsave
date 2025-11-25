package cli

import (
	"bytes"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/types"
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
	}{
		{"debug string", "debug", types.LogLevelDebug},
		{"debug number", "5", types.LogLevelDebug},
		{"info string", "info", types.LogLevelInfo},
		{"info number", "4", types.LogLevelInfo},
		{"warning string", "warning", types.LogLevelWarning},
		{"warning number", "3", types.LogLevelWarning},
		{"error string", "error", types.LogLevelError},
		{"error number", "2", types.LogLevelError},
		{"critical string", "critical", types.LogLevelCritical},
		{"critical number", "1", types.LogLevelCritical},
		{"none string", "none", types.LogLevelNone},
		{"none number", "0", types.LogLevelNone},
		{"unknown", "invalid", types.LogLevelInfo},
		{"uppercase defaults", "DEBUG", types.LogLevelInfo},
		{"mixed case defaults", "Debug", types.LogLevelInfo},
		{"leading whitespace", " debug", types.LogLevelInfo},
		{"trailing whitespace", "debug ", types.LogLevelInfo},
		{"empty string", "", types.LogLevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseLogLevel(tt.input)
			if result != tt.expected {
				t.Errorf("parseLogLevel(%q) = %v; want %v", tt.input, result, tt.expected)
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
		args.Restore || args.Install || args.NewInstall || args.EnvMigration || args.EnvMigrationDry || args.UpgradeConfig ||
		args.UpgradeConfigDry {
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
		"--env-migration",
		"--env-migration-dry-run",
		"--upgrade-config",
		"--upgrade-config-dry-run",
		"--old-env", "/legacy.env",
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
		!args.EnvMigration || !args.EnvMigrationDry || !args.UpgradeConfig ||
		!args.UpgradeConfigDry {
		t.Fatal("expected boolean flags to be set")
	}
	if args.LegacyEnvPath != "/legacy.env" {
		t.Fatalf("LegacyEnvPath = %q, want /legacy.env", args.LegacyEnvPath)
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

	printHelp(&buf, "proxmox-backup")
	out := buf.String()
	if !strings.Contains(out, "Usage: proxmox-backup [options]") {
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
	if !strings.Contains(out, "Proxmox Backup Manager (Go Edition)") {
		t.Fatalf("version output missing header: %q", out)
	}
	if !strings.Contains(out, "Version: 0.2.0-dev") || !strings.Contains(out, "Author: tis24dev") {
		t.Fatalf("version output missing fields: %q", out)
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
