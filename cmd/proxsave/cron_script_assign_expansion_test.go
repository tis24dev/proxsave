package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ${NAME:=VALUE} substitutes the operand AND assigns it, so a wrapper written as
//
//	: ${BIN:=/usr/local/bin/proxsave}
//	$BIN --backup
//
// runs the binary on both lines, and neither of them was seen. The gap has two halves and
// fixing either alone leaves the reported case undetected: the operator table did not list
// the assigning forms, so the word never expanded in command position; and the variable
// pass recorded a name only from a word looksLikeEnvAssignment accepts, so the assignment
// the operator performs was never written down and the later $BIN resolved to nothing.
//
// The guard rows are shapes a real shell REFUSES. Expanding them would invent a run out of
// a script that exits with an error.
func TestScriptProbeExpandsAnAssigningDefault(t *testing.T) {
	dir := t.TempDir()
	flagged := func(t *testing.T, body string) bool {
		t.Helper()
		p := filepath.Join(dir, strings.ReplaceAll(t.Name(), "/", "_")+".sh")
		if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		return len(indirectProxsaveCronRefs([]string{"0 2 * * * " + p}, cronProbeReadScripts)) > 0
	}

	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"assign-and-use in command position", "#!/bin/sh\n${BIN:=/usr/local/bin/proxsave} --backup\n", true},
		{"assign-and-use without the colon", "#!/bin/sh\n${BIN=/usr/local/bin/proxsave} --backup\n", true},
		{"assigned on a null command, used on the next line", "#!/bin/sh\n: ${BIN:=/usr/local/bin/proxsave}\n$BIN --backup\n", true},
		{"assigned on a null command without the colon", "#!/bin/sh\n: ${BIN=/usr/local/bin/proxsave}\n$BIN --backup\n", true},
		{"assigned quoted, used behind exec", "#!/bin/sh\n: \"${BIN:=/usr/local/bin/proxsave}\"\nexec \"$BIN\" --backup\n", true},

		// bash: "$1: cannot assign in this way", exit 1. dash: "1: bad variable name",
		// exit 2. The script never reaches a command, let alone the binary.
		{"positional parameter in command position", "#!/bin/sh\n${1:=/usr/local/bin/proxsave} --backup\n", false},
		{"positional parameter on a null command", "#!/bin/sh\n: ${1:=/usr/local/bin/proxsave}\n$1 --backup\n", false},
		// $0 is the script itself and cannot be assigned either; bash expands it and tries
		// to run the script, never the binary.
		{"the zeroth parameter", "#!/bin/sh\n${0:=/usr/local/bin/proxsave} --backup\n", false},

		// Unchanged: the operand of this one is a pattern to strip, not a path.
		{"pattern operand", "#!/bin/sh\n${BIN%%/usr/local/bin/proxsave} --backup\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := flagged(t, tc.body); got != tc.want {
				t.Errorf("flagged = %v, want %v for:\n%s", got, tc.want, tc.body)
			}
		})
	}
}

// The parser half on its own, so a failure says which half broke.
func TestParseScriptExpansionReadsTheAssigningOperators(t *testing.T) {
	for _, tc := range []struct {
		word         string
		name, fallb  string
		assigns, wan bool
	}{
		{"${BIN:=/usr/local/bin/proxsave}", "BIN", "/usr/local/bin/proxsave", true, true},
		{"${BIN=/usr/local/bin/proxsave}", "BIN", "/usr/local/bin/proxsave", true, true},
		{"${BIN:-/usr/local/bin/proxsave}", "BIN", "/usr/local/bin/proxsave", false, true},
		{"${BIN-/usr/local/bin/proxsave}", "BIN", "/usr/local/bin/proxsave", false, true},
		// A shell refuses to assign to a positional parameter, so the assigning forms are
		// not expansions this detector may follow. The non-assigning twin is fine and stays.
		{"${1:=/usr/local/bin/proxsave}", "", "", false, false},
		{"${1:-/usr/local/bin/proxsave}", "1", "/usr/local/bin/proxsave", false, true},
	} {
		t.Run(tc.word, func(t *testing.T) {
			name, fallback, assigns, ok := parseScriptExpansionOp(tc.word)
			if name != tc.name || fallback != tc.fallb || assigns != tc.assigns || ok != tc.wan {
				t.Errorf("parseScriptExpansionOp(%q) = (%q, %q, %v, %v), want (%q, %q, %v, %v)",
					tc.word, name, fallback, assigns, ok, tc.name, tc.fallb, tc.assigns, tc.wan)
			}
		})
	}
}
