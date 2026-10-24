package main

import (
	"flag"
	"io"
	"os"
	"reflect"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// parseArgv runs the real cli.Parse over argv, as main does, on a fresh flag set.
func parseArgv(t *testing.T, argv ...string) *cli.Args {
	t.Helper()
	origCommandLine, origUsage, origArgs := flag.CommandLine, flag.Usage, os.Args
	flag.CommandLine = flag.NewFlagSet("proxsave", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = append([]string{"proxsave"}, argv...)
	t.Cleanup(func() {
		flag.CommandLine, flag.Usage, os.Args = origCommandLine, origUsage, origArgs
	})
	return cli.Parse()
}

func TestValidateModeCompatibilityRefusesWordLeftAfterFlags(t *testing.T) {
	refusal := func(word string) []string {
		return []string{"Unexpected argument \"" + word + "\": only flags are accepted. Nothing was run."}
	}
	tests := []struct {
		name string
		argv []string
		want []string
	}{
		{name: "lone word", argv: []string{"help"}, want: refusal("help")},
		{name: "word drops the dry run after it", argv: []string{"--backup", "extra", "--dry-run"}, want: refusal("extra")},
		{name: "word hides the restore dry run refusal", argv: []string{"--restore", "x", "--dry-run"}, want: refusal("x")},
		{name: "only the word line is printed", argv: []string{"--install", "--new-install", "x"}, want: refusal("x")},
		{name: "upgrade takes only y", argv: []string{"--upgrade", "yes"}, want: refusal("yes")},
		{name: "flags after the terminator are words", argv: []string{"--", "--dry-run"}, want: refusal("--dry-run")},
		{name: "pasted em dash is a word", argv: []string{"—dry-run"}, want: refusal("—dry-run")},
		{name: "bare run", argv: nil},
		{name: "flags only", argv: []string{"--backup", "--dry-run"}},
		{name: "upgrade auto yes", argv: []string{"--upgrade", "y"}},
		{name: "upgrade auto yes uppercase then flag", argv: []string{"--upgrade", "Y", "--localfile"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateModeCompatibility(parseArgv(t, tt.argv...))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("validateModeCompatibility(%q) = %#v, want %#v", tt.argv, got, tt.want)
			}
		})
	}
}

func TestRejectIncompatibleModesStopsOnWordLeftAfterFlags(t *testing.T) {
	code, handled := rejectIncompatibleModes(parseArgv(t, "help"), logging.NewBootstrapLogger())
	if !handled || code != types.ExitConfigError.Int() {
		t.Fatalf("rejectIncompatibleModes(help) = (%d, %v), want (%d, true)", code, handled, types.ExitConfigError.Int())
	}
}

// --help and --version are dispatched before the refusal, so they still win when they
// come before the word; after it they were never read.
func TestHelpBeforeWordIsStillRead(t *testing.T) {
	if args := parseArgv(t, "--help", "extra"); !args.ShowHelp {
		t.Fatal("--help before the word must still be read")
	}
	if args := parseArgv(t, "--version", "extra"); !args.ShowVersion {
		t.Fatal("--version before the word must still be read")
	}
	if args := parseArgv(t, "extra", "--help"); args.ShowHelp {
		t.Fatal("--help after the word must not be read")
	}
}
