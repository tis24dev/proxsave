package main

import (
	"reflect"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestValidateModeCompatibilityRefusesUnknownLogLevel(t *testing.T) {
	refusal := func(value string) []string {
		return []string{"Unknown log level \"" + value + "\": use debug, info, warning, error or critical. Nothing was run."}
	}
	tests := []struct {
		name string
		argv []string
		want []string
	}{
		{name: "short form", argv: []string{"-l", "warn"}, want: refusal("warn")},
		{name: "typo", argv: []string{"--log-level", "debgu"}, want: refusal("debgu")},
		{name: "none", argv: []string{"--log-level", "none"}, want: refusal("none")},
		{name: "zero", argv: []string{"-l", "0"}, want: refusal("0")},
		{name: "out of range", argv: []string{"-l", "6"}, want: refusal("6")},
		{name: "only the log level line is printed", argv: []string{"--restore", "--decrypt", "-l", "none"}, want: refusal("none")},
		{
			name: "a word left after the flags comes first",
			argv: []string{"-l", "warn", "extra"},
			want: []string{"Unexpected argument \"extra\": only flags are accepted. Nothing was run."},
		},
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

func TestParseLogLevelIgnoresCase(t *testing.T) {
	tests := []struct {
		argv []string
		want types.LogLevel
	}{
		{argv: []string{"-l", "DEBUG"}, want: types.LogLevelDebug},
		{argv: []string{"--log-level", "Warning", "--backup"}, want: types.LogLevelWarning},
		{argv: []string{"-l", "debug"}, want: types.LogLevelDebug},
	}
	for _, tt := range tests {
		args := parseArgv(t, tt.argv...)
		if got := validateModeCompatibility(args); got != nil {
			t.Fatalf("validateModeCompatibility(%q) = %#v, want nil", tt.argv, got)
		}
		if args.LogLevel != tt.want {
			t.Fatalf("Parse(%q).LogLevel = %v, want %v", tt.argv, args.LogLevel, tt.want)
		}
	}
}
