package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// exitCodeDocs are the two references that publish the exit-code contract. Both carry a
// table AND a prose sentence counting how many non-zero codes are not failures, so both
// go stale together when a code is added.
var exitCodeDocs = []struct {
	name string
	path string
}{
	{"CLI_REFERENCE.md", "../../docs/CLI_REFERENCE.md"},
	{"TROUBLESHOOTING.md", "../../docs/TROUBLESHOOTING.md"},
}

// TestExitCodesAreDocumented pins the exit-code contract against doc drift. Exit codes
// are a SCRIPTING interface: an operator gates a cron wrapper or a monitoring probe on
// them, and the only place they can learn what a code means is these two files. A code
// that exists in the binary but not in the docs is indistinguishable, from outside, from
// a code that means "something broke".
//
// This is not hypothetical. ExitGuardsPending (17) shipped with --cleanup-guards and was
// absent from both documents, which went on asserting that 16 was the only non-zero code
// that does not mean a failure -- so a wrapper written from the docs would have paged
// someone for a datastore that was merely still mounted.
//
// The constants are read from the source rather than listed here on purpose: a test that
// repeats the list drifts in exactly the same way the docs did.
func TestExitCodesAreDocumented(t *testing.T) {
	codes := declaredExitCodes(t)
	// The interrupted code lives in main (128 + SIGINT), not in the types package, but it
	// is part of the same published contract.
	codes["exitCodeInterrupted"] = exitCodeInterrupted

	for _, doc := range exitCodeDocs {
		body, readErr := os.ReadFile(doc.path)
		if readErr != nil {
			t.Fatalf("read %s: %v", doc.name, readErr)
		}
		text := string(body)
		var missing []string
		for name, code := range codes {
			// A table row for the code: "| `17` | ... |". Matching the row rather than the
			// bare number avoids passing on an unrelated "17" elsewhere in the prose.
			row := regexp.MustCompile(`(?m)^\|\s*` + "`" + strconv.Itoa(code) + "`" + `\s*\|`)
			if !row.MatchString(text) {
				missing = append(missing, fmt.Sprintf("%s (%d)", name, code))
			}
		}
		if len(missing) > 0 {
			t.Errorf("%s has no table row for %d exit code(s): %s",
				doc.name, len(missing), strings.Join(missing, ", "))
		}
	}
}

// declaredExitCodes reads the exit-code constants straight out of
// internal/types/exit_codes.go. Reading them from the source rather than restating them
// here is what keeps a list from drifting the way the documents did, so every caller
// inherits that property instead of growing its own copy.
func declaredExitCodes(t *testing.T) map[string]int {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "../../internal/types/exit_codes.go", nil, 0)
	if err != nil {
		t.Fatalf("parse exit_codes.go: %v", err)
	}

	// Anything this matcher cannot read is recorded, never skipped quietly. The matcher only
	// understands "Name Type = <int literal>", which is how exit_codes.go declares every code
	// today. Rewrite one as an iota, an expression, or a grouped spec and it would vanish from
	// codes -- and a code absent from codes is a code this test stops requiring in the docs,
	// which is the exact drift the file exists to prevent, reintroduced through its own blind
	// spot. Failing here forces whoever changes the declaration style to teach the matcher.
	codes := map[string]int{}
	var unreadable []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				unreadable = append(unreadable, fmt.Sprintf("%s: not a value spec", fset.Position(spec.Pos())))
				continue
			}
			names := make([]string, 0, len(value.Names))
			for _, n := range value.Names {
				names = append(names, n.Name)
			}
			label := strings.Join(names, ",")
			if len(value.Names) != 1 || len(value.Values) != 1 {
				unreadable = append(unreadable, fmt.Sprintf("%s (%d name(s), %d value(s))", label, len(value.Names), len(value.Values)))
				continue
			}
			lit, ok := value.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				unreadable = append(unreadable, label+" (value is not an integer literal)")
				continue
			}
			n, convErr := strconv.Atoi(lit.Value)
			if convErr != nil {
				unreadable = append(unreadable, fmt.Sprintf("%s (%q: %v)", label, lit.Value, convErr))
				continue
			}
			codes[names[0]] = n
		}
	}
	if len(unreadable) > 0 {
		t.Fatalf("%d exit-code constant(s) the matcher cannot read, so they are silently exempt from the documentation check: %s",
			len(unreadable), strings.Join(unreadable, "; "))
	}
	if len(codes) == 0 {
		t.Fatalf("no exit-code constants found; the matcher has gone stale")
	}
	return codes
}

// TestExitCodeProseCountsTheBenignCodes pins the sentence a reader actually acts on.
// Both documents state, in prose, how many non-zero codes are NOT failures. That count
// is what tells someone whether their "|| alert" wrapper is safe, and it is what went
// wrong last time: the table could have been fixed while the sentence still said the
// wrong number, and the sentence is the part people read.
//
// The benign non-zero codes today are 1 (a run that succeeded with warnings), 16
// (nothing to back up), 17 (guards still holding the storage) and 130 (cancelled).
func TestExitCodeProseCountsTheBenignCodes(t *testing.T) {
	benign := []string{"`1`", "`16`", "`17`", "`130`"}

	for _, doc := range exitCodeDocs {
		body, err := os.ReadFile(doc.path)
		if err != nil {
			t.Fatalf("read %s: %v", doc.name, err)
		}
		text := string(body)

		// The claim that one specific code is the only benign one must not survive.
		for _, stale := range []string{
			"`16` is the one non-zero code",
			"Three of them are not failures",
		} {
			if strings.Contains(text, stale) {
				t.Errorf("%s still carries the pre-17 claim %q", doc.name, stale)
			}
		}

		// Search the PROSE, not the whole file. Every benign code also appears in the
		// exit-code table, so a whole-document search is satisfied by the table alone: the
		// sentence could be deleted outright, or state the wrong set, and this test still
		// passed. Verified by deleting it.
		//
		// The anchor is structural rather than textual because the two documents word the
		// claim differently -- "do not mean something went wrong" here, "are not failures"
		// there -- and a phrase match would silently stop covering one of them.
		var carriers []string
		for _, para := range exitCodeProsePara(text) {
			if containsAll(para, benign) {
				carriers = append(carriers, para)
			}
		}
		if len(carriers) == 0 {
			t.Errorf("%s has no prose paragraph naming every benign code %v; the table alone does not tell a reader which non-zero codes are safe to ignore", doc.name, benign)
			continue
		}
		// If the sentence counts them, the count has to be right. This is the half that
		// actually went stale last time: the table was corrected while the sentence still
		// said the old number.
		for _, para := range carriers {
			if m := benignCountWord.FindStringSubmatch(para); m != nil && m[1] != "Four" {
				t.Errorf("%s prose says %q of the non-zero codes are benign, but names %d of them", doc.name, m[1], len(benign))
			}
		}
	}
}

// benignCountWord matches a spelled-out count in the benign-codes sentence, so a
// corrected table beside a stale "Three of them" is caught.
var benignCountWord = regexp.MustCompile(`\b(One|Two|Three|Four|Five|Six)\b of them`)

// exitCodeProsePara splits a document into blank-line separated paragraphs and drops
// every block that is a table or a heading, leaving the prose a reader acts on.
func exitCodeProsePara(text string) []string {
	var out []string
	for _, para := range strings.Split(text, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		structural := false
		for _, line := range strings.Split(para, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "|") || strings.HasPrefix(line, "#") {
				structural = true
				break
			}
		}
		if !structural {
			out = append(out, para)
		}
	}
	return out
}

func containsAll(s string, needles []string) bool {
	for _, n := range needles {
		if !strings.Contains(s, n) {
			return false
		}
	}
	return true
}
